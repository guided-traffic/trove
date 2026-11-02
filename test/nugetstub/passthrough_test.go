package nugetstub

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestR_NUGET_015_GzipAlwaysCodesTheDocumentsWhateverTheClientAsked: with Mode.GzipAlways the
// version list and both registration hives answer gzip-coded to a client that asks for identity,
// as nuget.org's gzip hive does; the service index stays plain.
func TestR_NUGET_015_GzipAlwaysCodesTheDocumentsWhateverTheClientAsked(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{GzipAlways: true}); err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"Accept-Encoding": "identity"}
	for name, u := range map[string]string{
		"version list":     f.resource("PackageBaseAddress/3.0.0") + "contoso.versions/index.json",
		"gzip hive index":  f.resource("RegistrationsBaseUrl/3.6.0") + "contoso.versions/index.json",
		"plain hive index": f.resource("RegistrationsBaseUrl") + "newtonsoft.json/index.json",
		"page":             f.resource("RegistrationsBaseUrl/3.6.0") + "contoso.versions/page/1.0.0/1.63.0.json",
	} {
		r := f.do(http.MethodGet, u, identity, nil)
		if r.status != http.StatusOK || r.header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: %d %q", name, r.status, r.header.Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(bytes.NewReader(r.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b, _ := io.ReadAll(zr); !json.Valid(b) {
			t.Errorf("%s: the inflated body is no JSON", name)
		}
	}
	if r := f.do(http.MethodGet, f.api+"/v3/index.json", identity, nil); r.header.Get("Content-Encoding") != "" {
		t.Error("the service index was coded")
	}
}

// TestR_NUGET_015_TheCorpusDeprecatesAVersionAndRecordsAnAdvisory: Newtonsoft.Json 13.0.1 carries
// a deprecation with an alternate package and an advisory, with the JSON-LD @id members nuget.org
// writes, which point at the upstream; 13.0.3 carries neither.
func TestR_NUGET_015_TheCorpusDeprecatesAVersionAndRecordsAnAdvisory(t *testing.T) {
	f := newFixture(t, Config{})
	var idx struct {
		Items []struct {
			Items []struct {
				CatalogEntry map[string]json.RawMessage `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	f.getJSON(f.resource("RegistrationsBaseUrl/3.6.0")+"newtonsoft.json/index.json", &idx)
	leaves := idx.Items[0].Items
	if len(leaves) != 2 {
		t.Fatalf("%d leaves", len(leaves))
	}
	old, current := leaves[0].CatalogEntry, leaves[1].CatalogEntry
	dep, vul := string(old["deprecation"]), string(old["vulnerabilities"])
	if !strings.Contains(dep, `"reasons":["Legacy","CriticalBugs"]`) || !strings.Contains(dep, `"alternatePackage"`) || !strings.Contains(dep, `"@id":"`+f.api) {
		t.Errorf("deprecation = %s", dep)
	}
	if !strings.Contains(vul, `"advisoryUrl":"`+Advisory+`"`) || !strings.Contains(vul, `"severity":"2"`) || !strings.Contains(vul, `"@id":"`+f.api) {
		t.Errorf("vulnerabilities = %s", vul)
	}
	if current["deprecation"] != nil || current["vulnerabilities"] != nil {
		t.Errorf("13.0.3: %s %s", current["deprecation"], current["vulnerabilities"])
	}
}
