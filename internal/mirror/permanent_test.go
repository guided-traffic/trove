package mirror

import (
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// R-REPO-041, R-REPO-053 step 3: a fill that came back wrong and would come back wrong again fails its package at once, once, and
// the pass ends; the other items of the package are stored, and a transient failure still gets MaxPackageAttempts.
func TestR_REPO_053_APermanentlyFailedFillFailsThePackageAtOnceAndThePassEnds(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "3.0.0", "2.0.0", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.Contains(item.Path, "a/-/3.0.0") {
			return FillResult{}, &PermanentError{Cause: errors.New("the fetched artifact does not match the announced digest")}
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", res.get("outcome"), "partial")
	mustContain(t, "the items after the failing one and the other package", g.host.stored(), "a/-/2.0.0.tgz", "a/-/1.0.0.tgz", "b/-/1.0.0.tgz")
	mustEqual(t, "failed items, one attempt", g.item(ResultFailed), 1)
	mustEqual(t, "packagesFailed", g.lastPass()["packagesFailed"].(float64), 1)
}
