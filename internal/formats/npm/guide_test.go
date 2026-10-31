package npm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func guideData(class formats.Class, client string, anonymous, rules bool) formats.GuideData {
	return formats.GuideData{
		SetupData:     setupData("", !anonymous),
		Class:         class,
		Client:        client,
		PublishTarget: map[formats.Class]string{formats.Local: "npm-all", formats.Virtual: "npm-local"}[class],
		Anonymous:     anonymous,
		ExposureRules: rules,
		TokenEnvVar:   "TROVE_TOKEN",
	}
}

// renderGuide runs every section that applies to the class and the client.
func renderGuide(t *testing.T, d formats.GuideData) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range New().Guide() {
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
	for _, class := range New().Descriptor().Classes {
		for _, c := range New().Descriptor().Clients {
			for _, anonymous := range []bool{false, true} {
				for _, rules := range []bool{false, true} {
					for id, text := range renderGuide(t, guideData(class, c.ID, anonymous, rules)) {
						all[fmt.Sprintf("%s %s %s anonymous=%v rules=%v", id, class, c.ID, anonymous, rules)] = text
					}
				}
			}
		}
	}
	return all
}

var troubleshootingRow = regexp.MustCompile("(?m)^\\| (\\d{3}) \\| `([a-z_]+)` \\|")

// TestR_UI_049_TheTroubleshootingCodesAreTroves: every row of the troubleshooting
// table names a code of the problem catalog, under the status trove answers it with.
func TestR_UI_049_TheTroubleshootingCodesAreTroves(t *testing.T) {
	text := renderGuide(t, guideData(formats.Virtual, "npm", false, false))["npm.troubleshooting"]
	rows := troubleshootingRow.FindAllStringSubmatch(text, -1)
	if len(rows) < 30 {
		t.Fatalf("the table has %d rows:\n%s", len(rows), text)
	}
	statuses := map[int]bool{}
	for _, row := range rows {
		status, _ := strconv.Atoi(row[1])
		statuses[status] = true
		info, ok := errs.Lookup(row[2])
		if !ok {
			t.Errorf("%s: no such code in internal/errs", row[2])
			continue
		}
		got := errs.E(info.Kind, info.Code, "").Status()
		// An Invalid error is 400, or 422 when it names its fields (R-API-009).
		if got != status && (info.Kind != errs.Invalid || status != 422) {
			t.Errorf("%s is listed under %d and answers %d", row[2], status, got)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 405, 409, 413, 422, 429, 501, 502, 503, 504} {
		if !statuses[s] {
			t.Errorf("the table has no row for %d", s)
		}
	}
}

var codeSpan = regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")

// TestR_UI_049_EveryCodeTheGuideNamesExists: a snake_case word in code anywhere in the
// guide is an error code of the catalog.
func TestR_UI_049_EveryCodeTheGuideNamesExists(t *testing.T) {
	for where, text := range everyRendering(t) {
		for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
			if _, ok := errs.Lookup(m[1]); !ok {
				t.Errorf("%s: %q is no code of internal/errs", where, m[1])
			}
		}
	}
}

// TestR_NPM_041_TheGuideQuotesWhatTroveSends: the notices and refusals the guide quotes
// are the texts the handlers send.
func TestR_NPM_041_TheGuideQuotesWhatTroveSends(t *testing.T) {
	all := strings.Join(mapValues(everyRendering(t)), "\n")
	for _, want := range []string{
		fmt.Sprintf(latestKeptNotice, "<version>"),
		fmt.Sprintf(retryAcknowledgedNotice, "<name>", "<version>", "<time>"),
		strings.ReplaceAll(strings.Replace(strings.Replace(strings.Replace(auditPartialNotice, "%d", "<n>", 1), "%d", "<m>", 1), "%d", "<k>", 1), "%%", "%"),
		strings.Split(auditUnavailableText, "%s")[0],
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the guide does not quote %q", want)
		}
	}
}

// TestR_NPM_041_TheGuideStatesTheDefaults: the periods, limits and settings the guide
// names are the defaults of the descriptor, the configuration and the settings schema.
func TestR_NPM_041_TheGuideStatesTheDefaults(t *testing.T) {
	d := New().Descriptor()
	remote := renderGuide(t, guideData(formats.Remote, "npm", false, false))["npm.remote.what"]
	if *d.DefaultMetadataTTLSeconds != 7200 || !strings.Contains(remote, "two hours unless the repository sets another") {
		t.Errorf("metadata period %d", *d.DefaultMetadataTTLSeconds)
	}
	if *d.DefaultNotFoundTTLSeconds != 1800 || !strings.Contains(remote, "30 minutes unless the repository sets another") {
		t.Errorf("negative period %d", *d.DefaultNotFoundTTLSeconds)
	}
	if !strings.Contains(remote, d.DefaultUpstreamURL) {
		t.Errorf("the default upstream %s is not the example", d.DefaultUpstreamURL)
	}
	publish := renderGuide(t, guideData(formats.Local, "npm", false, false))["npm.publish"]
	pub := reflect.TypeFor[config.NPMPublish]()
	for field, want := range map[string]string{"MaxTarballSize": "256MiB", "MaxUnpackedSize": "2GiB", "MaxTarEntries": "100000", "RetryWindow": "10m"} {
		f, _ := pub.FieldByName(field)
		if got := f.Tag.Get("default"); got != want {
			t.Errorf("%s defaults to %s, the guide says %s", field, got, want)
		}
	}
	for _, s := range []string{"256 MiB", "2 GiB", "100,000 files", "1 MiB", "ten minutes"} {
		if !strings.Contains(publish, s) {
			t.Errorf("the publish section does not say %q", s)
		}
	}
	var schema struct {
		Properties map[string]struct {
			Default    any `json:"default"`
			Properties map[string]struct {
				Default any `json:"default"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(settingsSchema, &schema); err != nil {
		t.Fatal(err)
	}
	p := schema.Properties
	if p["latestGuard"].Default != true || p["serveRemovedVersions"].Default != false ||
		p["latestStrategy"].Default != "highest-member-latest" || p["distTagStrategy"].Default != "member-order" ||
		p["audit"].Properties["mode"].Default != "forward" {
		t.Errorf("the settings the guide describes changed their defaults: %+v", p)
	}
	local := renderGuide(t, guideData(formats.Local, "npm", false, false))["npm.local.what"]
	if !strings.Contains(local, "`latestGuard`, on by default") || !strings.Contains(remote, "`serveRemovedVersions`") {
		t.Error("the guide does not name the settings")
	}
}

// TestR_UI_049_AnAnonymousReaderGetsNoCredentialSteps: for a reader who is not signed in
// the sections that need a credential say nothing, and no text names the credential.
func TestR_UI_049_AnAnonymousReaderGetsNoCredentialSteps(t *testing.T) {
	for _, class := range []formats.Class{formats.Local, formats.Virtual} {
		for _, c := range New().Descriptor().Clients {
			got := renderGuide(t, guideData(class, c.ID, true, false))
			for _, id := range []string{"npm.publish", "npm.maintain"} {
				if strings.TrimSpace(got[id]) != "" {
					t.Errorf("%s %s: %s says %q", class, c.ID, id, got[id])
				}
			}
			if a := got["npm.authenticate"]; !strings.Contains(a, "without signing in") || strings.Contains(a, "personal access token") {
				t.Errorf("%s %s: %q", class, c.ID, a)
			}
			for id, text := range got {
				if strings.Contains(text, formats.SetupTokenRef) && id != "npm.configure" {
					t.Errorf("%s %s: %s names the credential", class, c.ID, id)
				}
			}
		}
	}
}

// TestR_UI_049_TheEndpointSectionIsForEndpointsWithRules: the note on exposure rules
// appears only on such an endpoint.
func TestR_UI_049_TheEndpointSectionIsForEndpointsWithRules(t *testing.T) {
	if got := renderGuide(t, guideData(formats.Virtual, "npm", false, false))["npm.endpoint"]; strings.TrimSpace(got) != "" {
		t.Errorf("without rules: %q", got)
	}
	got := renderGuide(t, guideData(formats.Virtual, "npm", false, true))["npm.endpoint"]
	for _, s := range []string{"`endpoint_read_only`", "`npm_audit_unavailable`", "`E404`"} {
		if !strings.Contains(got, s) {
			t.Errorf("with rules, %q is missing:\n%s", s, got)
		}
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
