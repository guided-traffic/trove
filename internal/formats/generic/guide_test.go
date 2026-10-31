package generic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

const registry = "https://trove.example.com/files/generic-local/"

func guideData(anonymous, rules bool, target string) formats.GuideData {
	return formats.GuideData{
		SetupData: formats.SetupData{
			RegistryURL: registry, Repository: "generic-local", Endpoint: "default",
			RegistryKey: strings.TrimPrefix(registry, "https:"), RequiresAuth: !anonymous,
		},
		Class: formats.Local, Client: ClientCurl, PublishTarget: target, Anonymous: anonymous, ExposureRules: rules,
		TokenEnvVar: "TROVE_TOKEN",
	}
}

// renderGuide runs every section of the guide over d.
func renderGuide(t *testing.T, d formats.GuideData) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range (Format{}).Guide() {
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

// everyRendering is the guide for every reader, endpoint kind and publish target.
func everyRendering(t *testing.T) map[string]string {
	t.Helper()
	all := map[string]string{}
	for _, anonymous := range []bool{false, true} {
		for _, rules := range []bool{false, true} {
			for _, target := range []string{"", "generic-local"} {
				for id, text := range renderGuide(t, guideData(anonymous, rules, target)) {
					all[fmt.Sprintf("%s anonymous=%v rules=%v target=%q", id, anonymous, rules, target)] = text
				}
			}
		}
	}
	return all
}

func template(t *testing.T, section string) formats.SetupTemplate {
	t.Helper()
	for _, tp := range (Format{}).SetupTemplates() {
		if tp.Section == section {
			return tp
		}
	}
	t.Fatalf("no %s template", section)
	return formats.SetupTemplate{}
}

// TestR_GENERIC_016_TheClientIsCurl: the descriptor lists curl, and curl has a
// configure, an install and a publish template in a shell.
func TestR_GENERIC_016_TheClientIsCurl(t *testing.T) {
	if c := (Format{}).Descriptor().Clients; !reflect.DeepEqual(c, []formats.Client{{ID: "curl", DisplayName: "curl"}}) {
		t.Fatalf("clients = %+v", c)
	}
	var sections []string
	for _, tp := range (Format{}).SetupTemplates() {
		if tp.Client != ClientCurl || tp.Lang != "shell" || tp.File != "" {
			t.Errorf("%+v", tp)
		}
		sections = append(sections, tp.Section)
	}
	if !slices.Equal(sections, []string{"configure", "install", "publish"}) {
		t.Errorf("sections = %v", sections)
	}
}

// TestR_GENERIC_016_TheTokenIsNoArgumentOfCurl (Q-GENERIC-9): the one line that
// holds the token is a line of curl's configuration on standard input, the body of
// the here-document of the function; dropping it, as credentials=none does, leaves a
// function that sends no credential and still runs.
func TestR_GENERIC_016_TheTokenIsNoArgumentOfCurl(t *testing.T) {
	conf := template(t, "configure").Template
	lines := strings.Split(conf, "\n")
	want := []string{
		CurlFunction + "() {",
		`  curl --silent --show-error --config - "$@" <<EOF`,
		`header = "Authorization: Bearer ` + formats.SetupTokenRef + `"`,
		"EOF",
		"}",
		"",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("the function is\n%s", conf)
	}
	var holding []string
	for _, l := range lines {
		if strings.Contains(l, formats.SetupTokenRef) {
			holding = append(holding, l)
		}
	}
	if len(holding) != 1 || !strings.HasPrefix(holding[0], "header = ") {
		t.Errorf("the token is referenced by %q", holding)
	}
	for _, section := range []string{"install", "publish"} {
		tp := template(t, section).Template
		if !strings.HasPrefix(tp, CurlFunction+" --fail ") || !strings.Contains(tp, `"{{.RegistryURL}}<path>"`) || strings.Contains(tp, formats.SetupTokenRef) {
			t.Errorf("%s: %q", section, tp)
		}
	}
}

var troubleshootingRow = regexp.MustCompile("(?m)^\\| (\\d{3}) \\| `([a-z_]+)` \\|")

// TestR_GENERIC_016_TheTroubleshootingCodesAreTroves: every row of the
// troubleshooting table names a code of the problem catalog, under the status trove
// answers it with, and the table covers the statuses /files/ answers.
func TestR_GENERIC_016_TheTroubleshootingCodesAreTroves(t *testing.T) {
	text := renderGuide(t, guideData(false, false, "generic-local"))["generic.troubleshooting"]
	rows := troubleshootingRow.FindAllStringSubmatch(text, -1)
	if len(rows) < 25 {
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
		if got := errs.E(info.Kind, info.Code, "").Status(); got != status {
			t.Errorf("%s is listed under %d and answers %d", row[2], status, got)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 405, 409, 412, 413, 416, 429, 503} {
		if !statuses[s] {
			t.Errorf("the table has no row for %d", s)
		}
	}
}

var codeSpan = regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")

// TestR_GENERIC_016_EveryCodeTheGuideNamesExists: a snake_case word in code anywhere
// in the guide is an error code of the catalog or the function of the snippets.
func TestR_GENERIC_016_EveryCodeTheGuideNamesExists(t *testing.T) {
	for where, text := range everyRendering(t) {
		for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
			if _, ok := errs.Lookup(m[1]); !ok && m[1] != CurlFunction {
				t.Errorf("%s: %q is no code of internal/errs", where, m[1])
			}
		}
	}
}

// TestR_GENERIC_016_TheGuideStatesTheLimits: the limits the guide names are the ones
// of the path rule and the defaults of the configuration.
func TestR_GENERIC_016_TheGuideStatesTheLimits(t *testing.T) {
	g := renderGuide(t, guideData(false, false, "generic-local"))
	if repository.MaxPathBytes != 4096 || repository.MaxSegmentBytes != 255 ||
		!strings.Contains(g["generic.local.what"], "at most 4096 bytes and a segment at most 255 bytes") {
		t.Errorf("path limits %d and %d", repository.MaxPathBytes, repository.MaxSegmentBytes)
	}
	upload, _ := reflect.TypeFor[config.StorageUpload]().FieldByName("MaxSize")
	if upload.Tag.Get("default") != "10GiB" || !strings.Contains(g["generic.publish"], "at most 10 GiB") || !strings.Contains(g["generic.troubleshooting"], "10 GiB by default") {
		t.Errorf("storage.upload.max_size defaults to %s", upload.Tag.Get("default"))
	}
	del, _ := reflect.TypeFor[config.StorageDelete]().FieldByName("MaxSyncItems")
	if del.Tag.Get("default") != "10000" || !strings.Contains(g["generic.maintain"], "more than 10,000 items (the default limit)") {
		t.Errorf("storage.delete.max_sync_items defaults to %s", del.Tag.Get("default"))
	}
}

// TestR_GENERIC_016_TheDigestsTheGuideNamesAreTroves: the ETag is the quoted
// hexadecimal SHA-256, and the Repr-Digest the publish section builds with openssl and
// base64 is one trove parses and checks.
func TestR_GENERIC_016_TheDigestsTheGuideNamesAreTroves(t *testing.T) {
	body := []byte("installer bytes")
	sum := sha256.Sum256(body)
	if got := (items.Item{Digest: sum}).ETag(); got != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Errorf("ETag %s", got)
	}
	g := renderGuide(t, guideData(false, false, "generic-local"))
	if !strings.Contains(g["generic.install"], "as the `ETag`, in hexadecimal and quoted, and as `Repr-Digest: sha-256=:<base64>:`") {
		t.Error("the install section does not name the digests")
	}
	if !strings.Contains(g["generic.publish"], `--header "Repr-Digest: sha-256=:$(openssl dgst -sha256 -binary <file> | base64):"`) {
		t.Fatal("the publish section does not build the digest")
	}
	h := http.Header{}
	h.Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
	exp, err := edge.ParseReprDigest(h)
	if err != nil || !slices.Equal(exp.SHA256, sum[:]) {
		t.Errorf("%v %x", err, exp.SHA256)
	}
}

var plainCurl = regexp.MustCompile("(?:^|[^_a-z])curl (--[a-z-]+)")

// TestR_GENERIC_016_TheGuideCallsTheFunctionOfTheSnippet: the commands of the guide
// use the function the configure snippet defines and carry no credential of their
// own; the one plain curl option it names is the one it advises against.
func TestR_GENERIC_016_TheGuideCallsTheFunctionOfTheSnippet(t *testing.T) {
	calls := 0
	for where, text := range everyRendering(t) {
		if strings.Contains(text, formats.SetupTokenRef) {
			t.Errorf("%s names the credential", where)
		}
		for _, m := range plainCurl.FindAllStringSubmatch(text, -1) {
			if m[1] != "--user" {
				t.Errorf("%s calls curl %s without the function", where, m[1])
			}
		}
		calls += strings.Count(text, CurlFunction+" --fail")
	}
	if calls == 0 {
		t.Error("the guide calls the function nowhere")
	}
}

// TestR_UI_049_AnAnonymousReaderGetsNoCredentialSteps: for a reader who is not signed
// in the sections that need a credential say nothing.
func TestR_UI_049_AnAnonymousReaderGetsNoCredentialSteps(t *testing.T) {
	got := renderGuide(t, guideData(true, false, ""))
	for _, id := range []string{"generic.publish", "generic.maintain"} {
		if strings.TrimSpace(got[id]) != "" {
			t.Errorf("%s says %q", id, got[id])
		}
	}
	if a := got["generic.authenticate"]; !strings.Contains(a, "without signing in") || strings.Contains(a, "personal access token") {
		t.Errorf("%q", a)
	}
	if c := got["generic.configure"]; !strings.Contains(c, "sends no credential") || strings.Contains(c, "Authorization") {
		t.Errorf("%q", c)
	}
}

// TestR_UI_049_AReaderWhoMayNotUploadIsToldSo: the publish section names the missing
// permission instead of the commands when the reader may not deploy.
func TestR_UI_049_AReaderWhoMayNotUploadIsToldSo(t *testing.T) {
	if got := renderGuide(t, guideData(false, false, ""))["generic.publish"]; !strings.Contains(got, "You cannot upload to `generic-local`") || strings.Contains(got, "--upload-file") {
		t.Errorf("%q", got)
	}
	if got := renderGuide(t, guideData(false, false, "generic-local"))["generic.publish"]; !strings.Contains(got, "--upload-file") {
		t.Errorf("%q", got)
	}
}

// TestR_UI_049_TheEndpointSectionIsForEndpointsWithRules: the note on exposure rules
// appears only on such an endpoint.
func TestR_UI_049_TheEndpointSectionIsForEndpointsWithRules(t *testing.T) {
	if got := renderGuide(t, guideData(false, false, ""))["generic.endpoint"]; strings.TrimSpace(got) != "" {
		t.Errorf("without rules: %q", got)
	}
	got := renderGuide(t, guideData(false, true, ""))["generic.endpoint"]
	for _, s := range []string{"`endpoint_read_only`", "`404`", "files surface"} {
		if !strings.Contains(got, s) {
			t.Errorf("with rules, %q is missing:\n%s", s, got)
		}
	}
}
