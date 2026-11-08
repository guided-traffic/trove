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

// TestR_BAS_015_R_BAS_026_ARestoreMakesABastionBeforeItStartAgain: a restore re-applies the narrowings made after its restore
// point with a journal replay, which writes no audit event, so the feed carries none of them. A Bastion whose cursor lies
// before the restore (every event it names is older than restored_at) is answered reset once, drops its answers, and then
// settles on a cursor after the restore instead of being reset on every poll (R-REPO-061).
func TestR_BAS_015_R_BAS_026_ARestoreMakesABastionBeforeItStartAgain(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	c := callOpts{token: w.full.token, addr: "198.51.100.171"}
	warm(t, b, c, path)

	// An event the Bastion's cursor names, then the restore.
	other := w.newReader(npmAll, npmLocal, npmRemote)
	before := sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total")
	w.mustAPI("DELETE", "/api/v1/tokens/"+other.id, 204, nil)
	testWait(t, "the Bastion applies the entry", func() bool {
		return sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total") > before
	})
	if sumMetric(r.metrics(t), "trove_bastion_feed_resets_total") != 0 {
		t.Fatal("a reset was counted before there was one")
	}
	w.st.Exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb(now()))
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`)
	testWait(t, "the reset", func() bool { return sumMetric(r.metrics(t), "trove_bastion_feed_resets_total") >= 1 })

	// It settles: no further reset while nothing happens.
	n := sumMetric(r.metrics(t), "trove_bastion_feed_resets_total")
	time.Sleep(3 * time.Second)
	if got := sumMetric(r.metrics(t), "trove_bastion_feed_resets_total"); got != n {
		t.Fatalf("the Bastion was reset %v times after the restore, want once", got)
	}
	m := w.front.Mark()
	if got := r.do(t, "GET", path, c); got.Status != http.StatusOK {
		t.Fatalf("after the reset: %d", got.Status)
	}
	if len(w.front.Kind(m, "check"))+len(w.front.Forwards(m, "/api/")) == 0 {
		t.Fatal("the answers survived the restore: the main installation was not asked")
	}
}

// TestR_BAS_015_AFirstPublishIntoAnAutoClaimRepositoryEndsTheCachedAnswersOfItsVirtualRepositories: the auto claim of a first
// publish is journaled as repository.claims_changed and read by the feed as a content entry (Q-BAS-146). A Bastion that has
// the virtual repository's answer cached applies the entry and asks the main installation again, where the claim now decides
// where the name resolves from.
func TestR_BAS_015_AFirstPublishIntoAnAutoClaimRepositoryEndsTheCachedAnswersOfItsVirtualRepositories(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{}).wait()
	r := b.reps[0]
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	c := callOpts{token: w.full.token, addr: "198.51.100.181"}
	warm(t, b, c, path)
	m := w.front.Mark()
	r.do(t, "GET", path, c)
	if n := w.front.Cost(m); n != 0 {
		t.Fatalf("the warm cache costs %d requests", n)
	}

	before := sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total")
	w.publishNPMKeepingEvents("fresh-lib", "1.0.0") // the auto claim of a first publish, with its event
	testWait(t, "the Bastion applies the content entry", func() bool {
		return sumMetric(r.metrics(t), "trove_bastion_feed_entries_applied_total") > before
	})
	m = w.front.Mark()
	if got := r.do(t, "GET", path, c); got.Status != http.StatusOK {
		t.Fatalf("after the entry: %d", got.Status)
	}
	if n := len(w.front.Kind(m, "check")) + len(w.front.Forwards(m, "/api/")); n == 0 {
		t.Fatal("the cached answer survived the claim: the main installation was not asked")
	}
}
