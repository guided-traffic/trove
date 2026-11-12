package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// The descriptor of GET /api/v1/formats carries the base path of the registry root for the OCI format, and the setup address of
// a registry-root format gains the image prefix (ADR-161, Q-OCI-4). The tests carry no requirement ID: the snippets that read the
// prefix are the work package of the client guide.
func TestTheFormatListServesTheRegistryRoot(t *testing.T) {
	found := false
	for _, f := range listedFormats(t) {
		if f.Name != "oci" {
			if f.ProtocolBasePath == catalog.RegistryRootBasePath {
				t.Errorf("%s serves the registry root", f.Name)
			}
			continue
		}
		found = true
		if f.ProtocolBasePath != "/v2/{project}/{key}/" {
			t.Errorf("oci descriptor = %+v", f)
		}
	}
	if !found {
		t.Fatal("the list has no oci format")
	}
}

type listedPreset struct {
	ID          string                   `json:"id"`
	DisplayName string                   `json:"displayName"`
	URL         string                   `json:"url"`
	ExtraHosts  []gen.UpstreamPresetHost `json:"extraHosts"`
	Default     bool                     `json:"default"`
}

type listedFormat struct {
	Name             string         `json:"name"`
	Classes          []string       `json:"classes"`
	ProtocolBasePath string         `json:"protocolBasePath"`
	UpstreamPresets  []listedPreset `json:"upstreamPresets"`
}

// listedFormats reads GET /api/v1/formats through the document's response validation.
func listedFormats(t *testing.T) []listedFormat {
	t.Helper()
	val := apitest.New(t, EmbeddedYAML())
	s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
	s.handler = val.Handler(t, s.handler)
	w := s.get("/api/v1/formats")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Formats []listedFormat `json:"formats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Formats
}

// TestR_REPO_062_TheFormatListServesThePresets: GET /api/v1/formats lists oci with its four presets in their order, docker-hub
// the default with auth.docker.io receiving the credential, and generic with none; every format serves a list.
func TestR_REPO_062_TheFormatListServesThePresets(t *testing.T) {
	byName := map[string]listedFormat{}
	for _, f := range listedFormats(t) {
		if f.UpstreamPresets == nil {
			t.Errorf("%s: upstreamPresets is not a list", f.Name)
		}
		byName[f.Name] = f
	}
	want := []listedPreset{
		{
			ID: "docker-hub", DisplayName: "Docker Hub", URL: "https://registry-1.docker.io/", Default: true,
			ExtraHosts: []gen.UpstreamPresetHost{{Pattern: "auth.docker.io", SendCredentials: true}},
		},
		{ID: "quay", DisplayName: "Quay", URL: "https://quay.io/", ExtraHosts: []gen.UpstreamPresetHost{}},
		{ID: "ghcr", DisplayName: "GitHub (ghcr.io)", URL: "https://ghcr.io/", ExtraHosts: []gen.UpstreamPresetHost{}},
		{
			ID: "gitlab", DisplayName: "GitLab", URL: "https://registry.gitlab.com/",
			ExtraHosts: []gen.UpstreamPresetHost{{Pattern: "gitlab.com", SendCredentials: true}},
		},
	}
	if got := byName["oci"].UpstreamPresets; !reflect.DeepEqual(got, want) {
		t.Errorf("oci presets = %+v", got)
	}
	if got, ok := byName["generic"]; !ok || len(got.UpstreamPresets) != 0 {
		t.Errorf("generic = %+v, %v", got, ok)
	}
}

func TestImagePrefixIsTheHostAndTheRepositoryOfARegistryRootFormat(t *testing.T) {
	f, ok := formats.Lookup("oci")
	if !ok {
		t.Fatal("oci is not registered")
	}
	for registry, want := range map[string]string{
		"https://trove.example.com/v2/web/docker/":         "trove.example.com/web/docker",
		"https://trove.example.com:8443/v2/web/docker/":    "trove.example.com:8443/web/docker",
		"http://localhost:5000/v2/<project>/<repository>/": "localhost:5000/<project>/<repository>",
	} {
		if got := imagePrefix(f, registry); got != want {
			t.Errorf("imagePrefix(%q) = %q, want %q", registry, got, want)
		}
	}
	npm, _ := formats.Lookup("npm")
	if got := imagePrefix(npm, "https://trove.example.com/api/npm/web/npm-all/"); got != "" {
		t.Errorf("a format without the registry root has the image prefix %q", got)
	}
}

// TestR_UI_052_TheFormatListSaysWhichFormatsAreScanned: the screens show a column of vulnerabilities only for a format whose versions
// are matched against advisories.
func TestR_UI_052_TheFormatListSaysWhichFormatsAreScanned(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
	s.handler = val.Handler(t, s.handler)
	w := s.get("/api/v1/formats")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Formats []struct {
			Name                  string `json:"name"`
			VulnerabilityScanning bool   `json:"vulnerabilityScanning"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"npm": true, "nuget": true, "oci": true, "deb": false, "generic": false}
	seen := 0
	for _, f := range body.Formats {
		if w, ok := want[f.Name]; ok {
			seen++
			if f.VulnerabilityScanning != w {
				t.Errorf("%s: vulnerabilityScanning = %v, want %v", f.Name, f.VulnerabilityScanning, w)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("saw %d of the formats %v", seen, want)
	}
}
