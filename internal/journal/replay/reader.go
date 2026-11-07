// Package replay reads the revocation journal and applies the handlers the domains registered (R-HA-054 "Replay run",
// R-BAK-024).
package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
)

// Reader is the [journal.Reader] over a [journal.Store].
//
// Whoever holds the runtime credential of the blob bucket can write objects below the journal prefix, so every object is
// untrusted: one that does not decode, whose ID differs from its key, or whose key names another day than its time is reported
// unreadable and never replayed. An ID that appears under a second key, and a key whose versions diverge, are not dropped
// either: every copy is replayed and flagged, because a handler only removes or narrows and checks the entry against the
// database, whereas dropping a copy would let the writer of the planted one hide the genuine one (audit finding H2).
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
		keysOf     = map[string][]string{} // entry ID -> the keys it was read under
	)
	for _, key := range keys {
		id, ok := journal.ParseKey(r.blobPrefix, key)
		if !ok {
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "the key is not a journal key"})
			continue
		}
		bodies, err := r.bodies(ctx, key)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			unreadable = append(unreadable, journal.Unreadable{Key: key, Reason: "read: " + err.Error()})
			continue
		}
		var readable []journal.Object
		for _, body := range bodies {
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
			readable = append(readable, o)
		}
		// trove writes a key only with the same bytes (R-HA-054), so a second content is somebody else's write.
		if len(readable) > 1 {
			for i := range readable {
				readable[i].Warning = "the versions of " + key + " diverge; every version is replayed"
			}
		}
		keysOf[id] = append(keysOf[id], key)
		out = append(out, readable...)
	}
	kept := out[:0]
	for _, o := range out {
		if ks := keysOf[o.ID]; len(ks) > 1 {
			o.Warning = strings.TrimSpace(o.Warning + " the entry " + o.ID + " is also under " +
				strings.Join(without(ks, journal.Key(r.blobPrefix, o)), ", ") + "; every copy is replayed")
		}
		if o.At.After(since) {
			kept = append(kept, o)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Less(kept[j]) })
	return kept, unreadable, nil
}

// bodies returns the contents to read for a key: every version when the store was listed with versions, else the one.
func (r *Reader) bodies(ctx context.Context, key string) ([][]byte, error) {
	if av, ok := r.store.(journal.AllVersions); ok {
		return av.GetAll(ctx, key)
	}
	b, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

func without(keys []string, drop string) []string {
	var out []string
	for _, k := range keys {
		if k != drop {
			out = append(out, k)
		}
	}
	return out
}
