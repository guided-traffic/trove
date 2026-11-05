//go:build integration

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// The mirror host (formathost.MirrorHost, the implementation of mirror.Host) over the real remote engine, the real decision point's
// audit and a format that stands for npm without being it: the engine's counting adapter of proxytest behind the format contract.

// hostFormat is a format with a remote adapter and nothing else the host asks of it.
type hostFormat struct{ formats.Format }

func (hostFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{Facts: catalog.Facts{ID: "proxytest", PatternSubject: catalog.SubjectName, PackageNames: true}}
}

func (hostFormat) Remote() formats.RemoteAdapter { return hostRemote{} }

func (hostFormat) Classify(p formats.Path) formats.PathInfo {
	return formats.PathInfo{Package: proxytest.Package(p.Path), Kind: formats.Content}
}

func (hostFormat) PackageDocumentPath(name string) (formats.Path, bool) {
	return formats.Path{Path: proxytest.Doc(name)}, name != "" && !strings.ContainsAny(name, " ?")
}

type hostRemote struct{ formats.RemoteBase }

func (hostRemote) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	u, err := proxy.UpstreamBase{URL: rc.Upstream}.Path(p.Path)
	if err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

func (hostRemote) AcceptResponse(formats.Path, *http.Response) error { return nil }

func (hostRemote) PrepareFill(context.Context, formats.RemoteContext, formats.Path, string, io.ReaderAt, int64) (formats.Prepared, error) {
	return formats.Prepared{}, nil
}

func (hostRemote) OnFill(context.Context, formats.Tx, formats.FillEvent) (formats.FillDecision, error) {
	return formats.FillByFetchTime, nil
}

func (hostRemote) Rewrite(_ context.Context, _ formats.RemoteContext, _ formats.Path, doc []byte, _ formats.LinkBuilder) ([]byte, error) {
	return doc, nil
}

func (hostRemote) TracksVersionRemoval() bool { return false }

// claims refuses the names it holds, as a global claim does.
type claims map[string]bool

func (c claims) RemoteRefused(_ context.Context, _ repository.Repository, name string, _ *repository.Repository) (bool, error) {
	return c[name], nil
}

// mirrorHost builds the host on a replica's engine: a decision point that records (it is not started; it records the populate event and
// decides nothing here), the item store and the package store.
func (r *replica) mirrorHost(refusal formathost.RemoteRefuser) *formathost.MirrorHost {
	r.t.Helper()
	dec, err := authz.New(authz.Options{
		Source: authz.NewPostgresSource(r.db, 10*time.Second), Changes: r.changes,
		Config:   config.Authz{Cache: config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: time.Hour}, Audit: config.AuthzAudit{Reads: config.AuthzReadsDenied}},
		Recorder: r.rec, Replica: r.name, Log: quiet,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	pk, err := packages.NewStore(r.db)
	if err != nil {
		r.t.Fatal(err)
	}
	h, err := formathost.New(formathost.Options{
		Runner: r.db, Decider: dec, Items: r.items, Packages: pk, Engine: r.engine,
		Deps: formathost.Deps{Refusal: refusal, Formats: func(id string) (formats.Format, bool) { return hostFormat{}, id == "proxytest" }},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	mh, err := formathost.NewMirrorHost(h, r.engine)
	if err != nil {
		r.t.Fatal(err)
	}
	return mh
}

func TestR_REPO_057_AFillOfTheMirrorWritesThePopulateEventWithTheSystemAsActorAndMirrorAsReason(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	for _, name := range []string{"a", "b"} {
		up.Set("/pkg/"+name+"/-/"+name+"-1.tgz", proxytest.Route{Body: []byte("tarball-" + name), Type: "application/octet-stream"})
	}
	rem := e.remote("hosted", up)
	r := e.newReplica("a", replicaOpts{})
	mh := r.mirrorHost(nil)
	pass := mirror.Pass{JobID: "job-7", Ceiling: 2}
	ctx := context.Background()

	t.Run("R-REPO-057 Q-REPO-145 a fill stores the artifact and the populate event names the system and the reason mirror", func(t *testing.T) {
		res, err := mh.Fill(ctx, rem.Repo, pass, discover.Item{Path: "pkg/a/-/a-1.tgz", Version: "1"})
		if err != nil || res.Present || res.Bytes != int64(len("tarball-a")) {
			t.Fatalf("%+v %v", res, err)
		}
		waitFor(t, "the populate event to be recorded", 2*time.Second, func() bool { return len(r.rec.ofType(authz.EventWriteAllowed)) == 1 })
		ev := r.rec.ofType(authz.EventWriteAllowed)[0]
		if ev.Actor.Kind != "system" || ev.Actor.Name != "mirror" || ev.Reason != "mirror" || ev.Repository != "hosted" || ev.Path != "pkg/a/-/a-1.tgz" {
			t.Fatalf("%+v", ev)
		}
		var d map[string]any
		if err := json.Unmarshal(ev.Details, &d); err != nil || d["reason"] != "mirror" || d["package"] != "a" || d["credential"] != "none" {
			t.Fatalf("%s %v", ev.Details, err)
		}
		cached := r.rec.ofType("artifact.cached")
		if len(cached) != 1 || cached[0].Actor.Kind != "system" {
			t.Fatalf("%+v", cached)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/a/-/a-1.tgz'"); n != 1 {
			t.Fatalf("items = %d", n)
		}
	})

	t.Run("R-REPO-055 an artifact that is stored already is Present, costs no upstream request and writes no second event", func(t *testing.T) {
		before := up.Total()
		res, err := mh.Fill(ctx, rem.Repo, pass, discover.Item{Path: "pkg/a/-/a-1.tgz", Version: "1"})
		if err != nil || !res.Present || res.Bytes != 0 {
			t.Fatalf("%+v %v", res, err)
		}
		if up.Total() != before {
			t.Fatalf("%d upstream requests", up.Total()-before)
		}
		if n := len(r.rec.ofType(authz.EventWriteAllowed)); n != 1 {
			t.Fatalf("%d populate events", n)
		}
	})

	t.Run("R-REPO-054 a rate-limit answer is an error discover.AsRateLimited finds, and the circuit stays closed", func(t *testing.T) {
		up.Set("/pkg/hot/-/hot-1.tgz", proxytest.Route{Status: 429, Header: map[string]string{"Retry-After": "11"}})
		_, err := mh.Fill(ctx, rem.Repo, pass, discover.Item{Path: "pkg/hot/-/hot-1.tgz"})
		rl, ok := discover.AsRateLimited(err)
		if !ok {
			t.Fatalf("%v", err)
		}
		if wait, hinted := rl.RetryAfter(); !hinted || wait != 11*time.Second {
			t.Fatalf("%v %v", wait, hinted)
		}
		if pk, _ := r.engine.Peek(ctx, rem, "pkg/b/-/b-1.tgz", ""); pk.CircuitOpenFor != 0 {
			t.Fatal("the circuit opened")
		}
	})

	t.Run("R-REPO-054 an open circuit is a *mirror.WaitError of at most 5 s, and nothing is sent", func(t *testing.T) {
		if s := r.artifact(rem, "pkg/hot/-/hot-1.tgz"); s.status != 503 { // a client's 429 opens it
			t.Fatalf("%d", s.status)
		}
		before := up.Total()
		_, err := mh.Fill(ctx, rem.Repo, pass, discover.Item{Path: "pkg/b/-/b-1.tgz"})
		var w *mirror.WaitError
		if !errors.As(err, &w) || w.Reason != mirror.WaitCircuitOpen || w.After < time.Second || w.After > 5*time.Second {
			t.Fatalf("%v", err)
		}
		if up.Total() != before {
			t.Fatal("a request was sent while the circuit was open")
		}
	})

	t.Run("R-ARCH-009 a format that does not discover has no lister, and no upstream is lent for it", func(t *testing.T) {
		if l, ok := mh.Lister("proxytest"); ok || l != nil {
			t.Fatal("a lister for a format without one")
		}
		if _, err := mh.Upstream(rem.Repo, pass); err == nil {
			t.Fatal("an upstream was lent for a format that does not discover")
		}
	})
}

func TestR_REPO_055_AFillThatAClientPreemptsReturnsMirrorErrPreempted(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	h := &held{}
	up.Set("/pkg/bg/-/bg-1.tgz", h.route(3*time.Second))
	up.Set("/pkg/c1/-/c1-1.tgz", h.route(2*time.Second))
	up.Set("/pkg/c2/-/c2-1.tgz", proxytest.Route{Body: []byte("c2"), Type: "application/octet-stream"})
	rem := e.remote("hosted-pre", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 2 })
	r := e.newReplica("a", replicaOpts{})
	mh := r.mirrorHost(nil)
	done := make(chan error, 1)
	go func() {
		_, err := mh.Fill(context.Background(), rem.Repo, mirror.Pass{JobID: "j", Ceiling: 1}, discover.Item{Path: "pkg/bg/-/bg-1.tgz"})
		done <- err
	}()
	waitFor(t, "the fill to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/bg/-/bg-1.tgz") == 1 })
	go r.artifact(rem, "pkg/c1/-/c1-1.tgz")
	waitFor(t, "the first client fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/c1/-/c1-1.tgz") == 1 })
	if s := r.artifact(rem, "pkg/c2/-/c2-1.tgz"); s.status != 200 {
		t.Fatalf("%d", s.status)
	}
	select {
	case err := <-done:
		if !errors.Is(err, mirror.ErrPreempted) {
			t.Fatalf("%v", err)
		}
		if _, ok := discover.AsRateLimited(err); ok {
			t.Fatal("a pre-emption is no rate limit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fill was not pre-empted")
	}
}

func TestR_REPO_023_TheNamesAPassMayStoreAreThoseThePatternsTheClaimsAndTheFormatAdmit(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	raw, err := json.Marshal(map[string]any{
		"remote": remoteSettings(up.URL), "includePatterns": []string{"acme-*", "claimed"}, "excludePatterns": []string{"acme-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.exec("INSERT INTO repositories (key, format, class, config) VALUES ('hosted-names', 'proxytest', 'remote', $1::jsonb)", string(raw))
	rem := e.lookup("hosted-names")
	r := e.newReplica("a", replicaOpts{})
	mh := r.mirrorHost(claims{"claimed": true})
	admits, err := mh.Admits(context.Background(), rem.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"acme-ui":     true,  // admitted by the include pattern
		"acme-secret": false, // the exclude pattern (R-REPO-005)
		"other":       false, // no include pattern matches
		"claimed":     false, // a global claim refuses it at the remote (R-REPO-023, R-REPO-025 step 2)
		"acme-a b":    false, // the format has no document for the name
		"":            false,
	} {
		if got := admits(name); got != want {
			t.Errorf("Admits(%q) = %v, want %v", name, got, want)
		}
	}
}
