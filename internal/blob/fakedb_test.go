package blob_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/blob"

	"github.com/guided-traffic/trove/internal/meta"
)

// fakeDB simulates the blobs and blob_gc_queue tables for the statements the
// commit protocol and the download path use. Transactions are serialized and
// apply at commit, which is enough to drive the protocol's branches; the
// integration tier runs the same code against PostgreSQL.
type fakeDB struct {
	mu    sync.Mutex
	rows  map[blob.Digest]*fakeRow
	queue map[blob.Digest]bool
	// beforeCommit runs inside a transaction attempt after its function, for
	// tests that change the world between statements.
	txCount int
	// failTx makes Tx return an error for the n-th transaction (1-based).
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

func (f *fakeDB) Tx(ctx context.Context, fn func(meta.Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txCount++
	if err := f.failTx[f.txCount]; err != nil {
		return err
	}
	tx := &fakeTx{db: f, inserts: map[blob.Digest]*fakeRow{}, statuses: map[blob.Digest]string{}, queue: map[blob.Digest]bool{}}
	if err := fn(tx); err != nil {
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
	return nil
}

type fakeTx struct {
	db       *fakeDB
	inserts  map[blob.Digest]*fakeRow
	statuses map[blob.Digest]string
	queue    map[blob.Digest]bool
}

func dg(b any) blob.Digest {
	var d blob.Digest
	copy(d[:], b.([]byte))
	return d
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	switch sql {
	case blob.SQLInsertBlob:
		d := dg(args[0])
		if t.db.conflict {
			return 0, nil
		}
		if _, ok := t.db.rows[d]; ok {
			return 0, nil
		}
		if _, ok := t.inserts[d]; ok {
			return 0, nil
		}
		t.inserts[d] = &fakeRow{state: "live", status: "ok", size: args[4].(int64)}
		return 1, nil
	case blob.SQLMarkOK:
		t.statuses[dg(args[0])] = "ok"
		return 1, nil
	case blob.SQLEnqueue:
		t.queue[dg(args[0])] = true
		return 1, nil
	case blob.SQLMarkDamaged:
		d := dg(args[0])
		if r := t.db.rows[d]; r != nil && r.state == "live" && r.status == "ok" {
			t.statuses[d] = args[1].(string)
			return 1, nil
		}
		return 0, nil
	}
	return 0, errors.New("fakeDB: unknown statement " + sql)
}

type fakeRows struct {
	data [][]any
	i    int
}

func (r *fakeRows) Next() bool { r.i++; return r.i <= len(r.data) }
func (r *fakeRows) Scan(dest ...any) error {
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = r.data[r.i-1][i].(string)
		case *int:
			*p = r.data[r.i-1][i].(int)
		case *int64:
			*p = r.data[r.i-1][i].(int64)
		case *[]byte:
			*p = r.data[r.i-1][i].([]byte)
		case *time.Time:
			*p = r.data[r.i-1][i].(time.Time)
		}
	}
	return nil
}
func (r *fakeRows) Err() error { return nil }
func (r *fakeRows) Close()     {}

func (t *fakeTx) Query(_ context.Context, sql string, args ...any) (meta.Rows, error) {
	switch sql {
	case blob.SQLLockShare:
		d := dg(args[0])
		if r, ok := t.inserts[d]; ok {
			return &fakeRows{data: [][]any{{r.state, r.status}}}, nil
		}
		if r, ok := t.db.rows[d]; ok {
			st := r.status
			if s, ok := t.statuses[d]; ok {
				st = s
			}
			return &fakeRows{data: [][]any{{r.state, st}}}, nil
		}
		return &fakeRows{}, nil
	case blob.SQLBlobExists:
		t.db.exists++
		if t.db.onExists != nil {
			t.db.onExists(t.db.exists)
		}
		if _, ok := t.db.rows[dg(args[0])]; ok {
			return &fakeRows{data: [][]any{{1}}}, nil
		}
		return &fakeRows{}, nil
	}
	return nil, errors.New("fakeDB: unknown query " + sql)
}

func (t *fakeTx) QueryRow(context.Context, string, ...any) meta.Row { panic("unused") }
func (t *fakeTx) Before(func(context.Context, meta.Tx) error)       {}
func (t *fakeTx) After(func())                                      {}
