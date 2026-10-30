package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The audit event types of this area (R-OPS-016), registered in
// internal/audit/events_repository.go.
const (
	EventCreated = "repository.created"
	EventUpdated = "repository.updated"
	EventDeleted = "repository.deleted"
)

// Notifier attaches a change of a change area to the writing transaction
// (R-ARCH-018 step 1). *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// JobEnqueuer enqueues a job in the caller's transaction. *jobs.Queue implements
// it.
type JobEnqueuer interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// Hooks lets another area write in the transaction of a repository update or
// deletion. The authorization area uses OnUpdated to record
// authz.settings.changed when cacheFillAction, packageOwnership,
// requireAuthentication or hideExistence changed (R-AUTHZ-031), and OnDelete to
// record the exposure rules that the removal of the row takes with it (H-164).
// A hook runs in the same transaction as the write, its audit event and its
// change-log row; an error fails the whole transaction.
type Hooks interface {
	OnUpdated(ctx context.Context, tx meta.Tx, caller Caller, before, after Repository) error
	// OnDelete runs in the transaction that removes the repository row, just
	// before the row goes: what refers to the row by a cascading foreign key is
	// still readable. The caller of a deletion that ran as a job is the actor
	// stored with the job (no source).
	OnDelete(ctx context.Context, tx meta.Tx, caller Caller, r Repository) error
}

// MultiHooks runs several hooks in order and stops at the first error.
func MultiHooks(hooks ...Hooks) Hooks { return multiHooks(hooks) }

type multiHooks []Hooks

func (m multiHooks) OnUpdated(ctx context.Context, tx meta.Tx, c Caller, before, after Repository) error {
	for _, h := range m {
		if err := h.OnUpdated(ctx, tx, c, before, after); err != nil {
			return err
		}
	}
	return nil
}

func (m multiHooks) OnDelete(ctx context.Context, tx meta.Tx, c Caller, r Repository) error {
	for _, h := range m {
		if err := h.OnDelete(ctx, tx, c, r); err != nil {
			return err
		}
	}
	return nil
}

// Options configure [NewService].
type Options struct {
	Runner meta.Runner
	// Catalog is the format catalog the model validates against (R-REPO-003);
	// nil is catalog.Default.
	Catalog  *catalog.Registry
	Recorder audit.Recorder
	Notifier Notifier
	Jobs     JobEnqueuer
	// Hooks is optional.
	Hooks Hooks
	Log   *slog.Logger
	// DeleteBatch is the number of items one transaction of the deletion job
	// removes (storage.delete.batch_size, R-STOR-018); zero means 5000.
	DeleteBatch int
}

// Service is the write and management-read side of the repository model:
// create, read, update, list and delete of R-REPO-009 to R-REPO-013 as domain
// functions. The HTTP layer authorizes first and calls these; it adds only the
// headers and the paging (merge patch and If-Match are inputs here, so a handler
// is thin). Reads here come from PostgreSQL (Q-API-12); the per-request lookup
// uses the snapshot of [Reader].
type Service struct {
	run   meta.Runner
	cat   *catalog.Registry
	rec   audit.Recorder
	notif Notifier
	jobs  JobEnqueuer
	hooks Hooks
	log   *slog.Logger
	batch int
}

// NewService creates the service.
func NewService(o Options) (*Service, error) {
	switch {
	case o.Runner == nil:
		return nil, errors.New("repository: no transaction runner")
	case o.Notifier == nil:
		return nil, errors.New("repository: no change notifier")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.DeleteBatch <= 0 {
		o.DeleteBatch = 5000
	}
	return &Service{
		run: o.Runner, cat: catalog.Or(o.Catalog), rec: o.Recorder, notif: o.Notifier, jobs: o.Jobs,
		hooks: o.Hooks, log: o.Log, batch: o.DeleteBatch,
	}, nil
}

// Caller is who acts and from where; it goes into the audit event and, as the
// principal id, into created_by and updated_by. Endpoint is the endpoint the
// request arrived on, for the events a hook records on the way (the audit
// recorder fills the column from the event alone); empty outside a request.
type Caller struct {
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
}

// principalID returns the actor id when it is a uuid and "" otherwise (a break-glass
// or admin-cli actor has none).
func (c Caller) principalID() string { return principal.IDOrEmpty(c.Actor.ID) }

// Stored is a repository as the management API returns it: the read model, the
// full document and the warnings of the write that produced it.
type Stored struct {
	Repository Repository
	Document   Document
	Warnings   []Warning
}

// Precondition is the If-Match of an update (R-REPO-011): either a config_version
// or the explicit blind write "*". The zero value is invalid; the handler answers
// 428 precondition_required before it calls the service.
type Precondition struct {
	Version int64
	Any     bool
}

// MatchVersion requires the stored config_version to equal v.
func MatchVersion(v int64) Precondition { return Precondition{Version: v} }

// MatchAny is If-Match: *, which passes for a repository that exists.
func MatchAny() Precondition { return Precondition{Any: true} }

// stale is the 412 of a mismatching config_version (R-REPO-011).
func stale(current int64) error { return errs.Stale("The repository", current) }

// Create implements R-REPO-009: validate, then write the row, the audit event and
// the change-log row in one transaction. Create is never an upsert: a key that is
// taken, also by a repository being deleted, answers 409 already_exists.
func (s *Service) Create(ctx context.Context, c Caller, body []byte) (Stored, error) {
	var out Stored
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		var err error
		out, err = s.CreateTx(ctx, tx, c, body)
		return err
	})
	if err != nil {
		return Stored{}, err
	}
	return out, nil
}

// CreateTx is Create in a transaction the caller owns: the management API runs it
// inside the transaction of an Idempotency-Key (R-API-014), so the stored outcome
// and the repository commit together. A validation failure is returned before
// anything is written.
func (s *Service) CreateTx(ctx context.Context, tx meta.Tx, c Caller, body []byte) (Stored, error) {
	d, err := DecodeDocument(body)
	if err != nil {
		return Stored{}, err
	}
	_, v := validate(s.cat, &d, true)
	if err := v.err(); err != nil {
		return Stored{}, err
	}
	cfg, err := d.marshalConfig()
	if err != nil {
		return Stored{}, err
	}
	qs, err := queries(tx)
	if err != nil {
		return Stored{}, err
	}
	rows, err := qs.InsertRepository(ctx, q.InsertRepositoryParams{
		Key: d.Key, Format: d.Format, Class: string(d.Class), Config: string(cfg), Actor: c.principalID(),
	})
	if err != nil {
		return Stored{}, err
	}
	if len(rows) == 0 {
		return Stored{}, errs.E(errs.Conflict, errs.CodeAlreadyExists,
			"A repository with the key "+strconv.Quote(d.Key)+" exists or is still being deleted; a key stays taken until the deletion job completes.")
	}
	out, err := s.stored(rows[0], v.warnings)
	if err != nil {
		return Stored{}, err
	}
	if err := s.afterWrite(ctx, tx, c, EventCreated, out.Repository, []string{"key", "class", "format"}); err != nil {
		return Stored{}, err
	}
	return out, nil
}

// Get implements R-REPO-010: the stored document, read from PostgreSQL. A
// repository being deleted is returned with Lifecycle Deleting; the handler shows
// it only to holders of repositories:read-config.
func (s *Service) Get(ctx context.Context, key string) (Stored, error) {
	rows, err := readRows(ctx, s.run, func(qs *q.Queries) ([]q.Repository, error) {
		return qs.GetRepositoryByKey(ctx, key)
	})
	if err != nil {
		return Stored{}, err
	}
	if len(rows) == 0 {
		return Stored{}, notFound(key)
	}
	return s.stored(rows[0], nil)
}

// Replace implements PUT of R-REPO-011: the body is a complete document and
// omitted settings revert to their defaults. Key, class and format must equal the
// stored ones (422 validation_failed with the field code immutable otherwise).
func (s *Service) Replace(ctx context.Context, c Caller, key string, pre Precondition, body []byte) (Stored, error) {
	return s.update(ctx, c, key, pre, func(Document) (Document, error) { return DecodeDocument(body) })
}

// Patch implements PATCH of R-REPO-011: patch is an RFC 7396 merge patch applied
// to the stored document, arrays replaced as a whole and null removing a setting.
func (s *Service) Patch(ctx context.Context, c Caller, key string, pre Precondition, patch []byte) (Stored, error) {
	return s.update(ctx, c, key, pre, func(cur Document) (Document, error) {
		base, err := json.Marshal(cur)
		if err != nil {
			return Document{}, err
		}
		merged, err := MergePatch(base, patch)
		if err != nil {
			return Document{}, err
		}
		return DecodeDocument(merged)
	})
}

func (s *Service) update(ctx context.Context, c Caller, key string, pre Precondition,
	build func(cur Document) (Document, error),
) (Stored, error) {
	if !pre.Any && pre.Version < 1 {
		return Stored{}, errors.New("repository: an update needs a precondition")
	}
	var out Stored
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.LockRepositoryByKey(ctx, key)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound(key)
		}
		row := rows[0]
		if Lifecycle(row.Lifecycle) == Deleting {
			return errs.E(errs.Conflict, errs.CodeConflict, "the repository is being deleted")
		}
		if !pre.Any && pre.Version != row.ConfigVersion {
			return stale(row.ConfigVersion)
		}
		cur, err := toDocument(row, s.cat)
		if err != nil {
			return fmt.Errorf("repository: stored document of %q: %w", key, err)
		}
		before, _ := toRepository(row, s.cat)
		nd, err := build(cur)
		if err != nil {
			return err
		}
		nd, v := s.checkUpdate(cur, nd)
		if err := v.err(); err != nil {
			return err
		}
		newCfg, err := nd.marshalConfig()
		if err != nil {
			return err
		}
		oldCfg, err := cur.marshalConfig()
		if err != nil {
			return err
		}
		changes := changedMembers(oldCfg, newCfg)
		if len(changes) == 0 {
			out, err = s.stored(row, v.warnings) // nothing to write: same version, no event
			return err
		}
		updated, err := qs.UpdateRepositoryConfig(ctx, q.UpdateRepositoryConfigParams{
			ID: row.ID, Version: row.ConfigVersion, Config: string(newCfg), Actor: c.principalID(),
		})
		if err != nil {
			return err
		}
		if len(updated) == 0 {
			return errors.New("repository: the update matched no row although the row was locked")
		}
		out, err = s.stored(updated[0], v.warnings)
		if err != nil {
			return err
		}
		if err := s.afterWrite(ctx, tx, c, EventUpdated, out.Repository, changes); err != nil {
			return err
		}
		if s.hooks != nil {
			return s.hooks.OnUpdated(ctx, tx, c, before, out.Repository)
		}
		return nil
	})
	if err != nil {
		return Stored{}, err
	}
	return out, nil
}

// checkUpdate puts the stored identity back into the new document, collecting
// immutable violations for the members the caller changed, and validates
// the rest in the same pass (R-REPO-008).
func (s *Service) checkUpdate(cur, nd Document) (Document, *validation) {
	var imm []errs.FieldError
	for _, f := range []struct {
		ptr       string
		got, want string
	}{{"/key", nd.Key, cur.Key}, {"/class", string(nd.Class), string(cur.Class)}, {"/format", nd.Format, cur.Format}} {
		if f.got != "" && f.got != f.want { // an omitted identity member means "unchanged"
			imm = append(imm, errs.FieldError{
				Pointer: f.ptr, Code: codeImmutable,
				Detail: "key, class and format are fixed at creation",
			})
		}
	}
	nd.Key, nd.Class, nd.Format = cur.Key, cur.Class, cur.Format
	_, v := validate(s.cat, &nd, false)
	v.fields = append(imm, v.fields...)
	return nd, v
}

// changedMembers lists the top-level members whose value differs between two
// stored configurations, sorted.
func changedMembers(a, b []byte) []string {
	var ma, mb map[string]json.RawMessage
	_ = json.Unmarshal(a, &ma)
	_ = json.Unmarshal(b, &mb)
	set := map[string]bool{}
	for k, v := range ma {
		if w, ok := mb[k]; !ok || string(w) != string(v) {
			set[k] = true
		}
	}
	for k := range mb {
		if _, ok := ma[k]; !ok {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Service) stored(row q.Repository, warnings []Warning) (Stored, error) {
	r, err := toRepository(row, s.cat)
	if err != nil {
		s.log.Warn("repository: stored configuration does not load", "key", row.Key, "error", err)
	}
	d, derr := toDocument(row, s.cat)
	if derr != nil {
		return Stored{}, fmt.Errorf("repository: stored document of %q: %w", row.Key, derr)
	}
	return Stored{Repository: r, Document: d, Warnings: warnings}, nil
}

// changePayload is the payload of a repo_config change: the repository id, never
// a grant (R-REPO-008).
type changePayload struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	Op  string `json:"op"`
}

// eventDetails is the data of the repository.* events (R-OPS-016).
type eventDetails struct {
	Class   Class    `json:"class"`
	Format  string   `json:"format"`
	Changes []string `json:"changes"`
	Phase   string   `json:"phase,omitempty"`
}

// afterWrite records the audit event and the change-log row of a write in the
// writer's transaction.
func (s *Service) afterWrite(ctx context.Context, tx meta.Tx, c Caller, event string, r Repository,
	changes []string,
) error {
	return s.recordAndNotify(ctx, tx, c, event, r, eventDetails{Class: r.Class, Format: r.Format, Changes: changes},
		strings.TrimPrefix(event, "repository."))
}

func (s *Service) recordAndNotify(ctx context.Context, tx meta.Tx, c Caller, event string, r Repository,
	d eventDetails, op string,
) error {
	if d.Changes == nil {
		d.Changes = []string{}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.rec.RecordTx(ctx, tx, audit.Event{
		Type: event, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject: audit.Subject{Kind: "repository", ID: r.ID, Name: r.Key}, Details: raw,
	}); err != nil {
		return err
	}
	return s.notif.Notify(tx, cluster.AreaRepoConfig, 1, changePayload{ID: r.ID, Key: r.Key, Op: op})
}

// ListFilter selects repositories (R-REPO-012). Class and format values are
// spelled as in documents; Query matches key and description case-insensitively.
type ListFilter struct {
	Classes, Formats []string
	Query            string
	// State "inactive" keeps disabled repositories. Offline and circuit-open remote
	// repositories (R-REPO-029) are inactive too once remote repositories exist.
	State string
	// IncludeDeleting adds repositories being deleted, for holders of
	// repositories:read-config.
	IncludeDeleting bool
	// After is the keyset cursor: the last key of the previous page. Limit 0 means
	// no limit.
	After string
	Limit int
}

// ListResult is one page.
type ListResult struct {
	Items   []Stored
	HasMore bool
}

// List implements the data side of R-REPO-012, sorted by key. The caller filters
// the result through authorization (a listing contains only what the caller may
// read) and pages with a cursor; the filter here narrows by the request's
// parameters. The number of repositories of an installation is small, so the
// filter runs over one read of the table.
func (s *Service) List(ctx context.Context, f ListFilter) (ListResult, error) {
	rows, err := readRows(ctx, s.run, func(qs *q.Queries) ([]q.Repository, error) { return qs.ListRepositories(ctx) })
	if err != nil {
		return ListResult{}, err
	}
	needle := strings.ToLower(f.Query)
	var res ListResult
	for _, row := range rows {
		if f.After != "" && row.Key <= f.After {
			continue
		}
		if Lifecycle(row.Lifecycle) == Deleting && !f.IncludeDeleting {
			continue
		}
		if len(f.Classes) > 0 && !slices.Contains(f.Classes, row.Class) {
			continue
		}
		if len(f.Formats) > 0 && !slices.Contains(f.Formats, row.Format) {
			continue
		}
		st, err := s.stored(row, nil)
		if err != nil {
			return ListResult{}, err
		}
		if needle != "" && !strings.Contains(strings.ToLower(row.Key), needle) &&
			!strings.Contains(strings.ToLower(st.Document.Description), needle) {
			continue
		}
		if f.State == "inactive" && !st.Repository.Disabled {
			continue
		}
		if f.Limit > 0 && len(res.Items) == f.Limit {
			res.HasMore = true
			break
		}
		res.Items = append(res.Items, st)
	}
	return res, nil
}
