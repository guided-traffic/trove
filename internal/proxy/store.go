package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("proxy: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package writes
// no SQL: its statements live in internal/meta/postgres/queries/proxy.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// micro converts microseconds since the epoch; 0 is the zero time (an absent
// column).
func micro(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}

func unixMicro(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// entry is a row of remote_cache_entries as the engine reads it, with the size of
// its blob.
type entry struct {
	Kind         string
	ItemID       int64
	Digest       [32]byte
	Size         int64
	URL          string
	ETag         string
	LastModified time.Time
	FetchedAt    time.Time
	ValidatedAt  time.Time
	ExpiresAt    time.Time
	GoneAt       time.Time
	// TTL is the remaining lifetime of a metadata entry on the database clock;
	// zero or negative when it has expired.
	TTL time.Duration
}

// Gone reports a gone mark (R-REPO-029).
func (e entry) Gone() bool { return !e.GoneAt.IsZero() }

// getEntry reads one entry; found is false when there is none.
func (e *Engine) getEntry(ctx context.Context, repoID int64, path, variant string) (en entry, found bool, err error) {
	err = e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ProxyEntryGet(ctx, q.ProxyEntryGetParams{RepoID: repoID, Path: path, Variant: variant})
		if err != nil || len(rows) == 0 {
			found = false
			return err
		}
		r := rows[0]
		found = true
		en = entry{
			Kind: r.Kind, ItemID: r.ItemID, Size: r.BlobSize, URL: r.UpstreamUrl, ETag: r.UpstreamEtag,
			LastModified: micro(r.LastModifiedUs), FetchedAt: micro(r.FetchedUs), ValidatedAt: micro(r.ValidatedUs),
			ExpiresAt: micro(r.ExpiresUs), GoneAt: micro(r.GoneUs), TTL: time.Duration(r.TtlUs) * time.Microsecond,
		}
		copy(en.Digest[:], r.BlobSha256)
		return nil
	})
	return en, found, err
}

// entryState reads what the entries of a path say, any variant.
func (e *Engine) entryState(ctx context.Context, repoID int64, path string) (st EntryState, err error) {
	err = e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		r, err := qs.ProxyEntryState(ctx, q.ProxyEntryStateParams{RepoID: repoID, Path: path})
		if err != nil {
			return err
		}
		st = EntryState{Stored: r.Total > 0}
		if r.Total > 0 && r.Gone == r.Total {
			st.GoneAt = micro(r.GoneUs)
		}
		return nil
	})
	return st, err
}

// upstreamURL reads the recorded upstream URL of a path, "" when the format's
// default applies (R-REPO-043).
func (e *Engine) upstreamURL(ctx context.Context, repoID int64, path string) (u string, err error) {
	err = e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ProxyUrlMapGet(ctx, q.ProxyUrlMapGetParams{RepoID: repoID, Path: path})
		if err != nil || len(rows) == 0 {
			u = ""
			return err
		}
		u = rows[0]
		return nil
	})
	return u, err
}

// dbNow reads the database clock in microseconds.
func (e *Engine) dbNow(ctx context.Context) (us int64, err error) {
	err = e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		us, err = qs.ProxyClock(ctx)
		return err
	})
	return us, err
}

// prefixEnd returns the smallest string above every string that starts with prefix,
// "" when there is none (a prefix of 0xFF bytes): the end of the key range.
func prefixEnd(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return ""
}

// cachedArtifacts lists the cached artifact paths below a prefix (a package's
// folder), at most limit.
func (e *Engine) cachedArtifacts(ctx context.Context, repoID int64, prefix string, limit int) (paths []string, err error) {
	err = e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		paths, err = qs.ProxyCachedArtifacts(ctx, q.ProxyCachedArtifactsParams{
			RepoID: repoID, RangeFrom: prefix, RangeTo: prefixEnd(prefix), MaxRows: int32(limit), //nolint:gosec // bounded by the caller
		})
		return err
	})
	return paths, err
}
