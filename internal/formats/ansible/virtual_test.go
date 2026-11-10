package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The tests of the virtual class run the handlers on the in-memory Host of the kit, with a resolver that behaves as the virtual engine
// does for one request (the plan, the merge of the members' documents, the first match) and a remote engine over canned documents.

// memberKey is a member of the virtual repository of a test.
type testMember struct {
	key      string
	id       int64
	class    formats.Class
	position int
	mayFill  bool
	cached   map[string]bool
}

func (m testMember) ref() formats.RepoRef {
	return formats.RepoRef{ID: m.id, Name: qname.Repo{Project: "it", Key: m.key}, Class: m.class, Format: ID}
}

func (m testMember) repo() repository.Repository {
	return repository.Repository{
		ID: strconv.FormatInt(m.id, 10), Name: qname.Repo{Project: "it", Key: m.key}, Class: m.class, Format: string(ID),
		Lifecycle: repository.Active,
	}
}

// fakeStore is the data port of the tests: the rows of the local members by repository id.
type fakeStore struct {
	Store
	versions  map[int64][]rows.VersionRow
	summaries map[int64]rows.CollectionSummary
	tasks     map[int64]map[string]rows.Task
	announced map[int64][]rows.Announced
}

func (s *fakeStore) Versions(_ context.Context, repoID int64, name string, limit, offset int) ([]rows.VersionRow, int, error) {
	var all []rows.VersionRow
	for _, v := range s.versions[repoID] {
		if v.Name == name {
			all = append(all, v)
		}
	}
	if offset >= len(all) {
		return nil, len(all), nil
	}
	return all[offset:min(offset+limit, len(all))], len(all), nil
}

func (s *fakeStore) Version(_ context.Context, repoID int64, name, ver string) (rows.VersionRow, bool, error) {
	for _, v := range s.versions[repoID] {
		if v.Name == name && v.Version == ver {
			return v, true, nil
		}
	}
	return rows.VersionRow{}, false, nil
}

func (s *fakeStore) Collection(_ context.Context, repoID int64, _ string) (rows.CollectionSummary, bool, error) {
	c, ok := s.summaries[repoID]
	return c, ok, nil
}

func (s *fakeStore) Task(_ context.Context, repoID int64, id string) (rows.Task, bool, error) {
	t, ok := s.tasks[repoID][id]
	return t, ok, nil
}

func (s *fakeStore) AnnouncedNames(_ context.Context, repoID int64, _ string) ([]rows.Announced, error) {
	return s.announced[repoID], nil
}

// fakeResolver plans the members of the test and resolves a document the way the engine does: merged over every member that holds it, or
// the first member's.
type fakeResolver struct {
	formats.Resolver
	f       *Format
	members []testMember
	docs    map[string]map[string][]byte // path -> member key -> document
	partial bool
	target  *repository.Repository
	// artifact is the path the last Artifact call asked for.
	artifact string
	metadata []string
	notAdmit bool
	// hidden are the versions the caller's grants hide by path, "<member key>/<version>".
	hidden map[string]bool
}

// pathScope is the read scope of a caller whose grants decide versions by path.
type pathScope struct{ hidden map[string]bool }

func (s pathScope) Shape(qname.Repo) formats.ReadShape { return formats.ReadRows }
func (s pathScope) MayRead(repo qname.Repo, _, version string) bool {
	return !s.hidden[repo.Key+"/"+version]
}
func (s pathScope) VersionsByPath(qname.Repo) bool { return true }

type allScope struct{}

func (allScope) Shape(qname.Repo) formats.ReadShape      { return formats.ReadAll }
func (allScope) MayRead(qname.Repo, string, string) bool { return true }
func (allScope) VersionsByPath(qname.Repo) bool          { return false }

func (r *fakeResolver) ReadScope(context.Context, *formats.Request, []qname.Repo) (formats.ReadScope, error) {
	if r.hidden == nil {
		return allScope{}, nil
	}
	return pathScope{hidden: r.hidden}, nil
}

func (r *fakeResolver) Plan(context.Context, *formats.Request, formats.Path, formats.PathInfo) (*formats.VirtualPlan, error) {
	plan := &formats.VirtualPlan{Admitted: !r.notAdmit}
	if r.notAdmit {
		return plan, nil
	}
	for _, m := range r.members {
		tier := formats.TierLocal
		if m.class == formats.Remote {
			tier = formats.TierRemote
		}
		plan.Members = append(plan.Members, formats.PlannedMember{Member: m.ref(), Tier: tier, Position: m.position, MayFill: m.mayFill})
	}
	return plan, nil
}

func (r *fakeResolver) Metadata(ctx context.Context, rq *formats.Request, p formats.Path, info formats.PathInfo, variant string) (*formats.Merged, error) {
	r.metadata = append(r.metadata, p.Path+"#"+variant)
	adapter := virtualAdapter{f: r.f}
	var parts []formats.MemberDocument
	for _, m := range r.members {
		doc, ok := r.docs[p.Path][m.key]
		if !ok {
			continue
		}
		tier := formats.TierLocal
		if m.class == formats.Remote {
			tier = formats.TierRemote
		}
		parts = append(parts, formats.MemberDocument{
			Member: m.ref(), Tier: tier, Position: m.position, Doc: doc, CachedVersions: m.cached, Restricted: m.class == formats.Remote && !m.mayFill,
		})
	}
	if len(parts) == 0 {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "No member holds the document.")
	}
	if adapter.Strategy(p) == formats.FirstMatch {
		return &formats.Merged{Bytes: parts[0].Doc}, nil
	}
	b, err := adapter.Merge(ctx, rq, p, parts)
	if err != nil {
		return nil, err
	}
	return &formats.Merged{Bytes: b, Partial: r.partial}, nil
}

func (r *fakeResolver) Artifact(_ context.Context, _ *formats.Request, p formats.Path, _ formats.PathInfo, w http.ResponseWriter) error {
	r.artifact = p.Path
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write([]byte("archive"))
	return nil
}

func (r *fakeResolver) DeployTarget(context.Context, *formats.Request) (*repository.Repository, error) {
	if r.target == nil {
		return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "The repository has no deploy target.")
	}
	return r.target, nil
}

// fakeRemote is the remote engine over documents by member key, path and variant.
type fakeRemote struct {
	formats.RemoteFetcher
	mu   sync.Mutex
	docs map[string][]byte
	asks []string
}

func rkey(member, path, variant string) string { return member + "|" + path + "|" + variant }

func (r *fakeRemote) Metadata(_ context.Context, rq *formats.Request, p formats.Path, variant string) (*formats.RemoteDocument, error) {
	k := rkey(rq.Repo.Name.Key, p.Path, variant)
	r.mu.Lock()
	r.asks = append(r.asks, k)
	b, ok := r.docs[k]
	r.mu.Unlock()
	if !ok {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no such document.")
	}
	return &formats.RemoteDocument{Bytes: func(context.Context) ([]byte, error) { return b, nil }}, nil
}

// world is a virtual repository over the members, served by the format.
type world struct {
	t     *testing.T
	f     *Format
	h     *formattest.Host
	res   *fakeResolver
	rem   *fakeRemote
	store *fakeStore
}

func newWorld(t *testing.T, members ...testMember) *world {
	t.Helper()
	f := New()
	w := &world{t: t, f: f, store: &fakeStore{
		versions: map[int64][]rows.VersionRow{}, summaries: map[int64]rows.CollectionSummary{},
		tasks: map[int64]map[string]rows.Task{}, announced: map[int64][]rows.Announced{},
	}, rem: &fakeRemote{docs: map[string][]byte{}}}
	w.res = &fakeResolver{f: f, members: members, docs: map[string]map[string][]byte{}}
	w.h = formattest.NewHost(f, formattest.HostOptions{Class: formats.Virtual, RepoKey: "virt", Data: w.store, Resolver: w.res, Remote: w.rem})
	var repos []repository.Repository
	for _, m := range members {
		repos = append(repos, m.repo())
	}
	*w.h.Repo() = repository.Assemble(*w.h.Repo(), repos, nil, 60)
	return w
}

// setTarget makes the member the deployment target.
func (w *world) setTarget(m testMember) {
	r := m.repo()
	w.res.target = &r
	*w.h.Repo() = repository.Assemble(*w.h.Repo(), w.h.Repo().Members(), &r, 60)
}

func (w *world) doc(path, member string, doc []byte) {
	if w.res.docs[path] == nil {
		w.res.docs[path] = map[string][]byte{}
	}
	w.res.docs[path][member] = doc
}

func (w *world) get(path string, mods ...func(*http.Request)) *formattest.Response {
	w.t.Helper()
	r, err := w.h.Serve(context.Background(), http.MethodGet, path, nil, false, mods...)
	if err != nil {
		w.t.Fatalf("GET %s: %v", path, err)
	}
	return r
}

func query(q string) func(*http.Request) { return func(r *http.Request) { r.URL.RawQuery = q } }

func local(key string, id int64, position int) testMember {
	return testMember{key: key, id: id, class: formats.Local, position: position}
}

func remote(key string, id int64, position int) testMember {
	return testMember{key: key, id: id, class: formats.Remote, position: position, mayFill: true}
}

// listPage is a page of an upstream's version list.
func listPage(count int, versions ...string) []byte {
	var data []map[string]any
	for _, v := range versions {
		data = append(data, map[string]any{"version": v, "created_at": "2025-01-01T00:00:00.000000Z", "updated_at": "2025-01-02T00:00:00.000000Z", "requires_ansible": nil})
	}
	doc, _ := json.Marshal(map[string]any{"meta": map[string]any{"count": count}, "links": map[string]any{"next": nil}, "data": data})
	return doc
}

func localList(t *testing.T, entries ...listEntry) []byte {
	t.Helper()
	doc, err := json.Marshal(listDoc{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func sha(b byte) [32]byte { return [32]byte{b} }

func hexSHA(sum [32]byte) string { return hex.EncodeToString(sum[:]) }

type versionsAnswer struct {
	Meta  struct{ Count int } `json:"meta"`
	Links struct{ Next *string }
	Data  []struct{ Version string }
}

func decodeVersions(t *testing.T, r *formattest.Response) versionsAnswer {
	t.Helper()
	var v versionsAnswer
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, r.Body.String())
	}
	return v
}

func versionsOf(v versionsAnswer) []string {
	var out []string
	for _, d := range v.Data {
		out = append(out, d.Version)
	}
	return out
}

const versionsPath = "v3/collections/acme/tools/versions/"

func TestR_ANS_022_AdapterStrategyAndArtifactPrefix(t *testing.T) {
	a := virtualAdapter{}
	for path, want := range map[string]formats.Strategy{
		"collections/acme/tools/versions.json":               formats.Merge,
		"collections/acme/tools/collection.json":             formats.Merge,
		"collections/acme/tools/1.0.0.json":                  formats.FirstMatch,
		"collections/acme/tools/acme-tools-1.0.0.tar.gz":     formats.FirstMatch,
		"roles/lookup.json":                                  formats.FirstMatch,
		"api-root.json":                                      formats.FirstMatch,
		"collections/acme/versions.json":                     formats.FirstMatch,
		"collections/_bad/tools/versions.json":               formats.FirstMatch,
		"collections/acme/tools/not-a-version.json":          formats.FirstMatch,
		"collections/acme/tools/versions.json/extra":         formats.FirstMatch,
		"collections/Acme/tools/versions.json":               formats.FirstMatch,
		"collections/acme/tools/1.0.0-rc.1+build.5.json":     formats.FirstMatch,
		"collections/acme/tools/acme-tools-1.0.0.tar.gz.sig": formats.FirstMatch,
	} {
		if got := a.Strategy(formats.Path{Path: path}); got != want {
			t.Errorf("Strategy(%s) = %v, want %v", path, got, want)
		}
	}
	if got := a.ArtifactPrefix("acme.tools"); got != "collections/acme/tools/" {
		t.Errorf("ArtifactPrefix = %q", got)
	}
	if got := a.ArtifactPrefix("role/16"); got != "" {
		t.Errorf("ArtifactPrefix of a role = %q", got)
	}
}

func TestR_ANS_022_TheVersionListIsTheUnionNewestFirstCutAfterTheMergeAndTheDigestConflictIsCounted(t *testing.T) {
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	m := observability.NewMetrics("web")
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "it/virt"}})
	w.f.Bind(w.h, &config.Config{}, m)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0", SHA256: hexSHA(sha(1))}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(2, "1.0.0", "0.9.0"))
	w.store.announced[2] = []rows.Announced{{Name: "acme.tools", Version: "1.0.0", SHA256: sha(2)}}

	r := w.get(versionsPath)
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if v := decodeVersions(t, r); v.Meta.Count != 2 || strings.Join(versionsOf(v), ",") != "1.0.0,0.9.0" {
		t.Errorf("list %+v", v)
	}
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", map[string]string{"repo": "it/virt"}); got != 1 {
		t.Errorf("conflicts = %v, want 1", got)
	}

	// The client's page is cut after the merge, and the links name the virtual repository.
	r = w.get(versionsPath, query("limit=1"))
	v := decodeVersions(t, r)
	if strings.Join(versionsOf(v), ",") != "1.0.0" || v.Meta.Count != 2 || v.Links.Next == nil {
		t.Fatalf("page 1 %+v", v)
	}
	if !strings.Contains(*v.Links.Next, "/api/ansible/it/virt/v3/collections/acme/tools/versions/?limit=1&offset=1") {
		t.Errorf("next = %s", *v.Links.Next)
	}
	r = w.get(versionsPath, query("limit=1&offset=1"))
	if v := decodeVersions(t, r); strings.Join(versionsOf(v), ",") != "0.9.0" || v.Links.Next != nil {
		t.Errorf("page 2 %+v", v)
	}
	if r := w.get(versionsPath, query("offset=50")); len(decodeVersions(t, r).Data) != 0 {
		t.Error("an offset beyond the list is not empty")
	}
}

func TestR_ANS_022_ADigestConflictIsFoundForTheVersionsAMemberSharesWithAnUpstreamAndOnlyThose(t *testing.T) {
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	m := observability.NewMetrics("web")
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "it/virt"}})
	w.f.Bind(w.h, &config.Config{}, m)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0", SHA256: hexSHA(sha(1))}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(2, "1.0.0", "0.9.0"))
	// No digest was ever recorded for the remote: the merge reads the document of the shared version, and of no other.
	w.rem.docs[rkey("rem", "collections/acme/tools/1.0.0.json", documentVariant)] = upstreamVersion("acme", "tools", "1.0.0", "up.example", sha(2))
	w.rem.docs[rkey("rem", "collections/acme/tools/0.9.0.json", documentVariant)] = upstreamVersion("acme", "tools", "0.9.0", "up.example", sha(9))
	if r := w.get(versionsPath); r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", nil); got != 1 {
		t.Errorf("conflicts = %v, want 1", got)
	}
	for _, ask := range w.rem.asks {
		if strings.Contains(ask, "0.9.0.json") {
			t.Errorf("the document of a version no local member holds was read: %s", ask)
		}
	}
	// The same digest on both sides is no conflict.
	w.res.docs["collections/acme/tools/versions.json"]["loc"] = localList(t, listEntry{Version: "1.0.0", SHA256: hexSHA(sha(2))})
	w.get(versionsPath)
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", nil); got != 1 {
		t.Errorf("conflicts = %v after a merge without one", got)
	}
}

func TestR_ANS_022_ALocalVersionNeverLosesToARemoteOneAndTheSameDigestIsNoConflict(t *testing.T) {
	loc, rem := local("loc", 1, 1), remote("rem", 2, 0) // the remote member comes first in the order, the tier decides
	w := newWorld(t, loc, rem)
	m := observability.NewMetrics("web")
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "it/virt"}})
	w.f.Bind(w.h, &config.Config{}, m)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0", SHA256: hexSHA(sha(7))}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(1, "1.0.0"))
	w.store.announced[2] = []rows.Announced{{Name: "acme.tools", Version: "1.0.0", SHA256: sha(7)}}
	w.get(versionsPath)
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", nil); got != 0 {
		t.Errorf("conflicts = %v, want 0", got)
	}
	var merged listDoc
	b, err := virtualAdapter{f: w.f}.Merge(context.Background(), w.mustRequest(versionsPath), formats.Path{Path: "collections/acme/tools/versions.json"}, []formats.MemberDocument{
		{Member: rem.ref(), Tier: formats.TierRemote, Position: 0, Doc: listPage(1, "1.0.0")},
		{Member: loc.ref(), Tier: formats.TierLocal, Position: 1, Doc: localList(t, listEntry{Version: "1.0.0"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &merged); err != nil || len(merged.Entries) != 1 || merged.Entries[0].Member != "loc" {
		t.Errorf("merged %s %v", b, err)
	}
}

func (w *world) mustRequest(path string) *formats.Request {
	w.t.Helper()
	rq, err := w.h.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return rq
}

func TestR_ANS_022_ARemoteMemberWithMoreThanAPageIsReadWholeThroughTheMemberAndTheReadingIsBounded(t *testing.T) {
	rem := remote("rem", 2, 0)
	w := newWorld(t, rem)
	var first, second, third []string
	for i := range 100 {
		first = append(first, "1.0."+strconv.Itoa(i))
		second = append(second, "1.1."+strconv.Itoa(i))
	}
	third = []string{"1.2.0", "1.2.1"}
	w.doc("collections/acme/tools/versions.json", "rem", listPage(202, first...))
	w.rem.docs[rkey("rem", "collections/acme/tools/versions.json", "offset=100")] = listPage(202, second...)
	w.rem.docs[rkey("rem", "collections/acme/tools/versions.json", "offset=200")] = listPage(202, third...)
	r := w.get(versionsPath, query("limit=100&offset=200"))
	v := decodeVersions(t, r)
	if v.Meta.Count != 202 || len(v.Data) != 2 || v.Data[0].Version != "1.0.1" || v.Data[1].Version != "1.0.0" {
		t.Fatalf("count %d, data %v", v.Meta.Count, versionsOf(v))
	}
	if got := strings.Join(w.rem.asks, ","); !strings.Contains(got, "offset=100") || !strings.Contains(got, "offset=200") {
		t.Errorf("pages asked through the member: %s", got)
	}

	// A page missing upstream ends the merge with an error, never with a short list.
	delete(w.rem.docs, rkey("rem", "collections/acme/tools/versions.json", "offset=200"))
	if r := w.get(versionsPath); r.Code != http.StatusBadGateway {
		t.Errorf("a missing page: %d, want 502", r.Code)
	}

	// The budget of one merge is shared by its members.
	b := newPageBudget()
	for range maxMergePages {
		if !b.take() {
			t.Fatal("the budget ended early")
		}
	}
	if b.take() {
		t.Error("the budget did not end")
	}
}

func TestR_AUTHZ_008_ARemoteMemberTheCallerMayNotFillSuppliesItsCachedVersionsOnly(t *testing.T) {
	loc := local("loc", 1, 0)
	rem := remote("rem", 2, 1)
	rem.mayFill = false
	rem.cached = map[string]bool{"0.9.0": true}
	w := newWorld(t, loc, rem)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0"}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(2, "0.9.0", "0.8.0"))
	if v := decodeVersions(t, w.get(versionsPath)); strings.Join(versionsOf(v), ",") != "1.0.0,0.9.0" {
		t.Errorf("list %v", versionsOf(v))
	}
}

func TestR_ANS_023_AMemberTheCallerMayNotReadTakesNoPartAndAMergeNobodyHoldsIsNotFound(t *testing.T) {
	loc := local("loc", 1, 0)
	w := newWorld(t, loc)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0"}))
	if v := decodeVersions(t, w.get(versionsPath)); len(v.Data) != 1 {
		t.Errorf("list %v", versionsOf(v))
	}
	if r := w.get("v3/collections/nobody/nothing/versions/"); r.Code != http.StatusNotFound {
		t.Errorf("status %d", r.Code)
	}
	w.res.notAdmit = true
	if r := w.get(versionsPath); r.Code >= http.StatusBadRequest {
		t.Logf("a repository whose patterns refuse the request still merges what the engine hands over: %d", r.Code)
	}
}

func TestR_ANS_023_AMergeThatCouldNotAskAMemberIsRefusedAsPartialAndNeverServed(t *testing.T) {
	loc := local("loc", 1, 0)
	w := newWorld(t, loc, remote("rem", 2, 1))
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0"}))
	w.res.partial = true
	r := w.get(versionsPath)
	if r.Code != http.StatusServiceUnavailable || r.Header().Get("Cache-Status") != "detail=partial" {
		t.Fatalf("status %d, Cache-Status %q: %s", r.Code, r.Header().Get("Cache-Status"), r.Body.String())
	}
	var body struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 || body.Errors[0].Code != "upstream_unavailable" {
		t.Errorf("body %s", r.Body.String())
	}
	w.res.partial = false
	if r := w.get(versionsPath); r.Code != http.StatusOK {
		t.Errorf("after the member is back: %d", r.Code)
	}
}

func TestR_ANS_022_TheCollectionHasTheHighestVersionTheEarliestCreationAndTheLatestUpdate(t *testing.T) {
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0"}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(1, "2.0.0"))
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	w.doc("collections/acme/tools/collection.json", "loc", []byte(`{"created_at":"`+t1.Format(time.RFC3339Nano)+`","updated_at":"`+t1.Format(time.RFC3339Nano)+`"}`))
	w.doc("collections/acme/tools/collection.json", "rem", []byte(`{"created_at":"`+t0.Format(time.RFC3339Nano)+`","updated_at":"2025-02-01T00:00:00Z"}`))
	r := w.get("v3/collections/acme/tools/")
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var c struct {
		Highest   struct{ Version, Href string } `json:"highest_version"`
		Created   string                         `json:"created_at"`
		Updated   string                         `json:"updated_at"`
		VersionsU string                         `json:"versions_url"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Highest.Version != "2.0.0" || !strings.HasPrefix(c.Created, "2024-01-01") || !strings.HasPrefix(c.Updated, "2025-06-01") {
		t.Errorf("collection %+v", c)
	}
	if !strings.Contains(c.VersionsU, "/api/ansible/it/virt/v3/collections/acme/tools/versions/") || !strings.Contains(c.Highest.Href, "/api/ansible/it/virt/") {
		t.Errorf("addresses %+v", c)
	}
	// A weak validator answers If-None-Match with 304.
	etag := r.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag %q", etag)
	}
	if r2 := w.get("v3/collections/acme/tools/", func(r *http.Request) { r.Header.Set("If-None-Match", etag) }); r2.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", r2.Code)
	}
}

func upstreamVersion(ns, name, ver, host string, sum [32]byte) []byte {
	doc, _ := json.Marshal(map[string]any{
		"version": ver, "href": "https://" + host + "/api/v3/x/", "created_at": "2025-01-01T00:00:00.000000Z", "updated_at": "2025-01-01T00:00:00.000000Z",
		"requires_ansible": ">=2.14", "marks": []string{},
		"artifact":     map[string]any{"filename": ns + "-" + name + "-" + ver + ".tar.gz", "sha256": hexSHA(sum), "size": 12},
		"collection":   map[string]any{"id": "c1", "name": name, "href": "https://" + host + "/c/"},
		"download_url": "https://" + host + "/download/" + ns + "-" + name + "-" + ver + ".tar.gz",
		"name":         name, "namespace": map[string]any{"name": ns}, "signatures": []any{},
		"metadata": map[string]any{"authors": []string{"A"}, "dependencies": map[string]string{}, "contents": []any{map[string]any{"name": "x"}}},
	})
	return doc
}

func TestR_ANS_022_TheVersionDocumentIsTheFirstMembersAndNamesOnlyTheVirtualRepository(t *testing.T) {
	w := newWorld(t, local("loc", 1, 0), remote("rem", 2, 1))
	w.doc("collections/acme/tools/1.0.0.json", "rem", upstreamVersion("acme", "tools", "1.0.0", "upstream.example", sha(3)))
	r := w.get(versionsPath + "1.0.0/")
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	body := r.Body.String()
	if strings.Contains(body, "upstream.example") || strings.Contains(body, `"contents"`) {
		t.Errorf("the document names the upstream or carries the plugin listing: %s", body)
	}
	var v struct {
		DownloadURL string `json:"download_url"`
		Artifact    struct{ Sha256 string }
	}
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.DownloadURL, "/api/ansible/it/virt/v3/artifacts/collections/acme-tools-1.0.0.tar.gz") || v.Artifact.Sha256 != hexSHA(sha(3)) {
		t.Errorf("%+v", v)
	}
	// A document that names another version than the one asked for is refused.
	w.doc("collections/acme/tools/2.0.0.json", "rem", upstreamVersion("acme", "tools", "9.9.9", "upstream.example", sha(3)))
	if r := w.get(versionsPath + "2.0.0/"); r.Code < http.StatusBadRequest {
		t.Errorf("a document of another version was served: %d", r.Code)
	}
	if r := w.get(versionsPath + "3.0.0/"); r.Code != http.StatusNotFound {
		t.Errorf("a version no member holds: %d", r.Code)
	}
}

func TestR_ANS_022_TheVersionDocumentOfALocalMemberIsRenderedFromItsRow(t *testing.T) {
	loc := local("loc", 1, 0)
	w := newWorld(t, loc)
	row := rows.VersionRow{
		RepoID: 1, Name: "acme.tools", Version: "1.0.0", SHA256: sha(4), Size: 99, RequiresAnsible: ">=2.15", PublishedAt: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
		CollectionInfo: json.RawMessage(`{"authors":["A"]}`),
	}
	w.store.versions[1] = []rows.VersionRow{row}
	w.store.summaries[1] = rows.CollectionSummary{Namespace: "acme", Name: "tools", HighestVersion: "1.0.0", CreatedAt: row.PublishedAt, UpdatedAt: row.PublishedAt}
	if err := w.h.Seed(context.Background(), func(pw formats.PackageWriter) error {
		_, _, err := pw.EnsureLocked(context.Background(), loc.ref(), "acme.tools", "acme.tools")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rq := w.mustRequest("v3/collections/acme/tools/")
	a := virtualAdapter{f: w.f}
	doc, _, found, err := a.LocalDocument(context.Background(), rq, loc.ref(), formats.Path{Path: "collections/acme/tools/1.0.0.json"}, "")
	if err != nil || !found || !strings.Contains(string(doc), hexSHA(sha(4))) || !strings.Contains(string(doc), ">=2.15") {
		t.Fatalf("version document %s %v %v", doc, found, err)
	}
	list, _, found, err := a.LocalDocument(context.Background(), rq, loc.ref(), formats.Path{Path: "collections/acme/tools/versions.json"}, variantFirstPage)
	l, derr := decodeList(list)
	if err != nil || derr != nil || !found || len(l.Entries) != 1 || l.Entries[0].SHA256 != hexSHA(sha(4)) || l.Entries[0].RequiresAnsible != ">=2.15" {
		t.Fatalf("version list %s %v %v", list, found, err)
	}
	if _, _, found, _ := a.LocalDocument(context.Background(), rq, loc.ref(), formats.Path{Path: "collections/acme/tools/9.9.9.json"}, ""); found {
		t.Error("a version the member does not hold was found")
	}
	if _, _, _, err := a.LocalDocument(context.Background(), rq, loc.ref(), formats.Path{Path: "api-root.json"}, ""); err == nil {
		t.Error("a path that is no document of a collection was rendered")
	}
}

func TestR_ANS_023_TheArchiveIsResolvedOnItsCanonicalPathAndAForeignFileNameIsNotFound(t *testing.T) {
	w := newWorld(t, local("loc", 1, 0))
	r := w.get("v3/artifacts/collections/acme-tools-1.0.0.tar.gz")
	if r.Code != http.StatusOK || w.res.artifact != "collections/acme/tools/acme-tools-1.0.0.tar.gz" || r.Body.String() != "archive" {
		t.Errorf("%d %q %q", r.Code, w.res.artifact, r.Body.String())
	}
	for _, f := range []string{"nonsense.tar.gz", "acme-tools-1.0.tar.gz"} {
		if r := w.get("v3/artifacts/collections/" + f); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", f, r.Code)
		}
	}
}

func TestR_ANS_022_TheDiscoveryDocumentListsV1OnlyWhenARemoteMembersUpstreamDoes(t *testing.T) {
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	listed := func() bool {
		t.Helper()
		r := w.get("")
		var d struct {
			Available map[string]string `json:"available_versions"`
		}
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &d) != nil {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		_, v1 := d.Available["v1"]
		if _, v3 := d.Available["v3"]; !v3 {
			t.Error("v3 is missing")
		}
		return v1
	}
	if listed() {
		t.Error("v1 listed although no upstream document is stored")
	}
	w.rem.docs[rkey("rem", RootDocument, documentVariant)] = []byte(`{"available_versions":{"v3":"v3/"}}`)
	if listed() {
		t.Error("v1 listed for an upstream without v1")
	}
	w.rem.docs[rkey("rem", RootDocument, documentVariant)] = []byte(`{"available_versions":{"v3":"v3/","v1":"v1/"}}`)
	if !listed() {
		t.Error("v1 not listed for an upstream with v1")
	}
	w.res.notAdmit = true
	if listed() {
		t.Error("v1 listed for a request the repository's patterns refuse")
	}
}

// withHandler registers a handler of another class for the length of a test, in place of the one the class file registered (if any).
func withHandler(t *testing.T, class formats.Class, route string, h formats.HandlerFunc) {
	t.Helper()
	handlerRegistry.mu.Lock()
	if handlerRegistry.by[class] == nil {
		handlerRegistry.by[class] = map[string]func(*Format) formats.HandlerFunc{}
	}
	old, had := handlerRegistry.by[class][route]
	handlerRegistry.by[class][route] = func(*Format) formats.HandlerFunc { return h }
	handlerRegistry.mu.Unlock()
	t.Cleanup(func() {
		handlerRegistry.mu.Lock()
		defer handlerRegistry.mu.Unlock()
		if had {
			handlerRegistry.by[class][route] = old
		} else {
			delete(handlerRegistry.by[class], route)
		}
	})
}

func TestR_ANS_024_APublishGoesToTheDeploymentTargetAndTheTaskNamesTheVirtualRepository(t *testing.T) {
	loc := local("loc", 1, 0)
	w := newWorld(t, loc, remote("rem", 2, 1))
	var seen struct {
		repo, addressed string
		body            string
	}
	withHandler(t, formats.Local, routePublish, func(_ context.Context, rq *formats.Request, rw http.ResponseWriter) error {
		b := make([]byte, 5)
		n, _ := rq.Body.Read(b)
		seen.repo, seen.addressed, seen.body = rq.Repo.Name.Key, rq.Addressed().Key, string(b[:n])
		link := rq.Host.Links(rq).Registry(rq.Addressed()) + "v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/"
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusAccepted)
		_, _ = rw.Write([]byte(`{"task": "` + link + `"}`))
		return nil
	})
	post := func() *formattest.Response {
		rq, err := w.h.NewRequest(context.Background(), http.MethodPost, "v3/artifacts/collections/", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		return w.h.ServeRequest(rq, false)
	}
	if r := post(); r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("without a deploy target: %d %s", r.Code, r.Body.String())
	} else if !strings.Contains(r.Body.String(), "no_deploy_target") {
		t.Errorf("code: %s", r.Body.String())
	}
	w.setTarget(loc)
	r := post()
	if r.Code != http.StatusAccepted {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if seen.repo != "loc" || seen.addressed != "loc" || seen.body != "hello" {
		t.Errorf("the target's handler saw %+v", seen)
	}
	var ans struct{ Task string }
	if err := json.Unmarshal(r.Body.Bytes(), &ans); err != nil {
		t.Fatalf("%v: %s", err, r.Body.String())
	}
	if !strings.Contains(ans.Task, "/api/ansible/it/virt/v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/") || strings.Contains(ans.Task, "/it/loc/") {
		t.Errorf("task = %s", ans.Task)
	}
	if got := r.Header().Get("Content-Length"); got != strconv.Itoa(r.Body.Len()) {
		t.Errorf("Content-Length %s for %d bytes", got, r.Body.Len())
	}

	// A refusal of the target passes through unchanged.
	withHandler(t, formats.Local, routePublish, func(context.Context, *formats.Request, http.ResponseWriter) error {
		return errs.E(errs.Conflict, errs.CodeAnsibleVersionExists, "The version exists.")
	})
	if r := post(); r.Code != http.StatusConflict {
		t.Errorf("refusal: %d", r.Code)
	}
}

func TestR_ANS_024_ADeploymentTargetThatIsNotLocalIsRefused(t *testing.T) {
	rem := remote("rem", 2, 0)
	w := newWorld(t, local("loc", 1, 1), rem)
	w.setTarget(rem)
	withHandler(t, formats.Local, routePublish, func(context.Context, *formats.Request, http.ResponseWriter) error { return nil })
	rq, _ := w.h.NewRequest(context.Background(), http.MethodPost, "v3/artifacts/collections/", strings.NewReader("x"))
	if r := w.h.ServeRequest(rq, false); r.Code != http.StatusMethodNotAllowed {
		t.Errorf("%d", r.Code)
	}
}

func TestR_ANS_024_TheImportTaskIsLookedUpInTheDeploymentTargetAtTheTimeOfTheRead(t *testing.T) {
	loc, other := local("loc", 1, 0), local("other", 3, 1)
	w := newWorld(t, loc, other)
	const id = "6f1c0000-0000-4000-8000-000000000002"
	finished := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	w.store.tasks[1] = map[string]rows.Task{id: {ID: id, State: rows.TaskCompleted, Namespace: "acme", Name: "tools", Version: "1.0.0", CreatedAt: finished, StartedAt: finished, UpdatedAt: finished, FinishedAt: finished}}
	path := "v3/imports/collections/" + id + "/"
	if r := w.get(path); r.Code != http.StatusNotFound {
		t.Errorf("without a target: %d", r.Code)
	}
	w.setTarget(loc)
	r := w.get(path)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"state":"completed"`) {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	// The target changed since the publish: the task is not found.
	w.setTarget(other)
	if r := w.get(path); r.Code != http.StatusNotFound {
		t.Errorf("after the target changed: %d", r.Code)
	}
	for _, bad := range []string{"v3/imports/collections/not-a-task/", "v3/imports/collections/6f1c0000-0000-4000-8000-00000000000g/"} {
		if r := w.get(bad); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", bad, r.Code)
		}
	}
}

func TestR_ANS_022_ARoleIdNamesItsMember(t *testing.T) {
	for in, want := range map[string]struct {
		member, id string
		ok         bool
	}{
		"rem-16":            {"rem", "16", true},
		"my-remote-9":       {"my-remote", "9", true},
		"a-1-2":             {"a-1", "2", true},
		"16":                {"", "", false},
		"-16":               {"", "", false},
		"rem-":              {"", "", false},
		"rem-x":             {"", "", false},
		"rem-1234567890123": {"", "", false},
	} {
		m, id, ok := decodeRoleID(in)
		if m != want.member || id != want.id || ok != want.ok {
			t.Errorf("decodeRoleID(%q) = %q %q %v", in, m, id, ok)
		}
	}
	if got := encodeRoleID("my-remote", "9"); got != "my-remote-9" {
		t.Error(got)
	}
	doc := `{"url":"/api/ansible/it/virt/v1/roles/16/","related":{"versions":"https://h/api/ansible/it/virt/v1/roles/16/versions/"},"download_url":"https://h/x/v1/roles/archives/16/v1.0.0.tar.gz","other":"v1/roles/xx/"}`
	got := string(rewriteRoleRoutes([]byte(doc), "rem"))
	for _, want := range []string{"v1/roles/rem-16/", "v1/roles/rem-16/versions/", "v1/roles/archives/rem-16/v1.0.0.tar.gz", "v1/roles/xx/"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing in %s", want, got)
		}
	}
}

func roleWorld(t *testing.T) (*world, *[]string) {
	t.Helper()
	w := newWorld(t, local("loc", 1, 0), remote("rem-a", 2, 1), remote("rem-b", 3, 2))
	var asked []string
	withHandler(t, formats.Remote, routeRolesFind, func(_ context.Context, rq *formats.Request, rw http.ResponseWriter) error {
		asked = append(asked, "find:"+rq.Repo.Name.Key)
		rw.Header().Set("Content-Type", "application/json")
		if rq.Repo.Name.Key == "rem-a" {
			_, _ = rw.Write([]byte(`{"count":0,"results":[]}`))
			return nil
		}
		_, _ = rw.Write([]byte(`{"count":1,"results":[{"id":16,"url":"/api/v1/roles/16/","related":{"versions":"/api/v1/roles/16/versions/"},"github_user":"geerlingguy"}]}`))
		return nil
	})
	withHandler(t, formats.Remote, routeRoleVersions, func(_ context.Context, rq *formats.Request, rw http.ResponseWriter) error {
		asked = append(asked, "versions:"+rq.Repo.Name.Key+":"+rq.Params.Get("id"))
		_, _ = rw.Write([]byte(`{"count":1,"next_link":"/api/v1/roles/16/versions/?page=2","results":[{"name":"1.0.0","download_url":"/api/v1/roles/archives/16/1.0.0.tar.gz"}]}`))
		return nil
	})
	withHandler(t, formats.Remote, routeRoleArchive, func(_ context.Context, rq *formats.Request, rw http.ResponseWriter) error {
		asked = append(asked, "archive:"+rq.Repo.Name.Key+":"+rq.Params.Get("id")+":"+rq.Params.Get("file"))
		_, _ = rw.Write([]byte("role bytes"))
		return nil
	})
	return w, &asked
}

func TestR_ANS_022_ARoleLookupIsAnsweredByTheFirstRemoteMemberWhoseAnswerIsNotEmptyWithTheMembersIdInIt(t *testing.T) {
	w, asked := roleWorld(t)
	r := w.get("v1/roles/", query("owner__username=geerlingguy&name=pip"))
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var ans struct {
		Results []struct {
			ID      string
			URL     string
			Related struct{ Versions string }
			User    string `json:"github_user"`
		}
	}
	if err := json.Unmarshal(r.Body.Bytes(), &ans); err != nil || len(ans.Results) != 1 {
		t.Fatalf("%v: %s", err, r.Body.String())
	}
	got := ans.Results[0]
	if got.ID != "rem-b-16" || got.URL != "/api/v1/roles/rem-b-16/" || got.Related.Versions != "/api/v1/roles/rem-b-16/versions/" || got.User != "geerlingguy" {
		t.Errorf("%+v", got)
	}
	if strings.Join(*asked, ",") != "find:rem-a,find:rem-b" {
		t.Errorf("members asked: %v (a local member takes no part)", *asked)
	}
}

func TestR_ANS_022_ARoleLookupNobodyAnswersIsEmptyAndOneAMemberCouldNotAnswerIsAnError(t *testing.T) {
	w := newWorld(t, remote("rem-a", 2, 0))
	withHandler(t, formats.Remote, routeRolesFind, func(_ context.Context, _ *formats.Request, rw http.ResponseWriter) error {
		_, _ = rw.Write([]byte(`{"count":0,"results":[]}`))
		return nil
	})
	if r := w.get("v1/roles/"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"count":0`) {
		t.Errorf("%d %s", r.Code, r.Body.String())
	}
	withHandler(t, formats.Remote, routeRolesFind, func(context.Context, *formats.Request, http.ResponseWriter) error {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
	})
	if r := w.get("v1/roles/"); r.Code != http.StatusBadGateway {
		t.Errorf("%d %s", r.Code, r.Body.String())
	}
	w = newWorld(t, local("loc", 1, 0))
	if r := w.get("v1/roles/"); r.Code != http.StatusNotFound {
		t.Errorf("without a remote member: %d", r.Code)
	}
}

func TestR_ANS_022_TheRoleVersionsAndTheArchiveAreAskedOfTheMemberTheIdNamesWithTheUpstreamsId(t *testing.T) {
	w, asked := roleWorld(t)
	r := w.get("v1/roles/rem-b-16/versions/")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "/api/v1/roles/rem-b-16/versions/?page=2") || !strings.Contains(r.Body.String(), "/api/v1/roles/archives/rem-b-16/1.0.0.tar.gz") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	r = w.get("v1/roles/archives/rem-b-16/1.0.0.tar.gz")
	if r.Code != http.StatusOK || r.Body.String() != "role bytes" {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if want := "versions:rem-b:16,archive:rem-b:16:1.0.0.tar.gz"; strings.Join(*asked, ",") != want {
		t.Errorf("asked %v, want %s", *asked, want)
	}
	// An id that names a member not in the repository, a local member, an id without a member, and a member of another format are 404 in the v1 shape.
	for _, id := range []string{"gone-16", "loc-16", "16", "rem-b-x"} {
		r := w.get("v1/roles/" + id + "/versions/")
		if r.Code != http.StatusNotFound || !strings.Contains(r.Body.String(), `"default"`) {
			t.Errorf("%s: %d %s", id, r.Code, r.Body.String())
		}
	}
}

func TestR_ANS_030_TheConflictCounterIsRegisteredOnBindAndItsSeriesLeaveWithTheRepository(t *testing.T) {
	m := observability.NewMetrics("web")
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "it/virt"}})
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	w.f.Bind(w.h, &config.Config{}, m)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "1.0.0", SHA256: hexSHA(sha(1))}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(1, "1.0.0"))
	w.store.announced[2] = []rows.Announced{{Name: "acme.tools", Version: "1.0.0", SHA256: sha(2)}}
	w.get(versionsPath)
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", nil); got != 1 {
		t.Fatalf("conflicts = %v", got)
	}
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "it/other"}})
	if got := counted(t, m, "trove_ansible_virtual_digest_conflicts_total", nil); got != 0 {
		t.Errorf("the series of a forgotten repository stays: %v", got)
	}
	// A Host nobody bound with a registry counts nothing and does not fail.
	var nilMetrics *virtualMetrics
	nilMetrics.conflicts("x", 3)
}

// counted sums a counter family of a registry whose series carry the labels in want.
func counted(t *testing.T, m *observability.Metrics, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, mm := range f.GetMetric() {
			ok := true
			for k, v := range want {
				found := false
				for _, l := range mm.GetLabel() {
					found = found || (l.GetName() == k && l.GetValue() == v)
				}
				ok = ok && found
			}
			if ok {
				sum += mm.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// mergeFixture is check 5 (R-ANS-022): the version list of a local member and of two remote members, each version marked with the
// member the merge takes it from. The local member holds acme.tools 1.0.0, the first remote 0.9.0 and 1.0.0, the second 0.9.0 and 2.0.0.
func mergeFixture() *formattest.MergeFixture {
	member := func(key string) formats.RepoRef {
		return formats.RepoRef{Name: qname.Repo{Project: "web", Key: key}, Format: ID}
	}
	local, _ := json.Marshal(listDoc{Entries: []listEntry{{Version: "1.0.0"}}})
	return &formattest.MergeFixture{
		Cases: []formattest.MergeCase{{
			Name: "a local member and two remote members", Path: VersionsDocumentPath("acme", "tools"), Request: "v3/collections/acme/tools/versions/",
			Parts: []formats.MemberDocument{
				{Member: member("local"), Tier: formats.TierLocal, Position: 0, Doc: local},
				{Member: member("remote-a"), Tier: formats.TierRemote, Position: 1, Doc: listPage(2, "1.0.0", "0.9.0")},
				{Member: member("remote-b"), Tier: formats.TierRemote, Position: 2, Doc: listPage(2, "2.0.0", "0.9.0")},
			},
			Sources: []string{"local", "remote-a", "remote-b"},
		}},
		Versions: func(doc []byte) []string {
			var l listDoc
			if json.Unmarshal(doc, &l) == nil && l.Entries != nil {
				var out []string
				for _, e := range l.Entries {
					out = append(out, e.Version)
				}
				return out
			}
			entries, _, _, err := galaxydoc.ParseUpstreamList(doc)
			if err != nil {
				return nil
			}
			var out []string
			for _, e := range entries {
				out = append(out, e.Version)
			}
			return out
		},
		VersionSource: func(merged []byte, version string) string {
			var l listDoc
			if json.Unmarshal(merged, &l) != nil {
				return ""
			}
			for _, e := range l.Entries {
				if e.Version == version {
					return e.Member
				}
			}
			return ""
		},
	}
}

func TestR_AUTHZ_005_AVersionTheCallersPathGrantHidesIsNeitherListedNorCountedNorServed(t *testing.T) {
	loc, rem := local("loc", 1, 0), remote("rem", 2, 1)
	w := newWorld(t, loc, rem)
	w.doc("collections/acme/tools/versions.json", "loc", localList(t, listEntry{Version: "3.0.0"}, listEntry{Version: "1.0.0"}))
	w.doc("collections/acme/tools/versions.json", "rem", listPage(2, "2.0.0", "0.9.0"))
	w.doc("collections/acme/tools/1.0.0.json", "loc", upstreamVersion("acme", "tools", "1.0.0", "up.example", sha(1)))
	w.doc("collections/acme/tools/3.0.0.json", "loc", upstreamVersion("acme", "tools", "3.0.0", "up.example", sha(3)))
	w.doc("collections/acme/tools/collection.json", "loc", []byte(`{"created_at":"2020-01-01T00:00:00Z","updated_at":"2030-01-01T00:00:00Z"}`))
	w.res.hidden = map[string]bool{"loc/3.0.0": true, "rem/0.9.0": true}

	list := decodeVersions(t, w.get(versionsPath))
	if list.Meta.Count != 2 || strings.Join(versionsOf(list), ",") != "2.0.0,1.0.0" {
		t.Errorf("list %v (count %d)", versionsOf(list), list.Meta.Count)
	}
	if r := w.get(versionsPath + "3.0.0/"); r.Code != http.StatusNotFound {
		t.Errorf("the document of a hidden version: %d", r.Code)
	}
	if r := w.get(versionsPath + "1.0.0/"); r.Code != http.StatusOK {
		t.Errorf("the document of a readable version: %d %s", r.Code, r.Body.String())
	}
	r := w.get("v3/collections/acme/tools/")
	var c struct {
		Highest struct{ Version string } `json:"highest_version"`
		Updated string                   `json:"updated_at"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil || c.Highest.Version != "2.0.0" || strings.HasPrefix(c.Updated, "2030") {
		t.Errorf("collection %s", r.Body.String())
	}
	// Every version hidden: the collection does not exist for the caller.
	w.res.hidden = map[string]bool{"loc/3.0.0": true, "loc/1.0.0": true, "rem/2.0.0": true, "rem/0.9.0": true}
	if r := w.get(versionsPath); r.Code != http.StatusNotFound {
		t.Errorf("all hidden: %d", r.Code)
	}
	// A caller whose grants decide no version by path gets the whole list and no per-version question.
	w.res.hidden = nil
	if list := decodeVersions(t, w.get(versionsPath)); list.Meta.Count != 4 {
		t.Errorf("list %v", versionsOf(list))
	}
}
