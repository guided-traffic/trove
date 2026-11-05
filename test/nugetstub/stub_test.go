package nugetstub

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture is a running stub with a client that trusts its CA. api and content are the two hosts of
// the service index: the same listener under 127.0.0.1 and localhost.
type fixture struct {
	t       *testing.T
	stub    *Server
	run     *Running
	client  *http.Client
	api     string
	content string
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	stub, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	tr := &http.Transport{TLSClientConfig: ss.ClientConfig(), DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	_, port, _ := net.SplitHostPort(run.Addr)
	return &fixture{
		t: t, stub: stub, run: run,
		client:  &http.Client{Transport: tr, Timeout: 20 * time.Second},
		api:     "https://127.0.0.1:" + port,
		content: "https://localhost:" + port,
	}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (f *fixture) do(method, rawURL string, header map[string]string, body io.Reader) reply {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, body)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatalf("%s %s: read: %v", method, rawURL, err)
	}
	return reply{resp.StatusCode, resp.Header, data}
}

func (f *fixture) get(rawURL string) reply { return f.do(http.MethodGet, rawURL, nil, nil) }

func (f *fixture) getJSON(rawURL string, into any) reply {
	f.t.Helper()
	r := f.get(rawURL)
	if r.status != http.StatusOK {
		f.t.Fatalf("GET %s: %d %s", rawURL, r.status, r.body)
	}
	if err := json.Unmarshal(r.body, into); err != nil {
		f.t.Fatalf("GET %s: %v", rawURL, err)
	}
	return r
}

func (f *fixture) counts() Counts { return f.stub.Counts() }

type serviceIndex struct {
	Version   string `json:"version"`
	Resources []struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	} `json:"resources"`
}

func (f *fixture) resource(typ string) string {
	f.t.Helper()
	var idx serviceIndex
	f.getJSON(f.api+"/v3/index.json", &idx)
	for _, r := range idx.Resources {
		if r.Type == typ {
			return r.ID
		}
	}
	f.t.Fatalf("service index has no %s", typ)
	return ""
}

func (f *fixture) setMode(m Mode) {
	f.t.Helper()
	if err := f.stub.SetMode(m); err != nil {
		f.t.Fatal(err)
	}
}

func TestR_NUGET_027_StartRequiresTLS(t *testing.T) {
	stub, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stub.Start("127.0.0.1:0", nil); err == nil {
		t.Fatal("Start without a certificate succeeded; the stub must not serve plain HTTP")
	}
	if _, err := stub.Start("127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12}); err == nil {
		t.Fatal("Start with a TLS config without a certificate succeeded")
	}
}

func TestR_NUGET_027_ServiceIndexSpreadsResourcesOverTwoHosts(t *testing.T) {
	f := newFixture(t, Config{})
	var idx serviceIndex
	f.getJSON(f.api+"/v3/index.json", &idx)
	if idx.Version != "3.0.0" {
		t.Fatalf("version %q", idx.Version)
	}
	want := map[string]string{
		"PackageBaseAddress/3.0.0":      f.content + "/v3-flatcontainer/",
		"SearchQueryService/3.5.0":      f.content + "/query",
		"RegistrationsBaseUrl":          f.api + "/v3/registration/",
		"RegistrationsBaseUrl/3.4.0":    f.api + "/v3/registration-gz/",
		"RegistrationsBaseUrl/3.6.0":    f.api + "/v3/registration-gz/",
		"RegistrationsBaseUrl/3.0.0-rc": f.api + "/v3/registration/",
	}
	got := map[string]string{}
	hosts := map[string]bool{}
	for _, r := range idx.Resources {
		got[r.Type] = r.ID
		u, _ := url.Parse(r.ID)
		hosts[u.Hostname()] = true
	}
	for typ, id := range want {
		if got[typ] != id {
			t.Errorf("%s = %q, want %q", typ, got[typ], id)
		}
	}
	if len(hosts) != 2 {
		t.Errorf("resources on hosts %v, want two", hosts)
	}
}

func TestR_NUGET_027_ConfiguredHostsAreUsedVerbatim(t *testing.T) {
	f := newFixture(t, Config{PublicURL: "https://api.example/", ContentURL: "https://content.example"})
	var idx serviceIndex
	f.getJSON(f.api+"/v3/index.json", &idx)
	for _, r := range idx.Resources {
		if strings.HasPrefix(r.Type, "PackageBaseAddress") && r.ID != "https://content.example/v3-flatcontainer/" {
			t.Errorf("PackageBaseAddress = %q", r.ID)
		}
		if strings.HasPrefix(r.Type, "RegistrationsBaseUrl") && !strings.HasPrefix(r.ID, "https://api.example/v3/registration") {
			t.Errorf("%s = %q", r.Type, r.ID)
		}
	}
	if _, err := New(Config{PublicURL: "api.example"}); err == nil {
		t.Error("a PublicURL without a scheme was accepted")
	}
}

func TestR_NUGET_027_FlatContainer(t *testing.T) {
	f := newFixture(t, Config{})
	base := f.resource("PackageBaseAddress/3.0.0")

	var list struct {
		Versions []string `json:"versions"`
	}
	f.getJSON(base+"newtonsoft.json/index.json", &list)
	if strings.Join(list.Versions, ",") != "13.0.1,13.0.3" {
		t.Fatalf("versions %v", list.Versions)
	}
	f.getJSON(base+"NEWTONSOFT.JSON/index.json", &list) // IDs match in any case

	nupkg := f.get(base + "newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg")
	if nupkg.status != http.StatusOK {
		t.Fatalf("nupkg: %d", nupkg.status)
	}
	zr, err := zip.NewReader(bytes.NewReader(nupkg.body), int64(len(nupkg.body)))
	if err != nil {
		t.Fatalf("the .nupkg is no zip archive: %v", err)
	}
	names := map[string]bool{}
	for _, zf := range zr.File {
		names[zf.Name] = true
	}
	for _, n := range []string{"Newtonsoft.Json.nuspec", "[Content_Types].xml", "_rels/.rels", "lib/netstandard2.0/Newtonsoft.Json.dll"} {
		if !names[n] {
			t.Errorf(".nupkg lacks %s (has %v)", n, names)
		}
	}

	nuspec := f.get(base + "newtonsoft.json/13.0.3/newtonsoft.json.nuspec")
	var spec struct {
		ID      string `xml:"metadata>id"`
		Version string `xml:"metadata>version"`
	}
	if err := xml.Unmarshal(nuspec.body, &spec); err != nil || spec.ID != "Newtonsoft.Json" || spec.Version != "13.0.3" {
		t.Fatalf("nuspec %q: %+v, %v", nuspec.body, spec, err)
	}

	for _, path := range []string{"nope/index.json", "newtonsoft.json/9.9.9/newtonsoft.json.9.9.9.nupkg", "newtonsoft.json/13.0.3/other.13.0.3.nupkg", "../index.json"} {
		if r := f.get(base + path); r.status != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, r.status)
		}
	}
	if r := f.do(http.MethodPost, base+"newtonsoft.json/index.json", nil, nil); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", r.status)
	}
	if h := f.do(http.MethodHead, base+"newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg", nil, nil); h.status != http.StatusOK || len(h.body) != 0 {
		t.Errorf("HEAD: %d with %d bytes", h.status, len(h.body))
	}
}

func TestR_NUGET_027_NupkgBytesAreDeterministic(t *testing.T) {
	c := Default()
	p, _ := c.Get("Contoso.Core.Client")
	v := p.Versions[0]
	a, b := Nupkg(p, v), Nupkg(p, v)
	if !bytes.Equal(a, b) {
		t.Fatal("two builds of one package differ")
	}
	other := Nupkg(p, p.Versions[len(p.Versions)-1])
	if bytes.Equal(a, other) {
		t.Fatal("two versions have the same bytes")
	}
	if PackageHash(a) == PackageHash(other) {
		t.Fatal("equal hashes for different bytes")
	}
}

type regIndex struct {
	ID    string `json:"@id"`
	Count int    `json:"count"`
	Items []struct {
		ID     string `json:"@id"`
		Count  int    `json:"count"`
		Lower  string `json:"lower"`
		Upper  string `json:"upper"`
		Parent string `json:"parent"`
		Items  []struct {
			ID           string `json:"@id"`
			CatalogEntry struct {
				ID             string `json:"id"`
				Version        string `json:"version"`
				Listed         bool   `json:"listed"`
				PackageContent string `json:"packageContent"`
				PackageHash    string `json:"packageHash"`
			} `json:"catalogEntry"`
			PackageContent string `json:"packageContent"`
			Registration   string `json:"registration"`
		} `json:"items"`
	} `json:"items"`
}

func TestR_NUGET_027_RegistrationOfASmallPackageIsInlined(t *testing.T) {
	f := newFixture(t, Config{})
	for _, hive := range []string{"RegistrationsBaseUrl", "RegistrationsBaseUrl/3.6.0"} {
		base := f.resource(hive)
		var idx regIndex
		f.getJSON(base+"newtonsoft.json/index.json", &idx)
		if idx.Count != 1 || len(idx.Items) != 1 || len(idx.Items[0].Items) != 2 {
			t.Fatalf("%s: count %d, %d pages, want one inlined page with 2 leaves", hive, idx.Count, len(idx.Items))
		}
		page := idx.Items[0]
		if page.Lower != "13.0.1" || page.Upper != "13.0.3" || page.Parent != idx.ID {
			t.Errorf("%s page %+v", hive, page)
		}
		if page.ID != idx.ID+"#page/13.0.1/13.0.3" {
			t.Errorf("inlined page @id %q", page.ID)
		}
		leaf := page.Items[1]
		if leaf.CatalogEntry.Version != "13.0.3" || !leaf.CatalogEntry.Listed ||
			leaf.PackageContent != f.content+"/v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg" {
			t.Errorf("%s leaf %+v", hive, leaf)
		}
		if r := f.get(base + "newtonsoft.json/13.0.3.json"); r.status != http.StatusOK || !strings.Contains(string(r.body), `"listed":true`) {
			t.Errorf("leaf document: %d %s", r.status, r.body)
		}
		if r := f.get(base + "newtonsoft.json/9.9.9.json"); r.status != http.StatusNotFound {
			t.Errorf("unknown leaf: %d", r.status)
		}
		if r := f.get(base + "unknown.id/index.json"); r.status != http.StatusNotFound {
			t.Errorf("unknown ID: %d", r.status)
		}
	}
}

func TestR_NUGET_027_TwoHundredVersionsAreInPagesThatAreNotInlined(t *testing.T) {
	f := newFixture(t, Config{})
	base := f.resource("RegistrationsBaseUrl/3.6.0")
	var idx regIndex
	f.getJSON(base+"contoso.versions/index.json", &idx)
	if idx.Count != 4 {
		t.Fatalf("%d pages, want 4 (64+64+64+8)", idx.Count)
	}
	total := 0
	for i, page := range idx.Items {
		if len(page.Items) != 0 {
			t.Fatalf("page %d is inlined with %d items", i, len(page.Items))
		}
		if !strings.HasPrefix(page.ID, base+"contoso.versions/page/") || !strings.HasSuffix(page.ID, ".json") {
			t.Fatalf("page %d @id %q", i, page.ID)
		}
		var doc struct {
			Count  int    `json:"count"`
			Lower  string `json:"lower"`
			Upper  string `json:"upper"`
			Parent string `json:"parent"`
			Items  []struct {
				CatalogEntry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		}
		f.getJSON(page.ID, &doc)
		if doc.Count != page.Count || len(doc.Items) != page.Count || doc.Lower != page.Lower || doc.Upper != page.Upper || doc.Parent != idx.ID {
			t.Fatalf("page %d: index says %+v, document %d items [%s,%s]", i, page, len(doc.Items), doc.Lower, doc.Upper)
		}
		if doc.Items[0].CatalogEntry.Version != page.Lower || doc.Items[len(doc.Items)-1].CatalogEntry.Version != page.Upper {
			t.Fatalf("page %d bounds do not match its leaves", i)
		}
		total += len(doc.Items)
	}
	if total != ManyVersions {
		t.Fatalf("pages hold %d leaves, want %d", total, ManyVersions)
	}
	var list struct {
		Versions []string `json:"versions"`
	}
	f.getJSON(f.resource("PackageBaseAddress/3.0.0")+"contoso.versions/index.json", &list)
	if len(list.Versions) != ManyVersions {
		t.Fatalf("version list has %d versions", len(list.Versions))
	}
}

func TestR_NUGET_027_GzipHive(t *testing.T) {
	f := newFixture(t, Config{})
	gzBase, plainBase := f.resource("RegistrationsBaseUrl/3.6.0"), f.resource("RegistrationsBaseUrl")

	plain := f.get(plainBase + "contoso.versions/index.json")
	if plain.header.Get("Content-Encoding") != "" {
		t.Fatal("the plain hive compressed its answer")
	}
	zipped := f.do(http.MethodGet, gzBase+"contoso.versions/index.json", map[string]string{"Accept-Encoding": "gzip"}, nil)
	if zipped.header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("the gzip hive did not compress: %v", zipped.header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped.body))
	if err != nil {
		t.Fatal(err)
	}
	inflated, _ := io.ReadAll(zr)
	var idx regIndex
	if err := json.Unmarshal(inflated, &idx); err != nil || idx.Count != 4 {
		t.Fatalf("inflated body: %v, count %d", err, idx.Count)
	}
	identity := f.get(gzBase + "contoso.versions/index.json")
	if identity.header.Get("Content-Encoding") != "" {
		t.Fatal("the gzip hive compressed for a client that did not accept it")
	}
	if zipped.header.Get("ETag") != identity.header.Get("ETag") || !strings.HasPrefix(identity.header.Get("ETag"), `W/"`) {
		t.Fatalf("ETags %q and %q: want one weak value with and without gzip", zipped.header.Get("ETag"), identity.header.Get("ETag"))
	}
}

func TestR_NUGET_027_ConditionalRequestsAnswer304AndAreCounted(t *testing.T) {
	f := newFixture(t, Config{})
	doc := f.resource("PackageBaseAddress/3.0.0") + "contoso.util/index.json"
	first := f.get(doc)
	etag := first.header.Get("ETag")
	if etag == "" || first.header.Get("Last-Modified") == "" {
		t.Fatalf("no validators: %v", first.header)
	}
	if r := f.do(http.MethodGet, doc, map[string]string{"If-None-Match": etag}, nil); r.status != http.StatusNotModified || len(r.body) != 0 {
		t.Fatalf("If-None-Match: %d", r.status)
	}
	if r := f.do(http.MethodGet, doc, map[string]string{"If-Modified-Since": first.header.Get("Last-Modified")}, nil); r.status != http.StatusNotModified {
		t.Fatalf("If-Modified-Since: %d", r.status)
	}
	if r := f.do(http.MethodGet, doc, map[string]string{"If-None-Match": `W/"other"`}, nil); r.status != http.StatusOK {
		t.Fatalf("a different validator: %d", r.status)
	}
	c := f.counts()
	if c.VersionLists("Contoso.Util") != 4 || c.ConditionalRequests("contoso.util") != 3 {
		t.Fatalf("counts: %d version lists, %d conditional (%s)", c.VersionLists("Contoso.Util"), c.ConditionalRequests("contoso.util"), c.Summary())
	}
	// A new version changes the validator.
	if err := f.stub.Publish("Contoso.Util", "2.2.0", nil); err != nil {
		t.Fatal(err)
	}
	if r := f.do(http.MethodGet, doc, map[string]string{"If-None-Match": etag}, nil); r.status != http.StatusOK {
		t.Fatalf("after a publish the old validator still matched: %d", r.status)
	}
}

func TestR_NUGET_027_Search(t *testing.T) {
	f := newFixture(t, Config{})
	base := f.resource("SearchQueryService")
	var res struct {
		TotalHits int `json:"totalHits"`
		Data      []struct {
			ID           string `json:"id"`
			Version      string `json:"version"`
			Registration string `json:"registration"`
			Versions     []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"data"`
	}
	f.getJSON(base+"?q=newtonsoft", &res)
	if res.TotalHits != 1 || len(res.Data) != 1 || res.Data[0].ID != "Newtonsoft.Json" || res.Data[0].Version != "13.0.3" {
		t.Fatalf("%+v", res)
	}
	if !strings.HasPrefix(res.Data[0].Registration, f.api+"/v3/registration-gz/") {
		t.Errorf("registration %q is not on the API host", res.Data[0].Registration)
	}
	f.getJSON(base+"?q="+exactIDField+"contoso.util", &res)
	if res.TotalHits != 1 {
		t.Fatalf("exact-ID query: %d hits", res.TotalHits)
	}
	f.getJSON(base+"?q=contoso&skip=10&take=5", &res)
	if res.TotalHits < 100 || len(res.Data) != 5 {
		t.Fatalf("paging: %d hits, %d returned", res.TotalHits, len(res.Data))
	}
	f.getJSON(base+"?q=contoso.core.abstractions&prerelease=false", &res)
	for _, d := range res.Data {
		for _, v := range d.Versions {
			if strings.Contains(v.Version, "-") {
				t.Errorf("prerelease %s returned without prerelease=true", v.Version)
			}
		}
	}
	if f.counts().Searches() != 4 {
		t.Errorf("searches %d", f.counts().Searches())
	}
}

func TestR_NUGET_027_PublishDropRestoreAndUnlist(t *testing.T) {
	f := newFixture(t, Config{})
	list := f.resource("PackageBaseAddress/3.0.0") + "contoso.util/index.json"
	versions := func() string {
		var l struct {
			Versions []string `json:"versions"`
		}
		f.getJSON(list, &l)
		return strings.Join(l.Versions, ",")
	}
	if got := versions(); got != "2.0.0,2.1.0" {
		t.Fatal(got)
	}
	if err := f.stub.Publish("Contoso.Util", "2.2.0", map[string]string{"Newtonsoft.Json": "13.0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.Publish("Contoso.Util", "2.2.0", nil); err == nil {
		t.Fatal("a second publish of one version succeeded")
	}
	if err := f.stub.Publish("Contoso.Util", "2.2", nil); err == nil {
		t.Fatal("a version in a non-normalized form was accepted")
	}
	if got := versions(); got != "2.0.0,2.1.0,2.2.0" {
		t.Fatal(got)
	}
	if err := f.stub.Publish("Brand.New", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.resource("PackageBaseAddress/3.0.0") + "brand.new/1.0.0/brand.new.1.0.0.nupkg"); r.status != http.StatusOK {
		t.Fatalf("published package: %d", r.status)
	}

	if err := f.stub.Drop("Contoso.Util", "2.1.0"); err != nil {
		t.Fatal(err)
	}
	if got := versions(); got != "2.0.0,2.2.0" {
		t.Fatal(got)
	}
	// A dropped version keeps its files, as a withdrawn release does on an upstream that cached them.
	if r := f.get(f.resource("PackageBaseAddress/3.0.0") + "contoso.util/2.1.0/contoso.util.2.1.0.nupkg"); r.status != http.StatusOK {
		t.Fatalf("nupkg of a dropped version: %d", r.status)
	}
	if err := f.stub.Restore("Contoso.Util", "2.1.0"); err != nil {
		t.Fatal(err)
	}
	if got := versions(); got != "2.0.0,2.1.0,2.2.0" {
		t.Fatal(got)
	}
	if err := f.stub.Drop("Contoso.Util", "9.9.9"); err == nil {
		t.Fatal("dropping an unknown version succeeded")
	}

	if err := f.stub.SetListed("Contoso.Util", "2.2.0", false); err != nil {
		t.Fatal(err)
	}
	var idx regIndex
	f.getJSON(f.resource("RegistrationsBaseUrl/3.6.0")+"contoso.util/index.json", &idx)
	last := idx.Items[0].Items[2]
	if last.CatalogEntry.Version != "2.2.0" || last.CatalogEntry.Listed {
		t.Fatalf("unlisted version: %+v", last.CatalogEntry)
	}
	var res struct {
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	f.getJSON(f.resource("SearchQueryService")+"?q="+exactIDField+"contoso.util", &res)
	if len(res.Data) != 1 || res.Data[0].Version != "2.1.0" {
		t.Fatalf("search shows %+v, want the listed 2.1.0", res.Data)
	}

	f.stub.ResetAll()
	if got := versions(); got != "2.0.0,2.1.0" {
		t.Fatalf("after ResetAll: %s", got)
	}
}

func TestR_NUGET_027_CountsPerPathAndHost(t *testing.T) {
	f := newFixture(t, Config{})
	f.get(f.api + "/v3/index.json")
	f.get(f.content + "/v3/index.json")
	nupkg := f.content + "/v3-flatcontainer/contoso.util/2.0.0/contoso.util.2.0.0.nupkg"
	f.get(nupkg)
	f.get(nupkg)
	f.get(f.content + "/v3-flatcontainer/contoso.util/2.1.0/contoso.util.2.1.0.nupkg")
	f.get(f.content + "/v3-flatcontainer/contoso.util/index.json")
	f.get(f.api + "/v3/registration-gz/contoso.util/index.json")
	f.get(f.api + "/v3/registration-gz/contoso.util/2.0.0.json")
	f.get(f.api + "/v3/registration-gz/contoso.versions/page/1.0.0/1.63.0.json")

	c := f.counts()
	if c.Total() != 9 {
		t.Fatalf("total %d", c.Total())
	}
	if c.Nupkgs("Contoso.Util") != 3 || c.Nupkg("contoso.util", "2.0.0") != 2 || c.Nupkg("contoso.util", "2.1.0") != 1 {
		t.Errorf("nupkg counts: %s", c.Summary())
	}
	if c.VersionLists("contoso.util") != 1 || c.Registrations("contoso.util") != 2 || c.Registrations("contoso.versions") != 1 {
		t.Errorf("document counts: %+v", c.Entries)
	}
	if c.Requests("GET", "/v3/index.json") != 2 || len(c.Hosts()) != 2 {
		t.Errorf("hosts %v, index requests %d", c.Hosts(), c.Requests("GET", "/v3/index.json"))
	}
	got := c.Violations(1, 1)
	if len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "contoso.util.2.0.0.nupkg requested 2 times") ||
		!strings.Contains(strings.Join(got, "\n"), "/v3/index.json requested 2 times") {
		t.Errorf("violations %v", got)
	}
	f.stub.Reset()
	if f.counts().Total() != 0 {
		t.Error("Reset left counters")
	}
}

func TestR_NUGET_027_ControlRequestsAreNotCounted(t *testing.T) {
	f := newFixture(t, Config{ControlToken: "s3cret"})
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	if r := f.get(f.api + "/-/stub/counts"); r.status != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", r.status)
	}
	if r := f.do(http.MethodGet, f.api+"/-/stub/counts", map[string]string{"Authorization": "Bearer wrong"}, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", r.status)
	}
	f.get(f.api + "/v3/index.json")
	var c Counts
	r := f.do(http.MethodGet, f.api+"/-/stub/counts", auth, nil)
	if err := json.Unmarshal(r.body, &c); err != nil || c.Total() != 1 {
		t.Fatalf("counts %s: %v", r.body, err)
	}
	if r := f.do(http.MethodPost, f.api+"/-/stub/mode", auth, strings.NewReader(`{"down":"503"}`)); r.status != http.StatusOK {
		t.Fatalf("set mode: %d %s", r.status, r.body)
	}
	if r := f.get(f.api + "/v3/index.json"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("mode not applied: %d", r.status)
	}
	if r := f.do(http.MethodPost, f.api+"/-/stub/mode", auth, strings.NewReader(`{"down":"nonsense"}`)); r.status != http.StatusBadRequest {
		t.Fatalf("invalid mode: %d", r.status)
	}
	if r := f.do(http.MethodPost, f.api+"/-/stub/mode", auth, strings.NewReader(`{"unknown_field":1}`)); r.status != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", r.status)
	}
	f.do(http.MethodPost, f.api+"/-/stub/mode", auth, strings.NewReader(`{}`))

	body := func(v string) io.Reader { return strings.NewReader(v) }
	for _, step := range []struct {
		path, body string
		want       int
	}{
		{"publish", `{"name":"Contoso.Util","version":"3.0.0","dependencies":{"Newtonsoft.Json":"13.0.1"}}`, 204},
		{"publish", `{"name":"Contoso.Util","version":"3.0.0"}`, 409},
		{"unlist", `{"name":"Contoso.Util","version":"3.0.0"}`, 204},
		{"relist", `{"name":"Contoso.Util","version":"3.0.0"}`, 204},
		{"drop", `{"name":"Contoso.Util","version":"3.0.0"}`, 204},
		{"restore", `{"name":"Contoso.Util","version":"3.0.0"}`, 204},
		{"drop", `{"name":"Contoso.Util","version":"0.0.1"}`, 400},
		{"drop", `{"name":""}`, 400},
	} {
		if r := f.do(http.MethodPost, f.api+"/-/stub/"+step.path, auth, body(step.body)); r.status != step.want {
			t.Errorf("%s %s: %d, want %d (%s)", step.path, step.body, r.status, step.want, r.body)
		}
	}
	if r := f.do(http.MethodPost, f.api+"/-/stub/reset?all=true", auth, nil); r.status != http.StatusNoContent {
		t.Fatalf("reset: %d", r.status)
	}

	none := newFixture(t, Config{})
	if r := none.do(http.MethodGet, none.api+"/-/stub/counts", map[string]string{"Authorization": "Bearer x"}, nil); r.status != http.StatusNotFound {
		t.Fatalf("control API without a token configured: %d, want 404", r.status)
	}
}

// A client that lost the answer of a publish reads the version back: GET /-/stub/version lists it as the documents do, and
// Matches tells a version made by exactly that publish from one with other content.
func TestR_NUGET_027_ServedVersionReadsBackAPublish(t *testing.T) {
	f := newFixture(t, Config{ControlToken: "s3cret"})
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	served := func(id, version string) (ServedVersion, int) {
		t.Helper()
		r := f.do(http.MethodGet, f.api+"/-/stub/version?"+url.Values{"name": {id}, "version": {version}}.Encode(), auth, nil)
		var v ServedVersion
		if r.status == http.StatusOK {
			if err := json.Unmarshal(r.body, &v); err != nil {
				t.Fatalf("version: %v: %s", err, r.body)
			}
		}
		return v, r.status
	}
	post := func(path, body string) {
		t.Helper()
		if r := f.do(http.MethodPost, f.api+"/-/stub/"+path, auth, strings.NewReader(body)); r.status != http.StatusNoContent {
			t.Fatalf("%s %s: %d %s", path, body, r.status, r.body)
		}
	}

	post("publish", `{"name":"Contoso.Fresh","version":"1.0.0","dependencies":{"Newtonsoft.Json":"13.0.1"}}`)
	v, status := served("Contoso.Fresh", "1.0.0")
	if status != http.StatusOK {
		t.Fatalf("the published version is not served: %d", status)
	}
	deps := map[string]string{"Newtonsoft.Json": "13.0.1"}
	if err := v.Matches("Contoso.Fresh", "1.0.0", deps); err != nil {
		t.Fatalf("the version of the publish does not match it: %v", err)
	}
	if err := v.Matches("Contoso.Fresh", "1.0.0", nil); err == nil || !strings.Contains(err.Error(), "dependencies") {
		t.Fatalf("a publish without dependencies matches: %v", err)
	}
	if err := v.Matches("contoso.fresh", "1.0.0", deps); err == nil {
		t.Fatal("an ID in another case matches")
	}
	post("unlist", `{"name":"Contoso.Fresh","version":"1.0.0"}`)
	if v, _ := served("Contoso.Fresh", "1.0.0"); v.Matches("Contoso.Fresh", "1.0.0", deps) == nil {
		t.Fatal("an unlisted version matches a publish")
	}
	// a version of the corpus with a deprecation and an advisory is not what a publish makes
	if v, status := served(NewtonsoftID, DeprecatedVersion); status != http.StatusOK || v.Matches(NewtonsoftID, DeprecatedVersion, v.Dependencies) == nil {
		t.Fatalf("the deprecated corpus version: %d, %+v matches a publish", status, v)
	}
	post("drop", `{"name":"Contoso.Fresh","version":"1.0.0"}`)
	if _, status := served("Contoso.Fresh", "1.0.0"); status != http.StatusNotFound {
		t.Fatalf("a dropped version is served: %d", status)
	}
	if _, status := served("Contoso.Unknown", "1.0.0"); status != http.StatusNotFound {
		t.Fatalf("an unknown ID: %d", status)
	}
	if r := f.do(http.MethodGet, f.api+"/-/stub/version?name=Contoso.Fresh", auth, nil); r.status != http.StatusBadRequest {
		t.Fatalf("version without a version: %d", r.status)
	}
	if got := f.counts().Total(); got != 0 {
		t.Fatalf("the reads of a version were counted: %d", got)
	}
}

func TestR_NUGET_027_ModeDown(t *testing.T) {
	f := newFixture(t, Config{})
	f.setMode(Mode{Down: DownUnavailable})
	for _, p := range []string{"/v3/index.json", "/v3-flatcontainer/contoso.util/index.json"} {
		if r := f.get(f.api + p); r.status != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", p, r.status)
		}
	}
	f.setMode(Mode{Down: DownReset})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.api+"/v3/index.json", nil)
	if resp, err := f.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("reset mode answered %d", resp.StatusCode)
	}
	f.setMode(Mode{Down: DownHang})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, f.api+"/v3/index.json", nil)
	if resp, err := f.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("hang mode answered %d", resp.StatusCode)
	}
	f.setMode(Mode{})
	if r := f.get(f.api + "/v3/index.json"); r.status != http.StatusOK {
		t.Fatalf("after clearing the mode: %d", r.status)
	}
	if f.counts().Requests("GET", "/v3/index.json") != 5 {
		t.Errorf("requests refused by a mode are counted too: %d", f.counts().Requests("GET", "/v3/index.json"))
	}
}

func TestR_NUGET_027_ModeDelayHoldsEveryResponse(t *testing.T) {
	f := newFixture(t, Config{})
	f.setMode(Mode{DelayMS: map[string]int{ClassAll: 300}})
	start := time.Now()
	f.get(f.api + "/v3/index.json")
	f.get(f.content + "/v3-flatcontainer/contoso.util/2.0.0/contoso.util.2.0.0.nupkg")
	if took := time.Since(start); took < 600*time.Millisecond {
		t.Fatalf("two requests took %v, want at least 2 x 300ms", took)
	}
	// A class only delays its own kind.
	f.setMode(Mode{DelayMS: map[string]int{ClassNupkg: 300}})
	start = time.Now()
	f.get(f.api + "/v3/index.json")
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatalf("the index was held %v by a nupkg delay", took)
	}
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{ClassAll: 2000}}); err != nil {
		t.Fatalf("the 2 s delay of the drain load is not accepted: %v", err)
	}
	for _, bad := range []Mode{{DelayMS: map[string]int{"bogus": 1}}, {DelayMS: map[string]int{ClassAll: -1}}, {DelayMS: map[string]int{ClassAll: 10 * 60 * 1000}}} {
		if err := f.stub.SetMode(bad); err == nil {
			t.Errorf("mode %+v accepted", bad)
		}
	}
}

func TestR_NUGET_027_ModePackageGone(t *testing.T) {
	f := newFixture(t, Config{})
	flat := f.resource("PackageBaseAddress/3.0.0")
	reg := f.resource("RegistrationsBaseUrl/3.6.0")
	f.setMode(Mode{NotFound: []string{"contoso.util"}, Gone: []string{"Newtonsoft.Json"}})
	for _, u := range []string{
		flat + "contoso.util/index.json", reg + "contoso.util/index.json",
		flat + "contoso.util/2.0.0/contoso.util.2.0.0.nupkg", flat + "contoso.util/2.0.0/contoso.util.nuspec",
	} {
		if r := f.get(u); r.status != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", u, r.status)
		}
	}
	for _, u := range []string{flat + "newtonsoft.json/index.json", reg + "newtonsoft.json/index.json"} {
		if r := f.get(u); r.status != http.StatusGone {
			t.Errorf("%s: %d, want 410", u, r.status)
		}
	}
	if r := f.get(flat + "contoso.versions/index.json"); r.status != http.StatusOK {
		t.Errorf("another package: %d", r.status)
	}
	if f.counts().VersionLists("contoso.util") != 1 {
		t.Error("a request refused as not found is not counted")
	}
}

func TestR_NUGET_027_ModeDigestMismatch(t *testing.T) {
	f := newFixture(t, Config{})
	reg := f.resource("RegistrationsBaseUrl/3.6.0")
	nupkg := f.get(f.resource("PackageBaseAddress/3.0.0") + "contoso.util/2.0.0/contoso.util.2.0.0.nupkg").body
	sum := sha512.Sum512(nupkg)
	actual := base64.StdEncoding.EncodeToString(sum[:])

	hashOf := func() string {
		var idx regIndex
		f.getJSON(reg+"contoso.util/index.json", &idx)
		return idx.Items[0].Items[0].CatalogEntry.PackageHash
	}
	if got := hashOf(); got != actual {
		t.Fatalf("healthy: packageHash %q, bytes %q", got, actual)
	}
	f.setMode(Mode{BadDigest: []string{"Contoso.Util"}})
	if got := hashOf(); got == actual || got == "" {
		t.Fatalf("mode set: packageHash %q still matches the bytes", got)
	}
	again := f.get(f.resource("PackageBaseAddress/3.0.0") + "contoso.util/2.0.0/contoso.util.2.0.0.nupkg").body
	if !bytes.Equal(again, nupkg) {
		t.Error("the digest mode changed the .nupkg bytes; only the announced hash must differ")
	}
}

func TestR_NUGET_027_ModeWrongContent(t *testing.T) {
	f := newFixture(t, Config{})
	f.setMode(Mode{WrongContent: []string{"contoso.util"}})
	r := f.get(f.resource("PackageBaseAddress/3.0.0") + "contoso.util/2.0.0/contoso.util.2.0.0.nupkg")
	zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("not an archive: %v", err)
	}
	found := false
	for _, zf := range zr.File {
		if zf.Name == OtherID+".nuspec" {
			found = true
		}
		if zf.Name == "Contoso.Util.nuspec" {
			t.Error("the archive still carries the requested identity")
		}
	}
	if !found {
		t.Error("the archive does not carry the other identity")
	}
}

func TestR_NUGET_027_ModeOmitResourcesAndAuthorization(t *testing.T) {
	f := newFixture(t, Config{})
	f.setMode(Mode{OmitResources: []string{"PackageBaseAddress"}})
	var idx serviceIndex
	f.getJSON(f.api+"/v3/index.json", &idx)
	for _, r := range idx.Resources {
		if strings.HasPrefix(r.Type, "PackageBaseAddress") {
			t.Fatalf("still listed: %+v", r)
		}
	}
	if len(idx.Resources) == 0 {
		t.Fatal("everything was omitted")
	}

	f.setMode(Mode{RequireAuthorization: "Basic dTpw"})
	if r := f.get(f.api + "/v3/index.json"); r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("without credentials: %d", r.status)
	}
	if r := f.do(http.MethodGet, f.api+"/v3/index.json", map[string]string{"Authorization": "Basic dTpw"}, nil); r.status != http.StatusOK {
		t.Fatalf("with credentials: %d", r.status)
	}
	var withAuth int
	for _, e := range f.counts().Entries {
		if e.Auth == "basic" {
			withAuth += e.Count
		}
	}
	if withAuth != 1 {
		t.Errorf("the counters saw %d basic credentials, want 1 (and never the value)", withAuth)
	}
}

func TestR_NUGET_027_ModeStrictHosts(t *testing.T) {
	f := newFixture(t, Config{})
	flat := "/v3-flatcontainer/contoso.util/index.json"
	// Without the mode both hosts serve everything.
	if r := f.get(f.api + flat); r.status != http.StatusOK {
		t.Fatalf("api host, flat container, mode off: %d", r.status)
	}
	f.setMode(Mode{StrictHosts: true})
	cases := []struct {
		url  string
		want int
	}{
		{f.api + "/v3/index.json", 200},
		{f.api + "/v3/registration-gz/contoso.util/index.json", 200},
		{f.content + flat, 200},
		{f.content + "/query?q=util", 200},
		{f.api + flat, 404},
		{f.api + "/query?q=util", 404},
		{f.content + "/v3/registration-gz/contoso.util/index.json", 404},
		{f.content + "/v3/index.json", 404},
	}
	for _, c := range cases {
		if r := f.get(c.url); r.status != c.want {
			t.Errorf("GET %s: %d, want %d", c.url, r.status, c.want)
		}
	}

	// With one host there is nothing to be strict about.
	single := newFixture(t, Config{PublicURL: "https://x.example"})
	single.setMode(Mode{StrictHosts: true})
	if r := single.get(single.api + flat); r.status != http.StatusOK {
		t.Errorf("single-host stub: %d", r.status)
	}
}

// TestR_NUGET_027_ARestoreOfTheReferenceSolution walks the reference solution the way a restore
// does with a cold cache: the registration of every package it meets, then each .nupkg once, and
// reads the dependencies from the .nuspec. It is the check of criterion E3.2's premise: at least
// 100 packages in the restore graph, all served, and a counter that shows one request per .nupkg.
func TestR_NUGET_027_ARestoreOfTheReferenceSolution(t *testing.T) {
	f := newFixture(t, Config{})
	flat := f.resource("PackageBaseAddress/3.0.0")
	reg := f.resource("RegistrationsBaseUrl/3.6.0")

	roots := Default().ReferenceRoots(ReferenceMinPackages)
	queue := append([]string(nil), roots...)
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := strings.ToLower(queue[0])
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		var idx regIndex
		f.getJSON(reg+id+"/index.json", &idx)
		var list struct {
			Versions []string `json:"versions"`
		}
		f.getJSON(flat+id+"/index.json", &list)
		version := list.Versions[0] // a dependency range of 1.0.0 resolves to the lowest version
		r := f.get(flat + id + "/" + version + "/" + id + "." + version + ".nupkg")
		if r.status != http.StatusOK {
			t.Fatalf("%s %s: %d", id, version, r.status)
		}
		zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
		if err != nil {
			t.Fatal(err)
		}
		for _, zf := range zr.File {
			if !strings.HasSuffix(zf.Name, ".nuspec") {
				continue
			}
			rc, _ := zf.Open()
			var spec struct {
				Deps []struct {
					ID string `xml:"id,attr"`
				} `xml:"metadata>dependencies>group>dependency"`
			}
			if err := xml.NewDecoder(rc).Decode(&spec); err != nil {
				t.Fatal(err)
			}
			rc.Close()
			for _, d := range spec.Deps {
				queue = append(queue, d.ID)
			}
		}
	}
	if len(seen) < 100 {
		t.Fatalf("the restore graph has %d packages, E3.2 needs at least 100", len(seen))
	}
	c := f.counts()
	if v := c.Violations(2, 1); len(v) != 0 {
		t.Fatalf("one .nupkg request per package expected: %v", v)
	}
	nupkgs := 0
	for _, e := range c.Entries {
		if e.Kind == KindNupkg {
			nupkgs += e.Count
		}
	}
	if nupkgs != len(seen) {
		t.Fatalf("%d .nupkg requests for %d packages", nupkgs, len(seen))
	}
}

func TestR_NUGET_027_ReferenceSolutionIsCurrent(t *testing.T) {
	c := Default()
	if n := len(c.Closure(c.ReferenceRoots(ReferenceMinPackages)...)); n < ReferenceMinPackages {
		t.Fatalf("the reference closure has %d packages, want at least %d", n, ReferenceMinPackages)
	}
	files := ReferenceFiles(c, ReferenceMinPackages)
	if len(files) != 3 {
		t.Fatalf("%d files", len(files))
	}
	dir := filepath.Join("..", "..", "perf", "nuget", "reference")
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("perf/nuget/reference/%s differs from a regeneration; run: go run ./test/nugetstub/cmd/nugetstub reference -out perf/nuget/reference", name)
		}
	}
	if !bytes.Contains(files["Reference/Reference.csproj"], []byte(`<PackageReference Include="`)) {
		t.Error("no PackageReference in the project")
	}
}

func TestR_NUGET_027_CorpusIsAnAcyclicDeterministicGraph(t *testing.T) {
	a, b := Default(), Default()
	if a.Len() != b.Len() || a.Len() < 180 {
		t.Fatalf("%d and %d packages", a.Len(), b.Len())
	}
	index := map[string]int{}
	for i, id := range a.IDs() {
		index[id] = i
	}
	for _, id := range a.IDs() {
		p, _ := a.Get(id)
		q, _ := b.Get(id)
		if len(p.Versions) == 0 || len(p.Versions) != len(q.Versions) {
			t.Fatalf("%s: versions differ between builds", id)
		}
		for i, v := range p.Versions {
			if i > 0 && compareVersions(p.Versions[i-1].Version, v.Version) >= 0 {
				t.Errorf("%s: versions not ascending at %s", id, v.Version)
			}
			for _, d := range v.Dependencies {
				if j, ok := index[strings.ToLower(d.ID)]; !ok || j >= index[id] {
					t.Errorf("%s depends on %s, which does not come before it", id, d.ID)
				}
			}
		}
	}
	many, _ := a.Get(ManyVersionsID)
	if len(many.Versions) != 200 {
		t.Errorf("%d versions", len(many.Versions))
	}
}

func TestR_NUGET_027_VersionOrder(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range ordered {
		for j := range ordered {
			if got, want := compareVersions(ordered[i], ordered[j]), cmpInt(i, j); got != want {
				t.Errorf("compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	for _, bad := range []string{"1.0", "1.0.0.0", "01.0.0", "1.0.0-", "1.0.0-Beta", "1.0.0+meta", ""} {
		if validVersion(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
