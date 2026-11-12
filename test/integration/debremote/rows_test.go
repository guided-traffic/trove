//go:build integration

package debremote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	debformat "github.com/guided-traffic/trove/internal/formats/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/test/debstub"
)

// poolFile is a package file of the stub's stable suite: the path a client asks for and the stub's digest.
func (e *denv) poolFile() (path, digest string) {
	e.t.Helper()
	for _, f := range e.stub.Files() {
		if strings.HasPrefix(f.Path, debstub.ArchiveBase+"/pool/main/") && strings.HasSuffix(f.Path, "_amd64.deb") {
			return strings.TrimPrefix(f.Path, debstub.ArchiveBase+"/"), f.SHA256
		}
	}
	e.t.Fatal("the stub has no package file")
	return "", ""
}

// fetchIndexes does what apt-get update does: the release and the index of main/binary-amd64.
func (e *denv) fetchIndexes(repo string) {
	e.t.Helper()
	for _, p := range []string{"InRelease", packages + ".gz"} {
		if a := e.get(repo, suitePath+p); a.Status != http.StatusOK {
			e.t.Fatalf("%s: %s: %d %s", repo, p, a.Status, a.Body)
		}
	}
}

// paragraphs counts the paragraphs of the stub's Packages index of main/binary-amd64 for stable.
func (e *denv) paragraphs(suite string) int {
	e.t.Helper()
	body := e.stubGet(debstub.ArchiveBase + "/dists/" + suite + "/" + packages)
	return strings.Count("\n"+string(body), "\nPackage: ")
}

func TestR_DEB_023_TheRowsOfAnIndexAreParsedAfterAnUpdateAndReplacedWhenItChanges(t *testing.T) {
	e := newEnv(t)
	want := e.paragraphs("stable")
	if want == 0 {
		t.Fatal("the stub's index has no paragraph")
	}
	e.fetchIndexes(rFast)
	id := e.repoID(rFast)
	eventually(t, 10*time.Second, "the rows of main/binary-amd64", func() bool {
		return e.st.Number(`SELECT count(*) FROM deb_remote_entries WHERE repo_id = $1 AND index_path = $2`, id, packages) == int64(want)
	})
	if n := e.st.Number(`SELECT entries FROM deb_remote_indexes WHERE repo_id = $1 AND path = $2`, id, packages); n != int64(want) {
		t.Errorf("deb_remote_indexes says %d entries, want %d", n, want)
	}
	// The stub publishes a new generation whose Packages changed. The client never fetches the index again: the rows follow the
	// release, which the next fetch of the InRelease stores (Q-DEB-36).
	if _, err := e.stub.NewGeneration("stable"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if a := e.get(rFast, suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	eventually(t, 10*time.Second, "the rows to follow the new release", func() bool {
		return e.st.Number(`SELECT count(*) FROM deb_remote_entries WHERE repo_id = $1 AND index_path = $2`, id, packages) == int64(want+1)
	})
}

func TestR_DEB_022_APackageFileIsVerifiedAgainstTheDigestOfTheParsedIndex(t *testing.T) {
	e := newEnv(t)
	path, digest := e.poolFile()
	e.fetchIndexes(rKeyed)
	id := e.repoID(rKeyed)
	eventually(t, 10*time.Second, "the rows", func() bool { return e.rows("deb_remote_entries", rKeyed) > 0 })
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_entries WHERE repo_id = $1 AND path = $2`, id, path); n < 1 {
		t.Fatalf("no parsed row lists %s", path)
	}
	e.stub.Reset()
	a := e.get(rKeyed, path)
	if a.Status != http.StatusOK || sum(a.Body) != digest {
		t.Fatalf("%d, %s, want %s", a.Status, sum(a.Body), digest)
	}
	if b := e.get(rKeyed, path); b.Status != http.StatusOK {
		t.Errorf("second read: %d", b.Status)
	}
	if n := e.requests(debstub.ArchiveBase + "/" + path); n != 1 {
		t.Errorf("the stub saw %d requests for the package file, want 1", n)
	}
}

func TestR_DEB_022_APackageFileThatIsNotTheListedOneIsRefusedAndNothingIsStored(t *testing.T) {
	e := newEnv(t)
	path, _ := e.poolFile()
	repo := e.fresh(true)
	e.fetchIndexes(repo)
	eventually(t, 10*time.Second, "the rows", func() bool { return e.rows("deb_remote_entries", repo) > 0 })
	if err := e.stub.SetMode(debstub.Mode{WrongPackage: []string{"all"}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(repo, path)
	if a.Status != http.StatusBadGateway || a.Code(t) != "upstream_integrity_mismatch" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM items i WHERE i.repo_id = $1 AND i.path = $2`, e.repoID(repo), path); n != 0 {
		t.Errorf("%d items stored for the refused file", n)
	}
}

func TestR_DEB_022_AFileNoParsedIndexListsIsStoredUnverifiedOrRefusedByTheRequirePolicy(t *testing.T) {
	e := newEnv(t)
	path, digest := e.poolFile()
	// No release was read and no row exists: verify-if-available stores the file unverified and counts it.
	if a := e.get(rPlain, path); a.Status != http.StatusOK || sum(a.Body) != digest {
		t.Fatalf("verify-if-available: %d", a.Status)
	}
	if !strings.Contains(e.r.Metrics(t), "trove_deb_unverified_fills_total") {
		t.Error("the unverified fill was not counted")
	}
	// The policy require refuses it, before the upstream is asked.
	e.stub.Reset()
	a := e.get(rRequire, path)
	if a.Status != http.StatusBadGateway || a.Code(t) != "upstream_digest_missing" {
		t.Fatalf("require: %d %s", a.Status, a.Body)
	}
	if n := e.stub.Counts().Total(); n != 0 {
		t.Errorf("the stub saw %d requests for a file the policy refuses", n)
	}
}

// upstreamOf is a discover.Upstream over the stub for the jobs the test runs itself: the engine's background path is the replica's.
type upstreamOf struct{ base *url.URL }

func (u upstreamOf) Base() *url.URL            { return u.base }
func (u upstreamOf) Settings() json.RawMessage { return nil }
func (u upstreamOf) Get(context.Context, *url.URL, string, int64) (*discover.Response, error) {
	return nil, errors.New("not used")
}

func (u upstreamOf) fetch(ctx context.Context, path string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.base.String()+path, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	var sb strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return []byte(sb.String()), true, nil
}

func (u upstreamOf) Document(ctx context.Context, path, _ string) ([]byte, bool, error) {
	return u.fetch(ctx, path)
}

func (u upstreamOf) Artifact(ctx context.Context, path string, _ int64) ([]byte, bool, error) {
	return u.fetch(ctx, path)
}

type backgroundOf struct{ base *url.URL }

func (b backgroundOf) Upstream(context.Context, int64, string) (discover.Upstream, error) {
	return upstreamOf(b), nil
}

func TestR_DEB_023_AnIndexOverTheBoundFailsItsRunAndTheEarlierRowsStay(t *testing.T) {
	e := newEnv(t)
	repo := e.fresh(true)
	// The suite bulk holds the 70,000 paragraphs of the stub's large index.
	for _, p := range []string{"InRelease", "main/binary-amd64/Packages.gz"} {
		if a := e.get(repo, "dists/bulk/"+p); a.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", p, a.Status, a.Body)
		}
	}
	id := e.repoID(repo)
	eventually(t, 60*time.Second, "the rows of the large index", func() bool {
		return e.st.Number(`SELECT count(*) FROM deb_remote_entries WHERE repo_id = $1`, id) >= 70000
	})
	before := e.rows("deb_remote_entries", repo)

	h := e.r.App.FormatHost()
	port, ok := h.FormatData(debformat.ID).(*debdata.Store)
	if !ok {
		t.Fatalf("the data port is %T", h.FormatData(debformat.ID))
	}
	base, _ := url.Parse(e.srv.URL + "/debian/")
	j := &debdata.RemoteJobs{Store: port, Background: backgroundOf{base: base}, MaxParagraphs: 1000}
	args, _ := json.Marshal(debdata.ParseArgs{RepoID: id, Dir: "dists/bulk", Path: "main/binary-amd64/Packages"})
	// The index is parsed already; a parse runs when the hash it recorded is not the one the release lists, so the recorded hash is changed.
	e.st.Exec(`UPDATE deb_remote_indexes SET sha256 = decode(repeat('00', 32), 'hex') WHERE repo_id = $1`, id)
	err := j.RunParse(context.Background(), &jobs.Run{Job: jobs.Job{ID: "it", Args: args}})
	if err == nil || !jobs.IsPermanent(err) || !strings.Contains(err.Error(), "too_many_entries") {
		t.Fatalf("RunParse = %v, want a permanent too_many_entries", err)
	}
	if after := e.rows("deb_remote_entries", repo); after != before {
		t.Errorf("the rows are %d after the failed run, were %d", after, before)
	}
}

func TestR_DEB_023_TheShadowingCheckAsksTheRowsOfTheScopeAndEnqueuesWhatSettlesIt(t *testing.T) {
	e := newEnv(t)
	h := e.r.App.FormatHost()
	st, ok := h.FormatData(debformat.ID).(debformat.Store)
	if !ok {
		t.Fatalf("the data port is %T", h.FormatData(debformat.ID))
	}
	repo := e.fresh(true)
	id := e.repoID(repo)
	ctx := context.Background()
	scope := func(name string, suites []string, comps ...string) rows.NameScope {
		return rows.NameScope{RepoID: id, Name: name, Suites: suites, Components: comps, Architectures: []string{"amd64"}}
	}
	// Nothing is fetched yet: the answer is undetermined and the fetch and the parse are enqueued through the background path.
	served, known, err := st.ServesName(ctx, scope("trove-hello", []string{"stable"}, "main"))
	if err != nil || served || known {
		t.Fatalf("before: served %v known %v err %v", served, known, err)
	}
	if enq, err := st.EnsureParsed(ctx, scope("trove-hello", []string{"stable"}, "main")); err != nil || !enq {
		t.Fatalf("EnsureParsed: %v %v", enq, err)
	}
	// The scope is settled when every index of it that the release lists is parsed (R-DEB-023). A name that an index lists is known
	// as soon as that one index is parsed, while another index of the scope (the architecture-independent one is always in it) may
	// still be queued, so the wait asks about a name nobody lists: it is known only when no index is left to parse.
	eventually(t, 30*time.Second, "the scope to be settled", func() bool {
		_, known, err := st.ServesName(ctx, scope("no-such-package", []string{"stable"}, "main"))
		return err == nil && known
	})
	if served, known, err := st.ServesName(ctx, scope("trove-hello", []string{"stable"}, "main")); err != nil || !served || !known {
		t.Errorf("a name the stub lists: served %v known %v err %v", served, known, err)
	}
	if served, known, err := st.ServesName(ctx, scope("acme-tool", []string{"stable"}, "main")); err != nil || served || !known {
		t.Errorf("a name the stub does not list: served %v known %v err %v", served, known, err)
	}
	// A suite the upstream does not have is recorded absent and settles the scope without a parse.
	if err := e.stub.SetMode(debstub.Mode{NotFound: []string{"testing"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureParsed(ctx, scope("acme-tool", []string{"stable", "testing"}, "main")); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the absent suite to be recorded", func() bool {
		_, known, err := st.ServesName(ctx, scope("acme-tool", []string{"stable", "testing"}, "main"))
		return err == nil && known
	})
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND dir = 'dists/testing' AND document = 'absent'`, id); n != 1 {
		t.Errorf("absent records = %d", n)
	}
}
