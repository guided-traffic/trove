package deb

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// ResealResult counts the sealed private keys a run of [ResealSigningKeys] walked.
type ResealResult struct {
	// Resealed were opened and sealed under the active key, Current were under it already, Failed could not be opened with the
	// ring of this replica and were left as they are.
	Resealed, Current, Failed int64
}

var errForeignTx = errors.New("deb: the transaction is not one of internal/meta/postgres")

// ResealSigningKeys seals the private keys of deb_signing_keys under the active key of the ring (R-ARCH-021 rotation step 3): the walk
// of the task secrets-reencrypt over the column deb_signing_keys.private_key. It is idempotent and works in batches of one transaction
// each, so a key is never unreadable for a moment, and it locks the rows of a batch so that a concurrent promotion or retirement waits.
// The row key of each value is the key's id, the AAD that binds a sealed key to its row.
func ResealSigningKeys(ctx context.Context, run meta.Runner, ring *secrets.KeyRing, batch int) (ResealResult, error) {
	if batch <= 0 {
		batch = 200
	}
	var res ResealResult
	after := ""
	for {
		var n int
		cursor, step := after, ResealResult{}
		// InTx runs the closure again after a serialization failure: the cursor and the counts of an attempt are local to it.
		err := run.InTx(ctx, func(tx meta.Tx) error {
			cursor, step = after, ResealResult{}
			ptx, ok := postgres.Unwrap(tx)
			if !ok {
				return errForeignTx
			}
			qs := q.New(ptx)
			list, err := qs.ListSealedDebKeysAfter(ctx, q.ListSealedDebKeysAfterParams{AfterID: after, Batch: int64(batch)})
			if err != nil {
				return err
			}
			n = len(list)
			for _, r := range list {
				cursor = r.ID
				sealed, changed, err := ring.Reseal(SigningKeys, secrets.Row(r.ID), r.PrivateKey)
				switch {
				case err != nil:
					step.Failed++
				case !changed:
					step.Current++
				default:
					if err := qs.UpdateSealedDebKey(ctx, q.UpdateSealedDebKeyParams{ID: r.ID, Sealed: sealed}); err != nil {
						return err
					}
					step.Resealed++
				}
			}
			return nil
		})
		if err != nil {
			return res, err
		}
		after = cursor
		res.Resealed += step.Resealed
		res.Current += step.Current
		res.Failed += step.Failed
		if n < batch {
			return res, nil
		}
	}
}
