package formathost

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// byIDReader serves one repository by its id.
type byIDReader struct {
	repository.Reader
	repo repository.Repository
}

func (r byIDReader) ByID(_ context.Context, id int64) (repository.Repository, error) {
	if id != 7 {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
	}
	return r.repo, nil
}

func backgroundTestRepo(t *testing.T, budget, concurrency string) repository.Repository {
	t.Helper()
	doc := `{"remote":{"url":"https://registry.example.org/","mode":"mirror","availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},` +
		`"limits":{"maxConcurrentFetches":` + budget + `},"mirror":{"concurrency":` + concurrency + `}}}`
	r, err := repository.Build(7, 1, qr("npm-remote"), repository.Remote, "npm", []byte(doc), nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func backgroundTestHost(t *testing.T, eng RemoteEngine, repos repository.Reader) *Host {
	t.Helper()
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Runner: nopRunner{}, Decider: &recordingDecider{}, Items: (*items.Store)(nil), Packages: pk, Engine: eng, Repos: repos})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestR_REPO_055_BackgroundUpstreamNeedsTheEngineAndTheRepositoryReader(t *testing.T) {
	ref := formats.RepoRef{ID: 7}
	repo := backgroundTestRepo(t, "16", "4")
	t.Run("a host without a remote engine answers not_implemented", func(t *testing.T) {
		_, err := backgroundTestHost(t, nil, byIDReader{repo: repo}).BackgroundUpstream(t.Context(), ref)
		if errs.KindOf(err) != errs.NotImplemented {
			t.Fatalf("%v", err)
		}
	})
	t.Run("an engine that cannot fill or get answers not_implemented", func(t *testing.T) {
		_, err := backgroundTestHost(t, plainEngine{}, byIDReader{repo: repo}).BackgroundUpstream(t.Context(), ref)
		if errs.KindOf(err) != errs.NotImplemented {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a host without a repository reader answers not_implemented", func(t *testing.T) {
		_, err := backgroundTestHost(t, &getEngine{}, nil).BackgroundUpstream(t.Context(), ref)
		if errs.KindOf(err) != errs.NotImplemented {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a repository that does not exist is the reader's not_found", func(t *testing.T) {
		_, err := backgroundTestHost(t, &getEngine{}, byIDReader{repo: repo}).BackgroundUpstream(t.Context(), formats.RepoRef{ID: 8})
		if errs.KindOf(err) != errs.NotFound {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a repository that is no remote is refused", func(t *testing.T) {
		local, err := repository.Build(7, 1, qr("npm-local"), repository.Local, "npm", []byte(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backgroundTestHost(t, &getEngine{}, byIDReader{repo: local}).BackgroundUpstream(t.Context(), ref); err == nil {
			t.Fatal("a local repository got a background upstream")
		}
	})
}

// plainEngine is a RemoteEngine that is not a MirrorEngine.
type plainEngine struct{ RemoteEngine }

func TestR_REPO_055_BackgroundUpstreamRequestsCarryTheShareAndTheJob(t *testing.T) {
	eng := &getEngine{res: &proxy.GetResult{Status: 200, Header: http.Header{}, Body: []byte("ok")}}
	// min(concurrency 4, max(1, 16/4)) = 4; min(8, max(1, 8/4)) = 2.
	for _, tc := range []struct {
		budget, concurrency string
		want                int
	}{{"16", "4", 4}, {"8", "8", 2}, {"2", "4", 1}} {
		h := backgroundTestHost(t, eng, byIDReader{repo: backgroundTestRepo(t, tc.budget, tc.concurrency)})
		u, err := h.BackgroundUpstream(WithJobID(t.Context(), "job-9"), formats.RepoRef{ID: 7})
		if err != nil {
			t.Fatal(err)
		}
		target, _ := url.Parse("https://registry.example.org/x")
		if _, err := u.Get(t.Context(), target, "", 0); err != nil {
			t.Fatal(err)
		}
		if eng.got.Ceiling != tc.want || eng.got.JobID != "job-9" {
			t.Fatalf("budget %s concurrency %s: ceiling %d job %q, want %d job-9", tc.budget, tc.concurrency, eng.got.Ceiling, eng.got.JobID, tc.want)
		}
	}
}

func TestR_REPO_054_BackgroundUpstreamRepeatsARequestTheUpstreamRateLimited(t *testing.T) {
	limit := &upstream.RateLimitError{RateLimit: upstream.RateLimit{Status: 429, Reason: upstream.ReasonTooManyRequests, Wait: 0, Hinted: false}, Repo: "r"}
	eng := &flakyEngine{getEngine: getEngine{res: &proxy.GetResult{Status: 200, Header: http.Header{}, Body: []byte("ok")}}, failFirst: limit}
	h := backgroundTestHost(t, eng, byIDReader{repo: backgroundTestRepo(t, "16", "4")})
	u, err := h.BackgroundUpstream(t.Context(), formats.RepoRef{ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := u.(discover.Bounded); !ok {
		t.Fatal("the background upstream tells its bounds as a pass's does")
	}
	target, _ := url.Parse("https://registry.example.org/x")
	res, err := u.Get(t.Context(), target, "", 0)
	if err != nil || string(res.Body) != "ok" || eng.calls != 2 {
		t.Fatalf("%v %v after %d calls", res, err, eng.calls)
	}
}

func TestR_REPO_054_BackgroundUpstreamEndsTheRunAfterALongWait(t *testing.T) {
	limit := &upstream.RateLimitError{RateLimit: upstream.RateLimit{Status: 429, Reason: upstream.ReasonRetryAfter, Wait: 3600e9, Hinted: true}, Repo: "r"}
	eng := &flakyEngine{getEngine: getEngine{res: &proxy.GetResult{Status: 200}}, failFirst: limit}
	h := backgroundTestHost(t, eng, byIDReader{repo: backgroundTestRepo(t, "16", "4")})
	u, err := h.BackgroundUpstream(t.Context(), formats.RepoRef{ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://registry.example.org/x")
	var d *mirror.DeferredError
	if _, err := u.Get(t.Context(), target, "", 0); !errors.As(err, &d) || eng.calls != 1 {
		t.Fatalf("%v after %d calls", err, eng.calls)
	}
}

// flakyEngine fails its first Get with failFirst.
type flakyEngine struct {
	getEngine
	failFirst error
	calls     int
}

func (f *flakyEngine) Get(ctx context.Context, in proxy.Get) (*proxy.GetResult, error) {
	f.calls++
	if f.calls == 1 {
		return nil, f.failFirst
	}
	return f.getEngine.Get(ctx, in)
}
