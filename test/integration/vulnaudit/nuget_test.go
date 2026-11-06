//go:build integration

package vulnaudit

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// seedNuGet inserts the NuGet advisories of the tests and marks the data complete.
func (e *env) seedNuGet() {
	e.t.Helper()
	eco := func(events ...string) string { return rangeJSON("ECOSYSTEM", events...) }
	e.advisory("NuGet", "GHSA-aaaa-aaaa-0101", "GHSA-aaaa-aaaa-0101", "vulnerability", "high", "Newtonsoft.Json", nil, false,
		aff{name: "newtonsoft.json", ranges: eco(ev("introduced", "13.0.1"), ev("fixed", "13.0.3"))})
	e.advisory("NuGet", "MAL-2026-1301", "MAL-2026-1301", "malicious", "critical", "", nil, false,
		aff{name: "contoso.json.codecs", versions: []string{"1.0.0"}})
	e.advisory("NuGet", "GHSA-aaaa-aaaa-0102", "GHSA-aaaa-aaaa-0102", "vulnerability", "medium", "Contoso.Util", nil, false,
		aff{name: "contoso.util", versions: []string{"2.0.0"}})
	e.advisory("NuGet", "GHSA-aaaa-aaaa-0105", "GHSA-aaaa-aaaa-0105", "vulnerability", "high", "Withdrawn", nil, true,
		aff{name: "withdrawn.pkg", every: true})
	e.data("NuGet", true, 1, 0)
}

// serviceIndexEntry returns the @id of the VulnerabilityInfo entry of the service index of a repository, "" when it is not
// listed.
func serviceIndexEntry(t *testing.T, a testenv.Answer) string {
	t.Helper()
	if a.Status != http.StatusOK {
		t.Fatalf("service index: %d %s", a.Status, a.Body)
	}
	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(a.Body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, r := range doc.Resources {
		if r.Type == "VulnerabilityInfo/6.7.0" {
			return r.ID
		}
	}
	return ""
}

// TestR_VULN_018_TheServiceIndexListsTheResourceOnEveryClass: local, remote and virtual repositories list it while the mode
// is local and the data is complete; a repository whose mode is off, and every repository before the data is complete, do not.
func TestR_VULN_018_TheServiceIndexListsTheResourceOnEveryClass(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")

	// Before the first import the resource is not listed.
	for _, repo := range []string{nugetLocal, nugetRemote, nugetAll} {
		if id := serviceIndexEntry(t, e.get(a, repo, "v3/index.json", e.tokReader)); id != "" {
			t.Errorf("%s lists %s before the data is complete", repo, id)
		}
	}
	before := e.get(a, nugetLocal, "v3/index.json", e.tokReader).Header.Get("ETag")
	e.seedNuGet()
	e.changed("vuln_data", `{"ecosystem":"NuGet","epoch":1}`, a)
	for _, repo := range []string{nugetLocal, nugetRemote, nugetAll} {
		id := serviceIndexEntry(t, e.get(a, repo, "v3/index.json", e.tokReader))
		if want := feedBase + "/api/nuget/it/" + repo + "/v3/vulnerabilities/index.json"; id != want {
			t.Errorf("%s: @id %q, want %q", repo, id, want)
		}
	}
	if id := serviceIndexEntry(t, e.get(a, nugetOff, "v3/index.json", e.tokReader)); id != "" {
		t.Errorf("nuget-off lists %s", id)
	}
	// The ETag of the index covers whether the entry is listed: a client's cached index gains it.
	after := e.get(a, nugetLocal, "v3/index.json", e.tokReader, "If-None-Match", before)
	if after.Status != http.StatusOK || after.Header.Get("ETag") == before {
		t.Errorf("the cached index of a client: %d, ETag %q was %q", after.Status, after.Header.Get("ETag"), before)
	}
}

// TestR_VULN_018_TheDocuments: the index, the page with its intervals, severities and urls, the withheld internal name, the
// headers, the 304, the ETag that follows an epoch and that two replicas share.
func TestR_VULN_018_TheDocuments(t *testing.T) {
	e := newEnv(t)
	e.seedNuGet()
	a, b := e.replica("web-a"), e.replica("web-b")

	idx := e.get(a, nugetRemote, "v3/vulnerabilities/index.json", e.tokReader)
	if idx.Status != 200 {
		t.Fatalf("index: %d %s", idx.Status, idx.Body)
	}
	var entries []map[string]string
	if err := json.Unmarshal(idx.Body, &entries); err != nil || len(entries) != 1 ||
		entries[0]["@name"] != "trove" || entries[0]["@id"] != feedBase+"/api/nuget/it/"+nugetRemote+"/v3/vulnerabilities/trove.json" ||
		entries[0]["@updated"] == "" || entries[0]["comment"] != "Advisory data mirrored by trove" {
		t.Fatalf("index: %v %s", err, idx.Body)
	}

	page := e.get(a, nugetRemote, "v3/vulnerabilities/trove.json", e.tokReader)
	if page.Status != 200 {
		t.Fatalf("page: %d %s", page.Status, page.Body)
	}
	var got map[string][]struct {
		Severity int    `json:"severity"`
		URL      string `json:"url"`
		Versions string `json:"versions"`
	}
	if err := json.Unmarshal(page.Body, &got); err != nil {
		t.Fatal(err)
	}
	if n := got["newtonsoft.json"]; len(n) != 1 || n[0].Severity != 2 || n[0].Versions != "[13.0.1, 13.0.3)" ||
		n[0].URL != "https://github.com/advisories/GHSA-aaaa-aaaa-0101" {
		t.Errorf("newtonsoft.json: %+v", n)
	}
	if m := got["contoso.json.codecs"]; len(m) != 1 || m[0].Severity != 3 || m[0].Versions != "[1.0.0]" || m[0].URL != "https://osv.dev/vulnerability/MAL-2026-1301" {
		t.Errorf("contoso.json.codecs: %+v", m)
	}
	if _, ok := got["contoso.util"]; ok {
		t.Error("the internal name contoso.util is on the page")
	}
	if _, ok := got["withdrawn.pkg"]; ok {
		t.Error("a withdrawn advisory is on the page")
	}
	if page.Header.Get("Cache-Control") != "private, max-age=1800" || !strings.HasPrefix(page.Header.Get("ETag"), `W/"`) ||
		page.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("headers %v", page.Header)
	}
	etag := page.Header.Get("ETag")
	if c := e.get(a, nugetLocal, "v3/vulnerabilities/trove.json", e.tokReader, "If-None-Match", etag); c.Status != http.StatusNotModified {
		t.Errorf("the same page through another repository: %d, want 304", c.Status)
	}
	if other := e.get(b, nugetAll, "v3/vulnerabilities/trove.json", e.tokReader); other.Header.Get("ETag") != etag || string(other.Body) != string(page.Body) {
		t.Errorf("the other replica: ETag %q, want %q", other.Header.Get("ETag"), etag)
	}

	// A sync that adds an advisory raises the epoch; the page of every replica follows.
	e.advisory("NuGet", "GHSA-aaaa-aaaa-0103", "GHSA-aaaa-aaaa-0103", "vulnerability", "high", "Contoso.Util 2.1.0", nil, false,
		aff{name: "newtonsoft.json", versions: []string{"12.0.0"}})
	e.data("NuGet", true, 2, 0)
	e.changed("vuln_data", `{"ecosystem":"NuGet","epoch":2}`, a, b)
	for _, r := range []*testenv.Replica{a, b} {
		x := e.get(r, nugetAll, "v3/vulnerabilities/trove.json", e.tokReader, "If-None-Match", etag)
		if x.Status != 200 || x.Header.Get("ETag") == etag || !strings.Contains(string(x.Body), "[12.0.0]") {
			t.Errorf("after the epoch change: %d, ETag %q", x.Status, x.Header.Get("ETag"))
		}
	}

	// A claim that makes a name internal changes the page, without a change of the advisory data.
	etag = e.get(a, nugetAll, "v3/vulnerabilities/trove.json", e.tokReader).Header.Get("ETag")
	e.claim(nugetLocal, "newtonsoft.*")
	e.changed("claims", `{}`, a, b)
	for _, r := range []*testenv.Replica{a, b} {
		x := e.get(r, nugetAll, "v3/vulnerabilities/trove.json", e.tokReader)
		if x.Header.Get("ETag") == etag || strings.Contains(string(x.Body), "newtonsoft.json") {
			t.Errorf("after the claim: ETag %q, %s", x.Header.Get("ETag"), x.Body)
		}
	}
}

// TestR_VULN_018_WhereTheDocumentsAreNotServed: 503 before the data is complete, 404 where the mode is off, and the permission.
func TestR_VULN_018_WhereTheDocumentsAreNotServed(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, f := range []string{"index.json", "trove.json"} {
		x := e.get(a, nugetRemote, "v3/vulnerabilities/"+f, e.tokReader)
		if x.Status != http.StatusServiceUnavailable || !strings.Contains(string(x.Body), "vulnerability_data_unavailable") {
			t.Errorf("%s before the data is complete: %d %s", f, x.Status, x.Body)
		}
	}
	e.seedNuGet()
	e.changed("vuln_data", `{"ecosystem":"NuGet","epoch":1}`, a)
	for _, f := range []string{"index.json", "trove.json"} {
		if x := e.get(a, nugetOff, "v3/vulnerabilities/"+f, e.tokReader); x.Status != http.StatusNotFound {
			t.Errorf("%s in mode off: %d", f, x.Status)
		}
		if x := e.get(a, nugetRemote, "v3/vulnerabilities/"+f, e.tokNobody); x.Status < 400 || strings.Contains(string(x.Body), "GHSA-") {
			t.Errorf("%s without read: %d", f, x.Status)
		}
	}
}
