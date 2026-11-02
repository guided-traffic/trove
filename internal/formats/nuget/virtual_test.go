package nuget

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/repository"
)

// mergeResolver stands in for the virtual engine: Metadata merges the member documents the
// test names with the format's own adapter, the way the engine does after it collected
// them, and counts the merges. The engine itself is tested in internal/virtual and over a
// database in test/integration/nuget.
type mergeResolver struct {
	formats.Resolver
	f *Format
	// docs are the member documents of a path.
	docs    func(p formats.Path) []formats.MemberDocument
	partial bool
	// sameFingerprint gives every merge of a path one fingerprint, so a test can tell a
	// rendering that was kept from one made again.
	sameFingerprint bool
	err             error
	merges          int

	plan   *formats.VirtualPlan
	keeps  func(member, term string) bool
	scope  formats.ReadScope
	target error
	served []string
}

func (r *mergeResolver) Metadata(ctx context.Context, rq *formats.Request, p formats.Path, _ formats.PathInfo, variant string) (*formats.Merged, error) {
	r.merges++
	if r.err != nil {
		return nil, r.err
	}
	docs := r.docs(p)
	body, err := r.f.Virtual().Merge(ctx, rq, p, docs)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(variant))
	for _, d := range docs {
		if r.sameFingerprint {
			break
		}
		h.Write([]byte(d.Member.Key))
		h.Write(d.Doc)
	}
	m := &formats.Merged{Bytes: body, Partial: r.partial, CacheStatus: "trove; fwd=miss"}
	copy(m.MergeFingerprint[:], h.Sum(nil))
	if r.partial {
		m.CacheStatus = "trove; detail=partial"
	}
	return m, nil
}

func (r *mergeResolver) Plan(context.Context, *formats.Request, formats.Path, formats.PathInfo) (*formats.VirtualPlan, error) {
	return r.plan, nil
}

func (r *mergeResolver) KeepsAway(_ context.Context, _ *formats.Request, member formats.RepoRef, term string) (bool, error) {
	return r.keeps != nil && r.keeps(member.Key, term), nil
}

func (r *mergeResolver) ReadScope(context.Context, *formats.Request, []string) (formats.ReadScope, error) {
	return r.scope, nil
}

func (r *mergeResolver) DeployTarget(context.Context, *formats.Request) (*repository.Repository, error) {
	if r.target != nil {
		return nil, r.target
	}
	return &repository.Repository{ID: "2", Key: "target", Class: repository.Local, Format: string(ID)}, nil
}

func (r *mergeResolver) Artifact(_ context.Context, rq *formats.Request, p formats.Path, _ formats.PathInfo, w http.ResponseWriter) error {
	r.served = append(r.served, p.Path)
	w.Header().Set("Trove-Resolved-From", "member-a")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("bytes of " + p.Path))
	return nil
}

func virtualHost(t *testing.T, r *mergeResolver, data any) *formattest.Host {
	t.Helper()
	f := New()
	r.f = f
	if data == nil {
		data = newDocStore()
	}
	return formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "all", Data: data, Resolver: r})
}

const feedOfAll = "https://trove.test/api/nuget/all/v3/"

// localPart is the document of a local member at the path of the registration entries.
func localPart(t *testing.T, key string, position int, vs ...rows.RegistrationVersion) formats.MemberDocument {
	t.Helper()
	entries := make([]merge.Entry, len(vs))
	for i, v := range vs {
		entries[i] = entryOf(v)
	}
	b, err := merge.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	return formats.MemberDocument{Member: formats.RepoRef{Key: key}, Tier: formats.TierLocal, Position: position, Doc: b}
}

func localFlat(t *testing.T, key string, position int, versions ...string) formats.MemberDocument {
	t.Helper()
	b, err := merge.EncodeFlat(versions)
	if err != nil {
		t.Fatal(err)
	}
	return formats.MemberDocument{Member: formats.RepoRef{Key: key}, Tier: formats.TierLocal, Position: position, Doc: b}
}

// remotePart is an inlined upstream registration index of Acme.Widgets with one catalog entry
// per version given.
func remotePart(key string, position int, extra string, versions ...string) formats.MemberDocument {
	var leaves []string
	for _, v := range versions {
		leaves = append(leaves, `{"@id":"https://up/`+v+`.json","catalogEntry":{"id":"Acme.Widgets","version":"`+v+`","description":"from `+key+`",`+extra+`"listed":true}}`)
	}
	return formats.MemberDocument{
		Member: formats.RepoRef{Key: key}, Tier: formats.TierRemote, Position: position,
		Doc: []byte(`{"count":1,"items":[{"@id":"https://up/index.json#page","count":` + string(rune('0'+len(versions))) + `,"items":[` + strings.Join(leaves, ",") + `]}]}`),
	}
}

func remoteFlat(key string, position int, versions ...string) formats.MemberDocument {
	b, _ := json.Marshal(map[string][]string{"versions": versions})
	return formats.MemberDocument{Member: formats.RepoRef{Key: key}, Tier: formats.TierRemote, Position: position, Doc: b}
}

func docsOf(flat, reg []formats.MemberDocument) func(formats.Path) []formats.MemberDocument {
	return func(p formats.Path) []formats.MemberDocument {
		if strings.Contains(p.Path, canonRegistration) {
			return reg
		}
		return flat
	}
}

// TestR_NUGET_017_TheVersionListIsTheUnionOfTheMembers: the flat container of a virtual
// repository lists every version of every member that took part, once, in version order, and
// answers with the weak validator of the other documents (R-NUGET-002).
func TestR_NUGET_017_TheVersionListIsTheUnionOfTheMembers(t *testing.T) {
	res := &mergeResolver{docs: docsOf([]formats.MemberDocument{
		localFlat(t, "local", 0, "1.0.0", "2.0.0"), remoteFlat("remote", 1, "2.0.0", "10.0.0", "2.1.0-beta"),
	}, nil)}
	h := virtualHost(t, res, nil)
	r := get(t, h, http.MethodGet, "v3/flatcontainer/Acme.Widgets/index.json")
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if want := `{"versions":["1.0.0","2.0.0","2.1.0-beta","10.0.0"]}`; r.Body.String() != want {
		t.Errorf("body = %s, want %s", r.Body.String(), want)
	}
	if r.Header().Get("Content-Type") != "application/json; charset=utf-8" || !strings.HasPrefix(r.Header().Get("ETag"), `W/"`) {
		t.Errorf("headers: %v", r.Header())
	}
	if r.Header().Get("Cache-Status") != "trove; fwd=miss" {
		t.Errorf("Cache-Status = %q", r.Header().Get("Cache-Status"))
	}
	head := get(t, h, http.MethodHead, "v3/flatcontainer/acme.widgets/index.json")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != r.Header().Get("ETag") ||
		head.Header().Get("Content-Length") != r.Header().Get("Content-Length") {
		t.Errorf("HEAD: %d %v", head.Code, head.Header())
	}
}

// registrationOf decodes the leaves of the registration index of a small package.
func leavesOf(t *testing.T, r *formattest.Response) []map[string]any {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var idx struct {
		Items []struct {
			Items []struct {
				CatalogEntry map[string]any `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, p := range idx.Items {
		for _, l := range p.Items {
			out = append(out, l.CatalogEntry)
		}
	}
	return out
}

// TestR_NUGET_017_ARegistrationEntryComesFromTheMemberTierResolutionPicks: a version two members
// hold shows the metadata of the local one (the bytes the client downloads are the local
// ones), a version only the remote holds shows the upstream's entry with its deprecation passed
// through, an unlisted local version stays unlisted, and every address is the virtual
// repository's.
func TestR_NUGET_017_ARegistrationEntryComesFromTheMemberTierResolutionPicks(t *testing.T) {
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{
		localPart(t, "local", 0,
			registrationVersion("Acme.Widgets", "1.0.0", false, `{"description":"local 1.0.0"}`),
			registrationVersion("Acme.Widgets", "2.0.0", true, `{"description":"local 2.0.0"}`)),
		remotePart("remote", 1, `"deprecation":{"message":"old"},`, "2.0.0", "3.0.0", "3.1.0+build.5"),
	})}
	h := virtualHost(t, res, nil)

	ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"))
	if len(ls) != 4 {
		t.Fatalf("%d leaves, want 4: %v", len(ls), ls)
	}
	byVersion := map[string]map[string]any{}
	for _, l := range ls {
		byVersion[l["version"].(string)] = l
	}
	if byVersion["2.0.0"]["description"] != "local 2.0.0" || byVersion["3.0.0"]["description"] != "from remote" {
		t.Errorf("the entries came from the wrong members: %v / %v", byVersion["2.0.0"]["description"], byVersion["3.0.0"]["description"])
	}
	if byVersion["1.0.0"]["listed"] != false {
		t.Errorf("an unlisted local version is listed: %v", byVersion["1.0.0"])
	}
	if byVersion["3.0.0"]["deprecation"] == nil || byVersion["2.0.0"]["deprecation"] != nil {
		t.Errorf("the deprecation of the upstream entry is passed through, and only on its own version: %v", byVersion)
	}
	for _, l := range ls {
		for _, k := range []string{"@id", "packageContent"} {
			if s := l[k].(string); !strings.HasPrefix(s, feedOfAll) {
				t.Errorf("%s of %v = %q does not point at the virtual repository", k, l["version"], s)
			}
		}
	}
	// The SemVer 1 hive leaves out the version with build metadata, the leaf and the page
	// are cut from the same merge.
	if ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration/acme.widgets/index.json")); len(ls) != 3 {
		t.Errorf("legacy hive lists %d versions, want 3", len(ls))
	}
	leaf := get(t, h, http.MethodGet, "v3/registration-gz/acme.widgets/3.0.0.json")
	if leaf.Code != http.StatusOK || !strings.Contains(leaf.Body.String(), `"packageContent":"`+feedOfAll+`flatcontainer/acme.widgets/3.0.0/`) {
		t.Errorf("leaf: %d %s", leaf.Code, leaf.Body.String())
	}
	page := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/page/1.0.0/2.0.0.json")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"count":2`) {
		t.Errorf("page: %d %s", page.Code, page.Body.String())
	}
	if get(t, h, http.MethodGet, "v3/registration/acme.widgets/9.9.9.json").Code != http.StatusNotFound {
		t.Error("a leaf of a version no member holds is not 404")
	}
}

// TestR_NUGET_017_AMemberTheCallerMayNotFillListsItsCachedVersionsOnly: the merge hands the
// handler only what can be downloaded, and the handler does not add anything of its own.
func TestR_NUGET_017_AMemberTheCallerMayNotFillListsItsCachedVersionsOnly(t *testing.T) {
	remote := remotePart("remote", 1, "", "1.0.0", "2.0.0")
	remote.CachedVersions, remote.Restricted = map[string]bool{"2.0.0": true}, true
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{remote})}
	h := virtualHost(t, res, nil)
	ls := leavesOf(t, get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"))
	if len(ls) != 1 || ls[0]["version"] != "2.0.0" {
		t.Fatalf("leaves = %v, want 2.0.0 alone", ls)
	}
}

// TestR_NUGET_002_TheValidatorOfAVirtualDocumentFollowsTheMerge: the weak validator is a
// function of the merge fingerprint and the serving inputs, so a conditional request after a
// push to a member (a new fingerprint) answers 200, and an unchanged merge answers 304 without
// rendering again.
func TestR_NUGET_002_TheValidatorOfAVirtualDocumentFollowsTheMerge(t *testing.T) {
	local := localPart(t, "local", 0, registrationVersion("Acme.Widgets", "1.0.0", true, `{}`))
	res := &mergeResolver{docs: docsOf(nil, []formats.MemberDocument{local})}
	h := virtualHost(t, res, nil)
	first := get(t, h, http.MethodGet, "v3/registration-gz/acme.widgets/index.json")
	etag := first.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q", etag)
	}
	ifNone := func(v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set("If-None-Match", v) } }
	if r := get(t, h, http.MethodGet, "v3/registration-gz/acme.widgets/index.json", ifNone(etag)); r.Code != http.StatusNotModified || r.Body.Len() != 0 ||
		r.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("same merge: %d, body %d, Vary %q", r.Code, r.Body.Len(), r.Header().Get("Vary"))
	}
	// Another hive of the same merge is another document.
	if other := get(t, h, http.MethodGet, "v3/registration-semver2/acme.widgets/index.json"); other.Header().Get("ETag") == etag {
		t.Error("two hives share a validator")
	}
	// A push to a member changes the fingerprint.
	res.docs = docsOf(nil, []formats.MemberDocument{
		localPart(t, "local", 0, registrationVersion("Acme.Widgets", "1.0.0", true, `{}`), registrationVersion("Acme.Widgets", "1.1.0", true, `{}`)),
	})
	after := get(t, h, http.MethodGet, "v3/registration-gz/acme.widgets/index.json", ifNone(etag))
	if after.Code != http.StatusOK || after.Header().Get("ETag") == etag || !bytes.Contains(after.Body.Bytes(), []byte("1.1.0")) {
		t.Errorf("after a push: %d, ETag %q, %s", after.Code, after.Header().Get("ETag"), after.Body.String())
	}
	// gzip and identity share the value.
	gz := get(t, h, http.MethodGet, "v3/registration-gz/acme.widgets/index.json", func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("ETag") != after.Header().Get("ETag") {
		t.Errorf("gzip: %v", gz.Header())
	}
}

// TestR_NUGET_017_APartialMergeIsServedAndNeverKept: a merge that left out a member that could
// not be asked answers with its Cache-Status and is counted, and its rendering is not kept: the
// complete merge afterwards (with the same fingerprint, to tell the two renderings apart) is
// rendered again, and that one is kept.
func TestR_NUGET_017_APartialMergeIsServedAndNeverKept(t *testing.T) {
	one := localPart(t, "local", 0, registrationVersion("Acme.Widgets", "1.0.0", true, `{}`))
	two := localPart(t, "local", 0, registrationVersion("Acme.Widgets", "1.0.0", true, `{}`), registrationVersion("Acme.Widgets", "2.0.0", true, `{}`))
	three := localPart(t, "local", 0, registrationVersion("Acme.Widgets", "3.0.0", true, `{}`))
	res := &mergeResolver{partial: true, sameFingerprint: true, docs: docsOf(nil, []formats.MemberDocument{one})}
	h := virtualHost(t, res, nil)
	m := observability.NewMetrics("web")
	res.f.Bind(h, &config.Config{}, m)
	const path = "v3/registration-semver2/acme.widgets/index.json"
	ae := func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") }
	if r := get(t, h, http.MethodGet, path, ae); r.Code != http.StatusOK || r.Header().Get("Cache-Status") != "trove; detail=partial" {
		t.Fatalf("partial: %d %v", r.Code, r.Header())
	}
	if got := counted(t, m, "trove_nuget_virtual_member_skipped_total"); got != 1 {
		t.Errorf("trove_nuget_virtual_member_skipped_total = %v, want 1", got)
	}
	res.partial, res.docs = false, docsOf(nil, []formats.MemberDocument{two})
	if r := get(t, h, http.MethodGet, path); len(leavesOf(t, r)) != 2 {
		t.Fatalf("the complete merge was answered from the partial rendering: %s", r.Body.String())
	}
	res.docs = docsOf(nil, []formats.MemberDocument{three})
	if r := get(t, h, http.MethodGet, path); len(leavesOf(t, r)) != 2 {
		t.Errorf("the complete rendering was not kept: %s", r.Body.String())
	}
}

// counted sums a counter family of a registry.
func counted(t *testing.T, m *observability.Metrics, name string) float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, f := range fams {
		if f.GetName() == name {
			for _, x := range f.GetMetric() {
				sum += x.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// TestR_NUGET_017_ANotFoundMergeIs404AndAFailedOneIsNotHiddenAs404: the errors of the engine
// are the handler's: nothing found is the 404 of the format, and an upstream that could not
// be asked is 502 and not 404 (R-REPO-019 step 7).
func TestR_NUGET_017_ANotFoundMergeIs404AndAFailedOneIsNotHiddenAs404(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
	}{
		"nothing found":    {errs.E(errs.NotFound, errs.CodeNotFound, "no member holds it"), http.StatusNotFound},
		"a member is down": {errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down"), http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			h := virtualHost(t, &mergeResolver{err: c.err}, nil)
			for _, path := range []string{"v3/flatcontainer/acme.widgets/index.json", "v3/registration/acme.widgets/index.json"} {
				if r := get(t, h, http.MethodGet, path); r.Code != c.status {
					t.Errorf("%s: %d %s", path, r.Code, r.Body.String())
				}
			}
		})
	}
}

// TestR_NUGET_017_TheEndpointsExposureCutsTheMerge: on an endpoint with exposure rules that
// exposes nothing a merged document is a missing one, before the engine is asked.
func TestR_NUGET_017_TheEndpointsExposureCutsTheMerge(t *testing.T) {
	res := &mergeResolver{docs: docsOf([]formats.MemberDocument{localFlat(t, "local", 0, "1.0.0")}, nil)}
	h := virtualHost(t, res, nil)
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/flatcontainer/acme.widgets/index.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	rq.Endpoint.Rules = true
	if r := h.ServeRequest(rq, false); r.Code != http.StatusNotFound {
		t.Errorf("status %d: %s", r.Code, r.Body.String())
	}
	if res.merges != 0 {
		t.Errorf("the engine was asked %d times for a package the endpoint hides", res.merges)
	}
}

// TestR_NUGET_017_AFileIsResolvedThroughTheTiers: a package and a .nuspec are handed to the
// engine's tier walk at their canonical path, with the media type of the protocol, after the
// read permission on the version was decided.
func TestR_NUGET_017_AFileIsResolvedThroughTheTiers(t *testing.T) {
	res := &mergeResolver{}
	h := virtualHost(t, res, nil)
	nupkg := get(t, h, http.MethodGet, "v3/flatcontainer/Acme.Widgets/1.0.0.0/acme.widgets.1.0.0.0.nupkg")
	if nupkg.Code != http.StatusOK || nupkg.Header().Get("Content-Type") != "application/octet-stream" || nupkg.Header().Get("Trove-Resolved-From") != "member-a" {
		t.Fatalf("nupkg: %d %v", nupkg.Code, nupkg.Header())
	}
	nuspec := get(t, h, http.MethodGet, "v3/flatcontainer/acme.widgets/1.0.0/acme.widgets.nuspec")
	if nuspec.Code != http.StatusOK || !strings.HasPrefix(nuspec.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("nuspec: %d %v", nuspec.Code, nuspec.Header())
	}
	if want := []string{NupkgPath("acme.widgets", "1.0.0"), NuspecPath("acme.widgets", "1.0.0")}; !slices.Equal(res.served, want) {
		t.Errorf("paths = %v, want %v", res.served, want)
	}
	var sawVersion bool
	for _, c := range h.Authorizations() {
		sawVersion = sawVersion || (c.Target.Package == "acme.widgets" && c.Target.Version == "1.0.0")
	}
	if !sawVersion {
		t.Error("the read permission on the version was not decided")
	}
}

// TestR_NUGET_017_TheLocalDocumentOfAMemberCarriesTheRevisionItWasReadAt: the document is built
// from the rows of the member, and the revision is the package row's, 0 and not found for an ID
// the member does not know, the package's revision and not found for a package without a
// stored version (R-ARCH-015).
func TestR_NUGET_017_TheLocalDocumentOfAMemberCarriesTheRevisionItWasReadAt(t *testing.T) {
	fx := newDocFixture(t)
	fx.put("1.0.0", true, `{"description":"one"}`)
	fx.put("2.0.0", false, `{"description":"two"}`)
	a := New().Virtual()
	rq, err := fx.host.NewRequest(context.Background(), http.MethodGet, "v3/flatcontainer/acme.widgets/index.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := func(p string) ([]byte, formats.RevSeq, bool) {
		b, rev, found, err := a.LocalDocument(context.Background(), rq, fx.host.Ref(), formats.Path{Path: p}, "")
		if err != nil {
			t.Fatal(err)
		}
		return b, rev, found
	}
	flat, rev, found := doc("acme.widgets/index.json")
	if !found || string(flat) != `{"versions":["1.0.0","2.0.0"]}` || rev != fx.host.RevSeq("acme.widgets") || rev == 0 {
		t.Errorf("flat = %s, rev %d, found %v (host rev %d)", flat, rev, found, fx.host.RevSeq("acme.widgets"))
	}
	reg, rev2, found := doc("acme.widgets/.registration/index.json")
	if !found || rev2 != rev {
		t.Fatalf("registration found %v rev %d", found, rev2)
	}
	es, err := merge.Decode(reg)
	if err != nil || len(es) != 2 || es[0].Listed != true || es[1].Listed != false || !strings.Contains(string(es[1].Metadata), "two") {
		t.Errorf("entries = %+v %v", es, err)
	}
	if _, rev, found := doc("other/index.json"); found || rev != 0 {
		t.Errorf("an unknown ID: found %v rev %d", found, rev)
	}
}

// TestR_NUGET_017_TheAdapterNamesWhatItMerges: only the version list and the registration
// entries of an ID are merged; a file of a version goes to the first member that has it; the
// cached artifacts of a package are below its directory.
func TestR_NUGET_017_TheAdapterNamesWhatItMerges(t *testing.T) {
	a := New().Virtual()
	for path, want := range map[string]formats.Strategy{
		"acme.widgets/index.json":                     formats.Merge,
		"acme.widgets/.registration/index.json":       formats.Merge,
		"acme.widgets/1.0.0/acme.widgets.nuspec":      formats.FirstMatch,
		"acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg": formats.FirstMatch,
		".nuget/index.json":                           formats.FirstMatch,
		"Acme.Widgets/index.json":                     formats.FirstMatch, // not a canonical path
	} {
		if got := a.Strategy(formats.Path{Path: path}); got != want {
			t.Errorf("%s: strategy %v, want %v", path, got, want)
		}
	}
	if a.ArtifactPrefix("acme.widgets") != "acme.widgets/" || a.ArtifactPrefix("../x") != "" || a.ArtifactPrefix("Acme.Widgets") != "" {
		t.Errorf("prefix = %q", a.ArtifactPrefix("acme.widgets"))
	}
}

// TestR_NUGET_019_ThePublishResourcesAreListedWhenThereIsADeploymentTarget: the service index of
// a virtual repository lists the publish and the symbol publish resource exactly when
// the engine resolves a target; any other failure is the request's.
func TestR_NUGET_019_ThePublishResourcesAreListedWhenThereIsADeploymentTarget(t *testing.T) {
	types := func(r *formattest.Response) string { return r.Body.String() }
	res := &mergeResolver{}
	h := virtualHost(t, res, nil)
	with := get(t, h, http.MethodGet, "v3/index.json")
	if !strings.Contains(types(with), "PackagePublish/2.0.0") || !strings.Contains(types(with), "SymbolPackagePublish/4.9.0") ||
		!strings.Contains(types(with), feedOfAll+"package") {
		t.Fatalf("with a target: %s", with.Body.String())
	}
	res.target = errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "none")
	without := get(t, h, http.MethodGet, "v3/index.json")
	if strings.Contains(types(without), "PackagePublish") || without.Code != http.StatusOK {
		t.Fatalf("without a target: %d %s", without.Code, without.Body.String())
	}
	if with.Header().Get("ETag") == without.Header().Get("ETag") {
		t.Error("the validator of the service index does not depend on the publish resources")
	}
	res.target = errs.E(errs.Unavailable, errs.CodeUnavailable, "down")
	if r := get(t, h, http.MethodGet, "v3/index.json"); r.Code != http.StatusServiceUnavailable {
		t.Errorf("a failing resolution: %d", r.Code)
	}
}
