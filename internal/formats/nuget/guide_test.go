package nuget

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
)

// guideClasses are the classes the guide is written for. The descriptor lists the local class only until the
// adapters of the others land (Q-NUGET-28), so Guide() returns the sections of the local class alone; the tests
// run over guideSections(), which holds the text of all three.
var guideClasses = []formats.Class{formats.Local, formats.Remote, formats.Virtual}

func guideData(class formats.Class, client string, anonymous, rules bool) formats.GuideData {
	repo := map[formats.Class]string{formats.Local: "nuget-local", formats.Remote: "nuget-org", formats.Virtual: "nuget-all"}[class]
	sd := setupData(!anonymous)
	sd.Repository = repo
	return formats.GuideData{
		SetupData:     sd,
		Class:         class,
		Client:        client,
		PublishTarget: map[formats.Class]string{formats.Local: "nuget-local", formats.Virtual: "nuget-local"}[class],
		Anonymous:     anonymous,
		ExposureRules: rules,
		TokenEnvVar:   "TROVE_TOKEN",
	}
}

func clientIDs() []string {
	var out []string
	for _, c := range New().Descriptor().Clients {
		out = append(out, c.ID)
	}
	return out
}

// renderGuide runs every section that applies to the class and the client, over all sections the format has
// text for.
func renderGuide(t *testing.T, d formats.GuideData) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range guideSections() {
		if !s.AppliesTo(d.Class, d.Client) {
			continue
		}
		text, err := formats.ExecuteGuide(s, d)
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		out[s.ID] = text
	}
	return out
}

// everyRendering is the guide for every class, client, reader and endpoint kind.
func everyRendering(t *testing.T) map[string]string {
	t.Helper()
	all := map[string]string{}
	for _, class := range guideClasses {
		for _, c := range clientIDs() {
			for _, anonymous := range []bool{false, true} {
				for _, rules := range []bool{false, true} {
					for sid, text := range renderGuide(t, guideData(class, c, anonymous, rules)) {
						all[fmt.Sprintf("%s %s %s anonymous=%v rules=%v", sid, class, c, anonymous, rules)] = text
					}
				}
			}
		}
	}
	return all
}

// TestR_UI_049_NuGetHasAGuideForEveryVariant: for each of the three classes and each of the three clients the
// guide has a section of each required topic that says something, whether or not the class is registered yet;
// Guide() hands the kit the sections of the registered classes only, and the others join with their class.
func TestR_UI_049_NuGetHasAGuideForEveryVariant(t *testing.T) {
	f := New()
	for _, class := range guideClasses {
		for _, client := range clientIDs() {
			got := renderGuide(t, guideData(class, client, false, false))
			for _, topic := range formats.RequiredGuideTopics() {
				var said bool
				for _, s := range guideSections() {
					if s.Topic == topic && s.AppliesTo(class, client) && strings.TrimSpace(got[s.ID]) != "" {
						said = true
					}
				}
				if !said {
					t.Errorf("%s %s: no section of topic %q says anything", class, client, topic)
				}
			}
		}
	}
	for _, s := range f.Guide() {
		for _, c := range s.Classes {
			if !f.Descriptor().Supports(c) {
				t.Errorf("Guide() returns %s for the class %s, which is not registered", s.ID, c)
			}
		}
		if s.ID == "nuget.virtual.what" {
			t.Errorf("Guide() returns %s before the class registers", s.ID)
		}
	}
	if len(f.Guide()) == 0 || f.Guide()[0].ID != "nuget.local.what" {
		t.Errorf("%v", f.Guide())
	}
}

var troubleshootingRow = regexp.MustCompile("(?m)^\\| (\\d{3}) \\| `([a-z_]+)` \\|")

// statusOf is the status of a code of the catalog.
func statusOf(code string) (int, bool) {
	if info, ok := errs.Lookup(code); ok {
		return errs.E(info.Kind, info.Code, "").Status(), true
	}
	return 0, false
}

// TestR_UI_049_TheNuGetTroubleshootingCodesAreTroves: every row of the troubleshooting table names a code of the
// problem catalog, under the status trove answers it with.
func TestR_UI_049_TheNuGetTroubleshootingCodesAreTroves(t *testing.T) {
	text := renderGuide(t, guideData(formats.Virtual, ClientDotnet, false, false))["nuget.troubleshooting"]
	rows := troubleshootingRow.FindAllStringSubmatch(text, -1)
	if len(rows) < 30 {
		t.Fatalf("the table has %d rows:\n%s", len(rows), text)
	}
	statuses := map[int]bool{}
	for _, row := range rows {
		status, _ := strconv.Atoi(row[1])
		statuses[status] = true
		got, ok := statusOf(row[2])
		if _, registered := errs.Lookup(row[2]); ok && !registered {
			t.Logf("the code %s is not registered yet (WP3.1e); the guide names it with its status %d", row[2], got)
		}
		if !ok {
			t.Errorf("%s: no such code in internal/errs", row[2])
			continue
		}
		info, _ := errs.Lookup(row[2])
		// An Invalid error is 400, or 422 when it names its fields (R-API-009).
		if got != status && (info.Kind != errs.Invalid || status != 422) {
			t.Errorf("%s is listed under %d and answers %d", row[2], status, got)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 405, 409, 413, 429, 501, 502, 503, 504} {
		if !statuses[s] {
			t.Errorf("the table has no row for %d", s)
		}
	}
	// The codes of the protocol surface of this format are in the table.
	for _, code := range []string{errs.CodeNuGetInvalidPackage, errs.CodeNuGetInvalidID, errs.CodeNuGetInvalidVersion, errs.CodeNuGetV2Unsupported, errs.CodeNuGetNotImplemented} {
		if !strings.Contains(text, "`"+code+"`") {
			t.Errorf("the table lacks %s", code)
		}
	}
}

var codeSpan = regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")

// TestR_UI_049_EveryCodeTheNuGetGuideNamesExists: a snake_case word in code anywhere in the guide is an error code
// of the catalog or one of the pending codes.
func TestR_UI_049_EveryCodeTheNuGetGuideNamesExists(t *testing.T) {
	for where, text := range everyRendering(t) {
		for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
			if _, ok := statusOf(m[1]); !ok {
				t.Errorf("%s: %q is no code of internal/errs", where, m[1])
			}
		}
	}
}

// TestR_NUGET_026_TheGuideStatesTheDefaults: the periods, the ID rule and the settings the guide names are the
// defaults of the descriptor and the schema.
func TestR_NUGET_026_TheGuideStatesTheDefaults(t *testing.T) {
	d := New().Descriptor()
	remote := renderGuide(t, guideData(formats.Remote, ClientDotnet, false, false))["nuget.remote.what"]
	if *d.DefaultMetadataTTLSeconds != 300 || !strings.Contains(remote, "five minutes unless the repository sets another") {
		t.Errorf("metadata period %d", *d.DefaultMetadataTTLSeconds)
	}
	if *d.DefaultNotFoundTTLSeconds != 1800 || !strings.Contains(remote, "30 minutes unless the repository sets another") {
		t.Errorf("negative period %d", *d.DefaultNotFoundTTLSeconds)
	}
	if !strings.Contains(remote, d.DefaultUpstreamURL) {
		t.Errorf("the default upstream %s is not the example", d.DefaultUpstreamURL)
	}
	local := renderGuide(t, guideData(formats.Local, ClientDotnet, false, false))["nuget.local.what"]
	if want := fmt.Sprintf("at most %d characters", id.MaxLength); !strings.Contains(local, want) {
		t.Errorf("the ID length is %d and the guide does not say %q", id.MaxLength, want)
	}
	if err := id.ValidateLocal("Acme.Widgets_2-x"); err != nil || id.ValidateLocal("Acme..Widgets") == nil {
		t.Errorf("the local ID rule the guide describes changed: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			Default any `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(settingsSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["deleteMode"].Default != "unlist" || schema.Properties["forwardSearch"].Default != true {
		t.Errorf("the settings the guide describes changed their defaults: %+v", schema.Properties)
	}
	maintain := renderGuide(t, guideData(formats.Local, ClientDotnet, false, false))["nuget.maintain"]
	for _, s := range []string{"`deleteMode`", "`unlist`", "`delete`"} {
		if !strings.Contains(maintain, s) {
			t.Errorf("the maintain section does not say %q", s)
		}
	}
	for _, s := range []string{"`forwardSearch`"} {
		if !strings.Contains(remote, s) {
			t.Errorf("the remote section does not say %q", s)
		}
	}
}

// TestR_UI_049_TheNuGetGuideNamesTheAddressAndThePackageSourceMapping: the configure section shows the address of
// the service index, never a v2 address, and the mapping against dependency confusion with <clear />.
func TestR_UI_049_TheNuGetGuideNamesTheAddressAndThePackageSourceMapping(t *testing.T) {
	for _, class := range guideClasses {
		for _, client := range clientIDs() {
			got := renderGuide(t, guideData(class, client, false, false))["nuget.configure"]
			for _, want := range []string{"https://trove.example.com/api/nuget/", "v3/index.json", "<clear />", "<packageSourceMapping>", "Acme.*"} {
				if !strings.Contains(got, want) {
					t.Errorf("%s %s: the configure section lacks %q", class, client, want)
				}
			}
		}
	}
}

// TestR_UI_049_AnAnonymousNuGetReaderGetsNoCredentialSteps: for a reader who is not signed in the sections that
// need a credential say nothing, and no text names the credential.
func TestR_UI_049_AnAnonymousNuGetReaderGetsNoCredentialSteps(t *testing.T) {
	for _, class := range guideClasses {
		for _, c := range clientIDs() {
			got := renderGuide(t, guideData(class, c, true, false))
			for _, sid := range []string{"nuget.publish", "nuget.maintain"} {
				if strings.TrimSpace(got[sid]) != "" {
					t.Errorf("%s %s: %s says %q", class, c, sid, got[sid])
				}
			}
			if a := got["nuget.authenticate"]; !strings.Contains(a, "without signing in") || strings.Contains(a, "personal access token") {
				t.Errorf("%s %s: %q", class, c, a)
			}
			for sid, text := range got {
				for _, ref := range formats.SetupTokenRefs() {
					if strings.Contains(text, ref) {
						t.Errorf("%s %s: %s names the credential %s", class, c, sid, ref)
					}
				}
			}
		}
	}
}

// TestR_UI_049_TheNuGetSectionsOfTheCommandsAreForTheCommandLine: Visual Studio has no command to push or to
// change a version, and its guide has no section about them.
func TestR_UI_049_TheNuGetSectionsOfTheCommandsAreForTheCommandLine(t *testing.T) {
	for _, class := range []formats.Class{formats.Local, formats.Virtual} {
		got := renderGuide(t, guideData(class, ClientVisualStudio, false, false))
		for _, sid := range []string{"nuget.publish", "nuget.maintain"} {
			if _, ok := got[sid]; ok {
				t.Errorf("%s: Visual Studio gets %s", class, sid)
			}
		}
		for _, c := range []string{ClientDotnet, ClientNuGet} {
			got := renderGuide(t, guideData(class, c, false, false))
			if strings.TrimSpace(got["nuget.publish"]) == "" || strings.TrimSpace(got["nuget.maintain"]) == "" {
				t.Errorf("%s %s: no publish or maintain section", class, c)
			}
		}
	}
	for _, c := range []string{ClientDotnet, ClientNuGet} {
		if got := renderGuide(t, guideData(formats.Remote, c, false, false)); got["nuget.publish"] != "" || slices.Contains(sectionIDs(got), "nuget.publish") {
			t.Errorf("remote %s: a remote repository takes no push", c)
		}
	}
}

func sectionIDs(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestR_UI_049_TheNuGetEndpointSectionIsForEndpointsWithRules: the note on exposure rules appears only on such an
// endpoint and names the codes and the client errors it causes.
func TestR_UI_049_TheNuGetEndpointSectionIsForEndpointsWithRules(t *testing.T) {
	if got := renderGuide(t, guideData(formats.Virtual, ClientDotnet, false, false))["nuget.endpoint"]; strings.TrimSpace(got) != "" {
		t.Errorf("without rules: %q", got)
	}
	got := renderGuide(t, guideData(formats.Virtual, ClientDotnet, false, true))["nuget.endpoint"]
	for _, s := range []string{"`endpoint_read_only`", "`NU1102`", "`NU1101`", "`403`"} {
		if !strings.Contains(got, s) {
			t.Errorf("with rules, %q is missing:\n%s", s, got)
		}
	}
}

// TestR_UI_049_TheNuGetGuideNamesThePublishTarget: a virtual repository's guide names the deployment target the
// reader may push to, and says the opposite for one without.
func TestR_UI_049_TheNuGetGuideNamesThePublishTarget(t *testing.T) {
	d := guideData(formats.Virtual, ClientDotnet, false, false)
	if got := renderGuide(t, d)["nuget.virtual.what"]; !strings.Contains(got, "For `nuget-all` that is `nuget-local`.") {
		t.Errorf("%s", got)
	}
	d.PublishTarget = ""
	if got := renderGuide(t, d)["nuget.virtual.what"]; !strings.Contains(got, "You cannot push through `nuget-all`") {
		t.Errorf("%s", got)
	}
	if got := renderGuide(t, d)["nuget.publish"]; !strings.Contains(got, "You cannot push through `nuget-all`") {
		t.Errorf("%s", got)
	}
}
