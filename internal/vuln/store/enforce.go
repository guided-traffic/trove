package store

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("vuln/store: the transaction is not one of internal/meta/postgres")

// Enforce reads what the refusal of a download needs beyond the item lookup (R-VULN-016).
type Enforce struct{ run meta.Runner }

// NewEnforce returns the reader on a transaction runner.
func NewEnforce(run meta.Runner) *Enforce { return &Enforce{run: run} }

// VersionNames returns the package name and the version of a stored version (package_versions.id), for the text of a
// refusal. It is read when a download is refused, never when one is served; found is false when the version is gone.
func (s *Enforce) VersionNames(ctx context.Context, versionID int64) (pkg, version string, found bool, err error) {
	err = s.run.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errForeignTx
		}
		rows, err := q.New(ptx).GetVersionNames(ctx, versionID)
		if err != nil || len(rows) == 0 {
			return err
		}
		pkg, version, found = rows[0].PackageName, rows[0].Version, true
		return nil
	})
	return pkg, version, found, err
}
