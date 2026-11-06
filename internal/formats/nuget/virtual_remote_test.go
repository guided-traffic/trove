package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/repository"
)

// gzipped codes b with gzip, as nuget.org stores the documents of its gzip hives.
func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pagedUpstream is the registration of n versions 1.i.0 of Acme.Widgets as nuget.org serves an
// ID with 128 or more versions: an index that lists pages of 64 leaves by address and bounds,
// and the pages, gzip-coded, by the canonical paths the remote stores them at (R-NUGET-015).
func pagedUpstream(t *testing.T, n int) (index []byte, pages map[string][]byte) {
	t.Helper()
	pages = map[string][]byte{}
	var refs []string
	for lo := 0; lo < n; lo += 64 {
		hi := min(lo+64, n) - 1
		lower, upper := fmt.Sprintf("1.%d.0", lo), fmt.Sprintf("1.%d.0", hi)
		var leaves []string
		for i := lo; i <= hi; i++ {
			leaves = append(leaves, fmt.Sprintf(`{"@id":"https://up.test/reg/acme.widgets/1.%d.0.json","catalogEntry":{"id":"Acme.Widgets","version":"1.%d.0","description":"remote","listed":true}}`, i, i))
		}
		url := "https://up.test/reg/acme.widgets/page/" + lower + "/" + upper + ".json"
		refs = append(refs, fmt.Sprintf(`{"@id":%q,"count":%d,"lower":%q,"upper":%q}`, url, hi-lo+1, lower, upper))
		pages[RegistrationPagePath("acme.widgets", lower, upper)] = gzipped(t,
			[]byte(fmt.Sprintf(`{"@id":%q,"count":%d,"lower":%q,"upper":%q,"items":[%s]}`, url, hi-lo+1, lower, upper, strings.Join(leaves, ","))))
	}
	return gzipped(t, []byte(`{"count":`+fmt.Sprint(len(refs))+`,"items":[`+strings.Join(refs, ",")+`]}`)), pages
}

// remoteMemberHost is a virtual repository "all" whose member "remote" stores the documents
// given; the merge reads the pages through the member (Request.Member).
func remoteMemberHost(t *testing.T, res *mergeResolver, fetcher formats.RemoteFetcher) *formattest.Host {
	t.Helper()
	f := New()
	res.f = f
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "all", Data: newDocStore(), Resolver: res, Remote: fetcher})
	member := repository.Repository{Name: "7", Key: "remote", Class: repository.Remote, Format: string(ID), Lifecycle: repository.Active}
	*h.Repo() = repository.Assemble(*h.Repo(), []repository.Repository{member}, nil, 60)
	return h
}

// TestR_NUGET_017_AGzipCodedRemoteMemberIsMerged (Q-NUGET-82): a remote stores the upstream's
// bytes as they came, and nuget.org codes its registration hive and may code any document with
// gzip whatever the request asked for. The merge reads the version list and the registration of
// such a member; it failed on the first gzip byte before.
func TestR_NUGET_017_AGzipCodedRemoteMemberIsMerged(t *testing.T) {
	reg := remotePart("remote", 1, "", "1.0.0", "2.0.0")
	reg.Doc = gzipped(t, reg.Doc)
	flat := remoteFlat("remote", 1, "1.0.0", "2.0.0")
	flat.Doc = gzipped(t, flat.Doc)
	res := &mergeResolver{docs: docsOf(
		[]formats.MemberDocument{localFlat(t, "local", 0, "0.9.0"), flat},
		[]formats.MemberDocument{localPart(t, "local", 0, registrationVersion("Acme.Widgets", "0.9.0", true, `{}`)), reg},
	)}
	h := virtualHost(t, res, nil)
	r := get(t, h, http.MethodGet, "v3/flatcontainer/acme.widgets/index.json")
	if r.Code != http.StatusOK || r.Body.String() != `{"versions":["0.9.0","1.0.0","2.0.0"]}` {
		t.Fatalf("version list: %d %s", r.Code, r.Body.String())
	}
	if ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json")); len(ls) != 3 {
		t.Fatalf("%d leaves, want 3: %v", len(ls), ls)
	}
}

// TestR_NUGET_017_ARemoteMemberWithPagesByAddressIsMergedWhole (Q-NUGET-82): an upstream lists
// the pages of an ID with 128 or more versions by address. The merge reads them through the
// member and lists every version, with trove's paging and the virtual repository's addresses; it
// refused the member as an upstream failure before, so the whole document answered 502.
func TestR_NUGET_017_ARemoteMemberWithPagesByAddressIsMergedWhole(t *testing.T) {
	index, pages := pagedUpstream(t, 200)
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{
		localPart(t, "local", 0, registrationVersion("Acme.Widgets", "1.5.0", true, `{"description":"local"}`)),
		{Member: formats.RepoRef{Name: "remote"}, Tier: formats.TierRemote, Position: 1, Doc: index},
	})}
	h := remoteMemberHost(t, res, &formattest.MemRemote{Docs: pages})
	r := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json")
	if r.Code != http.StatusOK {
		t.Fatalf("index: %d %s", r.Code, r.Body.String())
	}
	var idx struct {
		Count int `json:"count"`
		Items []struct {
			ID    string `json:"@id"`
			Count int    `json:"count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range idx.Items {
		total += p.Count
		if !strings.HasPrefix(p.ID, feedOfAll) {
			t.Errorf("page %q is no address of the virtual repository", p.ID)
		}
	}
	if total != 200 || len(idx.Items) != 4 {
		t.Fatalf("%d versions on %d pages, want 200 on 4 (trove's paging of 64)", total, len(idx.Items))
	}
	page := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/page/1.128.0/1.191.0.json")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"count":64`) {
		t.Fatalf("page: %d %.300s", page.Code, page.Body.String())
	}
	leaf := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/1.5.0.json")
	if leaf.Code != http.StatusOK {
		t.Fatalf("leaf: %d", leaf.Code)
	}
	first := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/page/1.0.0/1.63.0.json")
	var pg struct {
		Items []struct {
			CatalogEntry map[string]any `json:"catalogEntry"`
		} `json:"items"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &pg); err != nil || len(pg.Items) != 64 {
		t.Fatalf("first page: %d %v %.200s", first.Code, err, first.Body.String())
	}
	for _, l := range pg.Items {
		if l.CatalogEntry["version"] == "1.5.0" && l.CatalogEntry["description"] != "local" {
			t.Errorf("1.5.0 came from the remote member, the local member holds it: %v", l.CatalogEntry)
		}
	}
}

// pageless serves the documents of a MemRemote and answers a page it does not hold the way the
// remote engine answers a caller who may not fill the member's cache.
type pageless struct{ *formattest.MemRemote }

func (p pageless) Metadata(ctx context.Context, rq *formats.Request, path formats.Path, variant string) (*formats.RemoteDocument, error) {
	if _, ok := p.Docs[path.Path]; !ok {
		return nil, errs.E(errs.Forbidden, errs.CodePopulateNotPermitted, "The caller may read the member but not bring new content into its cache.")
	}
	return p.MemRemote.Metadata(ctx, rq, path, variant)
}

// TestR_AUTHZ_008_ARestrictedRemoteMemberContributesTheCachedVersionsOfTheStoredPages: a caller
// who may not fill the member cannot have a page fetched. The member contributes what its stored
// pages say of the versions it has cached, and the merge does not fail; a member the caller may
// fill fails as an upstream failure instead of contributing a short list.
func TestR_AUTHZ_008_ARestrictedRemoteMemberContributesTheCachedVersionsOfTheStoredPages(t *testing.T) {
	index, pages := pagedUpstream(t, 200)
	delete(pages, RegistrationPagePath("acme.widgets", "1.64.0", "1.127.0"))
	member := formats.MemberDocument{
		Member: formats.RepoRef{Name: "remote"}, Tier: formats.TierRemote, Position: 0, Doc: index,
		CachedVersions: map[string]bool{"1.3.0": true, "1.70.0": true, "1.150.0": true}, Restricted: true,
	}
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{member})}
	h := remoteMemberHost(t, res, pageless{&formattest.MemRemote{Docs: pages}})
	ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"))
	var got []string
	for _, l := range ls {
		got = append(got, l["version"].(string))
	}
	if strings.Join(got, ",") != "1.3.0,1.150.0" {
		t.Errorf("versions = %v, want the cached versions of the stored pages", got)
	}

	member.Restricted = false
	res2 := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{member})}
	h2 := remoteMemberHost(t, res2, &formattest.MemRemote{Docs: pages})
	if r := get(t, h2, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"); r.Code != http.StatusBadGateway {
		t.Errorf("a page the upstream does not have: %d %s, want 502", r.Code, r.Body.String())
	}
}

// TestR_NUGET_017_ARemoteEntryShowsTheSameThroughTheVirtualRepository (Q-NUGET-82): an upstream
// entry without an id, and with members the remote keeps, is rendered through the virtual
// repository as the remote renders it: the ID of the request and the kept members. The merge
// rendered "id":"" and dropped the members before.
func TestR_NUGET_017_ARemoteEntryShowsTheSameThroughTheVirtualRepository(t *testing.T) {
	doc := `{"count":1,"items":[{"@id":"https://up/index.json#page","count":1,"items":[{"@id":"https://up/1.0.0.json","catalogEntry":` +
		`{"version":"1.0.0","copyright":"(c) Acme","packageHashAlgorithm":"SHA512","summary":"s"}}]}]}`
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{
		{Member: formats.RepoRef{Name: "remote"}, Tier: formats.TierRemote, Position: 0, Doc: []byte(doc)},
	})}
	h := virtualHost(t, res, nil)
	ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"))
	if len(ls) != 1 {
		t.Fatalf("leaves = %v", ls)
	}
	if ls[0]["id"] != "acme.widgets" || ls[0]["copyright"] != "(c) Acme" || ls[0]["packageHashAlgorithm"] != "SHA512" {
		t.Errorf("entry = %v, want the request's ID and the kept members", ls[0])
	}
}
