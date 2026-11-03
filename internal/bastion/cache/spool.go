package cache

import "sync"

// spoolBudget bounds the bytes that the fills in progress of a replica hold in temporary storage together
// (bastion.cache.spool_max_bytes, Q-BAS-131). A fill reserves the bytes before it writes them and gives them
// back when its file is removed; a write that would pass the budget is refused with [ErrSpoolFull] and the
// caller passes the answer through unkept, so the temporary storage of the pod never fills beyond what the
// chart sized it for (an emptyDir over its sizeLimit evicts the pod). A limit of zero or less is no limit.
type spoolBudget struct {
	mu    sync.Mutex
	limit int64
	held  int64
}

func newSpoolBudget(limit int64) *spoolBudget { return &spoolBudget{limit: limit} }

// reserve takes n bytes and reports whether they fit.
func (b *spoolBudget) reserve(n int64) bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held+n > b.limit {
		return false
	}
	b.held += n
	return true
}

// release gives n bytes back.
func (b *spoolBudget) release(n int64) {
	if b == nil || b.limit <= 0 || n <= 0 {
		return
	}
	b.mu.Lock()
	b.held = max(b.held-n, 0)
	b.mu.Unlock()
}

// Held is the bytes the fills in progress hold now.
func (b *spoolBudget) Held() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}
