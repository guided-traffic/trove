package mirror

import (
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// R-REPO-053 step 3, Q-REPO-252: a package with one permanently failed item and one that failed transiently keeps its retries
// (the transient item may succeed later), and the recorded reason is the transient failure.
func TestR_REPO_053_ATransientFailureNextToAPermanentOneKeepsThePackagesRetries(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "3.0.0", "2.0.0", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		switch {
		case strings.Contains(item.Path, "3.0.0"):
			return FillResult{}, errors.New("the upstream answered 500")
		case strings.Contains(item.Path, "2.0.0"):
			return FillResult{}, &PermanentError{Cause: errors.New("the fetched artifact does not match the announced digest")}
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "failed items, two per attempt", g.item(ResultFailed), float64(2*MaxPackageAttempts))
	mustEqual(t, "packagesFailed", g.lastPass()["packagesFailed"].(float64), 1)
	if got := g.store.lastError(rigRepoID, "a"); !strings.Contains(got, "500") {
		t.Errorf("the recorded reason is %q, want the transient failure", got)
	}
}
