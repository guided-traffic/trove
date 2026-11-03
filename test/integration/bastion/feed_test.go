//go:build integration

package bastion

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
)

// TestR_BAS_015_R_BAS_026_AFeedResetDropsEveryAnswer: the entry the Bastion's cursor names no longer exists at the
// main installation although its time lies within the audit retention (what a point-in-time restore leaves). The
// feed answers reset, the Bastion drops every answer and the next request of a caller is decided by the main
// installation again.
func TestR_BAS_015_R_BAS_026_AFeedResetDropsEveryAnswer(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	c := callOpts{token: w.full.token, addr: "198.51.100.161"}
	warm(t, b, c, path)
	m := w.front.Mark()
	r.do(t, "GET", path, c)
	if n := w.front.Cost(m); n != 0 {
		t.Fatalf("the warm cache costs %d requests", n)
	}

	// An entry the Bastion consumes, then the audit rows are gone.
	other := w.newReader(npmAll, npmLocal, npmRemote)
	before := sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total")
	w.mustAPI("DELETE", "/api/v1/tokens/"+other.id, 204, nil)
	testWait(t, "the Bastion applies the entry", func() bool {
		return sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total") > before
	})
	if sumMetric(r.metrics(t), "trove_bastion_feed_resets_total") != 0 {
		t.Fatal("a reset was counted before there was one")
	}
	w.st.Exec(`DELETE FROM audit_event`)
	testWait(t, "the reset", func() bool { return sumMetric(r.metrics(t), "trove_bastion_feed_resets_total") >= 1 })

	m = w.front.Mark()
	got := r.do(t, "GET", path, c)
	if got.Status != http.StatusOK {
		t.Fatalf("after the reset: %d", got.Status)
	}
	if n := len(w.front.Kind(m, "check")) + len(w.front.Forwards(m, "/api/")); n == 0 {
		t.Fatal("the answers survived the reset: the main installation was not asked")
	}
	if n := len(w.front.Artifacts(m)); n != 0 {
		t.Fatalf("the reset made the Bastion fetch %d artifacts; the bytes are not answers", n)
	}
	// An answer obtained within the skew margin of the reset is not usable (R-BAS-026: the history starts at the
	// reset); past it the replica settles and the next requests are hits again.
	b.clk.Advance(answers.SkewMargin + time.Second)
	r.do(t, "GET", path, c)
	m = w.front.Mark()
	r.do(t, "GET", path, c)
	if n := w.front.Cost(m); n != 0 {
		t.Fatalf("after the margin the request costs %d requests", n)
	}
}
