package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// localStore is the group of the data port that holds the versions, tombstones and import tasks of a local repository and the step of
// the reindex (R-ANS-008 to R-ANS-012).
type localStore struct{ *group }

func errForeignTx() error {
	return errs.E(errs.Internal, errs.CodeInternal, "ansible: the transaction is not the PostgreSQL store's")
}

func txQueries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx()
	}
	return q.New(ptx), nil
}

// read runs fn in the transaction when there is one and in a short one of its own otherwise.
func (g *group) read(ctx context.Context, tx meta.Tx, fn func(qs *q.Queries) error) error {
	run := func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	}
	if tx != nil {
		return run(tx)
	}
	return g.o.DB.InTx(ctx, run)
}

func stamp(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func uuidOK(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func digestOf(b []byte) (d [32]byte) {
	copy(d[:], b)
	return d
}

func versionRow(r q.AnsibleCollectionVersion) rows.VersionRow {
	v := rows.VersionRow{
		RepoID: r.RepoID, Name: r.Name, Version: r.Version, SortKey: r.SortKey, ItemID: r.ItemID, SHA256: digestOf(r.Sha256), Size: r.Size,
		RequiresAnsible: r.RequiresAnsible, CollectionInfo: r.CollectionInfo, PublishedBy: r.PublishedBy, PublishedAt: r.PublishedAt.UTC(),
	}
	_ = json.Unmarshal(r.Dependencies, &v.Dependencies) //nolint:errcheck // a column this package wrote; an unreadable one is no dependencies
	return v
}

// event completes an audit event of the data port (R-ANS-030): the repository, the path as the subject and the actor.
func (g *group) event(ctx context.Context, typ string, a rows.Audit, name, version string, details, journal []byte) audit.Event {
	path := name
	if version != "" {
		path = name + "/" + version
	}
	ev := audit.Event{
		Type: typ, Outcome: audit.Success,
		Actor:      audit.Actor{Kind: a.Actor.Kind, ID: a.Actor.ID, Name: a.Actor.Name, TokenID: a.Actor.TokenID, AuthMethod: a.Actor.AuthMethod},
		Subject:    audit.Subject{Kind: "package", Name: a.RepoKey + "/" + name},
		Repository: a.RepoKey, Path: path, Endpoint: a.Endpoint, Details: details, Journal: journal,
	}
	if g.o.Source != nil {
		ev.Source = g.o.Source(ctx)
	}
	if ev.Actor.Kind == "" {
		ev.Actor = g.actorOf(ctx, a.Actor.ID)
	}
	return ev
}

// actorOf completes the actor of an event the format could name only by the principal's id: the actor of the request when it is the
// principal with that id, else a user with that id, else the system actor "removal".
func (g *group) actorOf(ctx context.Context, id string) audit.Actor {
	if id == "" {
		return audit.Actor{Kind: "system", Name: "removal"}
	}
	if g.o.Actor != nil {
		if a := g.o.Actor(ctx); a.Kind != "" && a.ID == id {
			return a
		}
	}
	return audit.Actor{Kind: "user", ID: id}
}

// noActor is the id a tombstone records for a removal that no principal asked for: the column is text and not null.
const noActor = "00000000-0000-0000-0000-000000000000"

func actorID(a rows.Audit) string {
	if a.Actor.ID == "" {
		return noActor
	}
	return a.Actor.ID
}

// PutVersion stores the row of a version and records package.published (R-ANS-011, R-ANS-030).
func (s localStore) PutVersion(ctx context.Context, tx meta.Tx, v rows.VersionRow, a rows.Audit, p rows.PublishInfo) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	deps := v.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	depsJSON, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	if len(v.CollectionInfo) == 0 {
		v.CollectionInfo = json.RawMessage(`{}`)
	}
	if err := qs.AnsibleUpsertVersion(ctx, q.AnsibleUpsertVersionParams{
		RepoID: v.RepoID, Name: v.Name, Version: v.Version, SortKey: v.SortKey, ItemID: v.ItemID, Sha256: v.SHA256[:], Size: v.Size,
		RequiresAnsible: v.RequiresAnsible, Dependencies: depsJSON, CollectionInfo: v.CollectionInfo, PublishedBy: v.PublishedBy,
		PublishedAt: stamp(v.PublishedAt),
	}); err != nil {
		return err
	}
	ns, name, _ := collection.SplitName(v.Name)
	details := map[string]any{
		"package": v.Name, "version": v.Version,
		"files": []map[string]any{{"path": "collections/" + ns + "/" + name + "/" + collection.FileName(ns, name, v.Version), "sha256": hex.EncodeToString(v.SHA256[:]), "size": v.Size}},
	}
	if p.Claim != "" {
		details["claim"] = p.Claim
	}
	if p.ShadowUndetermined {
		details["shadowCheck"] = "undetermined"
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if err := s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventPackagePublished, a, v.Name, v.Version, raw, nil)); err != nil {
		return err
	}
	if len(p.Shadowed) == 0 {
		return nil
	}
	// A first version of a name a remote member serves, published on the strength of manage: the security event of R-REPO-048, in the
	// commit transaction.
	exposure := make([]map[string]string, len(p.Shadowed))
	for i, x := range p.Shadowed {
		exposure[i] = map[string]string{"virtual": x.Virtual, "remote": x.Remote}
	}
	raw, err = json.Marshal(map[string]any{"package": v.Name, "exposure": exposure})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventRepositoryRemotePackageShadowed, a, v.Name, "", raw, nil))
}

// RemoveVersion deletes the row of a version and leaves its tombstone with the journaled event (R-ANS-012, R-ANS-029).
func (s localStore) RemoveVersion(ctx context.Context, tx meta.Tx, repoID int64, name, stored string, sha256 []byte, tombstone bool, a rows.Audit) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	number := version.Number(stored)
	gone, err := qs.AnsibleDeleteVersion(ctx, q.AnsibleDeleteVersionParams{RepoID: repoID, Name: name, Version: stored})
	if err != nil || !tombstone {
		return err
	}
	if len(sha256) != 32 && len(gone) > 0 {
		sha256 = gone[0].Sha256
	}
	if len(sha256) != 32 {
		return errs.E(errs.Internal, errs.CodeInternal, "The digest of a removed Ansible Galaxy archive is not known for its tombstone.")
	}
	n, err := qs.AnsibleInsertTombstone(ctx, q.AnsibleInsertTombstoneParams{RepoID: repoID, Name: name, Version: number, Sha256: sha256, DeletedBy: actorID(a)})
	if err != nil || n == 0 {
		return err
	}
	sum := hex.EncodeToString(sha256)
	details, err := json.Marshal(map[string]any{"name": name, "version": number, "stored": stored, "sha256": sum})
	if err != nil {
		return err
	}
	entry, err := json.Marshal(map[string]any{"repository": repoID, "name": name, "version": number, "stored": stored, "sha256": sum, "actorId": a.Actor.ID})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventAnsibleVersionDeleted, a, name, number, details, entry))
}

// Version reads the row of a version.
func (s localStore) Version(ctx context.Context, repoID int64, name, version string) (rows.VersionRow, bool, error) {
	var out rows.VersionRow
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleGetVersion(ctx, q.AnsibleGetVersionParams{RepoID: repoID, Name: name, Version: version})
		if err != nil || len(list) == 0 {
			return err
		}
		out, found = versionRow(list[0]), true
		return nil
	})
	return out, found, err
}

// Versions lists the versions of a collection newest first by precedence, with the count of all of them.
func (s localStore) Versions(ctx context.Context, repoID int64, name string, limit, offset int) ([]rows.VersionRow, int, error) {
	var out []rows.VersionRow
	var total int
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		n, err := qs.AnsibleCountVersions(ctx, q.AnsibleCountVersionsParams{RepoID: repoID, Name: name})
		if err != nil {
			return err
		}
		total = int(n)
		list, err := qs.AnsibleListVersions(ctx, q.AnsibleListVersionsParams{RepoID: repoID, Name: name, RowLimit: int32(min(limit, 1<<30)), RowOffset: int32(min(offset, 1<<30))}) //nolint:gosec // bounded above
		if err != nil {
			return err
		}
		for _, r := range list {
			out = append(out, versionRow(r))
		}
		return nil
	})
	return out, total, err
}

// Collection reads what the collection document shows; found is false for a name without versions.
func (s localStore) Collection(ctx context.Context, repoID int64, name string) (rows.CollectionSummary, bool, error) {
	var out rows.CollectionSummary
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		r, err := qs.AnsibleCollectionSummary(ctx, q.AnsibleCollectionSummaryParams{RepoID: repoID, Name: name})
		if err != nil || r.Versions == 0 {
			return err
		}
		ns, n, _ := collection.SplitName(name)
		out = rows.CollectionSummary{
			Namespace: ns, Name: n, CollectionID: rows.CollectionUUID(repoID, name), HighestVersion: r.Highest,
			CreatedAt: time.UnixMicro(r.CreatedUs).UTC(), UpdatedAt: time.UnixMicro(r.UpdatedUs).UTC(),
		}
		found = true
		return nil
	})
	return out, found, err
}

// Tombstoned reads the digest of the deleted version of the name; ok is false when the number is free.
func (s localStore) Tombstoned(ctx context.Context, repoID int64, name, ver string) ([]byte, bool, error) {
	var sum []byte
	ok := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleGetTombstone(ctx, q.AnsibleGetTombstoneParams{RepoID: repoID, Name: name, Version: version.Number(ver)})
		if err != nil || len(list) == 0 {
			return err
		}
		sum, ok = list[0].Sha256, true
		return nil
	})
	return sum, ok, err
}

// ClearTombstone deletes a tombstone and writes the journaled event ansible.tombstone.cleared in the same transaction.
func (s localStore) ClearTombstone(ctx context.Context, repoID int64, name, ver string, a rows.Audit) (bool, error) {
	found := false
	number := version.Number(ver)
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		n, err := qs.AnsibleDeleteTombstone(ctx, q.AnsibleDeleteTombstoneParams{RepoID: repoID, Name: name, Version: number})
		if err != nil || n == 0 {
			return err
		}
		found = true
		details, err := json.Marshal(map[string]any{"name": name, "version": number})
		if err != nil {
			return err
		}
		entry, err := json.Marshal(map[string]any{"repository": repoID, "name": name, "version": number, "actorId": a.Actor.ID})
		if err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventAnsibleTombstoneCleared, a, name, number, details, entry))
	})
	if err != nil {
		found = false
	}
	return found, err
}

// Tombstones lists the tombstones in name and version order after the cursor "<name>/<version>".
func (s localStore) Tombstones(ctx context.Context, repoID int64, after string, limit int) ([]rows.Tombstone, error) {
	afterName, afterVersion, _ := strings.Cut(after, "/")
	var out []rows.Tombstone
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleListTombstones(ctx, q.AnsibleListTombstonesParams{
			RepoID: repoID, AfterName: afterName, AfterVersion: afterVersion, RowLimit: int32(min(limit, 1<<20)), //nolint:gosec // bounded above
		})
		if err != nil {
			return err
		}
		for _, t := range list {
			out = append(out, rows.Tombstone{
				RepoID: t.RepoID, Name: t.Name, Version: t.Version, SHA256: digestOf(t.Sha256), DeletedAt: t.DeletedAt.UTC(), DeletedBy: t.DeletedBy,
			})
		}
		return nil
	})
	return out, err
}

type messageJSON struct {
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// CreateTask records an import task: in the publish transaction, or alone for a failed one, which also writes the event
// ansible.import_task.failed (R-ANS-009, R-ANS-030).
func (s localStore) CreateTask(ctx context.Context, tx meta.Tx, t rows.Task, a rows.Audit) error {
	if !uuidOK(t.ID) {
		return errs.E(errs.Internal, errs.CodeInternal, "ansible: an import task needs a UUID")
	}
	msgs := make([]messageJSON, len(t.Messages))
	for i, m := range t.Messages {
		msgs[i] = messageJSON{Level: m.Level, Message: m.Message, Time: m.Time}
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	by := t.CreatedBy
	if by == "" {
		by = noActor
	}
	full := t.Name
	if t.Namespace != "" {
		full = t.Namespace + "." + t.Name
	}
	err = s.read(ctx, tx, func(qs *q.Queries) error {
		return qs.AnsibleInsertTask(ctx, q.AnsibleInsertTaskParams{
			ID: t.ID, RepoID: t.RepoID, Name: full, Version: t.Version, State: t.State,
			ErrorCode: t.ErrorCode, ErrorDescription: t.ErrorDescription, Messages: raw, CreatedBy: by,
			CreatedAt: stamp(t.CreatedAt), FinishedAt: stamp(t.FinishedAt),
		})
	})
	if err != nil || t.State != rows.TaskFailed {
		return err
	}
	details, err := json.Marshal(map[string]any{"code": t.ErrorCode, "reason": truncate(t.ErrorDescription, 64), "name": full, "version": t.Version})
	if err != nil {
		return err
	}
	ev := s.event(ctx, audit.EventAnsibleImportTaskFailed, a, full, t.Version, details, nil)
	ev.Outcome = audit.Failure
	ev.Reason = t.ErrorCode
	s.o.Recorder.Record(ctx, ev)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func taskRow(r q.AnsibleGetTaskRow) rows.Task {
	t := rows.Task{
		ID: r.ID, State: r.State, RepoID: r.RepoID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.UTC(), FinishedAt: r.FinishedAt.UTC(),
		StartedAt: r.CreatedAt.UTC(), UpdatedAt: r.FinishedAt.UTC(), ErrorCode: r.ErrorCode, ErrorDescription: r.ErrorDescription,
		Name: r.Name, Version: r.Version,
	}
	t.Namespace, t.Name, _ = splitTaskName(r.Name)
	var ms []messageJSON
	if json.Unmarshal(r.Messages, &ms) == nil {
		for _, m := range ms {
			t.Messages = append(t.Messages, rows.TaskMessage{Level: m.Level, Message: m.Message, Time: m.Time})
		}
	}
	return t
}

// splitTaskName splits a stored "<ns>.<name>" into the namespace and the name; a failed task of an archive that named none holds "".
func splitTaskName(full string) (ns, name string, ok bool) {
	ns, name, ok = collection.SplitName(full)
	if !ok {
		return "", full, false
	}
	return ns, name, true
}

// Task reads a task of the repository; found is false for an unknown id.
func (s localStore) Task(ctx context.Context, repoID int64, id string) (rows.Task, bool, error) {
	if !uuidOK(id) {
		return rows.Task{}, false, nil
	}
	var out rows.Task
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleGetTask(ctx, q.AnsibleGetTaskParams{RepoID: repoID, ID: id})
		if err != nil || len(list) == 0 {
			return err
		}
		out, found = taskRow(list[0]), true
		return nil
	})
	return out, found, err
}

// Tasks lists the tasks newest first, optionally of one state, after the cursor (the id of the last one seen).
func (s localStore) Tasks(ctx context.Context, repoID int64, f rows.TaskFilter, after string, limit int) ([]rows.Task, error) {
	if after != "" {
		if !uuidOK(after) {
			return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The cursor is not one this list issued.")
		}
	}
	var out []rows.Task
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.AnsibleListTasks(ctx, q.AnsibleListTasksParams{RepoID: repoID, State: f.State, Name: f.Name, Version: f.Version, After: after, RowLimit: int32(min(limit, 1<<20))}) //nolint:gosec // bounded above
		if err != nil {
			return err
		}
		for _, r := range list {
			out = append(out, taskRow(q.AnsibleGetTaskRow(r)))
		}
		return nil
	})
	return out, err
}

// jsonOf encodes a map of dependencies; nil is the empty object.
func jsonOf(m map[string]string) (json.RawMessage, error) {
	if m == nil {
		m = map[string]string{}
	}
	return json.Marshal(m)
}
