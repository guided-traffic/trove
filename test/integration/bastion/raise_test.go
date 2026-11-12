//go:build integration

package bastion

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_BAS_015_ARaisedLevelKeepsWhatABastionHolds: a caller who reads by a grant is served from the cache after a warm-up, and an
// administrator who opens the project and its repositories to everyone afterwards (the levels rise: authz.project.visibility_changed
// and authz.settings.changed with raised levels only) costs it nothing: the feed asks the Bastion for the handshake again and drops no
// answer, so the next request is a hit and sends nothing to the main installation (R-BAS-015, R-AUTHZ-036).
func TestR_BAS_015_ARaisedLevelKeepsWhatABastionHolds(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	c := callOpts{token: w.full.token, addr: "198.51.100.61"}
	tgz := tarballPath(npmAll, "plain-lib", "1.0.0")
	warm(t, b, c, tgz)

	m := w.front.Mark()
	if got := r.do(t, "GET", tgz, c); got.Status != http.StatusOK || len(w.front.Forwards(m, "/api/")) != 0 {
		t.Fatalf("before the raise: %d, %d requests to the main installation, want a hit", got.Status, len(w.front.Forwards(m, "/api/")))
	}

	applied := func() float64 { return sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total") }
	before := applied()
	w.anonymousPolicy() // the levels rise to public
	testenv.WaitFor(t, "the Bastion to apply the entries of the raise", 20*time.Second, func() bool { return applied() > before })
	if got := sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total", `kind="grants"`); got != 0 {
		t.Errorf("a raise applied %v grants entries: it dropped every answer a Bastion holds", got)
	}

	m = w.front.Mark()
	if got := r.do(t, "GET", tgz, c); got.Status != http.StatusOK {
		t.Fatalf("after the raise: %d", got.Status)
	}
	if n := len(w.front.Forwards(m, "/api/")); n != 0 {
		t.Errorf("after the raise the request went to the main installation (%d requests), want a hit", n)
	}
}
