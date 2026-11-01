//go:build integration

package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/packages"
)

// queryFormat is a format with one Volatile route, whose remote adapter forwards the query to
// <upstream>/<path>?<params> and accepts only a 200.
type queryFormat struct{ formats.Base }

type queryAdapter struct {
	formats.RemoteAdapter
}

func (queryAdapter) BeforeArtifact(ctx context.Context, h formats.Host, rq *formats.Request, p formats.Path) (formats.Digests, error) {
	return formats.RemoteBase{}.BeforeArtifact(ctx, h, rq, p)
}

func (queryAdapter) UpstreamQuery(ctx context.Context, rc formats.RemoteContext, q formats.Query) (*http.Request, error) {
	u := rc.Upstream.JoinPath(q.Path.Path)
	u.RawQuery = q.Params.Encode()
	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

func (queryAdapter) AcceptResponse(_ formats.Path, resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "not a 200")
	}
	return nil
}

func (queryFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "proxytest", Classes: []formats.Class{formats.Local, formats.Remote}, Layout: "proxytest",
			PatternSubject: formats.SubjectName, PackageNames: true,
		},
		ContractVersion: formats.ContractVersion, MaxDocumentBytes: 1 << 20,
	}
}

func (queryFormat) Remote() formats.RemoteAdapter { return queryAdapter{} }
func (queryFormat) Routes() []formats.Route       { return nil }
func (queryFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (queryFormat) PackageDocumentPath(name string) (formats.Path, bool) {
	return formats.Path{Path: "pkg/" + name}, name != ""
}

func (queryFormat) Classify(formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.PathVolatile}
}

// hostDecider answers the populate check of the Host.
type hostDecider struct {
	authz.Decider
	allow atomic.Bool
	got   atomic.Value
}

func (d *hostDecider) MayPopulateOn(_ context.Context, _ *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	d.got.Store(c)
	if d.allow.Load() {
		return authz.Decision{Outcome: authz.Allow}
	}
	return authz.Decision{Outcome: authz.DenyVisible}
}

// TestR_ARCH_013_QueryThroughTheHost drives the format-facing fetcher over the real repository
// snapshot and the real engine: the adapter builds the request, the engine sends and caches it.
func TestR_ARCH_013_QueryThroughTheHost(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{"totalHits":7}`, 0)
	rem := e.remote("q-host", up)
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
	pk, err := packages.NewStore(r.db)
	if err != nil {
		t.Fatal(err)
	}
	dec := &hostDecider{}
	dec.allow.Store(true)
	host, err := formathost.New(formathost.Options{
		Runner: r.db, Decider: dec, Items: r.items, Packages: pk, Engine: r.engine,
		Deps: formathost.Deps{Formats: func(id string) (formats.Format, bool) { return queryFormat{}, id == "proxytest" }},
	})
	if err != nil {
		t.Fatal(err)
	}
	rq := &formats.Request{
		Request: httptest.NewRequest(http.MethodGet, "/api/proxytest/q-host/search?q=acme", nil),
		Repo:    &rem.Repo,
		Route:   &formats.Route{Name: "proxytest.search", Kind: formats.Volatile},
		Subject: authz.Subject{Name: "alice", Endpoint: "customers", CacheFill: true},
	}
	q := formats.Query{Path: formats.Path{Path: "search"}, Params: url.Values{"q": {"acme"}}, TTL: time.Minute}

	got, err := host.Remote().Query(t.Context(), rq, q)
	if err != nil || string(got.Body) != `{"totalHits":7}` || got.ContentType != "application/json" || got.Cached {
		t.Fatalf("first: %+v %v", got, err)
	}
	if s.queryCount("q=acme") != 1 {
		t.Fatalf("the adapter's request did not reach the upstream: %v", s.queries)
	}
	if c, ok := dec.got.Load().(authz.Coordinate); !ok || c.Kind != authz.KindSearch || c.Repository != "q-host" || c.Path != "search" {
		t.Fatalf("the populate check was asked for %+v", dec.got.Load())
	}
	if got, err = host.Remote().Query(t.Context(), rq, q); err != nil || !got.Cached || s.count() != 1 {
		t.Fatalf("second: %+v %v after %d requests", got, err, s.count())
	}

	t.Run("a caller who may not fill is served a copy and gets ErrOffline for a miss", func(t *testing.T) {
		dec.allow.Store(false)
		if got, err := host.Remote().Query(t.Context(), rq, q); err != nil || !got.Cached {
			t.Fatalf("copy: %+v %v", got, err)
		}
		miss := formats.Query{Path: formats.Path{Path: "search"}, Params: url.Values{"q": {"other"}}, TTL: time.Minute}
		if _, err := host.Remote().Query(t.Context(), rq, miss); !errors.Is(err, formats.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
		if s.count() != 1 {
			t.Fatalf("the upstream saw %d requests", s.count())
		}
	})
	t.Run("the cache view never contacts the upstream", func(t *testing.T) {
		dec.allow.Store(true)
		view := *rq
		view.CacheView = true
		miss := formats.Query{Path: formats.Path{Path: "search"}, Params: url.Values{"q": {"cold"}}, TTL: time.Minute}
		if _, err := host.Remote().Query(t.Context(), &view, miss); !errors.Is(err, formats.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the artifact gate is a method of the fetcher; a format without a gate allows everything", func(t *testing.T) {
		if err := host.Remote().Gate(t.Context(), rq, formats.Path{Path: "pkg/x/-/x-1.tgz"}); err != nil {
			t.Fatal(err)
		}
	})
}
