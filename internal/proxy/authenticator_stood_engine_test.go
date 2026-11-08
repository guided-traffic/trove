package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// allowPopulate lets a query contact the upstream.
type allowPopulate struct{}

func (allowPopulate) May(context.Context) bool              { return true }
func (allowPopulate) Deny(context.Context) error            { return nil }
func (allowPopulate) Record(context.Context, meta.Tx) error { return nil }
func (allowPopulate) Endpoint() (string, bool)              { return "default", true }

// query sends a pass-through query for path with the handshake a.
func (w *hsWorld) query(path string, a Authenticator) (*QueryResponse, error) {
	w.t.Helper()
	return w.e.Query(context.Background(), QueryRequest{
		Remote: w.rem, Path: "x" + path, TTL: time.Minute, Populate: allowPopulate{}, Auth: a,
		Build: func(ctx context.Context, b UpstreamBase) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, w.registry.URL+path, nil)
		},
	})
}

// A pass-through query whose fresh token the registry refuses with error="insufficient_scope" is the upstream's "no such resource" when
// the format reads it so, and the remembered challenge stays: the challenge was not at fault (Q-OCI-127).
func TestR_OCI_021_AQueryThatTheRegistryDeniesToAValidTokenIsMissingAndKeepsTheChallenge(t *testing.T) {
	w := newHSWorld(t)
	a := &stoodAuth{hsAuth{ttl: 5 * time.Minute}}
	mustGet(t, w, "/v2/a/manifests/1", a) // the first request learns the challenge
	if w.e.challenges.Len() != 1 {
		t.Fatalf("%d challenges remembered", w.e.challenges.Len())
	}
	w.mu.Lock()
	w.denyAl, w.scopeErr = true, true
	w.mu.Unlock()
	_, err := w.query("/v2/b/tags/list", a)
	if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
		t.Fatalf("err = %v, want the upstream's not found", err)
	}
	if w.e.challenges.Len() != 1 {
		t.Error("the remembered challenge was forgotten although the token service was not at fault")
	}
	// A format that does not read it is left with the 401, and the challenge is forgotten as before.
	plain := &hsAuth{ttl: 5 * time.Minute}
	if _, err := w.query("/v2/c/tags/list", plain); err == nil {
		t.Error("a 401 that no format read was answered")
	} else if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
		t.Errorf("err = %v: read as missing by a format that does not", err)
	}
	if w.e.challenges.Len() != 0 {
		t.Error("the challenge stayed remembered after a refused token of a format that reads no such 401")
	}
}
