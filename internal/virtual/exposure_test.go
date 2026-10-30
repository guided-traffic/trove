package virtual_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// Endpoints in the virtual engine (R-EP-018, R-EP-019): on an endpoint with exposure rules no
// answer names a member, and on an endpoint that does not fill caches the remote members the engine
// does not ask are counted as refused upstream contacts.

// memberTimeout is the failure the remote engine reports for a member: its detail names it.
func memberTimeout() error {
	return errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "The upstream of npm-remote did not answer in time.", errs.WithRetryAfter(3*time.Second))
}

func TestR_EP_018_AFailureNamesOnlyTheAddressedRepository(t *testing.T) {
	onRules := func(w *world, d virtual.Descriptor) (virtual.Caller, *virtual.Plan) {
		c, p := w.plan(alice, d)
		c.Rules = true
		return c, p
	}
	check := func(t *testing.T, err error) {
		t.Helper()
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.UpstreamTimeout || e.Code != errs.CodeUpstreamTimeout || e.RetryAfter != 3*time.Second {
			t.Fatalf("err = %v, want the member's kind, code and Retry-After", err)
		}
		if strings.Contains(e.Detail, "npm-remote") || !strings.Contains(e.Detail, "npm-all") {
			t.Fatalf("detail %q names a member, or not the addressed repository", e.Detail)
		}
	}
	t.Run("R-EP-018 an artifact whose only member failed", func(t *testing.T) {
		w := newWorld(t)
		w.arts.FetchErr["npm-remote"] = memberTimeout()
		c, p := onRules(w, artifactOf("left-pad", "1.0.0"))
		rec := httptest.NewRecorder()
		err := w.r.Artifact(context.Background(), c, p, w.arts, rec)
		check(t, err)
		if rec.Header().Get(virtual.ResolvedFromHeader) != "" {
			t.Fatal("Trove-Resolved-From on an endpoint with rules")
		}
	})
	t.Run("R-EP-018 a document whose only member failed", func(t *testing.T) {
		w := newWorld(t)
		w.remote.MetadataErr["npm-remote"] = memberTimeout()
		c, p := onRules(w, metadataOf("ghost"))
		_, err := w.r.Metadata(context.Background(), c, p, w.merger)
		check(t, err)
	})
	t.Run("R-EP-018 R-EP-013 a missing artifact is the one 404 of the engine", func(t *testing.T) {
		w := newWorld(t)
		c, p := onRules(w, artifactOf("left-pad", "9.9.9"))
		err := w.r.Artifact(context.Background(), c, p, w.arts, httptest.NewRecorder())
		if k, code := kind(err); k != errs.NotFound || code != errs.CodeNotFound {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-019 on an endpoint without rules the member's failure is the caller's to see", func(t *testing.T) {
		w := newWorld(t)
		w.arts.FetchErr["npm-remote"] = memberTimeout()
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if e, ok := errs.As(err); !ok || !strings.Contains(e.Detail, "npm-remote") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_EP_019_TheVirtualEngineCountsTheMembersItDoesNotAsk(t *testing.T) {
	var mu sync.Mutex
	refused := map[string]int{}
	count := func(endpoint string) { mu.Lock(); refused[endpoint]++; mu.Unlock() }
	t.Run("R-EP-019 an endpoint without cache_fill: no upstream contact, one refusal per remote member", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fillRefused = count })
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "x")
		sub := alice
		sub.Endpoint, sub.CacheFill = "customers", false
		if _, err := w.artifact(sub, "left-pad", "1.0.0"); err == nil {
			t.Fatal("served without a fill")
		}
		for _, c := range w.arts.Calls() {
			if strings.HasPrefix(c, "fetch:") {
				t.Fatalf("an upstream was asked: %v", w.arts.Calls())
			}
		}
		if refused["customers"] != 1 {
			t.Fatalf("refused %v", refused)
		}
	})
	t.Run("R-EP-019 the same request through an endpoint that fills reaches the upstream and counts nothing", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fill = "read"; o.fillRefused = count })
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "fetched")
		sub := alice
		sub.Endpoint, sub.CacheFill = "default", true
		before := refused["default"]
		// alice holds read on the remote, which is its cacheFillAction: she may fill.
		rec, err := w.artifact(sub, "left-pad", "1.0.0")
		if err != nil || rec.Body.String() != "fetched" || refused["default"] != before {
			t.Fatalf("body %q err %v refused %v", rec.Body.String(), err, refused)
		}
	})
	t.Run("R-AUTHZ-008 a caller without the populate right is not an endpoint refusal", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fill = "deploy"; o.fillRefused = count })
		sub := alice // read only: no deploy on the remote
		sub.Endpoint, sub.CacheFill = "default", true
		before := refused["default"]
		if _, err := w.artifact(sub, "left-pad", "1.0.0"); err == nil {
			t.Fatal("served without a fill")
		}
		if refused["default"] != before {
			t.Fatalf("counted a subject's refusal as the endpoint's: %v", refused)
		}
	})
}
