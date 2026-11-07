package journal

import (
	"context"
	"time"
)

// Store is the object store the journal lives in: the blob bucket of the installation that wrote it. The writer uses the
// runtime credential (put only); the reader uses the blob-store credential or the journal credential of a restore
// (R-BAK-015 "Journal reader"). The implementation is an adapter over the client that internal/blob/s3 builds.
type Store interface {
	// Put writes an object. Writing the same key again with the same bytes is harmless (R-HA-054).
	Put(ctx context.Context, key string, body []byte) error
	// List calls fn for every key below prefix in lexicographic order. With bucket versioning and a credential that may
	// list versions, an implementation configured with versions lists the keys of delete-marked objects as well, so a
	// delete marker cannot hide an entry.
	List(ctx context.Context, prefix string, fn func(key string) error) error
	// Get reads an object.
	Get(ctx context.Context, key string) ([]byte, error)
}

// Outbox is revocation_outbox (R-HA-054), the transactional queue the audit writer fills.
type Outbox interface {
	// Claim returns up to limit rows in id order.
	Claim(ctx context.Context, limit int) ([]OutboxRow, error)
	// Delete removes shipped rows.
	Delete(ctx context.Context, ids []string) error
	// Pending returns the number of rows and the age of the oldest (trove_revocation_journal_pending).
	Pending(ctx context.Context) (rows int64, oldest time.Duration, err error)
}

// Shipper writes the outbox rows to the store (R-HA-054 "Outbox and shipping"): a worker job runs it when its signal fires
// and at least every backup.revocation_journal.ship_interval. A change never waits for it; during an outage of the object
// store the outbox grows and ships afterwards. Ship writes the rows in id order, one object each, and deletes each row after
// its object was written; it returns the number shipped. It never deletes an object.
type Shipper interface {
	Ship(ctx context.Context) (int, error)
}

// Reader lists the journal entries a replay applies.
type Reader interface {
	// Entries returns the objects with at > since, sorted by at and id (R-HA-054 "Replay run"). The caller passes
	// snapshotAt minus backup.revocation_journal.replay_margin. An object that cannot be decoded is returned in Skipped
	// with its key and the reason, and the replay reports it; it never stops the replay.
	Entries(ctx context.Context, since time.Time) (entries []Object, skipped []Unreadable, err error)
}

// Unreadable is a journal object that could not be read or decoded.
type Unreadable struct {
	Key    string
	Reason string
}
