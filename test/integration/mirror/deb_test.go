//go:build integration

package mirror

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A Debian remote in mode mirror against the counting archive of test/debstub (docs/spec/formats/debian.md "Mirror discovery", R-DEB-024,
// R-REPO-051, R-REPO-052): the replicas are the real stack with the real format and host; the stub runs in the test process on loopback
// HTTP, which the installation admits as an operator would. Every pass runs on one of two worker processes that claim the queue mirror,
// and the clients read through a third, client-serving process.

type debOptions struct {
	include []string
	mirror  settings
	// deb is the member deb.mirror of the repository document; suites default to stable.
	deb map[string]any
	// keys is the armored upstream keys; empty: the key a.
	keys string
	// mode is the stub's switches.
	mode debstub.Mode
}

// debRig is a rig whose upstream is the Debian stub.
type debRig struct {
	*rig
	stub *debstub.Server
	srv  *httptest.Server
	repo string
}

func newDebRig(t *testing.T, o debOptions) *debRig {
	t.Helper()
	stub, err := debstub.New(debstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stub.SetMode(o.mode); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	keys := o.keys
	if keys == "" {
		ring, err := stub.Keyring("a")
		if err != nil {
			t.Fatal(err)
		}
		keys = string(ring)
	}
	mirrorMember := map[string]any{"suites": []string{"stable"}}
	for k, v := range o.deb {
		mirrorMember[k] = v
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(remoteDocAt(srv.URL+"/debian/", "mirror", o.include, o.mirror, nil)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["deb"] = map[string]any{"upstreamKeys": keys, "mirror": mirrorMember}
	raw, _ := json.Marshal(doc)

	r := &rig{t: t, format: "deb", traf: &traffic{}, url: srv.URL + "/", upstream: srv.URL + "/debian/"}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirrorit", Env: append([]string{
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}, fastJobs...)})
	st.Repository(repoKey, "remote", "deb", string(raw))
	st.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at) SELECT id, now() + interval '1 hour' FROM repositories WHERE key = $1`, repoKey)
	reader, rita := st.User("reader"), st.User("rita")
	st.Bind("admin", rita)
	st.Policy("readers", []string{"read"}, []string{repoKey}, reader)
	r.reader = st.Token(reader, scope.Default())
	r.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	db, err := postgres.Open(context.Background(), st.DatabaseConfig(), "mirror-it", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	r.env = &env{t: t, st: st, db: db, store: mirror.NewPGStore(db)}
	r.repoN = st.Number(`SELECT id FROM repositories WHERE key = $1`, repoKey)
	r.dumpOnFailure()
	return &debRig{rig: r, stub: stub, srv: srv, repo: "/api/deb/" + testenv.DefaultProject + "/" + repoKey + "/"}
}

// workers starts the two processes that claim the queue mirror and the web process; the pass may run on either worker.
func (r *debRig) workers() (first, second, web *testenv.Replica) {
	return r.runner("runner-1"), r.runner("runner-2"), r.web("web")
}

// items lists the paths of the files the repository holds below a prefix, sorted.
func (r *debRig) items(prefix string) []string {
	var out []string
	r.st.Query(`SELECT i.path FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`, func(rows pgx.Rows) error {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		out = append(out, p)
		return nil
	}, repoKey, prefix+"%")
	sort.Strings(out)
	return out
}

// listed returns the Filename values of a plain Packages index of the stub.
func (r *debRig) listed(suite, comp, arch string) []string {
	r.t.Helper()
	resp, err := http.Get(r.srv.URL + "/debian/dists/" + suite + "/" + comp + "/binary-" + arch + "/Packages")
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	var out []string
	for _, l := range strings.Split(sb.String(), "\n") {
		if f, ok := strings.CutPrefix(l, "Filename: "); ok {
			out = append(out, f)
		}
	}
	return out
}

// stubRequests counts the requests whose path contains one of the parts.
func (r *debRig) stubRequests(parts ...string) int {
	n := 0
	for _, e := range r.stub.Counts().Entries {
		for _, p := range parts {
			if strings.Contains(e.Path, p) {
				n += e.Count
				break
			}
		}
	}
	return n
}

// install is what apt-get install does through the mirror: the release, the index by its hash and the package file; any answer but 200
// fails the test. It returns the bytes of the package file.
func (r *debRig) install(rep *testenv.Replica, file string) []byte {
	r.t.Helper()
	a := r.st.Do(rep, http.MethodGet, r.repo+file, r.reader, nil)
	if a.Status != http.StatusOK {
		r.t.Fatalf("GET %s: %d %.200s", file, a.Status, a.Body)
	}
	return a.Body
}

// R-DEB-024, Mirror discovery: a pass with the defaults keeps the release, the Packages indexes of main for amd64 and all, the
// translation and every package file those indexes list, asks for nothing of contrib or arm64, and the repository then serves a client
// with the upstream stopped.
func TestR_DEB_024_APassStoresTheScopeAndTheMirrorServesWithTheStubStopped(t *testing.T) {
	r := newDebRig(t, debOptions{})
	one, _, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)

	want := append(r.listed("stable", "main", "amd64"), r.listed("stable", "main", "all")...)
	if len(want) == 0 {
		t.Fatal("the stub lists no package of main")
	}
	t.Run("R-DEB-024 every package file of main/binary-amd64 and binary-all is stored", func(t *testing.T) {
		got := r.items("pool/")
		sort.Strings(want)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("stored %v, want %v", got, want)
		}
	})
	t.Run("R-DEB-024 the stub saw no request for contrib or arm64", func(t *testing.T) {
		if n := r.stubRequests("/contrib/", "arm64"); n != 0 {
			t.Errorf("%d requests for contrib or arm64: %+v", n, r.stub.Counts().Entries)
		}
	})
	t.Run("R-DEB-024 the rows of the parsed indexes exist", func(t *testing.T) {
		if n := r.st.Number(`SELECT count(*) FROM deb_remote_entries WHERE repo_id = $1`, r.repoN); n < int64(len(want)) {
			t.Errorf("%d rows for %d files", n, len(want))
		}
	})
	t.Run("R-DEB-024 with the stub stopped, the release, an index by hash and a package file are served", func(t *testing.T) {
		r.srv.Close()
		rel := r.st.Do(web, http.MethodGet, r.repo+"dists/stable/InRelease", r.reader, nil)
		if rel.Status != http.StatusOK {
			t.Fatalf("InRelease: %d %.200s", rel.Status, rel.Body)
		}
		for _, f := range want {
			if len(r.install(web, f)) == 0 {
				t.Errorf("%s is empty", f)
			}
		}
		// Every variant of an index the release lists is served the way a client asks for it: by its hash.
		text, err := openpgp.ClearText(rel.Body, openpgp.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := release.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range parsed.Files {
			if !strings.HasPrefix(f.Path, "main/binary-amd64/Packages") && !strings.HasPrefix(f.Path, "main/binary-all/Packages") || strings.Contains(f.Path, "by-hash") {
				continue
			}
			p := "dists/stable/" + path.Dir(f.Path) + "/by-hash/SHA256/" + hex.EncodeToString(f.SHA256[:])
			if a := r.st.Do(web, http.MethodGet, r.repo+p, r.reader, nil); a.Status != http.StatusOK {
				t.Errorf("%s (%s) by hash: %d", f.Path, p, a.Status)
			}
			// APT takes the strongest hash the release lists: the stub lists SHA512 too.
			d, ok := parsed.SHA512[f.Path]
			if !ok {
				t.Errorf("the release lists no SHA512 for %s", f.Path)
				continue
			}
			p = "dists/stable/" + path.Dir(f.Path) + "/by-hash/SHA512/" + d
			if a := r.st.Do(web, http.MethodGet, r.repo+p, r.reader, nil); a.Status != http.StatusOK {
				t.Errorf("%s (%s) by hash: %d", f.Path, p, a.Status)
			}
		}
		for _, f := range r.items("dists/") {
			if a := r.st.Do(web, http.MethodGet, r.repo+f, r.reader, nil); a.Status != http.StatusOK {
				t.Errorf("%s: %d", f, a.Status)
			}
		}
	})
}

// R-DEB-024: with include patterns and one version per name, only the newest version of each name is stored, over two suites.
func TestR_DEB_024_TheVersionBoundKeepsTheNewestVersionOfEachNameAcrossTheSuites(t *testing.T) {
	r := newDebRig(t, debOptions{
		include: []string{"trove-hello", "trove-tools"}, mirror: settings{"maxVersionsPerPackage": 1},
		deb: map[string]any{"suites": []string{"stable", "testing"}},
	})
	one, _, _ := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)

	got := r.items("pool/")
	has := func(sub string) bool {
		for _, p := range got {
			if strings.Contains(p, sub) {
				return true
			}
		}
		return false
	}
	if !has("trove-hello_1.1-1_amd64.deb") || has("trove-hello_1.0-1_") {
		t.Errorf("trove-hello: %v", got)
	}
	if !has("trove-tools_2.1-1_amd64.deb") || has("trove-tools_2.0-1_") {
		t.Errorf("trove-tools: %v", got)
	}
	if has("trove-config") || has("libtrove1") {
		t.Errorf("a name no pattern names is stored: %v", got)
	}
}

// R-DEB-024: a second pass with nothing changed sends one conditional request per release document and nothing for an index or a
// package file; after the stub publishes a generation, the next pass stores the new index and the new file.
func TestR_DEB_024_ASecondPassIsQuietAndANewGenerationIsStored(t *testing.T) {
	r := newDebRig(t, debOptions{})
	one, _, _ := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)

	r.stub.Reset()
	t1 := r.pass(one)
	r.waitPass(t1, "full", "complete", passWithin)
	c := r.stub.Counts()
	if got := c.Total(); got != c.OfKind(debstub.ClassRelease, "") {
		t.Errorf("the second pass sent %d requests, %d of them for release documents: %+v", got, c.OfKind(debstub.ClassRelease, ""), c.Entries)
	}
	if c.OfKind(debstub.ClassRelease, "") != 1 || c.Conditional(debstub.ClassRelease) != 1 {
		t.Errorf("release requests %d, conditional %d, want one each", c.OfKind(debstub.ClassRelease, ""), c.Conditional(debstub.ClassRelease))
	}

	before := len(r.items("pool/"))
	if _, err := r.stub.NewGeneration("stable"); err != nil {
		t.Fatal(err)
	}
	t2 := r.pass(one)
	r.waitPass(t2, "full", "complete", passWithin)
	if after := len(r.items("pool/")); after != before+1 {
		t.Errorf("%d package files after the new generation, want %d", after, before+1)
	}
}

// R-DEB-024, R-REPO-052: with maxBytes below the mirrored size the pass ends bound_reached, and a package it did not store still
// installs by pull-through.
func TestR_DEB_024_TheByteBoundEndsThePassAndAPackageNotStoredIsPulledThrough(t *testing.T) {
	r := newDebRig(t, debOptions{mirror: settings{"maxBytes": 1 << 20}})
	// The packages of the stub are a few hundred bytes: a bound of 1 MiB is not reached, so the bound is set below the size of a package
	// by making the stub's files large is not possible; the bound counts the stored bytes of the pass, which the test lowers directly.
	r.st.Exec(`UPDATE repositories SET config = jsonb_set(config, '{remote,mirror,maxBytes}', '1') WHERE key = $1`, repoKey)
	one, _, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "bound_reached", passWithin)
	files := r.listed("stable", "main", "amd64")
	if len(r.items("pool/")) >= len(files) {
		t.Fatalf("the pass stored every file under a bound of one byte: %v", r.items("pool/"))
	}
	for _, f := range files {
		if len(r.install(web, f)) == 0 {
			t.Errorf("%s did not install by pull-through", f)
		}
	}
}

// R-DEB-024, Q-DEB-32: the stub signing with a key that is not in the set fails the pass's discovery and stores nothing new.
func TestR_DEB_024_AReleaseSignedByAnUnknownKeyFailsTheDiscoveryAndStoresNothing(t *testing.T) {
	r := newDebRig(t, debOptions{mode: debstub.Mode{SignWith: []string{"b"}}})
	one, _, _ := r.workers()
	t0 := r.pass(one)
	_ = t0
	testenv.WaitFor(t, "the pass to fail", passWithin, func() bool {
		return strings.HasPrefix(r.text(`SELECT COALESCE(last_error_code, '') FROM mirror_state WHERE repo_id = $1`), "discovery_failed")
	})
	if n := len(r.items("pool/")) + len(r.items("dists/")); n != 0 {
		t.Errorf("%d files stored from an unverified release", n)
	}
	if got := r.text(`SELECT COALESCE(last_error, '') FROM mirror_state WHERE repo_id = $1`); !strings.Contains(got, "signature") {
		t.Logf("last error: %s", got)
	}
}

// R-DEB-024: a pass on one of two workers while clients install through a third process; the sources are kept with sources: true.
func TestR_DEB_024_SourcesAreKeptOnRequestAndClientsInstallDuringThePass(t *testing.T) {
	r := newDebRig(t, debOptions{deb: map[string]any{"sources": true}})
	one, two, web := r.workers()
	_ = two
	t0 := r.pass(one)
	// A client installs while the pass runs: pull-through or the mirror, the answer is the same file.
	files := r.listed("stable", "main", "amd64")
	if a := r.st.Do(web, http.MethodGet, r.repo+files[0], r.reader, nil); a.Status != http.StatusOK {
		t.Fatalf("install during the pass: %d", a.Status)
	}
	r.waitPass(t0, "full", "complete", passWithin)
	var sources []string
	for _, p := range r.items("pool/") {
		if strings.HasSuffix(p, ".dsc") || strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tar.xz") {
			sources = append(sources, p)
		}
	}
	if len(sources) == 0 {
		t.Errorf("no source file stored: %v", r.items("pool/"))
	}
}

// TestProofMirrorServer is the harness of the by-hand proof of the Debian mirror with real APT clients (docs/developer/deb.md, "Proven by
// hand"). It does nothing unless TROVE_DEB_PROOF_DIR names a directory: then it runs a pass, stops the stub, writes the port, a token,
// the stub's key a and the repository path into the directory, and serves until the file "done" appears there.
func TestProofMirrorServer(t *testing.T) {
	dir := os.Getenv("TROVE_DEB_PROOF_DIR")
	if dir == "" {
		t.Skip("TROVE_DEB_PROOF_DIR is not set")
	}
	r := newDebRig(t, debOptions{})
	one, _, web := r.workers()
	t0 := r.pass(one)
	r.waitPass(t0, "full", "complete", passWithin)
	r.srv.Close() // the clients read a mirror whose upstream is gone
	port := web.Addr("default")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ring, err := r.stub.Keyring("a")
	if err != nil {
		t.Fatal(err)
	}
	write("port", port[strings.LastIndex(port, ":")+1:])
	write("token", r.reader)
	write("key.asc", string(ring))
	write("repo-keyed", r.repo)
	write("ready", "1")
	for deadline := time.Now().Add(25 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
			break
		}
	}
	t.Logf("the requests of the clients that failed:\n%s", logLines(web.Logs.String(), `"status":4`, `"status":5`))
}
