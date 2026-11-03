//go:build integration

package proxy

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy"
)

// TestR_BAS_013_UpstreamNotFoundIsMarkedAbsent: the remote engine tells the marking of a Bastion request which 404
// says "missing for every subject" (an upstream 404 and the negative entry that stands for it) and which says
// less (an item the cache view does not hold, a refused fill): only the first is `absent`, so a negative entry
// of a Bastion never stands for an item that is merely hidden or not cached.
func TestR_BAS_013_UpstreamNotFoundIsMarkedAbsent(t *testing.T) {
	const doc = "pkg/does-not-exist-4711"
	const art = "pkg/x/-/x-4711.tgz"

	t.Run("an upstream 404 for a document, and then its negative entry", func(t *testing.T) {
		e := newEnv(t)
		rem := e.remote("abs-doc", e.upstream())
		a := e.newReplica("a", replicaOpts{})
		first := a.metadata(rem, doc)
		if first.status != 404 {
			t.Fatalf("first answer %d", first.status)
		}
		if first.err != nil && !errs.IsAbsent(first.err) {
			t.Fatalf("an upstream 404 is not marked absent: %v", first.err)
		}
		// The second request is the negative entry: a document with the negative status, which the host reads
		// as the same statement (formathost.remote.document).
		rq := a.request(rem, doc, proxy.Metadata)
		d, err := a.engine.Metadata(t.Context(), rq)
		switch {
		case err != nil && !errs.IsAbsent(err):
			t.Fatalf("a negative hit is not marked absent: %v", err)
		case err == nil && !d.Status.Negative:
			t.Fatalf("second answer is no negative entry: %+v", d.Status)
		}
	})
	t.Run("an upstream 404 for an artifact, and then its negative entry", func(t *testing.T) {
		e := newEnv(t)
		rem := e.remote("abs-art", e.upstream())
		a := e.newReplica("a", replicaOpts{})
		for i := range 2 {
			s := a.artifact(rem, art)
			if s.status != 404 || !errs.IsAbsent(s.err) {
				t.Fatalf("request %d: %d, absent %v (%v)", i, s.status, errs.IsAbsent(s.err), s.err)
			}
		}
	})
	t.Run("an artifact the cache view does not hold is a 404 that says nothing", func(t *testing.T) {
		e := newEnv(t)
		rem := e.remote("abs-view", e.upstream())
		a := e.newReplica("a", replicaOpts{})
		s := a.artifact(rem, art, func(rq *proxy.Request) { rq.Mode = proxy.CacheView })
		if s.status != 404 || errs.IsAbsent(s.err) {
			t.Fatalf("%d, absent %v (%v)", s.status, errs.IsAbsent(s.err), s.err)
		}
	})
}
