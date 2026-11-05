//go:build integration

package mirror

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// The back-off of a pass that fails (R-REPO-053 errors) against the stub that is down, through the real discovery and the real
// status. The engine's own suite has the same sequence with a fake lister; what the stack adds is the real failure of the
// replication request and what the status shows of it.
func TestR_REPO_053_AnUpstreamThatFailsEveryDiscoveryRequestBacksOffAndTheNextPassAfterItsRecoveryCompletes(t *testing.T) {
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}})
	r.publishMany("@acme", 3)
	r.setStub(npmstub.Mode{Down: npmstub.DownUnavailable})
	run, web := r.runner("runner"), r.web("web")

	for i, base := range []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second} {
		r.pass(run)
		failures := int64(i + 1)
		deadline := time.Now().Add(passWithin)
		for r.number(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, r.repoN) != failures {
			if time.Now().After(deadline) {
				t.Fatalf("failure %d did not happen\n%s", failures, r.diag())
			}
			time.Sleep(100 * time.Millisecond)
		}
		// The write of the count and of next_pass_at is one statement; read it once it is there.
		ahead := time.Duration(r.number(`SELECT ROUND(EXTRACT(EPOCH FROM (next_pass_at - now())))::bigint FROM mirror_state WHERE repo_id = $1`, r.repoN)) * time.Second
		t.Run("R-REPO-053 failure "+string(rune('0'+failures))+" sets next_pass_at about "+base.String()+" ahead, plus or minus 20 percent", func(t *testing.T) {
			within(t, "next_pass_at", ahead, base*8/10-3*time.Second, base*12/10+time.Second)
		})
	}

	t.Run("R-REPO-056 after three failures the status is failing with a message that names hosts and no URL", func(t *testing.T) {
		a := r.api(web, http.MethodGet, "/api/v1/repositories/"+repoKey+"/mirror", nil)
		doc := a.JSON(t)
		errs, _ := doc["errors"].(map[string]any)
		msg, _ := errs["lastMessage"].(string)
		if doc["state"] != "failing" || errs["consecutiveFailures"] != float64(3) || errs["lastCode"] != "discovery_failed" {
			t.Errorf("status %s", a.Body)
		}
		if msg == "" || strings.Contains(msg, "://") || !strings.Contains(msg, "replicate.npmjs.com") {
			t.Errorf("errors.lastMessage %q: want a message that names the host and carries no URL", msg)
		}
	})

	t.Run("R-REPO-053 after the stub recovers the next pass completes and the count is 0", func(t *testing.T) {
		r.setStub(npmstub.Mode{})
		at := r.pass(run)
		r.waitPass(at, "full", "complete", passWithin)
		if n := r.number(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, r.repoN); n != 0 {
			t.Errorf("consecutive_failures = %d", n)
		}
		if n := r.storedItems(); n != 3 {
			t.Errorf("%d tarballs stored", n)
		}
	})
}
