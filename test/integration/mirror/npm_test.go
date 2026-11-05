//go:build integration

package mirror

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
)

// npm discovery against the counting npm stub (E6b.2; docs/spec/formats/npm.md "Mirror discovery", R-REPO-051).

const passWithin = 90 * time.Second

// wantQuery fails the test unless a raw query holds exactly the parameters.
func wantQuery(t *testing.T, raw string, want map[string]string) {
	t.Helper()
	got, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("query %q: %v", raw, err)
	}
	if len(got) != len(want) {
		t.Errorf("query %q has %d parameters, want %d (%v)", raw, len(got), len(want), want)
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query %q: %s = %q, want %q", raw, k, got.Get(k), v)
		}
	}
}

// docsOf and tarballsOf name the packages the registry host was asked for.
func docsOf(hits []hit) (out []string) {
	for _, h := range registryHits(hits) {
		if !h.tarball() {
			out = append(out, strings.TrimPrefix(h.name(), "/"))
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func tarballFilesOf(hits []hit) (out []string) {
	for _, h := range tarballHits(hits) {
		out = append(out, strings.TrimPrefix(h.name(), "/"))
	}
	sort.Strings(out)
	return out
}

func TestR_REPO_051_NpmPublicRegistryFeed(t *testing.T) {
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, token: "upstream-secret"})
	r.publish("@acme/a", "1.0.0", "1.1.0")
	r.publish("@acme/b", "1.0.0")
	r.publish("@acme/c", "1.0.0")
	r.publish("@other/x", "1.0.0")
	run, web := r.runner("runner"), r.web("web")

	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)
	full := r.traf.since(0)

	t.Run("R-REPO-051 the full pass reads the root and the key range of the include prefix from the replication host", func(t *testing.T) {
		repl := replicationHits(full)
		if len(repl) != 2 {
			t.Fatalf("the replication host received %d requests: %+v", len(repl), repl)
		}
		if repl[0].method != http.MethodGet || repl[0].path != "/" || repl[0].query != "" {
			t.Errorf("first request %+v, want GET /", repl[0])
		}
		if repl[1].method != http.MethodGet || repl[1].path != "/registry/_all_docs" {
			t.Errorf("second request %+v, want GET /registry/_all_docs", repl[1])
		}
		wantQuery(t, repl[1].query, map[string]string{"startkey": `"@acme/"`, "endkey": "\"@acme/￰\"", "limit": "10000"})
	})

	t.Run("R-REPO-051 the replication host never receives an Authorization header, the registry host receives the credential", func(t *testing.T) {
		for _, h := range replicationHits(full) {
			if h.auth != "none" {
				t.Errorf("the replication host received a %s credential: %+v", h.auth, h)
			}
		}
		for _, rq := range r.stub.Replications() {
			if rq.Authorized() {
				t.Errorf("the stub's log of the replication host has an Authorization header: %+v", rq)
			}
		}
		reg := registryHits(full)
		if len(reg) == 0 {
			t.Fatal("the registry host received nothing")
		}
		for _, h := range reg {
			if h.auth != "bearer" {
				t.Errorf("a request to the registry host went without the credential: %+v", h)
			}
		}
	})

	t.Run("R-REPO-051 the registry host is asked for the documents and tarballs of the @acme packages and nothing else", func(t *testing.T) {
		if got, want := docsOf(full), []string{"@acme/a", "@acme/b", "@acme/c"}; !slices.Equal(got, want) {
			t.Errorf("documents %v, want %v", got, want)
		}
		want := []string{tarballPath("@acme/a", "1.0.0"), tarballPath("@acme/a", "1.1.0"), tarballPath("@acme/b", "1.0.0"), tarballPath("@acme/c", "1.0.0")}
		if got := tarballFilesOf(full); !slices.Equal(got, want) {
			t.Errorf("tarballs %v, want %v", got, want)
		}
		for _, h := range registryHits(full) {
			if !strings.HasPrefix(h.name(), "/@acme/") {
				t.Errorf("a request outside the include pattern reached the registry host: %+v", h)
			}
		}
		if n := r.stub.Counts().Documents("left-pad") + r.stub.Counts().Documents("@other/x"); n != 0 {
			t.Errorf("%d document requests for left-pad and @other/x", n)
		}
	})

	t.Run("R-REPO-051 only the @acme packages are stored: the feed's other names are never asked for", func(t *testing.T) {
		if n := r.storedItems(); n != 4 {
			t.Errorf("%d artifacts stored, want 4", n)
		}
		if st := r.summary(); st["method"] != "feed" || st["kind"] != "full" || st["packages"] != float64(3) || st["itemsStored"] != float64(4) {
			t.Errorf("summary %v", st)
		}
	})

	storedSeq := r.st.Text(`SELECT feed_cursor->>'seq' FROM mirror_state WHERE repo_id = $1`, r.repoN)
	if _, err := strconv.Atoi(storedSeq); err != nil {
		t.Fatalf("the stored feed cursor is %q", storedSeq)
	}

	// The stub's feed adds a change for @acme/a (a new version) and a deleted one for @acme/b.
	r.publish("@acme/a", "1.2.0")
	r.stub.Change("@acme/b", true)
	mark := r.traf.mark()
	t1 := r.pass(run)
	r.waitPass(t1, "incremental", "complete", passWithin)
	inc := r.traf.since(mark)

	t.Run("R-REPO-051 the incremental pass reads the changes after the stored sequence", func(t *testing.T) {
		repl := replicationHits(inc)
		if len(repl) != 1 || repl[0].path != "/registry/_changes" {
			t.Fatalf("the replication host received %+v, want one GET /registry/_changes", repl)
		}
		wantQuery(t, repl[0].query, map[string]string{"since": storedSeq, "limit": "10000"})
		if repl[0].auth != "none" {
			t.Errorf("credential %s", repl[0].auth)
		}
	})

	t.Run("R-REPO-051 the new tarball of @acme/a is stored and the document of @acme/b is refreshed", func(t *testing.T) {
		if got, want := tarballFilesOf(inc), []string{tarballPath("@acme/a", "1.2.0")}; !slices.Equal(got, want) {
			t.Errorf("tarballs %v, want %v", got, want)
		}
		if got, want := docsOf(inc), []string{"@acme/a", "@acme/b"}; !slices.Equal(got, want) {
			t.Errorf("documents %v, want %v", got, want)
		}
		if !r.storedItem(tarballPath("@acme/a", "1.2.0")) {
			t.Error("@acme/a 1.2.0 is not stored")
		}
	})

	t.Run("R-REPO-051 a read of the deleted package through the remote answers 404", func(t *testing.T) {
		if a := r.get(web, "@acme%2fb"); a.Status != http.StatusNotFound {
			t.Errorf("GET @acme/b: %d %s, want 404", a.Status, a.Body)
		}
	})
}

func TestR_REPO_051_NpmPrivateRegistryTakesNamesAndTheHeldPackages(t *testing.T) {
	r := newRig(t, rigOptions{mode: "pullThrough", include: []string{"@acme/a", "@acme/*"}})
	r.publish("@acme/a", "1.0.0")
	r.publish("@acme/held", "1.0.0")
	r.publish("@acme/other", "1.0.0")
	run, web := r.runner("runner"), r.web("web")

	// A client pulled @acme/held through the remote before the mode was switched.
	if a := r.get(web, "@acme%2fheld"); a.Status != http.StatusOK {
		t.Fatalf("GET @acme/held: %d %s", a.Status, a.Body)
	}
	if a := r.get(web, tarballPath("@acme/held", "1.0.0")); a.Status != http.StatusOK {
		t.Fatalf("GET the tarball of @acme/held: %d %s", a.Status, a.Body)
	}
	r.publish("@acme/held", "1.1.0")
	switched := time.Now()
	r.setDocument(run, remoteDocAt(r.upstream, "mirror", []string{"@acme/a", "@acme/*"}, nil, nil))
	r.waitMirror(run, "the mode mirror", func(s repository.RemoteSettings) bool { return s.Mirrored() })

	mark := r.traf.mark()
	r.pass(run)
	t0 := switched
	r.waitPass(t0, "full", "complete", passWithin)
	hits := r.traf.since(mark)

	t.Run("R-REPO-051 the method is names and no request goes to a replication host", func(t *testing.T) {
		if st := r.summary(); st["method"] != "names" {
			t.Errorf("summary %v, want the method names", st)
		}
		if repl := replicationHits(hits); len(repl) != 0 {
			t.Errorf("replication requests %+v", repl)
		}
		if n := len(r.stub.Replications()); n != 0 {
			t.Errorf("the stub's replication log holds %d requests", n)
		}
	})

	t.Run("R-REPO-051 the pass stores the exact name and the held package, and no other", func(t *testing.T) {
		if !r.storedItem(tarballPath("@acme/a", "1.0.0")) {
			t.Error("@acme/a is not stored")
		}
		if !r.storedItem(tarballPath("@acme/held", "1.1.0")) {
			t.Error("the new version of the held package is not stored: the pass did not refresh it")
		}
		if got := docsOf(hits); !slices.Equal(got, []string{"@acme/a", "@acme/held"}) {
			t.Errorf("documents asked for: %v", got)
		}
		if r.stub.Counts().Documents("@acme/other")+r.stub.Counts().Tarballs("@acme/other") != 0 {
			t.Error("@acme/other was requested although no pattern names it and nobody pulled it")
		}
	})
}

func TestR_REPO_051_NpmLatestIsPinnedWithinTheVersionBound(t *testing.T) {
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, mirror: settings{"maxVersionsPerPackage": 1}})
	// 1.2.0 is the highest version, latest names 1.1.0.
	r.publish("@acme/pin", "1.2.0", "1.0.0")
	if err := r.stub.Publish("@acme/pin", "1.1.0", nil, "latest"); err != nil {
		t.Fatal(err)
	}
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-051 maxVersionsPerPackage 1 stores the highest version and the version of latest", func(t *testing.T) {
		got := tarballFilesOf(r.traf.since(0))
		want := []string{tarballPath("@acme/pin", "1.1.0"), tarballPath("@acme/pin", "1.2.0")}
		if !slices.Equal(got, want) {
			t.Errorf("tarballs %v, want %v", got, want)
		}
		if n := r.storedItems(); n != 2 {
			t.Errorf("%d artifacts stored", n)
		}
	})
}

func TestR_REPO_051_NpmVersionBoundsIncrementalAndPatternChange(t *testing.T) {
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, mirror: settings{"maxVersionsPerPackage": 2, "includePrereleases": false}})
	// 1.0.0, 1.1.0, 2.0.0-rc.1 and 1.2.0, latest on 1.1.0.
	r.publish("@acme/a", "1.2.0", "1.0.0", "2.0.0-rc.1")
	if err := r.stub.Publish("@acme/a", "1.1.0", nil, "latest"); err != nil {
		t.Fatal(err)
	}
	run, web := r.runner("runner"), r.web("web")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-051 the pass stores 1.2.0 and 1.1.0, and 1.0.0 still installs by pull-through", func(t *testing.T) {
		got := tarballFilesOf(r.traf.since(0))
		want := []string{tarballPath("@acme/a", "1.1.0"), tarballPath("@acme/a", "1.2.0")}
		if !slices.Equal(got, want) {
			t.Fatalf("tarballs %v, want %v", got, want)
		}
		if r.storedItem(tarballPath("@acme/a", "1.0.0")) {
			t.Fatal("1.0.0 is stored before a client asked")
		}
		if a := r.get(web, tarballPath("@acme/a", "1.0.0")); a.Status != http.StatusOK {
			t.Fatalf("pull-through of 1.0.0: %d %s", a.Status, a.Body)
		}
		if a := r.get(web, tarballPath("@acme/a", "2.0.0-rc.1")); a.Status != http.StatusOK {
			t.Fatalf("pull-through of the prerelease: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-REPO-051 after a publish the next pass is incremental and fetches only that document and tarball", func(t *testing.T) {
		if err := r.stub.Publish("@acme/a", "1.3.0", nil); err != nil {
			t.Fatal(err)
		}
		mark := r.traf.mark()
		at := r.pass(run)
		r.waitPass(at, "incremental", "complete", passWithin)
		inc := r.traf.since(mark)
		if got, want := tarballFilesOf(inc), []string{tarballPath("@acme/a", "1.3.0")}; !slices.Equal(got, want) {
			t.Errorf("tarballs %v, want %v", got, want)
		}
		if got := docsOf(inc); !slices.Equal(got, []string{"@acme/a"}) {
			t.Errorf("documents %v, want only @acme/a", got)
		}
		if repl := replicationHits(inc); len(repl) != 1 || repl[0].path != "/registry/_changes" {
			t.Errorf("replication requests %+v", repl)
		}
		if !r.storedItem(tarballPath("@acme/a", "1.3.0")) {
			t.Error("1.3.0 is not stored")
		}
	})

	t.Run("R-REPO-051 adding an include pattern makes the next pass full", func(t *testing.T) {
		r.publish("@acme2/x", "1.0.0")
		r.setDocument(run, remoteDocAt(r.upstream, "mirror", []string{"@acme/*", "@acme2/*"}, settings{"maxVersionsPerPackage": 2, "includePrereleases": false}, nil))
		r.waitAdmits(run, "@acme2/x")
		mark := r.traf.mark()
		at := r.pass(run)
		r.waitPass(at, "full", "complete", passWithin)
		repl := replicationHits(r.traf.since(mark))
		if len(repl) != 3 || repl[0].path != "/" || repl[1].path != "/registry/_all_docs" || repl[2].path != "/registry/_all_docs" {
			t.Fatalf("the full pass sent %+v, want the root and one _all_docs per prefix", repl)
		}
		if !r.storedItem(tarballPath("@acme2/x", "1.0.0")) {
			t.Error("@acme2/x is not stored")
		}
	})
}

func TestR_REPO_051_NpmAHeldPackageThatTheFeedMissedIsRefreshedByTheNextFullPass(t *testing.T) {
	r := newRig(t, rigOptions{public: true, mode: "pullThrough", include: []string{"@acme/*"}})
	r.publish("@acme/held", "1.0.0")
	r.publish("@acme/listed", "1.0.0")
	r.stub.Unlist("@acme/held")
	run, web := r.runner("runner"), r.web("web")
	if a := r.get(web, "@acme%2fheld"); a.Status != http.StatusOK {
		t.Fatalf("GET @acme/held: %d %s", a.Status, a.Body)
	}
	if a := r.get(web, tarballPath("@acme/held", "1.0.0")); a.Status != http.StatusOK {
		t.Fatalf("GET the tarball of @acme/held: %d %s", a.Status, a.Body)
	}
	r.publish("@acme/held", "1.1.0")
	switched := time.Now()
	r.setDocument(run, remoteDocAt(r.upstream, "mirror", []string{"@acme/*"}, nil, nil))
	r.waitMirror(run, "the mode mirror", func(s repository.RemoteSettings) bool { return s.Mirrored() })

	r.pass(run)
	t0 := switched
	r.waitPass(t0, "full", "complete", passWithin)
	t.Run("R-REPO-051 the held package is stored although _all_docs does not list it", func(t *testing.T) {
		if !r.storedItem(tarballPath("@acme/held", "1.1.0")) || !r.storedItem(tarballPath("@acme/held", "1.0.0")) {
			t.Errorf("the held package is not refreshed: %d artifacts stored", r.storedItems())
		}
	})
}
