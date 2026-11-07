package create

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
	"github.com/guided-traffic/trove/internal/config"
)

// fakeRuns is an in-memory RunStore with the fencing of the real one.
type fakeRuns struct {
	mu       sync.Mutex
	run      backup.Run
	have     bool
	pins     map[format.Digest]int64
	holds    []backup.Hold // every hold the run had, in order
	taken    *backup.Run   // when set, Begin reports the slot as taken by this run
	fenceOK  bool
	finished backup.State
	result   json.RawMessage
	errJSON  json.RawMessage
}

func newFakeRuns() *fakeRuns { return &fakeRuns{pins: map[format.Digest]int64{}, fenceOK: true} }

func (f *fakeRuns) Begin(_ context.Context, r backup.NewRun) (backup.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.taken != nil {
		return backup.Run{}, &backup.ErrSlotTaken{Active: *f.taken}
	}
	f.run = backup.Run{
		ID: r.ID, Operation: r.Operation, BackupID: r.BackupID, State: backup.StateStarting, Hold: backup.HoldNone, Trigger: r.Trigger,
		Pod: r.Pod, LeaseExpiresAt: time.Now().Add(r.Lease),
	}
	f.have = true
	f.holds = []backup.Hold{backup.HoldNone}
	return f.run, nil
}

func (f *fakeRuns) Get(context.Context, string) (backup.Run, bool, error) { return f.run, f.have, nil }

func (f *fakeRuns) Active(context.Context) (backup.Run, bool, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.taken != nil {
		return *f.taken, true, time.Now(), nil
	}
	return backup.Run{}, false, time.Now(), nil
}

func (f *fakeRuns) Heartbeat(_ context.Context, _ string, expected backup.State, _ backup.Progress, _ *time.Time, _ bool, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.State != expected || !f.run.State.Active() {
		return backup.ErrSuperseded
	}
	return nil
}

func (f *fakeRuns) SetState(_ context.Context, _ string, from, to backup.State, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.State != from {
		return backup.ErrSuperseded
	}
	f.run.State = to
	return nil
}

func (f *fakeRuns) SetHold(_ context.Context, _ string, from, to backup.Hold) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Hold != from {
		return backup.ErrSuperseded
	}
	f.run.Hold = to
	f.holds = append(f.holds, to)
	return nil
}

func (f *fakeRuns) Fence(_ context.Context, _ string, from, to backup.State, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fenceOK || f.run.State != from {
		return false, nil
	}
	f.run.State = to
	return true, nil
}

func (f *fakeRuns) AddPins(_ context.Context, _ string, es []format.BlobEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range es {
		f.pins[e.Digest] = e.Size
	}
	return nil
}

func (f *fakeRuns) DeletePins(_ context.Context, _ string, ds []format.Digest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range ds {
		delete(f.pins, d)
	}
	return nil
}

func (f *fakeRuns) Finish(_ context.Context, _ string, s backup.State, res, e json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.run.State.Active() {
		return backup.ErrSuperseded
	}
	f.run.State, f.run.Hold = s, backup.HoldNone
	f.holds = append(f.holds, backup.HoldNone)
	f.finished, f.result, f.errJSON = s, res, e
	f.pins = map[format.Digest]int64{}
	return nil
}
func (f *fakeRuns) PruneHistory(context.Context, time.Duration) (int64, error) { return 0, nil }

// Pins implements PinReader.
func (f *fakeRuns) Pins(_ context.Context, _ string, after format.Digest, limit int) ([]format.BlobEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []format.BlobEntry
	for d, s := range f.pins {
		if after.Less(d) {
			out = append(out, format.BlobEntry{Digest: d, Size: s})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest.Less(out[j].Digest) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRuns) Hold() backup.Hold {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run.Hold
}

// fakeFacts serves the facts and the grant check.
type fakeFacts struct {
	facts  backup.SystemFacts
	grants error
	states map[format.Digest]string // blob row state; absent = gone
}

func (f *fakeFacts) SystemFacts(context.Context) (backup.SystemFacts, error) { return f.facts, nil }
func (f *fakeFacts) CheckGrants(context.Context) error                       { return f.grants }
func (f *fakeFacts) BlobState(_ context.Context, d format.Digest) (string, string, bool, error) {
	s, ok := f.states[d]
	return s, "ok", ok, nil
}

// fakeSnap is a snapshotter over fixed tables and blobs.
type fakeSnap struct {
	runs       *fakeRuns
	tables     []backup.TableInfo
	rows       map[string]string
	blobs      []format.BlobEntry
	openErr    error
	exportErr  error
	holdAtOpen backup.Hold
	opened     bool
	closed     bool
	released   bool
	onExport   func(ctx context.Context)
}

func (s *fakeSnap) Open(ctx context.Context, _ backup.SnapshotOptions, onLocked func(context.Context) error) (backup.Snapshot, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	if err := onLocked(ctx); err != nil {
		return nil, err
	}
	s.holdAtOpen, s.opened = s.runs.Hold(), true
	return s, nil
}

func (s *fakeSnap) Info() backup.SnapshotInfo {
	return backup.SnapshotInfo{
		At: time.Now(), ServerVersion: "17.6", KeyIDs: []string{"2026-10"}, Tables: s.tables,
		Schema: format.SchemaInfo{Step: 2, Steps: []format.SchemaStep{{Step: 1, Name: "a", SHA256: "00"}, {Step: 2, Name: "b", SHA256: "11"}}},
	}
}

func (s *fakeSnap) Referenced(context.Context) (format.BlobIterator, error) {
	es := append([]format.BlobEntry(nil), s.blobs...)
	sort.Slice(es, func(i, j int) bool { return es[i].Digest.Less(es[j].Digest) })
	return &format.EntryList{Entries: es}, nil
}

func (s *fakeSnap) ExportTable(ctx context.Context, t backup.TableInfo, w io.Writer) (int64, error) {
	if s.onExport != nil {
		s.onExport(ctx)
	}
	if s.exportErr != nil {
		return 0, s.exportErr
	}
	n, err := io.WriteString(w, s.rows[t.Name])
	_ = n
	return int64(bytes.Count([]byte(s.rows[t.Name]), []byte("\n"))), err
}

func (s *fakeSnap) Sequences(context.Context) ([]format.Sequence, error) {
	return []format.Sequence{{Name: "items_id_seq", LastValue: 7, IsCalled: true}}, nil
}
func (s *fakeSnap) ReleaseMigrationLock(context.Context) error { s.released = true; return nil }
func (s *fakeSnap) Close(context.Context) error                { s.closed = true; return nil }

// fakeSource serves the blobs of the source store.
type fakeSource struct {
	data map[format.Digest][]byte
}

func (s *fakeSource) Size(_ context.Context, d format.Digest) (int64, error) {
	b, ok := s.data[d]
	if !ok {
		return 0, backup.ErrBlobNotFound
	}
	return int64(len(b)), nil
}

func (s *fakeSource) Open(_ context.Context, d format.Digest, off, length int64) (io.ReadCloser, error) {
	b, ok := s.data[d]
	if !ok {
		return nil, backup.ErrBlobNotFound
	}
	end := int64(len(b))
	if length >= 0 {
		end = min(off+length, end)
	}
	return io.NopCloser(bytes.NewReader(b[off:end])), nil
}

func (s *fakeSource) Ref(d format.Digest) target.ObjectRef {
	return target.ObjectRef{Bucket: "src", Key: d.Hex()}
}

type recEvents struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (r *recEvents) Record(_ context.Context, ev audit.Event) error {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
	return nil
}

func (r *recEvents) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		out = append(out, e.Type)
	}
	return out
}

// env bundles a pipeline with its fakes.
type env struct {
	t      *testing.T
	runs   *fakeRuns
	facts  *fakeFacts
	snap   *fakeSnap
	src    *fakeSource
	mem    *targettest.Mem
	events *recEvents
	cfg    config.Backup
	deps   Deps
	prune  *fakePrune
	now    time.Time
}

type fakePrune struct {
	called bool
	err    error
}

func (p *fakePrune) Prune(_ context.Context, _ *backup.Handle, into *backup.Result) error {
	p.called = true
	into.PoolObjectsDeleted = 3
	return p.err
}

func digestOf(b []byte) format.Digest { return format.Digest(sha256.Sum256(b)) }

func testConfig() config.Backup {
	return config.Backup{
		Target:           config.BackupTarget{Bucket: "backups", Prefix: "trove", SSE: config.BackupSSE{Mode: "s3"}},
		Database:         config.BackupDatabase{Parallel: 2, LockTimeout: time.Minute, MigrationWait: time.Minute},
		Copy:             config.BackupCopy{Concurrency: 4, ServerSide: "auto", PartSize: 5 * config.MiB, CheckExisting: "auto"},
		CompressionLevel: 3, LeaseDuration: time.Minute, HeartbeatInterval: 100 * time.Millisecond, StallTimeout: time.Minute,
		FinalizeTimeout: time.Second, VerifySample: 10, RunHistory: time.Hour, Retention: config.BackupRetention{KeepLast: 7},
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, runs: newFakeRuns(), mem: targettest.New(), events: &recEvents{}, cfg: testConfig(), prune: &fakePrune{}, now: time.Now().UTC().Truncate(time.Second)}
	e.facts = &fakeFacts{facts: backup.SystemFacts{InstallationID: "inst-1", Incarnation: "inc-1"}, states: map[format.Digest]string{}}
	e.src = &fakeSource{data: map[format.Digest][]byte{}}
	e.snap = &fakeSnap{runs: e.runs, rows: map[string]string{}}
	e.mem.Source = func(r target.ObjectRef) ([]byte, bool) {
		d, err := format.ParseDigest(r.Key)
		if err != nil {
			return nil, false
		}
		b, ok := e.src.data[d]
		return b, ok
	}
	e.snap.tables = []backup.TableInfo{
		{Name: "items", Class: tables.Data, Columns: [][2]string{{"id", "bigint"}, {"path", "text"}}, Registered: true, EstimatedBytes: 10},
		{Name: "blobs", Class: tables.Blobs, Columns: [][2]string{{"sha256", "bytea"}}, Registered: true, EstimatedBytes: 5},
	}
	e.snap.rows["items"] = "1\ta\n2\tb\n"
	e.snap.rows["blobs"] = "x\n"
	return e
}

// addBlob adds a live blob to the source and the snapshot.
func (e *env) addBlob(content string) format.Digest {
	d := digestOf([]byte(content))
	e.src.data[d] = []byte(content)
	e.snap.blobs = append(e.snap.blobs, format.BlobEntry{Digest: d, Size: int64(len(content))})
	e.facts.states[d] = "live"
	return d
}

func (e *env) build() *Pipeline {
	e.t.Helper()
	e.deps = Deps{
		Cfg: e.cfg, SourceRef: format.StorageRef{Bucket: "blobs", Prefix: "b"}, Instance: "pod-1", Version: "0.1.0", Revision: "abc",
		BackupRole: "trove_backup", Store: e.runs, Facts: e.facts, Grants: e.facts, Pins: e.runs, Snapshot: e.snap, Source: e.src,
		BlobState: e.facts, Target: e.mem, Prune: e.prune, Events: e.events, Metrics: nil, Registry: tables.New(),
		Now:   func() time.Time { return e.now },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	p, err := New(e.deps)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) create(opts backup.CreateOptions) (*backup.Result, error) {
	return e.build().Create(context.Background(), opts)
}

func (e *env) manifest(id string) *format.Manifest {
	e.t.Helper()
	b, ok := e.mem.Data("trove/backups/" + id + "/manifest.json")
	if !ok {
		e.t.Fatalf("no manifest for %s; keys: %v", id, e.mem.Keys())
	}
	m, err := format.DecodeManifest(b)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) hasManifest() bool {
	for _, k := range e.mem.Keys() {
		if len(k) > 14 && k[len(k)-13:] == "manifest.json" {
			return true
		}
	}
	return false
}

func errorf(f string, a ...any) error { return fmt.Errorf(f, a...) }
