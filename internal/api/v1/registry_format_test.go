package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// The descriptor of GET /api/v1/formats carries the base path of the registry root and the hosts that receive credentials by
// default for the OCI format, and the setup address of a registry-root format gains the image prefix (ADR-161, Q-OCI-4). The
// tests carry no requirement ID: the snippets that read the prefix are the work package of the client guide.
func TestTheFormatListServesTheRegistryRootAndTheCredentialHosts(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
	s.handler = val.Handler(t, s.handler)
	w := s.get("/api/v1/formats")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Formats []struct {
			Name                   string   `json:"name"`
			Classes                []string `json:"classes"`
			ProtocolBasePath       string   `json:"protocolBasePath"`
			DefaultUpstreamURL     *string  `json:"defaultUpstreamUrl"`
			DefaultExtraHosts      []string `json:"defaultExtraHosts"`
			DefaultCredentialHosts []string `json:"defaultCredentialHosts"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range body.Formats {
		if f.DefaultCredentialHosts == nil {
			t.Errorf("%s: defaultCredentialHosts is not a list", f.Name)
		}
		if f.Name != "oci" {
			if f.ProtocolBasePath == catalog.RegistryRootBasePath {
				t.Errorf("%s serves the registry root", f.Name)
			}
			continue
		}
		found = true
		if f.ProtocolBasePath != "/v2/{project}/{key}/" || f.DefaultUpstreamURL == nil || *f.DefaultUpstreamURL != "https://registry-1.docker.io/" ||
			!reflect.DeepEqual(f.DefaultCredentialHosts, []string{"auth.docker.io"}) || !reflect.DeepEqual(f.DefaultExtraHosts, []string{"auth.docker.io"}) {
			t.Errorf("oci descriptor = %+v", f)
		}
	}
	if !found {
		t.Fatal("the list has no oci format")
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
