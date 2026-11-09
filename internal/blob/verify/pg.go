package verify

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// PGSource reads the blob and item rows from PostgreSQL. Every call is one short read on a pooled connection, so the run
// holds no transaction open over the slow existence checks.
type PGSource struct{ DB *postgres.DB }

// Page implements [Source].
func (s PGSource) Page(ctx context.Context, after blob.Digest, first bool, since time.Time, limit int) ([]Row, error) {
	arg := q.VerifyBlobPageParams{After: []byte{}, PageSize: clamp32(limit)}
	if !first {
		arg.After = after.Bytes()
	}
	if !since.IsZero() {
		arg.SinceUnix = float64(since.UnixNano()) / 1e9
	}
	var rows []Row
	err := s.DB.Read(ctx, func(c q.DBTX) error {
		rows = rows[:0]
		got, err := q.New(c).VerifyBlobPage(ctx, arg)
		if err != nil {
			return err
		}
		for _, r := range got {
			if len(r.Sha256) != len(blob.Digest{}) {
				return fmt.Errorf("a blob row has a digest of %d bytes", len(r.Sha256))
			}
			rows = append(rows, Row{Digest: blob.Digest(r.Sha256), Size: r.Size})
		}
		return nil
	})
	return rows, err
}

// Items implements [Source].
func (s PGSource) Items(ctx context.Context, d blob.Digest, limit int) ([]Item, int64, error) {
	var items []Item
	var n int64
	err := s.DB.Read(ctx, func(c q.DBTX) error {
		qs := q.New(c)
		got, err := qs.VerifyBlobItems(ctx, q.VerifyBlobItemsParams{Sha256: d.Bytes(), MaxItems: clamp32(limit)})
		if err != nil {
			return err
		}
		items = items[:0]
		for _, r := range got {
			items = append(items, Item{Project: r.ProjectKey, Repository: r.RepoKey, Path: r.Path, Trashed: r.Trashed})
		}
		n, err = qs.VerifyBlobItemCount(ctx, d.Bytes())
		return err
	})
	return items, n, err
}

// clamp32 is n as the int32 of a query parameter, at most math.MaxInt32.
func clamp32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}
