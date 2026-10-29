package blob_test

import (
	"context"
	"sync"

	"github.com/guided-traffic/trove/internal/blob"

	"github.com/guided-traffic/trove/internal/meta"
)

// fakeDB simulates the blobs and blob_gc_queue tables for the operations the
// commit protocol and the download path use (the store's seam to the table, installed
// with blob.WithRows). Transactions are serialized and
// apply at commit, which is enough to drive the protocol's branches; the
// integration tier runs the same code against PostgreSQL.
type fakeDB struct {
	mu    sync.Mutex
	rows  map[blob.Digest]*fakeRow
	queue map[blob.Digest]bool
	// beforeCommit runs inside a transaction attempt after its function, for
	// tests that change the world between statements.
	txCount int
	// failTx makes InTx return an error for the n-th transaction (1-based).
	failTx map[int]error
	// gone is called when a poll looks for a row, to let a test delete it.
	onExists func(n int)
	exists   int
	// conflict makes every insert report a concurrent winner whose row is invisible.
	conflict bool
}

type fakeRow struct {
	state, status string
	size          int64
}

func newFakeDB() *fakeDB {
	return &fakeDB{rows: map[blob.Digest]*fakeRow{}, queue: map[blob.Digest]bool{}, failTx: map[int]error{}}
}

func (f *fakeDB) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txCount++
	if err := f.failTx[f.txCount]; err != nil {
		return err
	}
	tx := &fakeTx{db: f, inserts: map[blob.Digest]*fakeRow{}, statuses: map[blob.Digest]string{}, queue: map[blob.Digest]bool{}}
	if err := fn(tx); err != nil {
		tx.RunRollback()
		return err
	}
	if err := tx.RunBefore(ctx, tx); err != nil {
		tx.RunRollback()
		return err
	}
	for d, r := range tx.inserts {
		f.rows[d] = r
	}
	for d, s := range tx.statuses {
		if r := f.rows[d]; r != nil {
			r.status = s
		}
	}
	for d := range tx.queue {
		f.queue[d] = true
	}
	tx.RunAfter()
	return nil
}

// fakeTx is the transaction of the simulation: it holds what the attempt wrote
// until it commits.
type fakeTx struct {
	meta.TxState
	db       *fakeDB
	inserts  map[blob.Digest]*fakeRow
	statuses map[blob.Digest]string
	queue    map[blob.Digest]bool
}

func (f *fakeDB) LockShare(_ context.Context, tx meta.Tx, d blob.Digest) (state, status string, found bool, err error) {
	t := tx.(*fakeTx)
	if r, ok := t.inserts[d]; ok {
		return r.state, r.status, true, nil
	}
	if r, ok := f.rows[d]; ok {
		st := r.status
		if s, ok := t.statuses[d]; ok {
			st = s
		}
		return r.state, st, true, nil
	}
	return "", "", false, nil
}

func (f *fakeDB) InsertLive(_ context.Context, tx meta.Tx, sp *blob.Spooled) (bool, error) {
	t := tx.(*fakeTx)
	d := sp.SHA256
	if f.conflict {
		return false, nil
	}
	if _, ok := f.rows[d]; ok {
		return false, nil
	}
	if _, ok := t.inserts[d]; ok {
		return false, nil
	}
	t.inserts[d] = &fakeRow{state: "live", status: "ok", size: sp.Size}
	return true, nil
}

func (f *fakeDB) Exists(_ context.Context, _ meta.Tx, d blob.Digest) (bool, error) {
	f.exists++
	if f.onExists != nil {
		f.onExists(f.exists)
	}
	_, ok := f.rows[d]
	return ok, nil
}

func (f *fakeDB) MarkOK(_ context.Context, tx meta.Tx, d blob.Digest) error {
	tx.(*fakeTx).statuses[d] = "ok"
	return nil
}

func (f *fakeDB) Enqueue(_ context.Context, tx meta.Tx, d blob.Digest) error {
	tx.(*fakeTx).queue[d] = true
	return nil
}

func (f *fakeDB) Row(_ context.Context, _ meta.Tx, d blob.Digest) (blob.Row, bool, error) {
	r, ok := f.rows[d]
	if !ok {
		return blob.Row{}, false, nil
	}
	return blob.Row{Digests: blob.Digests{SHA256: d, Size: r.size}, State: r.state, ContentStatus: r.status}, true, nil
}

func (f *fakeDB) MarkDamaged(_ context.Context, tx meta.Tx, d blob.Digest, status string) (bool, error) {
	if r := f.rows[d]; r != nil && r.state == "live" && r.status == "ok" {
		tx.(*fakeTx).statuses[d] = status
		return true, nil
	}
	return false, nil
}
