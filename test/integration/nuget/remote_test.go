//go:build integration

package nuget

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

// eventually polls until the condition holds: the tests wait for a period of the cache to pass
// by asking, never by assuming how long a loaded machine takes.
func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", within, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

const (
	newtonsoft = "newtonsoft.json"
	abstract   = "contoso.core.abstractions"
)

func TestR_NUGET_015_AColdFillAsksTheUpstreamOnceAndTheNextRequestsNone(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]

	a := e.get(rep, rRemote, "v3/flatcontainer/Newtonsoft.Json/index.json")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if got := versionsOf(t, a); strings.Join(got, ",") != "13.0.1,13.0.3" {
		t.Errorf("versions = %v", got)
	}
	if a.Header.Get("Cache-Status") == "" {
		t.Error("no Cache-Status on a document of a remote repository")
	}
	for range 3 {
		if b := e.get(rep, rRemote, "v3/flatcontainer/newtonsoft.json/index.json"); b.Status != 200 {
			t.Fatalf("%d %s", b.Status, b.Body)
		}
	}
	// The service index is the discovery document: one request, shared by every document.
	c := e.stub.Counts()
	if got := c.VersionLists(newtonsoft); got != 1 {
		t.Errorf("%d version list requests for four requests inside the period, want 1", got)
	}
	if got := c.Requests("GET", "/v3/index.json"); got != 1 {
		t.Errorf("%d service index requests, want 1", got)
	}
	// The resources are spread over two hosts: the index on the API host, the list on the other.
	hosts := c.Hosts()
	if len(hosts) != 2 {
		t.Errorf("the upstream saw the hosts %v, want two", hosts)
	}
}

func TestR_NUGET_015_RegistrationsAreRenderedOnTheServingRepository(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	base := feed(rRemote)

	for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
		a := e.get(rep, rRemote, "v3/"+hive+"/Newtonsoft.Json/index.json", "Accept-Encoding", "gzip")
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", hive, a.Status, a.Body)
		}
		wantGzip := hive != "registration"
		if got := a.Header.Get("Content-Encoding") == "gzip"; got != wantGzip {
			t.Errorf("%s: gzip = %v, want %v", hive, got, wantGzip)
		}
		d := registrationOf(t, a)
		leaves := 0
		for _, p := range d.Items {
			if !strings.HasPrefix(p.ID, base+hive+"/newtonsoft.json/index.json#page/") {
				t.Errorf("%s: page @id %q", hive, p.ID)
			}
			for _, l := range p.Items {
				leaves++
				if !strings.HasPrefix(l.ID, base+hive+"/newtonsoft.json/") || !strings.HasPrefix(l.PackageContent, base+"flatcontainer/newtonsoft.json/") {
					t.Errorf("%s: leaf %q packageContent %q", hive, l.ID, l.PackageContent)
				}
				if l.CatalogEntry.ID != "Newtonsoft.Json" {
					t.Errorf("%s: id %q", hive, l.CatalogEntry.ID)
				}
			}
		}
		if leaves != 2 {
			t.Errorf("%s: %d leaves, want 2", hive, leaves)
		}
	}

	// Every address of the document except the web links of people (projectUrl, licenseUrl,
	// iconUrl, advisoryUrl) is a trove address; the upstream's deprecation and advisory of 13.0.1
	// come without its @id and @type (Q-NUGET-85).
	a := e.get(rep, rRemote, "v3/registration-semver2/newtonsoft.json/index.json")
	if !strings.Contains(string(a.Body), `"deprecation":{"alternatePackage":{"id":"Newtonsoft.Json","range":"[13.0.3, )"},"message":"Use 13.0.3 or later.","reasons":["Legacy","CriticalBugs"]}`) ||
		!strings.Contains(string(a.Body), `"vulnerabilities":[{"advisoryUrl":"`+nugetstub.Advisory+`","severity":"2"}]`) {
		t.Errorf("the deprecation and the advisory of 13.0.1 are not passed through as the client reads them: %s", a.Body)
	}
	var any_ any
	if err := json.Unmarshal(a.Body, &any_); err != nil {
		t.Fatal(err)
	}
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				walk(k, e)
			}
		case []any:
			for _, e := range x {
				walk(key, e)
			}
		case string:
			if (strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://")) && key != "projectUrl" && key != "licenseUrl" && key != "iconUrl" && key != "advisoryUrl" {
				if !strings.HasPrefix(x, base) {
					t.Errorf("%s = %q is not below %s", key, x, base)
				}
			}
		}
	}
	walk("", any_)
	if strings.Contains(string(a.Body), e.srv.Listener.Addr().String()) {
		t.Error("the upstream's address is in a document")
	}

	// A leaf and the version list of the same ID.
	leaf := e.get(rep, rRemote, "v3/registration-semver2/newtonsoft.json/13.0.3.json")
	if leaf.Status != 200 || !strings.Contains(string(leaf.Body), `"listed":true`) {
		t.Errorf("leaf: %d %s", leaf.Status, leaf.Body)
	}
	if got := e.stub.Counts().Registrations(newtonsoft); got != 1 {
		t.Errorf("%d registration requests for seven documents of one ID, want 1", got)
	}
}

func TestR_NUGET_015_AnIDWithManyVersionsIsRePagedAndCollapsed(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	id := strings.ToLower(nugetstub.ManyVersionsID)

	// The upstream does not inline 200 versions: the index lists four pages to fetch.
	a := e.get(rep, rRemote, "v3/registration-semver2/"+id+"/index.json")
	if a.Status != 200 {
		t.Fatalf("%d %.300s", a.Status, a.Body)
	}
	d := registrationOf(t, a)
	if d.Count != 4 || len(d.Items) != 4 {
		t.Fatalf("%d pages, want 4 (200 versions in pages of 64)", d.Count)
	}
	total := 0
	for _, p := range d.Items {
		if len(p.Items) != 0 {
			t.Errorf("page %s..%s is inlined: trove's paging does not inline 200 versions", p.Lower, p.Upper)
		}
		path := strings.TrimPrefix(p.ID, feedBase+"/api/nuget/"+rRemote+"/")
		pg := e.get(rep, rRemote, path)
		if pg.Status != 200 {
			t.Fatalf("page %s: %d %s", p.ID, pg.Status, pg.Body)
		}
		pd := struct {
			Count int `json:"count"`
			Items []struct {
				PackageContent string `json:"packageContent"`
			} `json:"items"`
		}{}
		if err := json.Unmarshal(pg.Body, &pd); err != nil {
			t.Fatal(err)
		}
		if pd.Count != p.Count || len(pd.Items) != p.Count {
			t.Errorf("page %s: count %d, %d leaves, index says %d", p.ID, pd.Count, len(pd.Items), p.Count)
		}
		total += len(pd.Items)
	}
	if total != nugetstub.ManyVersions {
		t.Errorf("%d leaves over the pages, want %d", total, nugetstub.ManyVersions)
	}
	// The index and the four pages of the upstream, each once, however many documents were served.
	if got := e.stub.Counts().Registrations(id); got != 5 {
		t.Errorf("%d registration requests of the upstream, want 5 (index and four pages)", got)
	}
	for _, hive := range []string{"registration", "registration-gz"} {
		if b := e.get(rep, rRemote, "v3/"+hive+"/"+id+"/index.json"); b.Status != 200 {
			t.Errorf("%s: %d", hive, b.Status)
		}
	}
	if got := e.stub.Counts().Registrations(id); got != 5 {
		t.Errorf("%d registration requests after the other hives, want 5", got)
	}
	// A leaf of the last page is found, one the upstream does not have is not.
	if l := e.get(rep, rRemote, "v3/registration-semver2/"+id+"/1.199.0.json"); l.Status != 200 {
		t.Errorf("leaf 1.199.0: %d", l.Status)
	}
	if l := e.get(rep, rRemote, "v3/registration-semver2/"+id+"/9.0.0.json"); l.Status != 404 {
		t.Errorf("leaf 9.0.0: %d", l.Status)
	}
}

func TestR_NUGET_015_APackageIsCachedWithItsRowsAndServedWhenTheUpstreamIsDown(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	corpus := nugetstub.Default()
	pkg, _ := corpus.Get(nugetstub.NewtonsoftID)
	want := nugetstub.Nupkg(pkg, pkg.Versions[1])
	sum := sha512.Sum512(want)

	a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3"))
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if got := sha512.Sum512(a.Body); got != sum {
		t.Errorf("the SHA-512 of the package served differs from the upstream's")
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got := e.stub.Counts().Nupkg(newtonsoft, "13.0.3"); got != 1 {
		t.Errorf("%d package requests, want 1", got)
	}
	// The item and the rows of origin cache exist, with the declared casing.
	if got := e.itemCount(rRemote, newtonsoft+"/13.0.3/"); got != 1 {
		t.Errorf("%d items of the version, want 1", got)
	}
	if got := e.cacheRows(rRemote, newtonsoft); got != 1 {
		t.Errorf("%d NuGet rows of origin cache, want 1", got)
	}
	if got := e.st.Text(`SELECT p.display_name FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, rRemote, newtonsoft); got != "Newtonsoft.Json" {
		t.Errorf("display name = %q", got)
	}
	// The version list is cached too.
	if l := e.get(rep, rRemote, "v3/flatcontainer/newtonsoft.json/index.json"); l.Status != 200 {
		t.Fatalf("%d", l.Status)
	}

	// The upstream goes down: the package, the list and the registration are served from the cache.
	if err := e.stub.SetMode(nugetstub.Mode{Down: nugetstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	before := e.stub.Counts().Total()
	b := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3"))
	if b.Status != 200 || sha512.Sum512(b.Body) != sum {
		t.Errorf("package with the upstream down: %d", b.Status)
	}
	if l := e.get(rep, rRemote, "v3/flatcontainer/newtonsoft.json/index.json"); l.Status != 200 {
		t.Errorf("list with the upstream down: %d", l.Status)
	}
	if got := e.stub.Counts().Total(); got != before {
		t.Errorf("%d requests reached the upstream for cached content", got-before)
	}
	// The cache view serves the package too, and never asks.
	cv := e.get(rep, rRemote+"~cache", nupkgPath("Newtonsoft.Json", "13.0.3"))
	if cv.Status != 200 {
		t.Errorf("cache view: %d", cv.Status)
	}
	if cv := e.get(rep, rRemote+"~cache", nupkgPath("Newtonsoft.Json", "13.0.1")); cv.Status != 404 {
		t.Errorf("cache view of an uncached package: %d, want 404", cv.Status)
	}
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}

	// A cached package is found by the cache-only search of the repository.
	s := e.get(rep, rRemote, "v3/search?q=newtonsoft")
	if s.Status != 200 || !strings.Contains(string(s.Body), `"totalHits":1`) {
		t.Errorf("search: %d %.300s", s.Status, s.Body)
	}
}

func TestR_NUGET_015_StaleMetadataIsServedWhenTheUpstreamIsDownAndRefreshesWhenItIsBack(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	if a := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json"); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if err := e.stub.SetMode(nugetstub.Mode{Down: nugetstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	// After the period the document is stale, and the upstream cannot be asked.
	eventually(t, 20*time.Second, "a stale document is served", func() bool {
		a := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json")
		return a.Status == 200 && strings.Contains(a.Header.Get("Cache-Status"), "stale")
	})
	// The upstream is back with a new version: the list follows within one period.
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if err := e.stub.Publish(nugetstub.NewtonsoftID, "13.0.4", nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "the new version is listed", func() bool {
		a := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json")
		return a.Status == 200 && strings.Contains(string(a.Body), "13.0.4")
	})
	// The registration follows the same way, with the version in it.
	eventually(t, 20*time.Second, "the registration lists the new version", func() bool {
		a := e.get(rep, rFast, "v3/registration-semver2/newtonsoft.json/13.0.4.json")
		return a.Status == 200
	})
}

func TestR_NUGET_015_ARefreshOfAnUnchangedDocumentIsAConditionalRequest(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	first := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json")
	if first.Status != 200 {
		t.Fatalf("%d", first.Status)
	}
	etag := first.Header.Get("ETag")
	eventually(t, 20*time.Second, "a conditional refresh reached the upstream", func() bool {
		e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json")
		return e.stub.Counts().ConditionalRequests(newtonsoft) >= 1
	})
	// The refresh found the document unchanged: the validator of the rendering did not move.
	again := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json")
	if again.Header.Get("ETag") != etag {
		t.Errorf("ETag changed from %s to %s for an unchanged document", etag, again.Header.Get("ETag"))
	}
	if cond := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json", "If-None-Match", etag); cond.Status != 304 {
		t.Errorf("conditional request: %d, want 304", cond.Status)
	}
	head := e.st.Do(rep, "HEAD", "/api/nuget/"+rFast+"/v3/flatcontainer/newtonsoft.json/index.json", e.tokReader, nil)
	if head.Status != 200 || len(head.Body) != 0 || head.Header.Get("Content-Length") != fmt.Sprint(len(again.Body)) {
		t.Errorf("HEAD: %d, %d bytes, Content-Length %q", head.Status, len(head.Body), head.Header.Get("Content-Length"))
	}
}

func TestR_NUGET_015_AnIdentityMismatchIs502AndStoresNothing(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(nugetstub.Mode{WrongContent: []string{"Contoso.Core.Abstractions"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		a := e.get(rep, rRemote, nupkgPath("Contoso.Core.Abstractions", "1.0.0"))
		if a.Status != 502 || code(t, a) != "upstream_package_mismatch" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	}
	if got := e.itemCount(rRemote, abstract+"/"); got != 0 {
		t.Errorf("%d items after a mismatch, want none", got)
	}
	if got := e.cacheRows(rRemote, abstract); got != 0 {
		t.Errorf("%d NuGet rows after a mismatch, want none", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, rRemote, abstract); got != 0 {
		t.Errorf("%d package rows after a mismatch", got)
	}
	// Nothing was negatively cached: the right package is served as soon as the upstream is right.
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(rep, rRemote, nupkgPath("Contoso.Core.Abstractions", "1.0.0")); a.Status != 200 {
		t.Fatalf("after the upstream is right: %d %s", a.Status, a.Body)
	}
	if got := e.cacheRows(rRemote, abstract); got != 1 {
		t.Errorf("%d NuGet rows, want 1", got)
	}
}

func TestR_NUGET_015_AWithdrawnIDIsGoneUntilTheUpstreamServesItAgain(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	if a := e.get(rep, rFast, nupkgPath("Newtonsoft.Json", "13.0.1")); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if a := e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json"); a.Status != 200 {
		t.Fatalf("%d", a.Status)
	}
	if err := e.stub.SetMode(nugetstub.Mode{NotFound: []string{"Newtonsoft.Json"}}); err != nil {
		t.Fatal(err)
	}
	// The version list is the package document: its refresh finds the ID gone.
	eventually(t, 20*time.Second, "the version list answers 404", func() bool {
		return e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json").Status == 404
	})
	before := e.stub.Counts().Total()
	for _, repo := range []string{rFast, rFast + "~cache"} {
		for _, p := range []string{
			"v3/registration-semver2/newtonsoft.json/index.json", "v3/registration/newtonsoft.json/13.0.1.json",
			"v3/flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.nuspec", nupkgPath("Newtonsoft.Json", "13.0.1"),
		} {
			if a := e.get(rep, repo, p); a.Status != 404 {
				t.Errorf("%s %s: %d, want 404", repo, p, a.Status)
			}
		}
	}
	if got := e.stub.Counts().Total(); got != before {
		t.Errorf("%d requests reached the upstream for a gone ID", got-before)
	}
	// The cached package stays stored and listed.
	if got := e.itemCount(rFast, newtonsoft+"/13.0.1/"); got != 1 {
		t.Errorf("%d items of the gone ID, want the cached package to stay", got)
	}
	// The upstream serves the ID again: after the negative period it is back.
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the ID is served again", func() bool {
		return e.get(rep, rFast, "v3/flatcontainer/newtonsoft.json/index.json").Status == 200
	})
	if a := e.get(rep, rFast, nupkgPath("Newtonsoft.Json", "13.0.1")); a.Status != 200 {
		t.Errorf("package after the ID is back: %d", a.Status)
	}
}

func TestR_NUGET_015_PatternsKeepAnIDAwayFromTheUpstreamAndLeaveTheServiceIndex(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	before := e.stub.Counts().Total()
	for _, p := range []string{
		"v3/flatcontainer/contoso.core.abstractions/index.json", "v3/registration-semver2/contoso.core.abstractions/index.json",
		nupkgPath("Contoso.Core.Abstractions", "1.0.0"),
	} {
		if a := e.get(rep, rExclude, p); a.Status != 404 {
			t.Errorf("%s: %d, want 404", p, a.Status)
		}
	}
	if got := e.stub.Counts().Total(); got != before {
		t.Errorf("%d requests reached the upstream for an excluded ID", got-before)
	}
	// An ID outside the pattern is restorable: the service index, which names no package, is admitted.
	if a := e.get(rep, rExclude, "v3/flatcontainer/newtonsoft.json/index.json"); a.Status != 200 {
		t.Errorf("an ID outside the exclude pattern: %d %s", a.Status, a.Body)
	}
}

func TestR_NUGET_014_ResourcesOnAHostThatIsNeitherTheUpstreamNorAnExtraHostAreRefused(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	a := e.get(rep, rNoHost, "v3/flatcontainer/newtonsoft.json/index.json")
	if a.Status != 502 || code(t, a) != "foreign_host_denied" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	// The content host got no request; the API host serves the registrations of the same remote.
	for _, h := range e.stub.Counts().Hosts() {
		if strings.HasPrefix(h, "localhost") {
			t.Errorf("the host %s was asked", h)
		}
	}
	if b := e.get(rep, rNoHost, "v3/registration-semver2/newtonsoft.json/index.json"); b.Status != 200 {
		t.Errorf("registrations on the API host: %d %s", b.Status, b.Body)
	}
}

func TestR_NUGET_014_AnUpstreamWithoutPackageContentAnswers502OnTheFirstFetch(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(nugetstub.Mode{OmitResources: []string{"PackageBaseAddress"}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rep, rRemote, "v3/flatcontainer/newtonsoft.json/index.json")
	if a.Status != 502 || code(t, a) != "upstream_error" || !strings.Contains(string(a.Body), "PackageBaseAddress/3.0.0") {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	// Nothing was stored: the repaired index is used at once.
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if b := e.get(rep, rRemote, "v3/flatcontainer/newtonsoft.json/index.json"); b.Status != 200 {
		t.Errorf("after the repair: %d %s", b.Status, b.Body)
	}
}

// manyPages answers the registration index of contoso.huge with an index that lists pages pages
// by address in both hives, and counts the requests for a page of it; everything else goes to the
// upstream.
type manyPages struct {
	next  http.Handler
	pages int
	mu    sync.Mutex
	asked int
}

func (m *manyPages) wrap(next http.Handler) http.Handler { m.next = next; return m }

func (m *manyPages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, hive := range []string{"/v3/registration/contoso.huge/", "/v3/registration-gz/contoso.huge/"} {
		rest, ok := strings.CutPrefix(r.URL.Path, hive)
		if !ok {
			continue
		}
		if rest != "index.json" {
			m.mu.Lock()
			m.asked++
			m.mu.Unlock()
			http.Error(w, "not here", http.StatusNotFound)
			return
		}
		items := make([]string, m.pages)
		for i := range items {
			items[i] = fmt.Sprintf(`{"@id":"https://%s%spage/1.%d.0/1.%d.0.json","count":1,"lower":"1.%d.0","upper":"1.%d.0"}`, r.Host, hive, i, i, i, i)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"count":%d,"items":[%s]}`, m.pages, strings.Join(items, ","))
		return
	}
	m.next.ServeHTTP(w, r)
}

// TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIs502AndFetchesNoPage (Q-NUGET-83): an upstream
// whose registration index of an ID lists more pages than one rendering reads is answered 502
// nuget_invalid_upstream_document before a page is fetched, and nothing of the index is stored,
// so the next request is refused the same way and costs one index request, not one per page.
// Before, every page was fetched, stored and mapped, each retry paying again.
func TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIs502AndFetchesNoPage(t *testing.T) {
	mp := &manyPages{pages: 600}
	e := newRemoteEnv(t, remoteOptions{Wrap: mp.wrap})
	rep := e.start(1)[0]
	for range 2 {
		a := e.get(rep, rRemote, "v3/registration-semver2/contoso.huge/index.json")
		if a.Status != 502 || !strings.Contains(string(a.Body), "nuget_invalid_upstream_document") {
			t.Fatalf("%d %s, want 502 nuget_invalid_upstream_document", a.Status, a.Body)
		}
	}
	mp.mu.Lock()
	asked := mp.asked
	mp.mu.Unlock()
	if asked != 0 {
		t.Errorf("%d page requests, want none", asked)
	}
	if n := e.st.Number(`SELECT count(*) FROM remote_url_map m JOIN repositories r ON r.id = m.repo_id WHERE r.key = $1`, rRemote); n != 0 {
		t.Errorf("%d URL-map rows, want none", n)
	}
}

// gzipHives answers the documents of the gzip registration hive gzip-coded whatever the request
// asked for, as an upstream does whose blobs carry the coding themselves.
func gzipHives(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v3/registration-gz/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &recorder{header: http.Header{}, status: 200}
		next.ServeHTTP(rec, r)
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		body := rec.body.Bytes()
		if rec.status == 200 {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(body)
			_ = zw.Close()
			body = buf.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	})
}

// recorder holds the answer of a handler.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(code int)        { r.status = code }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }

func TestR_NUGET_015_AGzipHiveIsAnsweredToEveryClientInTheCodingItAccepts(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Wrap: gzipHives})
	rep := e.start(1)[0]
	id := strings.ToLower(nugetstub.ManyVersionsID)

	plain := e.get(rep, rRemote, "v3/registration-semver2/newtonsoft.json/index.json", "Accept-Encoding", "identity")
	if plain.Status != 200 || plain.Header.Get("Content-Encoding") != "" {
		t.Fatalf("identity: %d %q", plain.Status, plain.Header.Get("Content-Encoding"))
	}
	if d := registrationOf(t, plain); len(d.Items) != 1 || len(d.Items[0].Items) != 2 {
		t.Errorf("the document of a stored gzip hive: %+v", d)
	}
	zipped := e.get(rep, rRemote, "v3/registration-semver2/newtonsoft.json/index.json", "Accept-Encoding", "gzip")
	if zipped.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %d %q", zipped.Status, zipped.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped.Body))
	if err != nil {
		t.Fatal(err)
	}
	inflated, _ := io.ReadAll(zr)
	if !bytes.Equal(inflated, plain.Body) {
		t.Error("the gzip and the identity variant differ in content")
	}
	if zipped.Header.Get("ETag") != plain.Header.Get("ETag") {
		t.Errorf("ETags differ: %s %s", zipped.Header.Get("ETag"), plain.Header.Get("ETag"))
	}
	// The pages of a large ID are stored gzip-coded too, and the plain hive serves identity.
	d := registrationOf(t, e.get(rep, rRemote, "v3/registration/"+id+"/index.json"))
	if len(d.Items) != 4 {
		t.Fatalf("%d pages", len(d.Items))
	}
	path := strings.TrimPrefix(d.Items[1].ID, feedBase+"/api/nuget/"+rRemote+"/")
	pg := e.get(rep, rRemote, path)
	if pg.Status != 200 || pg.Header.Get("Content-Encoding") != "" || !json.Valid(pg.Body) {
		t.Errorf("page: %d %q valid=%v", pg.Status, pg.Header.Get("Content-Encoding"), json.Valid(pg.Body))
	}
}

// altPackageContent serves the flat container nowhere and the packages under /v3-alt/, which the
// registrations announce as packageContent: a feed whose flat container does not have a version
// its registration lists.
type altPackageContent struct {
	next http.Handler
	mu   sync.Mutex
	alt  map[string]int
}

func (a *altPackageContent) wrap(next http.Handler) http.Handler {
	a.next, a.alt = next, map[string]int{}
	return a
}

func (a *altPackageContent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v3-alt/"):
		a.mu.Lock()
		a.alt[r.URL.Path]++
		a.mu.Unlock()
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/v3-flatcontainer/" + strings.TrimPrefix(r.URL.Path, "/v3-alt/")
		r2.URL.RawPath = ""
		a.next.ServeHTTP(w, r2)
	case strings.HasPrefix(r.URL.Path, "/v3-flatcontainer/") && strings.HasSuffix(r.URL.Path, ".nupkg"):
		http.Error(w, "not here", http.StatusNotFound)
	case strings.HasPrefix(r.URL.Path, "/v3/registration"):
		rec := &recorder{header: http.Header{}, status: 200}
		a.next.ServeHTTP(rec, r)
		body := bytes.ReplaceAll(rec.body.Bytes(), []byte("/v3-flatcontainer/"), []byte("/v3-alt/"))
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	default:
		a.next.ServeHTTP(w, r)
	}
}

func (a *altPackageContent) hits() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.alt {
		n += c
	}
	return n
}

func TestR_NUGET_015_APackageTheFlatContainerDoesNotHaveIsFetchedFromThePackageContentOfItsLeaf(t *testing.T) {
	alt := &altPackageContent{}
	e := newRemoteEnv(t, remoteOptions{Wrap: alt.wrap})
	rep := e.start(1)[0]

	// The registration names the address; reading it records the address of the version.
	if a := e.get(rep, rRemote, "v3/registration-semver2/newtonsoft.json/index.json"); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3"))
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if alt.hits() != 1 {
		t.Errorf("%d requests for the address of the leaf, want 1", alt.hits())
	}
	if e.cacheRows(rRemote, newtonsoft) != 1 {
		t.Error("the package was cached without its row")
	}
}

func TestR_NUGET_003_ANonASCIIIDOnARemoteIsServedAndCached(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	const declared = "Ünïcode.Pkg"
	if err := e.stub.Publish(declared, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	escaped := url.PathEscape("Ünïcode.pkg") // only ASCII letters are lowercased by trove
	l := e.get(rep, rRemote, "v3/flatcontainer/"+escaped+"/index.json")
	if l.Status != 200 || strings.Join(versionsOf(t, l), ",") != "1.0.0" {
		t.Fatalf("version list: %d %s", l.Status, l.Body)
	}
	r := e.get(rep, rRemote, "v3/registration-semver2/"+escaped+"/index.json")
	if r.Status != 200 || !strings.Contains(string(r.Body), `"id":"`+declared+`"`) {
		t.Fatalf("registration: %d %s", r.Status, r.Body)
	}
	if !strings.Contains(string(r.Body), feed(rRemote)+"flatcontainer/"+escaped+"/1.0.0/"+escaped+".1.0.0.nupkg") {
		t.Errorf("the package address is not the escaped one: %s", r.Body)
	}
	n := e.get(rep, rRemote, "v3/flatcontainer/"+escaped+"/1.0.0/"+escaped+".1.0.0.nupkg")
	if n.Status != 200 {
		t.Fatalf("package: %d %s", n.Status, n.Body)
	}
	if got := e.cacheRows(rRemote, "Ünïcode.pkg"); got != 1 {
		t.Errorf("%d rows for the ID, want 1", got)
	}
	if got := e.st.Text(`SELECT p.display_name FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, rRemote, "Ünïcode.pkg"); got != declared {
		t.Errorf("display name = %q", got)
	}
}

func TestR_NUGET_031_FiftyConcurrentRestoresOverThreeReplicasCauseBoundedUpstreamRequests(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		name, limit := "Valkey stopped", 3
		if valkey {
			name, limit = "Valkey running", 1
		}
		t.Run(name, func(t *testing.T) {
			e := newRemoteEnv(t, remoteOptions{Valkey: valkey})
			reps := e.start(3)
			// A slow upstream makes the requests overlap, as a cold cache does.
			if err := e.stub.SetMode(nugetstub.Mode{DelayMS: map[string]int{nugetstub.ClassNupkg: 600, nugetstub.ClassFlat: 300, nugetstub.ClassRegistration: 300}}); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			failures := make(chan string, 150)
			for i := range 50 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for _, p := range []string{
						"v3/flatcontainer/contoso.core.abstractions/index.json",
						"v3/registration-semver2/contoso.core.abstractions/index.json",
						nupkgPath("Contoso.Core.Abstractions", "1.0.0"),
					} {
						if a := e.get(reps[i%3], rRemote, p); a.Status != 200 {
							failures <- fmt.Sprintf("%s: %d %s", p, a.Status, a.Body)
						}
					}
				}()
			}
			wg.Wait()
			close(failures)
			for f := range failures {
				t.Error(f)
			}
			c := e.stub.Counts()
			if got := c.Nupkg(abstract, "1.0.0"); got < 1 || got > limit {
				t.Errorf("%d package requests for 50 restores, want at most %d", got, limit)
			}
			if got := c.VersionLists(abstract); got < 1 || got > limit {
				t.Errorf("%d version list requests, want at most %d", got, limit)
			}
			if got := c.Registrations(abstract); got < 1 || got > limit {
				t.Errorf("%d registration requests, want at most %d", got, limit)
			}
			if got := c.Requests("GET", "/v3/index.json"); got < 1 || got > limit {
				t.Errorf("%d service index requests, want at most %d", got, limit)
			}
			if got := e.cacheRows(rRemote, abstract); got != 1 {
				t.Errorf("%d rows, want one however many requests", got)
			}
		})
	}
}

func TestR_NUGET_030_ReplicasRenderTheSameBytesAndTheSameValidator(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Valkey: true})
	reps := e.start(2)
	for _, p := range []string{
		"v3/flatcontainer/newtonsoft.json/index.json", "v3/registration-semver2/newtonsoft.json/index.json",
		"v3/registration/newtonsoft.json/13.0.1.json",
	} {
		a, b := e.get(reps[0], rRemote, p), e.get(reps[1], rRemote, p)
		if a.Status != 200 || !bytes.Equal(a.Body, b.Body) || a.Header.Get("ETag") != b.Header.Get("ETag") || a.Header.Get("ETag") == "" {
			t.Errorf("%s: replicas differ: %d/%d etag %q/%q", p, a.Status, b.Status, a.Header.Get("ETag"), b.Header.Get("ETag"))
		}
	}
}

func TestR_NUGET_015_APushToARemoteIs405(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	a := e.st.Do(rep, "PUT", "/api/nuget/"+rRemote+"/v3/package", e.tokAdmin, []byte("x"))
	if a.Status != 405 {
		t.Errorf("push: %d %s", a.Status, a.Body)
	}
	if got := e.stub.Counts().Total(); got != 0 {
		t.Errorf("%d requests reached the upstream", got)
	}
}

func TestR_AUTHZ_016_AnIDTheCallerMayNotReadIsNotFetched(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	a := e.st.Do(rep, "GET", "/api/nuget/"+rRemote+"/v3/flatcontainer/newtonsoft.json/index.json", e.tokNobody, nil)
	if a.Status == 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if got := e.stub.Counts().Total(); got != 0 {
		t.Errorf("%d requests reached the upstream for a caller without a grant", got)
	}
}

// answerPackages changes what the upstream answers for the packages of one ID: garbage serves
// bytes that are no package, other serves the package of another version.
func answerPackages(garbage, otherVersion bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const prefix = "/v3-flatcontainer/contoso.core.abstractions/"
			if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, ".nupkg") {
				next.ServeHTTP(w, r)
				return
			}
			switch {
			case garbage:
				_, _ = w.Write([]byte("this is not a package"))
			case otherVersion:
				r2 := r.Clone(r.Context())
				r2.URL.Path = prefix + "1.0.0/contoso.core.abstractions.1.0.0.nupkg"
				r2.URL.RawPath = ""
				next.ServeHTTP(w, r2)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

func TestR_NUGET_015_APackageThatIsNoPackageIsServedAsItCameCountedAndGetsNoRows(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Wrap: answerPackages(true, false)})
	rep := e.start(1)[0]
	a := e.get(rep, rRemote, nupkgPath("Contoso.Core.Abstractions", "1.0.0"))
	if a.Status != 200 || string(a.Body) != "this is not a package" {
		t.Fatalf("%d %.100s", a.Status, a.Body)
	}
	if got := e.itemCount(rRemote, abstract+"/1.0.0/"); got != 1 {
		t.Errorf("%d items, want the bytes stored as they came", got)
	}
	if got := e.cacheRows(rRemote, abstract); got != 0 {
		t.Errorf("%d NuGet rows for bytes that are no package", got)
	}
	if got := sumSeries(rep.Metrics(t), "trove_nuget_remote_unparseable_packages_total{"); got != 1 {
		t.Errorf("trove_nuget_remote_unparseable_packages_total = %v, want 1", got)
	}
	// A second request is served from the cache.
	if b := e.get(rep, rRemote, nupkgPath("Contoso.Core.Abstractions", "1.0.0")); b.Status != 200 || string(b.Body) != "this is not a package" {
		t.Errorf("second request: %d %.100s", b.Status, b.Body)
	}
}

func TestR_NUGET_015_APackageOfAnotherVersionAtTheURLOfThisOneIs502(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Wrap: answerPackages(false, true)})
	rep := e.start(1)[0]
	a := e.get(rep, rRemote, nupkgPath("Contoso.Core.Abstractions", "1.1.0"))
	if a.Status != 502 || code(t, a) != "upstream_package_mismatch" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if e.itemCount(rRemote, abstract+"/") != 0 || e.cacheRows(rRemote, abstract) != 0 {
		t.Error("something was stored for a package of another version")
	}
}

var _ = http.StatusOK
