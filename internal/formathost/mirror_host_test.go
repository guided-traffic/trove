package formathost

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

func TestMapBackground(t *testing.T) {
	t.Run("R-REPO-055 a pre-emption wraps mirror.ErrPreempted and keeps the engine's error", func(t *testing.T) {
		err := mapBackground(proxy.ErrPreempted)
		if !errors.Is(err, mirror.ErrPreempted) || !errors.Is(err, proxy.ErrPreempted) {
			t.Fatalf("%v", err)
		}
		if _, ok := discover.AsRateLimited(err); ok {
			t.Fatal("a pre-emption is no rate limit")
		}
	})
	t.Run("R-REPO-054 R-REPO-052 an open circuit and a refused spool admission are a *mirror.WaitError with the engine's wait", func(t *testing.T) {
		for reason, after := range map[string]time.Duration{proxy.WaitCircuitOpen: 3 * time.Second, proxy.WaitSpoolBusy: 5 * time.Second} {
			var w *mirror.WaitError
			err := mapBackground(&proxy.BackgroundWait{Reason: reason, After: after})
			if !errors.As(err, &w) || w.Reason != reason || w.After != after {
				t.Fatalf("%s: %v", reason, err)
			}
		}
		if mirror.WaitCircuitOpen != proxy.WaitCircuitOpen || mirror.WaitSpoolBusy != proxy.WaitSpoolBusy {
			t.Fatal("the reasons of the engine and of the seam differ")
		}
	})
	t.Run("R-REPO-054 a rate-limit answer is left as it is, and any other error is a failure of the item", func(t *testing.T) {
		rl := &upstream.RateLimitError{RateLimit: upstream.RateLimit{Status: 429, Reason: upstream.ReasonTooManyRequests, Wait: time.Second, Hinted: true}, Repo: "r"}
		if got := mapBackground(rl); !errors.Is(got, rl) {
			t.Fatalf("%v", got)
		}
		if _, ok := discover.AsRateLimited(mapBackground(rl)); !ok {
			t.Fatal("not a rate limit")
		}
		failure := errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "x")
		got := mapBackground(failure)
		var w *mirror.WaitError
		if !errors.Is(got, failure) || errors.Is(got, mirror.ErrPreempted) || errors.As(got, &w) {
			t.Fatalf("%v", got)
		}
		if mapBackground(nil) != nil {
			t.Fatal("nil")
		}
	})
}

type recordingDecider struct {
	authz.Decider
	sub authz.Subject
	c   authz.Coordinate
	d   authz.Decision
}

func (r *recordingDecider) RecordAllowed(_ context.Context, _ meta.Tx, sub authz.Subject, c authz.Coordinate, d authz.Decision) error {
	r.sub, r.c, r.d = sub, c, d
	return nil
}

func TestMirrorPopulate(t *testing.T) {
	d := &recordingDecider{}
	h := newTestHost(t, d)
	p := &mirrorPopulate{h: h, coord: authz.Coordinate{Repository: "npm-mirror", Kind: authz.KindWrite, Package: "left-pad"}}
	t.Run("R-REPO-057 the check asks nobody and admits the pass", func(t *testing.T) {
		if !p.May(t.Context()) || p.Deny(t.Context()) != nil {
			t.Fatal("the mirror's populate check refused")
		}
		if name, fills := p.Endpoint(); name != "" || !fills {
			t.Fatalf("%q %v", name, fills)
		}
	})
	t.Run("R-REPO-057 Q-REPO-145 the event of the fill names the system as subject and mirror as reason", func(t *testing.T) {
		if err := p.Record(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if d.sub.Kind != "system" || d.sub.Name != "mirror" || d.d.Reason != "mirror" || d.d.Outcome != authz.Allow || d.c.Package != "left-pad" {
			t.Fatalf("%+v %+v %+v", d.sub, d.d, d.c)
		}
		if a := authz.MirrorActor(); a.Kind != "system" || a.Name != "mirror" {
			t.Fatalf("%+v", a)
		}
	})
}

// getEngine answers Get and records what the host asked.
type getEngine struct {
	MirrorEngine
	got proxy.Get
	res *proxy.GetResult
	err error
}

func (g *getEngine) Get(_ context.Context, in proxy.Get) (*proxy.GetResult, error) {
	g.got = in
	return g.res, g.err
}

func TestPassUpstreamGet(t *testing.T) {
	base, _ := url.Parse("https://registry.example.org/")
	eng := &getEngine{res: &proxy.GetResult{Status: 404, Header: http.Header{"X-A": {"b"}}, Body: []byte("nope")}}
	u := &passUpstream{
		m: &MirrorHost{e: eng}, base: base, hosts: []string{"replicate.example.org"}, pass: mirror.Pass{JobID: "job-3", Ceiling: 2},
		settings: []byte(`{"k":1}`),
	}
	target, _ := url.Parse("https://replicate.example.org/registry/_all_docs")
	t.Run("R-REPO-051 the request carries the discovery hosts, the pass's job and ceiling; another status is a response", func(t *testing.T) {
		got, err := u.Get(t.Context(), target, "application/json", 1024)
		if err != nil || got.Status != 404 || string(got.Body) != "nope" || got.Header.Get("X-A") != "b" {
			t.Fatalf("%+v %v", got, err)
		}
		g := eng.got
		if g.URL != target || g.Accept != "application/json" || g.Max != 1024 || g.JobID != "job-3" || g.Ceiling != 2 || len(g.MirrorHosts) != 1 || g.MirrorHosts[0] != "replicate.example.org" {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("R-ARCH-009 the base and the settings are the repository's", func(t *testing.T) {
		if u.Base().String() != base.String() || string(u.Settings()) != `{"k":1}` {
			t.Fatal("base or settings")
		}
		u.Base().Host = "changed" // a copy
		if u.Base().Host != "registry.example.org" {
			t.Fatal("Base returned the shared URL")
		}
	})
	t.Run("R-REPO-054 R-REPO-055 a rate limit, a pre-emption and a wait keep their shapes", func(t *testing.T) {
		eng.res, eng.err = nil, &upstream.RateLimitError{RateLimit: upstream.RateLimit{Status: 503, Reason: upstream.ReasonSlowDown}, Repo: "r"}
		if _, err := u.Get(t.Context(), target, "", 0); err == nil {
			t.Fatal("no error")
		} else if rl, ok := discover.AsRateLimited(err); !ok || rl.RateLimitReason() != upstream.ReasonSlowDown {
			t.Fatalf("%v", err)
		}
		eng.err = proxy.ErrPreempted
		if _, err := u.Get(t.Context(), target, "", 0); !errors.Is(err, mirror.ErrPreempted) {
			t.Fatalf("%v", err)
		}
		eng.err = &proxy.BackgroundWait{Reason: proxy.WaitCircuitOpen, After: 2 * time.Second}
		var w *mirror.WaitError
		if _, err := u.Get(t.Context(), target, "", 0); !errors.As(err, &w) || w.After != 2*time.Second {
			t.Fatalf("%v", err)
		}
		eng.err = errs.E(errs.UpstreamFailed, errs.CodeForeignHostDenied, "x")
		if _, err := u.Get(t.Context(), target, "", 0); errs.KindOf(err) != errs.UpstreamFailed {
			t.Fatalf("%v", err)
		}
	})
}

func TestNewMirrorHost(t *testing.T) {
	t.Run("R-ARCH-009 the host needs the format host and the remote engine", func(t *testing.T) {
		if _, err := NewMirrorHost(nil, &getEngine{}); err == nil {
			t.Fatal("no format host accepted")
		}
		if _, err := NewMirrorHost(newTestHost(t, &recordingDecider{}), nil); err == nil {
			t.Fatal("no engine accepted")
		}
	})
	t.Run("R-ARCH-009 a format the registry does not know has no lister", func(t *testing.T) {
		m, err := NewMirrorHost(newTestHost(t, &recordingDecider{}), &getEngine{})
		if err != nil {
			t.Fatal(err)
		}
		if l, ok := m.Lister("no-such-format"); ok || l != nil {
			t.Fatal("a lister for an unknown format")
		}
	})
}
