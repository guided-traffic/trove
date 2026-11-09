package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/meta"
)

// failRunner fails the test when the pause touches the database.
type failRunner struct{ t *testing.T }

func (f failRunner) InTx(context.Context, func(tx meta.Tx) error) error {
	f.t.Error("the database was opened")
	return errors.New("unexpected transaction")
}

// TestR_HA_025_PauseDeletionsNeedsAReason: a pause without a reason is refused before any transaction, so a pause always says why
// it was set. The paths that change the state are tested against PostgreSQL in test/integration/restoreversions.
func TestR_HA_025_PauseDeletionsNeedsAReason(t *testing.T) {
	d := New(Options{DB: failRunner{t}})
	if _, err := d.Pause(context.Background(), ""); err == nil {
		t.Fatal("a pause without a reason was accepted")
	}
}
