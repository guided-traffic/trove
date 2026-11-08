//go:build integration

package mirror

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// waitLatestPassJob waits until the latest mirror-pass job has the state. The pass writes its end (mirror_state.last_pass) in its
// own transaction and returns; the job framework then marks the job succeeded in a second write. The pass outlives a job by design
// (R-REPO-053, Q-REPO-135), so a reader that saw the outcome may still see the job running for a moment: the job's state is
// waited for with a bound, never read once.
func waitLatestPassJob(tb testing.TB, st *testenv.Stack, state string, within time.Duration) {
	tb.Helper()
	testenv.WaitFor(tb, "the latest mirror-pass job to be "+state, within, func() bool {
		n, err := st.TryNumber(`SELECT count(*) FROM (SELECT state::text AS s FROM job WHERE kind = 'mirror-pass' ORDER BY created_at DESC LIMIT 1) j WHERE j.s = $1`, state)
		return err == nil && n == 1
	})
}
