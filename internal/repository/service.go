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
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/qname"

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
// authz.settings.changed when cacheFillAction, packageOwnership, hideExistence or a
// visibility level changed (R-AUTHZ-031, R-AUTHZ-036), and OnDelete to
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
	// PublicAvailable reports whether the default endpoint admits requests without a credential (R-EP-010): while it does not, a
	// write that opens a part of a repository to public answers 409 public_not_allowed (R-AUTHZ-033 rule 5). Nil means it does not.
	PublicAvailable func() bool
	// DeleteBatch is the number of items one transaction of the deletion job
	// removes (storage.delete.batch_size, R-STOR-018); zero means 5000.
	DeleteBatch int
	// Remote holds the key ring and the upstream prober of remote repositories
	// (secrets_store.go); the composition root may bind them later (Bind).
	Remote RemoteDeps

	// >>> M2 virtual

	// MaxVirtualMembers is repositories.max_virtual_members (R-REPO-017); zero
	// means DefaultMaxVirtualMembers.
	MaxVirtualMembers int

	// <<< M2 virtual
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
	// remote is set once by Bind or NewService, read by every request.
	remote atomic.Pointer[RemoteDeps]

	// maxMembers bounds the member list of a virtual repository (R-REPO-017).
	maxMembers int
	// publicOK is Options.PublicAvailable.
	publicOK func() bool
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
	if o.MaxVirtualMembers <= 0 {
		o.MaxVirtualMembers = DefaultMaxVirtualMembers
	}
	s := &Service{
		run: o.Runner, cat: catalog.Or(o.Catalog), rec: o.Recorder, notif: o.Notifier, jobs: o.Jobs,
		hooks: o.Hooks, log: o.Log, batch: o.DeleteBatch, maxMembers: o.MaxVirtualMembers, publicOK: o.PublicAvailable,
	}
	s.Bind(o.Remote)
	return s, nil
}

// Caller is who acts and from where; it goes into the audit event and, as the
// principal id, into created_by and updated_by. Endpoint is the endpoint the
// request arrived on, for the events a hook records on the way (the audit
// recorder fills the column from the event alone); empty outside a request.
type Caller struct {
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
	// Restricted is set by the API layer for a write that is allowed by the role owner of the repository's project and not by
	// repositories:write-config (R-AUTHZ-026): the upstream rules of Q-REPO-127, Q-REPO-128, Q-REPO-142 and Q-REPO-193 apply
	// (restrictedRemote), and the member vulnerabilities may not be weakened (Q-VULN-69) unless MayWeakenVulnerabilities.
	Restricted bool
	// MayWeakenVulnerabilities is set for a caller who holds vulnerabilities:triage (Q-VULN-69).
	MayWeakenVulnerabilities bool
	// RaiseGate is set by the API layer. A write that raises a level (R-AUTHZ-033 rule 4) calls it, in the transaction of the
	// write, before anything is stored; it returns the refusal that asks the caller to authenticate again (R-API-007). Nil
	// asks nothing.
	RaiseGate func(context.Context) error
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
// the change-log row in one transaction. The repository is created in project.
// Create is never an upsert: a key that is taken in the project, also by a
// repository being deleted, answers 409 already_exists.
func (s *Service) Create(ctx context.Context, c Caller, project string, body []byte) (Stored, error) {
	var out Stored
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		var err error
		out, err = s.CreateTx(ctx, tx, c, project, body)
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
func (s *Service) CreateTx(ctx context.Context, tx meta.Tx, c Caller, project string, body []byte) (Stored, error) {
	d, err := DecodeDocument(body)
	if err != nil {
		return Stored{}, err
	}
	_, v := validate(s.cat, &d, true)
	if d.Project != "" && d.Project != project {
		v.add("/project", codeImmutable, "the project is the one of the address; a repository changes it only by a move")
	}
	d.Project = project
	restrictedRemote(v, c, nil, &d)
	// A create is checked against the defaults (Q-VULN-69): otherwise deleting and re-creating a repository would lift
	// the blocks an Owner may not lift by an update.
	if err := restrictedVulnerabilities(c, &Document{}, &d); err != nil {
		return Stored{}, err
	}
	qs, err := queries(tx)
	if err != nil {
		return Stored{}, err
	}
	projectID, projectLevel, err := lockProject(ctx, qs, project)
	if err != nil {
		return Stored{}, err
	}
	levels, err := s.resolveLevels(ctx, qs, v, c, projectLevel, nil, &d)
	if err != nil {
		return Stored{}, err
	}
	// >>> M2 virtual
	rel, err := s.resolveVirtual(ctx, qs, v, project, &d)
	if err != nil {
		return Stored{}, err
	}
	// <<< M2 virtual
	if err := v.err(); err != nil {
		return Stored{}, err
	}
	cfg, err := d.marshalConfig()
	if err != nil {
		return Stored{}, err
	}
	rows, err := qs.InsertRepository(ctx, q.InsertRepositoryParams{
		Key: d.Key, Format: d.Format, Class: string(d.Class), Config: string(cfg), ProjectID: projectID, Actor: c.principalID(),
		VisibilityArtifacts: levels.Artifacts, VisibilityWiki: levels.Wiki,
	})
	if err != nil {
		return Stored{}, err
	}
	if len(rows) == 0 {
		return Stored{}, errs.E(errs.Conflict, errs.CodeAlreadyExists,
			"The project "+strconv.Quote(project)+" has a repository with the key "+strconv.Quote(d.Key)+", or one that is still being deleted; a key stays taken until the deletion job completes.")
	}
	if d.Class == Remote {
		if err := s.writeSecrets(ctx, tx, rows[0].ID, d.pending); err != nil {
			return Stored{}, err
		}
	}
	// >>> M2 virtual
	if rel != nil {
		if _, _, err := writeVirtual(ctx, qs, rows[0].ID, rel, nil); err != nil {
			return Stored{}, err
		}
	}
	// <<< M2 virtual
	out, err := s.stored(ctx, qs, rows[0], project, v.warnings)
	if err != nil {
		return Stored{}, err
	}
	// The rows a format's data port adds in the creating transaction (Q-REPO-273): a Debian repository's first signing key.
	if err := runCreateHooks(ctx, tx, CreatedRepo{ID: rows[0].ID, Name: qname.Repo{Project: project, Key: d.Key}, Format: d.Format}, string(d.Class)); err != nil {
		return Stored{}, err
	}
	if err := s.afterWrite(ctx, tx, c, EventCreated, out.Repository, []string{"key", "class", "format"}); err != nil {
		return Stored{}, err
	}
	// >>> M2 virtual
	if rel != nil && len(rel.pins) > 0 {
		if err := s.notifyPins(tx, out.Repository); err != nil {
			return Stored{}, err
		}
	}
	// <<< M2 virtual
	return out, nil
}

// Get implements R-REPO-010: the stored document, read from PostgreSQL. A
// repository being deleted is returned with Lifecycle Deleting; the handler shows
// it only to holders of repositories:read-config.
func (s *Service) Get(ctx context.Context, name qname.Repo) (Stored, error) {
	var out Stored
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.GetRepositoryByName(ctx, q.GetRepositoryByNameParams{Project: name.Project, Key: name.Key})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound(name)
		}
		out, err = s.stored(ctx, qs, rows[0], name.Project, nil)
		return err
	})
	if err != nil {
		return Stored{}, err
	}
	return out, nil
}

// Replace implements PUT of R-REPO-011: the body is a complete document and
// omitted settings revert to their defaults. Key, class and format must equal the
// stored ones (422 validation_failed with the field code immutable otherwise).
func (s *Service) Replace(ctx context.Context, c Caller, name qname.Repo, pre Precondition, body []byte) (Stored, error) {
	return s.update(ctx, c, name, pre, func(Document) (Document, error) { return DecodeDocument(body) })
}

// Patcher applies a client's patch to the stored document: the current document
// as JSON in, the patched document as JSON out. The management API passes its
// RFC 7396 merge (Q-API-157), so the service owns the row lock and the version
// precondition and the API owns how a patch reads.
type Patcher func(current []byte) ([]byte, error)

// Patch implements PATCH of R-REPO-011: apply is the client's patch, run on the
// stored document under the row lock. The result is read strictly like a
// document of a PUT, so a patch cannot introduce an unknown member.
func (s *Service) Patch(ctx context.Context, c Caller, name qname.Repo, pre Precondition, apply Patcher) (Stored, error) {
	return s.update(ctx, c, name, pre, func(cur Document) (Document, error) {
		base, err := json.Marshal(cur)
		if err != nil {
			return Document{}, err
		}
		merged, err := apply(base)
		if err != nil {
			return Document{}, err
		}
		return DecodeDocument(merged)
	})
}

func (s *Service) update(ctx context.Context, c Caller, name qname.Repo, pre Precondition,
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
		row, projectLevel, err := lockForUpdate(ctx, qs, name, pre)
		if err != nil {
			return err
		}
		cur, curRel, err := s.document(ctx, qs, row, name.Project)
		if err != nil {
			return fmt.Errorf("repository: stored document of %q: %w", name, err)
		}
		before, _ := toRepository(row, name.Project, s.cat)
		nd, v, rel, err := s.updatedDocument(ctx, qs, c, row.ID, name.Project, projectLevel, &cur, build)
		if err != nil {
			return err
		}
		newCfg, err := nd.marshalConfig()
		if err != nil {
			return err
		}
		changes, err := updateChanges(&cur, &nd)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			out, err = s.stored(ctx, qs, row, name.Project, v.warnings) // nothing to write: same version, no event
			return err
		}
		updated, err := qs.UpdateRepositoryConfig(ctx, q.UpdateRepositoryConfigParams{
			ID: row.ID, Version: row.ConfigVersion, Config: string(newCfg), Actor: c.principalID(),
			VisibilityArtifacts: nd.Visibility.Artifacts, VisibilityWiki: nd.Visibility.Wiki,
		})
		if err != nil {
			return err
		}
		if len(updated) == 0 {
			return errors.New("repository: the update matched no row although the row was locked")
		}
		if nd.Class == Remote {
			if err := s.writeSecrets(ctx, tx, row.ID, nd.pending); err != nil {
				return err
			}
			if err := s.upstreamChanged(ctx, tx, c, cur, nd, before); err != nil {
				return err
			}
		}
		// >>> M2 virtual
		pinsChanged := false
		if rel != nil {
			if _, pinsChanged, err = writeVirtual(ctx, qs, row.ID, rel, curRel); err != nil {
				return err
			}
		}
		// <<< M2 virtual
		out, err = s.stored(ctx, qs, updated[0], name.Project, v.warnings)
		if err != nil {
			return err
		}
		if err := s.recordUpdate(ctx, tx, c, out.Repository, changes, &cur, &nd, curRel, rel); err != nil {
			return err
		}
		// >>> M2 virtual
		if pinsChanged {
			if err := s.notifyPins(tx, out.Repository); err != nil {
				return err
			}
		}
		// <<< M2 virtual
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

// lockForUpdate locks the repository an update writes and returns its row and its project's level. The project is locked FOR
// SHARE before the repository, as a create does and as the lowering of a project's level orders its locks: a repository is never
// written above a level that is being lowered (R-AUTHZ-033 rule 3, Q-AUTHZ-282). A repository being deleted answers 409, and a
// version precondition that does not hold answers stale.
func lockForUpdate(ctx context.Context, qs *q.Queries, name qname.Repo, pre Precondition) (q.Repository, string, error) {
	shares, err := qs.RepositoryProjectShare(ctx, name.Project)
	if err != nil {
		return q.Repository{}, "", err
	}
	rows, err := qs.LockRepositoryByName(ctx, q.LockRepositoryByNameParams{Project: name.Project, Key: name.Key})
	if err != nil {
		return q.Repository{}, "", err
	}
	if len(rows) == 0 || len(shares) == 0 {
		return q.Repository{}, "", notFound(name)
	}
	row := rows[0]
	if Lifecycle(row.Lifecycle) == Deleting {
		return q.Repository{}, "", errs.E(errs.Conflict, errs.CodeConflict, "the repository is being deleted")
	}
	if !pre.Any && pre.Version != row.ConfigVersion {
		return q.Repository{}, "", stale(row.ConfigVersion)
	}
	return row, shares[0].Visibility, nil
}

// updatedDocument builds the document an update stores from the stored one, cur, and checks it: the identity stays as stored,
// the levels are resolved against the project's level (R-AUTHZ-033), the rules of a restricted caller hold, and the rows of a
// virtual repository are resolved. It returns the document, the validation with its warnings and those rows, nil for another
// class; a document that does not validate answers the validation's error.
func (s *Service) updatedDocument(ctx context.Context, qs *q.Queries, c Caller, id int64, project, projectLevel string, cur *Document,
	build func(cur Document) (Document, error),
) (Document, *validation, *virtualRel, error) {
	nd, err := build(*cur)
	if err != nil {
		return Document{}, nil, nil, err
	}
	nd, v := s.checkUpdate(*cur, nd)
	levels, err := s.resolveLevels(ctx, qs, v, c, projectLevel, &cur.Visibility, &nd)
	if err != nil {
		return Document{}, nil, nil, err
	}
	nd.Visibility = levels
	restrictedRemote(v, c, cur, &nd)
	if err := restrictedVulnerabilities(c, cur, &nd); err != nil {
		return Document{}, nil, nil, err
	}
	// >>> M2 virtual
	rel, err := s.resolveVirtual(ctx, qs, v, project, &nd)
	if err != nil {
		return Document{}, nil, nil, err
	}
	// <<< M2 virtual
	if err := s.checkDebKeysKept(ctx, qs, v, id, cur, &nd); err != nil {
		return Document{}, nil, nil, err
	}
	if err := v.err(); err != nil {
		return Document{}, nil, nil, err
	}
	return nd, v, rel, nil
}

// updateChanges lists the members an update changes, sorted. What changed is read from the complete documents, the rows of a
// virtual repository included, since the stored configuration lacks them; a new secret value counts as a change of remote,
// and new levels as a change of visibility, which are columns of the row.
func updateChanges(cur, nd *Document) ([]string, error) {
	newFull, err := nd.marshalFull()
	if err != nil {
		return nil, err
	}
	oldFull, err := cur.marshalFull()
	if err != nil {
		return nil, err
	}
	changes := changedMembers(oldFull, newFull)
	changes = withSecretChange(changes, *nd)
	if nd.Visibility != cur.Visibility {
		changes = append(changes, "visibility")
		sort.Strings(changes)
	}
	return changes, nil
}

// checkUpdate puts the stored identity back into the new document, collecting
// immutable violations for the members the caller changed, and validates
// the rest in the same pass (R-REPO-008).
func (s *Service) checkUpdate(cur, nd Document) (Document, *validation) {
	var imm []errs.FieldError
	for _, f := range []struct {
		ptr       string
		got, want string
	}{{"/project", nd.Project, cur.Project}, {"/key", nd.Key, cur.Key}, {"/class", string(nd.Class), string(cur.Class)}, {"/format", nd.Format, cur.Format}} {
		if f.got != "" && f.got != f.want { // an omitted identity member means "unchanged"
			imm = append(imm, errs.FieldError{
				Pointer: f.ptr, Code: codeImmutable,
				Detail: "project, key, class and format are fixed at creation; the project changes only by a move",
			})
		}
	}
	nd.Project, nd.Key, nd.Class, nd.Format = cur.Project, cur.Key, cur.Class, cur.Format
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

// stored renders a row as the management API shows it. The members, the deploy
// target and the pin rules of a virtual repository are read from their rows
// through qs (M2 virtual).
func (s *Service) stored(ctx context.Context, qs *q.Queries, row q.Repository, project string, warnings []Warning) (Stored, error) {
	var rel *virtualRel
	if Class(row.Class) == Virtual {
		rels, err := loadVirtualRels(ctx, qs, []int64{row.ID})
		if err != nil {
			return Stored{}, err
		}
		rel = rels[row.ID]
	}
	return s.storedRel(row, project, rel, warnings)
}

func (s *Service) storedRel(row q.Repository, project string, rel *virtualRel, warnings []Warning) (Stored, error) {
	r, err := toRepository(row, project, s.cat)
	if err != nil {
		s.log.Warn("repository: stored configuration does not load", "name", r.Name.String(), "error", err)
	}
	d, derr := toDocument(row, s.cat)
	if derr != nil {
		return Stored{}, fmt.Errorf("repository: stored document of %q: %w", r.Name.String(), derr)
	}
	d.Project = project
	d.renderVirtual(rel)
	if r.virt != nil && d.Virtual != nil {
		r.virt.settings = d.Virtual.clone()
	}
	return Stored{Repository: r, Document: d, Warnings: warnings}, nil
}

// document reads the stored document of a row with the rows of its virtual
// section, and those rows as they are (nil for another class).
func (s *Service) document(ctx context.Context, qs *q.Queries, row q.Repository, project string) (Document, *virtualRel, error) {
	var rel *virtualRel
	if Class(row.Class) == Virtual {
		rels, err := loadVirtualRels(ctx, qs, []int64{row.ID})
		if err != nil {
			return Document{}, nil, err
		}
		rel = rels[row.ID]
	}
	d, err := toDocument(row, s.cat)
	if err != nil {
		return Document{}, nil, err
	}
	d.Project = project
	d.renderVirtual(rel)
	return d, rel, nil
}

// changePayload is the payload of a repo_config change: the repository id and the
// config_version the write left, never a grant (R-REPO-008). A cache keyed by the
// version drops only the entries of older versions (Q-NUGET-94).
type changePayload struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Op      string `json:"op"`
	Version int64  `json:"version"`
}

// The ops of a repo_config change that end a repository: the first transaction of a
// deletion and the removal of the row (R-REPO-013). The other ops are the event names
// without "repository.", such as "created" and "updated".
const (
	ChangeOpDeleting = "deleting"
	ChangeOpDeleted  = "deleted"
)

// ChangeEviction reads a repo_config payload (a changePayload, R-REPO-008) for a cache keyed by
// the repository id and its config_version: the repository, and which of its configuration
// versions the change has made gone. A replica applies every change twice, at once and again
// with the change-log poll (R-ARCH-018), so an entry of the version the change produced must
// survive both. older reports true for the versions below the one the change left; for every
// version on a deletion and on a payload without a version (an older replica's). ok is false
// for a payload that names no repository, which the cache answers by flushing (Q-NUGET-94).
func ChangeEviction(payload json.RawMessage) (id int64, older func(version int64) bool, ok bool) {
	var p changePayload
	if json.Unmarshal(payload, &p) != nil {
		return 0, nil, false
	}
	id, err := strconv.ParseInt(p.ID, 10, 64)
	if err != nil {
		return 0, nil, false
	}
	if p.Version <= 0 || p.Op == ChangeOpDeleting || p.Op == ChangeOpDeleted {
		return id, func(int64) bool { return true }, true
	}
	return id, func(v int64) bool { return v < p.Version }, true
}

// eventDetails is the data of the repository.* events (R-OPS-016). Project is the
// key of the repository's project, absent for a repository of none (R-AUTHZ-026), as
// the events of a project service account carry it.
type eventDetails struct {
	Class   Class    `json:"class"`
	Format  string   `json:"format"`
	Changes []string `json:"changes"`
	Phase   string   `json:"phase,omitempty"`
	Project string   `json:"project,omitempty"`
	// Block is "lifted" when the write weakens the member `vulnerabilities` and so may lift download blocks of the
	// repository, and Weakened names the members (R-VULN-002, Q-VULN-69): one audit filter finds every such write.
	Block    string   `json:"block,omitempty"`
	Weakened []string `json:"weakened,omitempty"`
}

// withSecretChange adds "remote" to the changes of a write that sends a new secret value. A new secret value reads like the old
// one ({"set": true}); it is still a change, and it moves config_version so that the clients built with the old value are
// replaced.
func withSecretChange(changes []string, nd Document) []string {
	if hasSecretWrites(nd.pending) && !slices.Contains(changes, "remote") {
		changes = append(changes, "remote")
		sort.Strings(changes)
	}
	return changes
}

// updateDetails is the audit details of an update: what changed, and, for a write that weakens the member `vulnerabilities`,
// `block: lifted` (Q-VULN-69).
func updateDetails(r Repository, changes []string, cur, nd *Document) eventDetails {
	d := eventDetails{Class: r.Class, Format: r.Format, Changes: changes, Project: r.Name.Project}
	if weak := weakenedVulnerabilities(cur, nd); len(weak) > 0 {
		d.Block, d.Weakened = "lifted", weak
	}
	return d
}

// afterWrite records the audit event and the change-log row of a write in the
// writer's transaction. r.Name.Project names the project in the event.
func (s *Service) afterWrite(ctx context.Context, tx meta.Tx, c Caller, event string, r Repository,
	changes []string,
) error {
	return s.recordAndNotify(ctx, tx, c, event, r, eventDetails{Class: r.Class, Format: r.Format, Changes: changes, Project: r.Name.Project},
		strings.TrimPrefix(event, "repository."))
}

// recordAndNotify records the event and the change-log row of a write. repository.deleted, of either phase, carries the
// journal entry of R-AUTHZ-031: a deletion ends every access to the repository, and a restore to a point before it brings
// the repository back, which the replay disables (Q-AUTHZ-239).
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
	ev := audit.Event{
		Type: event, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject: audit.Subject{Kind: "repository", ID: r.ID, Name: r.Name.String()}, Project: r.Name.Project, Details: raw,
	}
	if event == EventDeleted {
		ev.Journal = journalEntry(r)
	}
	if err := s.rec.RecordTx(ctx, tx, ev); err != nil {
		return err
	}
	return s.notif.Notify(tx, cluster.AreaRepoConfig, 1, changePayload{ID: r.ID, Key: r.Name.String(), Op: op, Version: r.Version})
}

// recordUpdate records repository.updated and its change-log row, repository.narrowed when the update narrowed what the
// repository serves (R-REPO-061), and repository.disabled when the update set disabled. curRel and rel are the rows of a
// virtual repository before and after, nil for another class.
func (s *Service) recordUpdate(ctx context.Context, tx meta.Tx, c Caller, r Repository, changes []string, cur, nd *Document,
	curRel, rel *virtualRel,
) error {
	if err := s.recordAndNotify(ctx, tx, c, EventUpdated, r, updateDetails(r, changes, cur, nd), strings.TrimPrefix(EventUpdated, "repository.")); err != nil {
		return err
	}
	narrowed := s.narrowing(r, cur, nd, curRel, rel)
	if err := s.recordNarrowed(ctx, tx, c, r, narrowed); err != nil {
		return err
	}
	if err := runKeysRemovedHooks(ctx, tx, CreatedRepo{ID: r.NumericID(), Name: r.Name, Format: r.Format}, narrowed.DebUpstreamKeysRemoved); err != nil {
		return err
	}
	if cur.Disabled || !nd.Disabled {
		return nil
	}
	return s.recordDisabled(ctx, tx, c, r)
}

// recordDisabled records repository.disabled for an update that set disabled (R-REPO-004), next to repository.updated:
// the repository takes no part in resolution, download or deploy any more, and the entry keeps it disabled across a
// restore to a point before the update (R-AUTHZ-031, Q-AUTHZ-238). An update that clears disabled narrows nothing and
// records only repository.updated.
func (s *Service) recordDisabled(ctx context.Context, tx meta.Tx, c Caller, r Repository) error {
	d := map[string]string{"class": string(r.Class), "format": r.Format}
	if r.Name.Project != "" {
		d["project"] = r.Name.Project
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventRepositoryDisabled, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject: audit.Subject{Kind: "repository", ID: r.ID, Name: r.Name.String()}, Project: r.Name.Project, Details: raw,
		Journal: journalEntry(r),
	})
}

// journalEntry is the journal entry of repository.disabled and repository.deleted: the repository's ID, which the replay
// acts on, and its qualified name for display (R-AUTHZ-031).
func journalEntry(r Repository) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"repositoryId": r.ID, "key": r.Name.String()}) // two strings always marshal
	return raw
}

// ListFilter selects repositories (R-REPO-012). Class and format values are
// spelled as in documents; Query matches key and description case-insensitively.
type ListFilter struct {
	Classes, Formats []string
	Query            string
	// State "inactive" keeps disabled repositories. Offline and circuit-open remote
	// repositories (R-REPO-029) are inactive too once remote repositories exist.
	State string
	// Project keeps the repositories assigned to the project with this key; an unknown
	// key keeps none (R-REPO-012, Q-REPO-22).
	Project string
	// Visibility keeps the repositories whose artifacts level is one of these (R-API-057); empty keeps every level.
	Visibility []string
	// IncludeDeleting adds repositories being deleted, for holders of
	// repositories:read-config.
	IncludeDeleting bool
	// After is the keyset cursor: the last name of the previous page, by project key
	// and key. Limit 0 means no limit.
	After qname.Repo
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
	var rows []q.Repository
	var rels map[int64]*virtualRel
	var projects map[string]string
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if rows, err = qs.ListRepositories(ctx); err != nil {
			return err
		}
		if projects, err = projectKeys(ctx, qs); err != nil {
			return err
		}
		rels, err = loadVirtualRels(ctx, qs, virtualIDs(rows))
		return err
	})
	if err != nil {
		return ListResult{}, err
	}
	needle := strings.ToLower(f.Query)
	var res ListResult
	for _, row := range rows {
		project := projects[row.ProjectID.String()]
		if !f.After.IsZero() && !afterRepo(project, row.Key, f.After) {
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
		if f.Project != "" && project != f.Project {
			continue
		}
		if len(f.Visibility) > 0 && !slices.Contains(f.Visibility, row.VisibilityArtifacts) {
			continue
		}
		st, err := s.storedRel(row, project, rels[row.ID], nil)
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

// afterRepo reports whether the repository (project, key) sorts after the cursor, byte by byte as the
// list's order is (project key, then key).
func afterRepo(project, key string, cursor qname.Repo) bool {
	if project != cursor.Project {
		return project > cursor.Project
	}
	return key > cursor.Key
}
