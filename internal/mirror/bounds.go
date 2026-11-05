package mirror

import (
	"context"
	"sync"
	"time"
)

// BoundBytes is the bound of the repository's logical bytes, maxBytes (R-REPO-052). The quota and the capacity guard are not
// built (H-506).
const BoundBytes = "bytes"

// byteBound decides whether the pass may fetch one more artifact (R-REPO-052): the folded counter of the repository's
// logical bytes (one primary-key read per artifact) plus the bytes this process stored since the counter's last fold. The
// counter also counts what clients filled, which is why it is the bound's base and not the pass's own tally.
type byteBound struct {
	store  Store
	clock  Clock
	repoID int64
	max    int64

	mu     sync.Mutex
	stored []storedBytes
}

type storedBytes struct {
	at    time.Time
	bytes int64
}

func newByteBound(s Store, c Clock, repoID int64, maxBytes int64) *byteBound {
	return &byteBound{store: s, clock: c, repoID: repoID, max: maxBytes}
}

// setMax changes the bound: the settings may change while a pass runs.
func (b *byteBound) setMax(n int64) {
	b.mu.Lock()
	b.max = n
	b.mu.Unlock()
}

// limit returns the bound.
func (b *byteBound) limit() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// used returns the repository's bytes as the pass counts them: the counter and what the pass stored after its last fold.
func (b *byteBound) used(ctx context.Context) (int64, error) {
	u, err := b.store.Usage(ctx, b.repoID)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// The pass's clock and the store's clock are compared by their offset at this read, so a skew between the replica and
	// the database moves the cut by that skew and no more.
	offset := u.ReadAt.Sub(b.clock.Now())
	total := u.LogicalBytes
	kept := b.stored[:0]
	for _, s := range b.stored {
		if s.at.Add(offset).After(u.FoldedAt) {
			total += s.bytes
			kept = append(kept, s)
		}
	}
	b.stored = kept
	return total, nil
}

// admits reports whether an artifact of the announced size (0: unknown) may be fetched: not when it would cross the bound,
// and not when the bound is reached already. An artifact without an announced size is fetched and counted afterwards.
func (b *byteBound) admits(ctx context.Context, size int64) (ok bool, used int64, err error) {
	used, err = b.used(ctx)
	if err != nil {
		return false, 0, err
	}
	max := b.limit()
	if used >= max || (size > 0 && used+size > max) {
		return false, used, nil
	}
	return true, used, nil
}

// add counts the bytes of an artifact the pass stored.
func (b *byteBound) add(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.stored = append(b.stored, storedBytes{at: b.clock.Now(), bytes: n})
	b.mu.Unlock()
}
