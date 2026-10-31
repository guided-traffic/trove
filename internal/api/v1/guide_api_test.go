package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/readme"
)

func guideOf(t *testing.T, w *httptest.ResponseRecorder) gen.Guide {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var g gen.Guide
	if err := json.Unmarshal(w.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func sectionIDs(g gen.Guide) []string {
	var out []string
	for _, s := range g.Sections {
		out = append(out, s.Id)
	}
	return out
}

func sectionOf(t *testing.T, g gen.Guide, id string) gen.GuideSection {
	t.Helper()
	for _, s := range g.Sections {
		if s.Id == id {
			return s
		}
	}
	t.Fatalf("no section %s in %v", id, sectionIDs(g))
	return gen.GuideSection{}
}

// TestR_API_053_TheGuideShowsTheSnippetsOfTheClientSetup: for every client, the
// snippets the guide attaches are the client setup's answer for the same repository
// with credentials=env, byte for byte.
func TestR_API_053_TheGuideShowsTheSnippetsOfTheClientSetup(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	for _, client := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
		g := guideOf(t, get(h, "/api/v1/repositories/npm-local/guide?client="+client))
		var setup gen.ClientSetup
		w := get(h, "/api/v1/repositories/npm-local/client-setup?credentials=env&client="+client)
		if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		for _, tab := range []string{"configure", "install"} {
			var want []gen.ClientSnippet
			for _, sn := range setup.Snippets {
				if string(sn.Tab) == tab {
					want = append(want, sn)
				}
			}
			got := sectionOf(t, g, "npm."+tab).Snippets
			if len(want) == 0 || !slices.EqualFunc(got, want, snippetsEqual) {
				t.Errorf("%s %s: guide %+v, setup %+v", client, tab, got, want)
			}
		}
		if g.RegistryUrl != setup.RegistryUrl || g.Credentials != gen.GuideCredentialsEnv || g.Client != client {
			t.Errorf("%s: %+v", client, g)
		}
	}
}

func snippetsEqual(a, b gen.ClientSnippet) bool {
	return a.Id == b.Id && a.Tab == b.Tab && a.Content == b.Content && a.Language == b.Language &&
		(a.FileName == nil) == (b.FileName == nil) && (a.FileName == nil || *a.FileName == *b.FileName)
}

// TestR_API_053_TheSectionsFollowTheClassInTopicOrder: a local repository gets its
// sections in topic order, publishing included; a remote one has none of the writes;
// the HTML is the sanitized rendering and the credential reference is the configured
// variable's.
func TestR_API_053_TheSectionsFollowTheClassInTopicOrder(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	local := guideOf(t, get(h, "/api/v1/repositories/npm-local/guide?client=yarn"))
	want := []string{"npm.local.what", "npm.configure", "npm.authenticate", "npm.install", "npm.publish", "npm.maintain", "npm.audit", "npm.lockfiles", "npm.troubleshooting"}
	if got := sectionIDs(local); !slices.Equal(got, want) {
		t.Fatalf("sections %v, want %v", got, want)
	}
	if local.PublishTarget == nil || *local.PublishTarget != "npm-local" || local.Repository == nil || *local.Repository != "npm-local" {
		t.Errorf("%+v", local)
	}
	what := sectionOf(t, local, "npm.local.what")
	if !strings.Contains(what.Html, `<h3 id="user-content-published-versions-never-change">`) || strings.Contains(what.Html, "{{") {
		t.Errorf("not rendered: %s", what.Html)
	}
	conf := sectionOf(t, local, "npm.configure")
	if !strings.Contains(conf.Html, "${MY_TOKEN}") || strings.Contains(conf.Html, "TROVE_TOKEN") {
		t.Errorf("the credential reference is not the configured variable's: %s", conf.Html)
	}

	remote := guideOf(t, get(h, "/api/v1/repositories/npm-remote/guide?client=npm"))
	for _, id := range sectionIDs(remote) {
		if id == "npm.publish" || id == "npm.maintain" || id == "npm.local.what" {
			t.Errorf("a remote repository's guide has %s", id)
		}
	}
	if remote.PublishTarget != nil || sectionIDs(remote)[0] != "npm.remote.what" {
		t.Errorf("%+v", remote)
	}
	if strings.Contains(sectionOf(t, remote, "npm.remote.what").Html, "<script") {
		t.Error("unsanitized")
	}
}

// TestR_API_053_ACallerWhoMayNotPublishIsTold: without deploy on the target the guide
// names no target and says why.
func TestR_API_053_ACallerWhoMayNotPublishIsTold(t *testing.T) {
	g := guideOf(t, get(setupServer(t, "https://trove.example.com", false), "/api/v1/repositories/npm-local/guide?client=npm"))
	if g.PublishTarget != nil {
		t.Fatalf("%v", *g.PublishTarget)
	}
	if got := sectionOf(t, g, "npm.publish").Html; !strings.Contains(got, "you do not hold the deploy permission") {
		t.Errorf("%s", got)
	}
}

// TestR_API_053_AnAnonymousReaderGetsNoCredentialSteps: an anonymous caller gets the
// snippets without a credential line and no publishing sections.
func TestR_API_053_AnAnonymousReaderGetsNoCredentialSteps(t *testing.T) {
	h := setupServerAs(t, "https://trove.example.com", true, nil)
	g := guideOf(t, get(h, "/api/v1/repositories/npm-local/guide?client=yarn"))
	if g.Credentials != gen.GuideCredentialsNone || g.PublishTarget != nil {
		t.Fatalf("%+v", g)
	}
	for _, id := range sectionIDs(g) {
		if id == "npm.publish" || id == "npm.maintain" {
			t.Errorf("an anonymous reader gets %s", id)
		}
	}
	// The configuration a reader copies holds no credential; the troubleshooting table
	// still says what a 401 asks for.
	var shown []string
	for _, id := range []string{"npm.configure", "npm.authenticate", "npm.install"} {
		s := sectionOf(t, g, id)
		shown = append(shown, s.Html)
		for _, sn := range s.Snippets {
			shown = append(shown, sn.Content)
		}
	}
	for _, s := range []string{"TOKEN", "npmAuthToken", "npmAlwaysAuth"} {
		if strings.Contains(strings.Join(shown, "\n"), s) {
			t.Errorf("the anonymous guide holds %q", s)
		}
	}
}

// TestR_API_053_TheGuideOfAFormat: the guide without a repository names the address
// with the placeholder and no target; it refuses what it cannot answer.
func TestR_API_053_TheGuideOfAFormat(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	g := guideOf(t, get(h, "/api/v1/formats/npm/guide?class=virtual&client=pnpm"))
	if g.Repository != nil || g.PublishTarget != nil || g.RegistryUrl != "https://trove.example.com/api/npm/"+formats.GuideRepositoryPlaceholder+"/" ||
		g.Class != gen.GuideClassVirtual || sectionIDs(g)[0] != "npm.virtual.what" {
		t.Fatalf("%+v", g)
	}
	conf := sectionOf(t, g, "npm.configure").Snippets
	if len(conf) != 1 || !strings.Contains(conf[0].Content, "//trove.example.com/api/npm/<repository>/:_authToken=${MY_TOKEN}") {
		t.Errorf("%+v", conf)
	}
	for q, want := range map[string]int{
		"/api/v1/formats/nuget/guide?class=local&client=dotnet":  404,
		"/api/v1/formats/npm/guide?class=bogus&client=npm":       400,
		"/api/v1/formats/npm/guide?class=local&client=bun":       400,
		"/api/v1/formats/generic/guide?class=local&client=curl":  200,
		"/api/v1/formats/generic/guide?class=remote&client=curl": 400,
		"/api/v1/formats/generic/guide?class=local&client=npm":   400,
		"/api/v1/repositories/files/guide?client=curl":           200,
		"/api/v1/repositories/files/guide?client=npm":            400,
		"/api/v1/repositories/npm-local/guide?client=bun":        400,
		"/api/v1/repositories/ghost/guide?client=npm":            404,
	} {
		if w := get(h, q); w.Code != want {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
}

// TestR_API_053_AFormatWithoutClientsHasNoGuide: a format without clients offers no
// client, so its guide answers 400 whichever client is asked for.
func TestR_API_053_AFormatWithoutClientsHasNoGuide(t *testing.T) {
	f, _ := formats.Lookup("generic")
	for _, client := range []string{"curl", ""} {
		e, ok := errs.As(guideClient(clientless{f}, client))
		if !ok || e.Status() != 400 || e.Code != errs.CodeInvalid {
			t.Errorf("%q: %v", client, e)
		}
	}
	if err := guideClient(f, "curl"); err != nil {
		t.Errorf("generic offers curl: %v", err)
	}
}

// clientless is a format without clients and therefore without a guide.
type clientless struct{ formats.Format }

func (c clientless) Descriptor() formats.Descriptor {
	d := c.Format.Descriptor()
	d.Clients = nil
	return d
}

func (clientless) Guide() []formats.GuideSection { return nil }

// TestR_GENERIC_016_TheGuideShowsTheSnippetsOfTheClientSetup: the configure, install
// and publish snippets of the guide of a generic repository are the client setup's
// answer for curl with credentials=env, byte for byte.
func TestR_GENERIC_016_TheGuideShowsTheSnippetsOfTheClientSetup(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	g := guideOf(t, get(h, "/api/v1/repositories/files/guide?client=curl"))
	var setup gen.ClientSetup
	w := get(h, "/api/v1/repositories/files/client-setup?credentials=env&client=curl")
	if err := json.Unmarshal(w.Body.Bytes(), &setup); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, tab := range []string{"configure", "install", "publish"} {
		var want []gen.ClientSnippet
		for _, sn := range setup.Snippets {
			if string(sn.Tab) == tab {
				want = append(want, sn)
			}
		}
		got := sectionOf(t, g, "generic."+tab).Snippets
		if len(want) != 1 || !slices.EqualFunc(got, want, snippetsEqual) {
			t.Errorf("%s: guide %+v, setup %+v", tab, got, want)
		}
	}
	if g.RegistryUrl != "https://trove.example.com/files/files/" || g.Format != "generic" || g.Class != gen.GuideClassLocal || g.Credentials != gen.GuideCredentialsEnv {
		t.Errorf("%+v", g)
	}
	want := []string{
		"generic.local.what", "generic.configure", "generic.authenticate", "generic.install", "generic.publish", "generic.maintain", "generic.troubleshooting",
	}
	if !slices.Equal(sectionIDs(g), want) {
		t.Errorf("sections %v", sectionIDs(g))
	}
}

// TestR_API_053_AddressesNeverComeFromTheHostOfTheRequest: without a configured base URL
// both guides answer 503.
func TestR_API_053_AddressesNeverComeFromTheHostOfTheRequest(t *testing.T) {
	h := setupServer(t, "", true)
	for _, p := range []string{"/api/v1/repositories/npm-local/guide?client=npm", "/api/v1/formats/npm/guide?class=local&client=npm"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "evil.example"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 503 || strings.Contains(w.Body.String(), "evil.example") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
}

// TestR_API_053_NoGuideHoldsAToken: no answer, for any class, client and caller, matches
// the shape of a token.
func TestR_API_053_NoGuideHoldsAToken(t *testing.T) {
	shape := regexp.MustCompile(`trv_(pat|sat|cit)_`)
	for _, h := range []http.Handler{setupServer(t, "https://trove.example.com", true), setupServerAs(t, "https://trove.example.com", false, nil)} {
		for _, p := range []string{"repositories/npm-local/guide?", "repositories/npm-remote/guide?", "formats/npm/guide?class=virtual&"} {
			for _, client := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
				w := get(h, "/api/v1/"+p+"client="+client)
				if w.Code != 200 || shape.Match(w.Body.Bytes()) {
					t.Errorf("%s %s: %d", p, client, w.Code)
				}
			}
		}
		for _, p := range []string{"repositories/files/guide?client=curl", "formats/generic/guide?class=local&client=curl"} {
			if w := get(h, "/api/v1/"+p); w.Code != 200 || shape.Match(w.Body.Bytes()) {
				t.Errorf("%s: %d", p, w.Code)
			}
		}
	}
}

// TestR_API_053_TheEndpointNoteIsForEndpointsWithRules: the section on exposure rules is
// rendered only for an endpoint with rules, and a section that says nothing is left out.
func TestR_API_053_TheEndpointNoteIsForEndpointsWithRules(t *testing.T) {
	rd, err := readme.New(readme.Options{MaxSourceBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := formats.Lookup("npm")
	in := guideInput{
		Format: f, Class: formats.Virtual, Client: "npm", TokenEnvVar: "TROVE_TOKEN",
		Data: formats.SetupData{RegistryURL: "https://trove.example.com/api/npm/npm-all/", Repository: "npm-all", RegistryKey: "//trove.example.com/api/npm/npm-all/"},
	}
	without, err := renderGuide(rd, in)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sectionIDs(without), "npm.endpoint") || without.ExposureRules {
		t.Errorf("without rules: %v", sectionIDs(without))
	}
	in.ExposureRules = true
	with, err := renderGuide(rd, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(t, with, "npm.endpoint").Html; !strings.Contains(got, "endpoint_read_only") || !with.ExposureRules {
		t.Errorf("with rules: %s", got)
	}
}

// TestR_API_053_TheFormatsNameTheirClients: the catalog lists npm's four clients in the
// format's order and curl for generic.
func TestR_API_053_TheFormatsNameTheirClients(t *testing.T) {
	w := get(setupServer(t, "https://trove.example.com", true), "/api/v1/formats")
	var out gen.FormatList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	got := map[string][]string{}
	for _, f := range out.Formats {
		got[f.Name] = []string{}
		for _, c := range f.Clients {
			got[f.Name] = append(got[f.Name], c.Id+"="+c.DisplayName)
		}
	}
	if !slices.Equal(got["npm"], []string{"npm=npm", "pnpm=pnpm", "yarn=Yarn Berry (2 and later)", "yarn-classic=Yarn Classic (1.x)"}) || !slices.Equal(got["generic"], []string{"curl=curl"}) {
		t.Fatalf("%v", got)
	}
}
