package formattest

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

type itemRec struct {
	ref     formats.ItemRef
	data    []byte
	digests formats.Digests
	media   string
	at      time.Time
	version formats.VersionID
	removed bool
}

type pkgRec struct {
	id      formats.PackageID
	name    string
	display string
	attrs   json.RawMessage
	rev     formats.RevSeq
}

type verRec struct {
	id      formats.VersionID
	pkg     formats.PackageID
	version string
	display string
	lead    int64
	attrs   json.RawMessage
	at      time.Time
	by      string
	trashed bool
	deleted bool
}

// memTx is the transaction of the in-memory Host: opaque to the format (it is a
// formats.Tx), with the hooks of meta.Tx and an undo log that a rollback plays
// backwards.
type memTx struct {
	before     []func(context.Context, formats.Tx) error
	after      []func()
	onRollback []func()
	values     map[any]any
	undo       []func()
	bumped     map[formats.PackageID]bool
	notes      []Notification
	noted      map[Notification]bool
}

var _ formats.Tx = (*memTx)(nil)

func newTx() *memTx {
	return &memTx{values: map[any]any{}, bumped: map[formats.PackageID]bool{}, noted: map[Notification]bool{}}
}

// note attaches a notification once per transaction, as the production writer does
// for one (area, payload).
func (t *memTx) note(n Notification) {
	if t.noted[n] {
		return
	}
	t.noted[n] = true
	t.notes = append(t.notes, n)
}

func (t *memTx) Before(fn func(context.Context, formats.Tx) error) { t.before = append(t.before, fn) }
func (t *memTx) After(fn func())                                   { t.after = append(t.after, fn) }
func (t *memTx) OnRollback(fn func())                              { t.onRollback = append(t.onRollback, fn) }
func (t *memTx) Value(key any) any                                 { return t.values[key] }
func (t *memTx) SetValue(key, v any)                               { t.values[key] = v }

// Write implements formats.Host: fn runs against the Host's rows under an undo
// log. A failure of fn or of a Before hook plays the log backwards, so the rows,
// the notifications of the attempt vanish; a commit publishes the notifications
// and runs the After hooks.
//
// Transactions run one at a time, in the order they begin. PostgreSQL lets two transactions
// run side by side and makes the conflicting ones wait: the package row lock (EnsureLocked), the
// unique path of an item and the unique version of a package. A rolled-back transaction there
// never shows a row, and a transaction never sees the rows of one that has not ended. The undo
// log here writes straight into the shared maps, so two overlapping transactions would see each
// other's uncommitted rows and a rollback would take away rows another transaction had built on;
// running them one after the other is one of the outcomes the database allows and is the one
// this Host models. The reads outside a transaction (Stat, Versions) are not blocked by it.
func (h *Host) Write(ctx context.Context, rq *formats.Request, fn func(tx formats.Tx, w formats.PackageWriter) error) error {
	h.runInterleaved()
	h.txMu.Lock()
	defer h.txMu.Unlock()
	return h.write(ctx, rq, fn)
}

// OnNextWrite registers fn to run once, before the next transaction begins and while no transaction is
// running: another client's request that completes between the checks a handler makes outside its
// transaction and the transaction itself. It cannot run inside a transaction, where a second
// transaction on the same rows would wait for the first (see [Host.Write]).
func (h *Host) OnNextWrite(fn func()) {
	h.mu.Lock()
	h.interleaved = append(h.interleaved, fn)
	h.mu.Unlock()
}

func (h *Host) runInterleaved() {
	h.mu.Lock()
	fns := h.interleaved
	h.interleaved = nil
	h.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (h *Host) write(ctx context.Context, rq *formats.Request, fn func(tx formats.Tx, w formats.PackageWriter) error) error {
	tx := newTx()
	w := &writer{h: h, tx: tx, rq: rq, prefix: h.f.Descriptor().PropertyPrefix}
	err := fn(tx, w)
	for i := 0; err == nil && i < len(tx.before); i++ {
		err = tx.before[i](ctx, tx)
	}
	if err != nil {
		h.mu.Lock()
		for i := len(tx.undo) - 1; i >= 0; i-- {
			tx.undo[i]()
		}
		h.mu.Unlock()
		for _, fn := range tx.onRollback {
			fn()
		}
		return err
	}
	h.mu.Lock()
	h.notified = append(h.notified, tx.notes...)
	h.mu.Unlock()
	for _, fn := range tx.after {
		fn()
	}
	return nil
}

// Seed runs fn in a transaction bound to no request, the way a fill or a job
// writes: it prepares the rows a case starts from. Claim answers an internal
// error on such a writer.
func (h *Host) Seed(ctx context.Context, fn func(w formats.PackageWriter) error) error {
	return h.Write(ctx, nil, func(_ formats.Tx, w formats.PackageWriter) error { return fn(w) })
}

// writer is the PackageWriter of one memTx. It follows internal/packages: a
// package gets one revision raise per transaction, however many calls touch it.
type writer struct {
	h      *Host
	tx     *memTx
	rq     *formats.Request
	prefix string
}

var _ formats.PackageWriter = (*writer)(nil)

func (w *writer) ref(p *pkgRec) formats.PackageRef {
	return formats.PackageRef{ID: p.id, Repo: w.h.ref, Name: p.name, RevSeq: p.rev}
}

// touch raises the revision of a package once per transaction. The caller holds
// the Host's lock.
func (w *writer) touch(p *pkgRec) {
	if w.tx.bumped[p.id] {
		return
	}
	old := p.rev
	p.rev++
	w.tx.bumped[p.id] = true
	w.tx.undo = append(w.tx.undo, func() { p.rev = old; delete(w.tx.bumped, p.id) })
	w.signal(p)
}

// signal attaches the early-eviction signal of a package the transaction raised: the
// writer does it, once per package, so a format never repeats it (Q-ARCH-259).
func (w *writer) signal(p *pkgRec) {
	w.tx.note(Notification{Area: catalog.MetadataChangedArea, Payload: catalog.PackageChanged(w.h.ref.ID, p.name)})
}

func (w *writer) EnsureLocked(_ context.Context, repo formats.RepoRef, name, displayName string) (formats.PackageRef, bool, error) {
	if repo.ID != w.h.ref.ID {
		return formats.PackageRef{}, false, errs.E(errs.Internal, errs.CodeInternal, "formattest: the repository is not the host's")
	}
	if displayName == "" {
		displayName = name
	}
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	if p, ok := w.h.pkgs[name]; ok {
		return w.ref(p), false, nil
	}
	p := &pkgRec{id: formats.PackageID(w.h.id()), name: name, display: displayName, rev: 1}
	w.h.pkgs[name] = p
	w.h.pkgByID[p.id] = p
	w.tx.bumped[p.id] = true // a created package starts at 1, which is this transaction's raise
	w.signal(p)
	w.tx.undo = append(w.tx.undo, func() { delete(w.h.pkgs, name); delete(w.h.pkgByID, p.id); delete(w.tx.bumped, p.id) })
	return w.ref(p), true, nil
}

func (w *writer) live(p *pkgRec) []formats.VersionRow {
	var out []formats.VersionRow
	for _, v := range w.h.vers {
		if v.pkg == p.id && !v.trashed && !v.deleted {
			out = append(out, formats.VersionRow{ID: v.id, Version: v.version, DisplayVersion: v.display, LeadItem: v.lead, Attributes: v.attrs, CreatedAt: v.at, CreatedBy: v.by})
		}
	}
	slices.SortFunc(out, func(a, b formats.VersionRow) int { return int(a.ID - b.ID) })
	return out
}

func (w *writer) Versions(_ context.Context, pkg formats.PackageRef) ([]formats.VersionRow, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	p, ok := w.h.pkgByID[pkg.ID]
	if !ok {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
	}
	return w.live(p), nil
}

func (w *writer) InsertVersion(_ context.Context, pkg formats.PackageRef, v formats.NewVersion) (formats.VersionID, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	p, ok := w.h.pkgByID[pkg.ID]
	if !ok {
		return 0, errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
	}
	for _, e := range w.live(p) {
		if e.Version == v.Version {
			return 0, errs.E(errs.Conflict, errs.CodeAlreadyExists, "The version already exists.")
		}
	}
	if v.DisplayVersion == "" {
		v.DisplayVersion = v.Version
	}
	rec := &verRec{id: formats.VersionID(w.h.id()), pkg: p.id, version: v.Version, display: v.DisplayVersion, attrs: v.Attributes, by: v.CreatedBy, at: time.Now()}
	w.h.vers[rec.id] = rec
	w.tx.undo = append(w.tx.undo, func() { delete(w.h.vers, rec.id) })
	if v.LeadItem != nil {
		rec.lead = v.LeadItem.ID
		w.link(v.LeadItem.ID, rec.id)
	}
	w.touch(p)
	return rec.id, nil
}

// link attaches an item to a version, logging the undo. The caller holds the lock.
func (w *writer) link(item int64, ver formats.VersionID) {
	if it, ok := w.h.byID[item]; ok {
		old := it.version
		it.version = ver
		w.tx.undo = append(w.tx.undo, func() { it.version = old })
	}
}

func (w *writer) TrashOrDeleteVersion(_ context.Context, v formats.VersionID, purge bool) error {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	rec, ok := w.h.vers[v]
	if !ok || rec.trashed || rec.deleted {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
	}
	oldT, oldD := rec.trashed, rec.deleted
	if purge {
		rec.deleted = true
	} else {
		rec.trashed = true
	}
	w.tx.undo = append(w.tx.undo, func() { rec.trashed, rec.deleted = oldT, oldD })
	w.touch(w.h.pkgByID[rec.pkg])
	return nil
}

func (w *writer) SetPackageAttributes(_ context.Context, pkg formats.PackageRef, attrs json.RawMessage) error {
	if err := checkAttrs(attrs); err != nil {
		return err
	}
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	p, ok := w.h.pkgByID[pkg.ID]
	if !ok {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
	}
	old := p.attrs
	p.attrs = attrs
	w.tx.undo = append(w.tx.undo, func() { p.attrs = old })
	w.touch(p)
	return nil
}

func (w *writer) SetVersionAttributes(_ context.Context, v formats.VersionID, attrs json.RawMessage) error {
	if err := checkAttrs(attrs); err != nil {
		return err
	}
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	rec, ok := w.h.vers[v]
	if !ok || rec.trashed || rec.deleted {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
	}
	old := rec.attrs
	rec.attrs = attrs
	w.tx.undo = append(w.tx.undo, func() { rec.attrs = old })
	w.touch(w.h.pkgByID[rec.pkg])
	return nil
}

func checkAttrs(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return errs.E(errs.Internal, errs.CodeInternal, "formattest: attributes are a JSON object")
	}
	return nil
}

func (w *writer) LinkItem(_ context.Context, it formats.ItemRef, v formats.VersionID, lead bool) error {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	rec, ok := w.h.vers[v]
	if !ok || rec.trashed || rec.deleted {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item or the version does not exist in the repository.")
	}
	if _, ok := w.h.byID[it.ID]; !ok {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item or the version does not exist in the repository.")
	}
	w.link(it.ID, v)
	if lead {
		old := rec.lead
		rec.lead = it.ID
		w.tx.undo = append(w.tx.undo, func() { rec.lead = old })
	}
	w.touch(w.h.pkgByID[rec.pkg])
	return nil
}

func (w *writer) SetProperties(_ context.Context, it formats.ItemRef, props map[string]string) error {
	if w.prefix == "" {
		return errs.E(errs.Internal, errs.CodeInternal, "formattest: this writer has no property namespace")
	}
	for k := range props {
		if !strings.HasPrefix(k, w.prefix) {
			return errs.E(errs.Internal, errs.CodeInternal, "formattest: the property "+k+" is outside the namespace "+w.prefix)
		}
	}
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	old, had := w.h.props[it.ID]
	w.h.props[it.ID] = maps0(props)
	w.tx.undo = append(w.tx.undo, func() {
		if had {
			w.h.props[it.ID] = old
		} else {
			delete(w.h.props, it.ID)
		}
	})
	if rec, ok := w.h.byID[it.ID]; ok && rec.version != 0 {
		if v, ok := w.h.vers[rec.version]; ok {
			w.touch(w.h.pkgByID[v.pkg])
		}
	}
	return nil
}

func maps0(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (w *writer) BumpRevision(_ context.Context, pkg formats.PackageRef) (formats.RevSeq, time.Time, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	p, ok := w.h.pkgByID[pkg.ID]
	if !ok {
		return 0, time.Time{}, errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
	}
	w.touch(p)
	return p.rev, time.Now(), nil
}

func (w *writer) Claim(_ context.Context, t formats.Target, ow formats.OwnershipWrite) error {
	if w.rq == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "A writer bound to no request decides no ownership.")
	}
	w.h.mu.Lock()
	w.h.claims = append(w.h.claims, ClaimCall{Target: t, Write: ow})
	w.h.mu.Unlock()
	if w.h.opts.Claim != nil {
		return w.h.opts.Claim(t, ow)
	}
	return nil
}

func (w *writer) AutoClaim(context.Context, string) (formats.AutoClaimResult, error) {
	return formats.AutoClaimResult{Reason: "disabled"}, nil
}

// DeleteItems removes the files at the paths, a folder's subtree included, and runs
// OnItemsRemoved in this transaction with a writer bound to no request, as the
// production Host does for the hook of an item store.
func (w *writer) DeleteItems(ctx context.Context, repo formats.RepoRef, paths ...string) error {
	if w.rq == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "A writer bound to no request removes no items.")
	}
	if repo.ID != w.h.ref.ID {
		return errs.E(errs.Internal, errs.CodeInternal, "formattest: the repository is not the host's")
	}
	if err := w.h.lockRemovalIn(ctx, w.tx, w, paths); err != nil {
		return err
	}
	return w.removeCovered(ctx, paths, "delete", w.rq.Subject.ID, true)
}

// removeCovered marks the live files the paths cover as removed and runs OnItemsRemoved for them in this transaction, with a Cascade that
// does the same for the paths the hook names, as the production host does through the item store. For a request's removal (guarded) the
// paths of a cascade are decided for `delete` first, as the production host's guard does, and the first refusal refuses the whole removal.
func (w *writer) removeCovered(ctx context.Context, paths []string, reason, actor string, guarded bool) error {
	ev := formats.ItemsEvent{Repo: w.h.ref, Reason: reason, Actor: actor, Data: w.h.opts.Data}
	w.h.mu.Lock()
	for _, rec := range w.h.sortedItems() {
		if rec.removed || !coveredBy(rec.ref.Path, paths) {
			continue
		}
		rec.removed = true
		w.tx.undo = append(w.tx.undo, func() { rec.removed = false })
		ev.Items = append(ev.Items, formats.RemovedItem{Item: rec.ref, Version: rec.version, SHA256: rec.digests.SHA256})
	}
	w.h.mu.Unlock()
	if len(ev.Items) == 0 {
		return nil
	}
	ev.Packages = &writer{h: w.h, tx: w.tx, prefix: w.prefix}
	ev.Cascade = func(ctx context.Context, cascaded ...string) error {
		if guarded {
			for i, ok := range w.h.allowedPaths(formats.ActionDelete, cascaded) {
				if !ok {
					return errs.E(errs.Forbidden, errs.CodeForbidden, "The removal would also take "+cascaded[i]+", which the caller may not delete.")
				}
			}
		}
		return w.removeCovered(ctx, cascaded, reason, actor, guarded)
	}
	w.h.noteHook("OnItemsRemoved")
	return w.h.f.OnItemsRemoved(ctx, w.tx, ev)
}

// coveredBy reports whether the path is one of the paths or below one of them.
func coveredBy(p string, paths []string) bool {
	for _, q := range paths {
		if p == q || strings.HasPrefix(p, q+"/") {
			return true
		}
	}
	return false
}

// sortedItems lists the item records by path. The caller holds the Host's lock.
func (h *Host) sortedItems() []*itemRec {
	out := make([]*itemRec, 0, len(h.items))
	for _, rec := range h.items {
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b *itemRec) int { return strings.Compare(a.ref.Path, b.ref.Path) })
	return out
}

// Notify accepts the areas the format declared in its descriptor, as the production
// writer does, and attaches a notification once per (area, payload).
func (w *writer) Notify(area string, payload any) error {
	if area == "" {
		return errs.E(errs.Internal, errs.CodeInternal, "formattest: a notification has an area")
	}
	if !slices.Contains(w.h.f.Descriptor().NotifyAreas, area) {
		return errs.E(errs.Internal, errs.CodeInternal, "The format did not declare the notification area "+area+".")
	}
	w.tx.note(Notification{Area: area, Payload: payload})
	return nil
}

// packageStore reads the committed rows.
type packageStore struct{ h *Host }

func (s packageStore) Package(ctx context.Context, repo formats.RepoRef, name string) (formats.PackageRow, bool, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return formats.PackageRow{}, false, err
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	p, ok := s.h.pkgs[name]
	if !ok {
		return formats.PackageRow{}, false, nil
	}
	return formats.PackageRow{
		Ref:         formats.PackageRef{ID: p.id, Repo: s.h.ref, Name: p.name, RevSeq: p.rev},
		DisplayName: p.display, Attributes: p.attrs,
	}, true, nil
}

func (s packageStore) Versions(ctx context.Context, repo formats.RepoRef, name string) ([]formats.VersionRow, formats.RevSeq, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return nil, 0, err
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	p, ok := s.h.pkgs[name]
	if !ok {
		return nil, 0, nil
	}
	w := &writer{h: s.h}
	return w.live(p), p.rev, nil
}

func (s packageStore) RevSeqs(ctx context.Context, repos []formats.RepoRef, name string) (map[int64]formats.RevSeq, error) {
	for _, r := range repos {
		if err := formats.RepositoryAllowed(ctx, r); err != nil {
			return nil, err
		}
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	out := make(map[int64]formats.RevSeq, len(repos))
	for _, r := range repos {
		out[r.ID] = 0
		if r.ID == s.h.ref.ID {
			if p, ok := s.h.pkgs[name]; ok {
				out[r.ID] = p.rev
			}
		}
	}
	return out, nil
}

// RevSeq returns packages.rev_seq of a package of the addressed repository, 0 when
// the package does not exist.
func (h *Host) RevSeq(name string) formats.RevSeq {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.pkgs[name]; ok {
		return p.rev
	}
	return 0
}

// PutItem stores an item without running any hook: the state a case starts from.
func (h *Host) PutItem(p, mediaType string, data []byte) formats.ItemRef {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.putLocked(p, mediaType, data, nil).ref
}

// putLocked stores or replaces the item at p. undo, when not nil, receives the
// function that restores the previous state.
func (h *Host) putLocked(p, mediaType string, data []byte, undo *[]func()) *itemRec {
	old := h.items[p]
	rec := &itemRec{
		ref:  formats.ItemRef{ID: h.id(), Repo: h.ref, Path: p},
		data: append([]byte(nil), data...), digests: digests(data), media: mediaType, at: time.Now(),
	}
	h.items[p], h.byID[rec.ref.ID] = rec, rec
	if undo != nil {
		*undo = append(*undo, func() {
			delete(h.byID, rec.ref.ID)
			if old != nil {
				h.items[p] = old
			} else {
				delete(h.items, p)
			}
		})
	}
	return rec
}

type itemStore struct{ h *Host }

func (s itemStore) Stat(ctx context.Context, repo formats.RepoRef, p formats.Path) (formats.Item, bool, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return formats.Item{}, false, err
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	rec, ok := s.h.items[p.Path]
	if !ok || rec.removed {
		return formats.Item{}, false, nil
	}
	return s.h.itemOf(rec), true, nil
}

func (h *Host) itemOf(rec *itemRec) formats.Item {
	return formats.Item{
		Ref: rec.ref, Size: int64(len(rec.data)), Digests: rec.digests, MediaType: rec.media, Modified: rec.at,
		Version: rec.version, Handle: rec,
	}
}

func (s itemStore) Serve(ctx context.Context, rq *formats.Request, w http.ResponseWriter, it formats.Item, mode formats.ServeMode) error {
	rec, ok := it.Handle.(*itemRec)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The item was not read by this host.")
	}
	if err := s.h.f.ServeCheck(ctx, s.h, it.Ref.Repo, it.Ref, mode); err != nil {
		return err
	}
	s.h.mu.Lock()
	s.h.served = true
	s.h.servedPaths = append(s.h.servedPaths, it.Ref.Path)
	s.h.mu.Unlock()
	s.h.noteRead(rec.data)
	hd := w.Header()
	if rec.media != "" && hd.Get("Content-Type") == "" {
		hd.Set("Content-Type", rec.media)
	}
	hd.Set("Content-Length", strconv.Itoa(len(rec.data)))
	hd.Set("ETag", `"`+hex.EncodeToString(rec.digests.SHA256)+`"`)
	w.WriteHeader(http.StatusOK)
	if rq != nil && rq.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(rec.data)
	return err
}

func (s itemStore) Open(_ context.Context, it formats.Item) (io.ReadCloser, error) {
	rec, ok := it.Handle.(*itemRec)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The item was not read by this host.")
	}
	s.h.noteRead(rec.data)
	return io.NopCloser(bytes.NewReader(rec.data)), nil
}

// memSpool is a spooled body held in memory.
type memSpool struct {
	data []byte
	d    formats.Digests
}

var _ formats.Spooled = (*memSpool)(nil)

func (s *memSpool) Digests() formats.Digests { return s.d }
func (s *memSpool) Size() int64              { return int64(len(s.data)) }
func (s *memSpool) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *memSpool) ReaderAt() (io.ReaderAt, error) { return bytes.NewReader(s.data), nil }

// Spool wraps bytes as the spool of an upload, with their digests: what
// Ingest().Spool returns, for a test that calls Inspect directly.
func Spool(data []byte) formats.Spooled { return &memSpool{data: data, d: digests(data)} }

type ingestor struct{ h *Host }

func (i ingestor) Spool(_ context.Context, rq *formats.Request, body io.Reader, o formats.IngestOptions) (formats.Spooled, error) {
	r := body
	if o.MaxSize > 0 {
		r = io.LimitReader(body, o.MaxSize+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if o.MaxSize > 0 && int64(len(data)) > o.MaxSize {
		return nil, errs.E(errs.TooLarge, errs.CodeTooLarge, "The upload is larger than the limit.")
	}
	sp := &memSpool{data: data, d: digests(data)}
	for _, c := range []struct{ want, got []byte }{
		{o.Expect.SHA256, sp.d.SHA256}, {o.Expect.SHA512, sp.d.SHA512}, {o.Expect.SHA1, sp.d.SHA1}, {o.Expect.MD5, sp.d.MD5},
	} {
		if c.want != nil && !bytes.Equal(c.want, c.got) {
			return nil, errs.E(errs.Conflict, errs.CodeConflict, "The body does not match the digest the request announced.")
		}
	}
	_ = rq
	return sp, nil
}

// Commit stores the spooled body at the path and runs OnCommit in the same
// transaction (items.Store.Commit with Origin Upload): the item and every row the
// hook writes commit together or not at all. The same content at the same path is
// an identical commit that runs no hook.
func (i ingestor) Commit(ctx context.Context, rq *formats.Request, sp formats.Spooled, c formats.Commit) (formats.Committed, error) {
	s, ok := sp.(*memSpool)
	if !ok {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The body was not spooled by this host.")
	}
	h := i.h
	var out formats.Committed
	err := h.Write(ctx, rq, func(tx formats.Tx, w formats.PackageWriter) error {
		mt := tx.(*memTx)
		h.mu.Lock()
		old := h.items[c.Path.Path]
		if old != nil && !old.removed {
			if bytes.Equal(old.digests.SHA256, s.d.SHA256) {
				h.mu.Unlock()
				out = formats.Committed{Item: old.ref, Identical: true}
				return nil
			}
			if c.CreateOnly || !c.MayOverwrite {
				h.mu.Unlock()
				return errs.E(errs.Forbidden, errs.CodeForbidden, "The action overwrite is not permitted on "+c.Path.Path+".")
			}
		}
		rec := h.putLocked(c.Path.Path, c.MediaType, s.data, &mt.undo)
		h.mu.Unlock()
		replaced := old != nil && !old.removed
		out = formats.Committed{Item: rec.ref, Created: !replaced, Overwritten: replaced}
		h.noteHook("OnCommit")
		if err := h.f.OnCommit(ctx, tx, formats.CommitEvent{
			Origin: formats.OriginUpload, Repo: h.ref, Item: rec.ref, Path: c.Path, Size: int64(len(s.data)), Digests: rec.digests,
			Inspection: c.Inspection, Payload: c.Payload, Replaced: replaced, Packages: w, Data: h.opts.Data,
		}); err != nil {
			return err
		}
		// The companions commit in the transaction of the lead, after it, each with its own
		// hook (R-ARCH-009): everything one client request writes is one unit.
		for _, comp := range c.Companions {
			cs, ok := comp.Spooled.(*memSpool)
			if !ok {
				return errs.E(errs.Internal, errs.CodeInternal, "The body was not spooled by this host.")
			}
			h.mu.Lock()
			crec := h.putLocked(comp.Path.Path, comp.MediaType, cs.data, &mt.undo)
			h.mu.Unlock()
			out.Companions = append(out.Companions, crec.ref)
			if err := h.f.OnCommit(ctx, tx, formats.CommitEvent{
				Origin: formats.OriginUpload, Repo: h.ref, Item: crec.ref, Lead: rec.ref, Path: comp.Path, Size: int64(len(cs.data)),
				Digests: crec.digests, Payload: comp.Payload, Packages: w, Data: h.opts.Data,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (i ingestor) Close(formats.Spooled) {}

// Link commits an item with the content of the item named by from (the Host's own repository only), which the Host must hold live and the caller (HostOptions.Allow) may
// read; anything else is errs.NotFound, as the production host answers (ADR-161). The commit is Commit's, hooks included.
func (i ingestor) Link(ctx context.Context, rq *formats.Request, from formats.SourceRef, c formats.Commit) (formats.Committed, error) {
	h := i.h
	if from.Repo != h.ref.Name { // the in-memory Host holds one repository
		return formats.Committed{}, errs.E(errs.NotFound, errs.CodeNotFound, "The content is not stored.")
	}
	h.mu.Lock()
	rec := h.items[from.Path]
	var data []byte
	if rec != nil && !rec.removed {
		data = append([]byte(nil), rec.data...)
	}
	h.mu.Unlock()
	if rec == nil || rec.removed {
		return formats.Committed{}, errs.E(errs.NotFound, errs.CodeNotFound, "The content is not stored.")
	}
	if h.opts.Allow != nil {
		if err := h.opts.Allow(formats.Target{Path: from.Path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
			return formats.Committed{}, errs.E(errs.NotFound, errs.CodeNotFound, "The content is not stored.")
		}
	}
	if len(c.Companions) > 0 {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "A linked commit takes no companions.")
	}
	return i.Commit(ctx, rq, Spool(data), c)
}

// Remove removes the items at the paths and runs OnItemsRemoved in the same
// transaction; reason is "delete", "trash", "move" or "purge".
func (h *Host) Remove(ctx context.Context, rq *formats.Request, reason string, paths ...string) error {
	return h.Write(ctx, rq, func(tx formats.Tx, w formats.PackageWriter) error {
		if err := h.lockRemovalIn(ctx, tx, w, paths); err != nil {
			return err
		}
		mt := tx.(*memTx)
		wr := &writer{h: h, tx: mt, prefix: h.f.Descriptor().PropertyPrefix}
		// Only the listed files go (a path is a file here, as the items API removes them), and the writer of the event is the transaction's.
		ev := formats.ItemsEvent{Repo: h.ref, Reason: reason, Packages: w, Data: h.opts.Data}
		h.mu.Lock()
		for _, p := range paths {
			rec, ok := h.items[p]
			if !ok || rec.removed {
				continue
			}
			rec.removed = true
			mt.undo = append(mt.undo, func() { rec.removed = false })
			ev.Items = append(ev.Items, formats.RemovedItem{Item: rec.ref, Version: rec.version, SHA256: rec.digests.SHA256})
		}
		h.mu.Unlock()
		ev.Cascade = func(ctx context.Context, cascaded ...string) error {
			return wr.removeCovered(ctx, cascaded, reason, "", false)
		}
		h.noteHook("OnItemsRemoved")
		return h.f.OnItemsRemoved(ctx, tx, ev)
	})
}

// Restore restores removed items and runs the format's optional OnItemsRestored
// (formats.ItemsRestorer) in the same transaction.
func (h *Host) Restore(ctx context.Context, rq *formats.Request, paths ...string) error {
	return h.Write(ctx, rq, func(tx formats.Tx, w formats.PackageWriter) error {
		ev := formats.ItemsEvent{Repo: h.ref, Reason: "restore", Packages: w, Data: h.opts.Data}
		h.mu.Lock()
		for _, p := range paths {
			rec, ok := h.items[p]
			if !ok || !rec.removed {
				continue
			}
			rec.removed = false
			tx.(*memTx).undo = append(tx.(*memTx).undo, func() { rec.removed = true })
			ev.Items = append(ev.Items, formats.RemovedItem{Item: rec.ref, Version: rec.version, SHA256: rec.digests.SHA256})
		}
		h.mu.Unlock()
		r := formats.CapabilitiesOf(h.f).ItemsRestorer
		if r == nil {
			return nil // the format keeps no state for a restored item
		}
		h.noteHook("OnItemsRestored")
		return r.OnItemsRestored(ctx, tx, ev)
	})
}

// MemRemote is an in-memory formats.RemoteFetcher over fixed documents and
// artifacts. A Host it is given to records what a handler reads through it.
type MemRemote struct {
	// Docs are the stored metadata documents by canonical path; Artifacts the
	// artifacts by canonical path.
	Docs      map[string][]byte
	Artifacts map[string][]byte
	// Exchanges answers Exchange; nil answers it with [formats.ErrOffline].
	Exchanges func(ctx context.Context, rq *formats.Request, x formats.Exchange) (*http.Response, error)
	// Queries answers Query; nil answers it with [formats.ErrOffline].
	Queries func(ctx context.Context, rq *formats.Request, q formats.Query) (*formats.QueryResult, error)

	note func([]byte)
}

var _ formats.RemoteFetcher = (*MemRemote)(nil)

func (m *MemRemote) read(b []byte) {
	if m.note != nil {
		m.note(b)
	}
}

func (m *MemRemote) doc(p formats.Path) (*formats.RemoteDocument, error) {
	b, ok := m.Docs[p.Path]
	if !ok {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no such document.")
	}
	d := digests(b)
	var dig [32]byte
	copy(dig[:], d.SHA256)
	return &formats.RemoteDocument{
		Digest: dig, Size: int64(len(b)),
		Bytes: func(context.Context) ([]byte, error) { m.read(b); return append([]byte(nil), b...), nil },
	}, nil
}

func (m *MemRemote) Metadata(_ context.Context, _ *formats.Request, p formats.Path, _ string) (*formats.RemoteDocument, error) {
	return m.doc(p)
}

func (m *MemRemote) Artifact(_ context.Context, _ *formats.Request, p formats.Path, _ formats.Digests, w http.ResponseWriter) error {
	b, ok := m.Artifacts[p.Path]
	if !ok {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no such artifact.")
	}
	m.read(b)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(b)
	return err
}

func (m *MemRemote) Cached(context.Context, *formats.Request, formats.Path) (formats.Item, bool, error) {
	return formats.Item{}, false, nil
}

func (m *MemRemote) EnsureCurrent(_ context.Context, _ *formats.Request, p formats.Path, _ string) (*formats.RemoteDocument, formats.Currency, error) {
	d, err := m.doc(p)
	return d, formats.Current, err
}

func (m *MemRemote) EntryState(_ context.Context, _ *formats.Request, p formats.Path) (formats.EntryState, error) {
	_, ok := m.Docs[p.Path]
	return formats.EntryState{Stored: ok}, nil
}

// Query answers with Queries, or with [formats.ErrOffline].
func (m *MemRemote) Query(ctx context.Context, rq *formats.Request, q formats.Query) (*formats.QueryResult, error) {
	if m.Queries == nil {
		return nil, formats.ErrOffline
	}
	return m.Queries(ctx, rq, q)
}

// Gate allows every artifact: the fake has no marks.
func (m *MemRemote) Gate(context.Context, *formats.Request, formats.Path) error { return nil }

// GateWith allows every artifact and keeps the facts it was given.
func (m *MemRemote) GateWith(_ context.Context, _ *formats.Request, _ formats.Path, have formats.ServeFacts) (formats.ServeFacts, error) {
	return have, nil
}

func (m *MemRemote) Exchange(ctx context.Context, rq *formats.Request, x formats.Exchange) (*http.Response, error) {
	if m.Exchanges == nil {
		return nil, formats.ErrOffline
	}
	return m.Exchanges(ctx, rq, x)
}

// lockRemovalIn runs the format's optional RemovalLocker (formats.RemovalLocker) with the live files the paths cover, before the rows are
// marked removed, as the item store does before it deletes them.
func (h *Host) lockRemovalIn(ctx context.Context, tx formats.Tx, w formats.PackageWriter, paths []string) error {
	l := formats.CapabilitiesOf(h.f).RemovalLocker
	if l == nil {
		return nil
	}
	h.mu.Lock()
	var files []string
	for _, rec := range h.sortedItems() {
		if !rec.removed && coveredBy(rec.ref.Path, paths) {
			files = append(files, rec.ref.Path)
		}
	}
	h.mu.Unlock()
	if len(files) == 0 {
		return nil
	}
	h.noteHook("LockRemoval")
	return l.LockRemoval(ctx, tx, w, h.ref, files)
}
