// Package replay reads the revocation journal and applies the handlers the domains registered (R-HA-054 "Replay run",
// R-BAK-024).
package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
)

// Reader is the [journal.Reader] over a [journal.Store].
//
// Whoever holds the runtime credential of the blob bucket can write objects below the journal prefix, so every object is
// untrusted: one that does not decode, whose ID differs from its key, whose key names another day than its time, or whose
// ID appears under a second key is reported unreadable and never replayed.
type Reader struct {
	store      journal.Store
	blobPrefix string
	now        func() time.Time
}

var _ journal.Reader = (*Reader)(nil)

// NewReader builds a reader of the journal below blobPrefix.
func NewReader(store journal.Store, blobPrefix string) *Reader {
	return &Reader{store: store, blobPrefix: blobPrefix, now: time.Now}
}

// Entries lists the days from since to now, reads every object of them and returns the readable ones with at > since, sorted
// by at and id. An object that cannot be read or decoded is returned in the second result and stops nothing. A listing that
// fails does stop the read: a journal that cannot be listed is a journal that is not known to be complete, and the caller
// refuses the restore (R-BAK-020 check 8) or reports the failure.
func (r *Reader) Entries(ctx context.Context, since time.Time) ([]journal.Object, []journal.Unreadable, error) {
	var keys []string
	// A day further than now is listed too: a key cannot name the future, but the clock of the writer may run ahead of
	// this one by a day boundary.
	for _, p := range journal.DayPrefixes(r.blobPrefix, since, r.now().Add(24*time.Hour)) {
		if err := r.store.List(ctx, p, func(key string) error { keys = append(keys, key); return nil }); err != nil {
			return nil, nil, fmt.Errorf("journal: listing %s: %w", p, err)
		}
	}
	sort.Strings(keys)
	var (
		out        []journal.Object
		unreadable []journal.Unreadable
		seen       = map[string]string{}
	)
	for _, key := range keys {
		id, ok := journal.ParseKey(r.blobPrefix, key)
		if !ok {
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "the key is not a journal key"})
			continue
		}
		body, err := r.store.Get(ctx, key)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "read: " + err.Error()})
			continue
		}
		o, err := journal.Decode(body)
		if err != nil {
			reason := err.Error()
			if errors.Is(err, journal.ErrUnknownVersion) {
				reason = "unknown version: " + reason
			}
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: reason})
			continue
		}
		if o.ID != id || journal.Key(r.blobPrefix, o) != key {
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "the object does not belong under this key"})
			continue
		}
		if first, dup := seen[o.ID]; dup {
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "the entry " + o.ID + " is also under " + first})
			continue
		}
		seen[o.ID] = key
		if !o.At.After(since) {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out, unreadable, nil
}
