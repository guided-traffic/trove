package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// payloadVersion is the version of the change payloads authz writes.
const payloadVersion = 1

// changePayload names the changed object's type and ID, never a grant
// (R-AUTHZ-028).
type changePayload struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func queriesOf(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("authz: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

// stateAccountOutsideProject is the SQLSTATE of the integrity trigger that refuses a
// project service account in authz_role_binding and authz_policy_entry (AI005,
// Q-AUTHZ-175); the writes turn it into the answer the validation gives.
const stateAccountOutsideProject = "AI005"

// pgForeignKeyViolation is the SQLSTATE of a row that names a row that is not there.
const pgForeignKeyViolation = "23503"

// stateNameOfProject is the SQLSTATE of the guard of 00019 that compares the stored name of a project's policy
// with its project (AI003); the validation matched them, so it meets only a project that is gone.
const stateNameOfProject = "AI003"

func notFoundPolicy(name string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The permission policy %q does not exist.", name))
}

func staleVersion(current int64) error { return errs.Stale("The permission policy", current) }

// nonNilStrings makes "no warnings" an empty list on the wire, not null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func actorID(sub Subject) string {
	if sub.Anonymous {
		return ""
	}
	return sub.ID
}

// afterEpoch registers a hook that runs after the change log wrote its note in
// this transaction: it reads the epoch the transaction produced (the row is
// locked until commit, so the value is ours) and lets fn use it. Hooks run in
// registration order, so call it after the Notify.
func afterEpoch(tx meta.Tx, epoch *int64, fn func(ctx context.Context, tx meta.Tx, epoch int64) error) {
	tx.Before(func(ctx context.Context, tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ListClusterEpochs(ctx, []string{AreaAuthz})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errNoEpochRow
		}
		*epoch = rows[0].Epoch
		if fn == nil {
			return nil
		}
		return fn(ctx, tx, rows[0].Epoch)
	})
}

// Create stores a new policy (R-AUTHZ-021). The answer carries the epoch the
// write produced, for the header Trove-Authz-Epoch (R-AUTHZ-032). in.Name is the
// stored name ([StoredPolicyName]), by which every later call of this service
// addresses the policy.
func (s *Service) Create(ctx context.Context, actor Subject, in PolicyInput, opts ...WriteOption) (Policy, int64, error) {
	var out Policy
	var epoch int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) (err error) {
		out, err = s.CreateIn(ctx, tx, actor, in, &epoch, opts...)
		return err
	})
	if err != nil {
		return Policy{}, 0, err
	}
	return out, epoch, nil
}

// CreateIn is Create on the caller's transaction, for an operation that carries
// more state in the same transaction (the idempotency row of R-API-014). The
// epoch is not known before the transaction commits: *epoch is set by a hook that
// runs after the change log raised it, so a caller that needs the value in its
// answer registers its own hook after this call returns (meta.Tx.Before runs the
// hooks in registration order). The returned policy is complete, with the
// warnings of the validation.
func (s *Service) CreateIn(ctx context.Context, tx meta.Tx, actor Subject, in PolicyInput, epoch *int64, opts ...WriteOption) (Policy, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return Policy{}, err
	}
	// The project lookups run on this transaction, like the insert (Q-AUTHZ-199).
	rp, problems, err := s.resolve(ctx, in, append(opts, s.inTx(qs))...)
	if err != nil {
		return Policy{}, err
	}
	if len(problems) > 0 {
		return Policy{}, problemError(problems)
	}
	id := newID()
	rows, err := qs.AuthzInsertPolicy(ctx, q.AuthzInsertPolicyParams{
		ID: id, Name: in.Name, Description: in.Description, ResourceType: rp.in.ResourceType, ProjectID: rp.project, Actor: actorID(actor),
	})
	if err != nil {
		if rp.project != "" && (postgres.IsState(err, pgForeignKeyViolation) || postgres.IsState(err, stateNameOfProject)) {
			// The project was deleted between the lookup and the insert: the guard of the stored name
			// finds no project key (AI003), or the foreign key no project.
			return Policy{}, problemError([]ProblemField{field("/project", errs.CodeUnknownProject, fmt.Sprintf("No project %q exists.", *in.Project))})
		}
		return Policy{}, err
	}
	if len(rows) == 0 {
		where, name := "instance-wide", in.Name
		if project, bare := splitPolicyName(in.Name); project != nil {
			where, name = "in the project "+*project, bare
		}
		return Policy{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("A permission policy named %q exists %s.", name, where))
	}
	if err := writeChildren(ctx, qs, id, rp); err != nil {
		return Policy{}, err
	}
	if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "policy", ID: id}); err != nil {
		return Policy{}, err
	}
	out, err := s.load(ctx, qs, in.Name)
	if err != nil {
		return Policy{}, err
	}
	afterEpoch(tx, epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
		return s.o.Recorder.RecordTx(ctx, tx, policyEvent(EventPolicyCreated, actor, out, nil, stateOf(rp), epoch, false))
	})
	out.Warnings = nonNilStrings(rp.warnings)
	return out, nil
}

// Update replaces a policy as a whole (R-AUTHZ-021: full replacements). name is
// the stored name ([StoredPolicyName]); the name and the project are immutable:
// in.Name is taken from it, and in.Project must be the policy's project
// (Q-AUTHZ-186). expected is the version the client saw (If-Match);
// errs.AnyRevision is the deliberate blind write. A different version answers
// 412, a missing policy 404.
func (s *Service) Update(ctx context.Context, actor Subject, name string, expected int64, in PolicyInput, opts ...WriteOption) (Policy, int64, error) {
	in.Name = name // the name is immutable
	o := collectOptions(opts)
	var out Policy
	var epoch int64
	var rp *resolvedPolicy
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if err := qs.AuthzLockPolicy(ctx, name); err != nil {
			return err
		}
		current, err := s.load(ctx, qs, name)
		if err != nil {
			return err
		}
		if err := ownsPolicy(o, current); err != nil {
			return err
		}
		// Before the rules of the new project: the project is fixed, so a change is
		// the error, not whatever the other project would say (Q-AUTHZ-186).
		if !sameProject(current.Project, in.Project) {
			return problemError([]ProblemField{field("/project", "immutable", "The project of a policy is fixed when it is created.")})
		}
		var problems []ProblemField
		if rp, problems, err = s.resolve(ctx, in, append(opts, s.inTx(qs))...); err != nil {
			return err
		}
		if len(problems) > 0 {
			return problemError(problems)
		}
		before, err := s.snapshotState(ctx, qs, current)
		if err != nil {
			return err
		}
		rows, err := qs.AuthzUpdatePolicy(ctx, q.AuthzUpdatePolicyParams{
			Description: in.Description, ResourceType: rp.in.ResourceType, Actor: actorID(actor), Name: name, Expected: expected,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			cur, verr := qs.AuthzPolicyVersion(ctx, name)
			if verr != nil {
				return verr
			}
			if len(cur) == 0 {
				return notFoundPolicy(name)
			}
			return staleVersion(cur[0])
		}
		id := rows[0].ID
		if err := qs.AuthzClearPolicyChildren(ctx, id); err != nil {
			return err
		}
		if err := writeChildren(ctx, qs, id, rp); err != nil {
			return err
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "policy", ID: id}); err != nil {
			return err
		}
		if out, err = s.load(ctx, qs, name); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, policyEvent(EventPolicyUpdated, actor, out, before, stateOf(rp), epoch, true))
		})
		return nil
	})
	if err != nil {
		return Policy{}, 0, err
	}
	out.Warnings = nonNilStrings(rp.warnings)
	return out, epoch, nil
}

// ownsPolicy checks, under the policy lock, that the policy under change belongs
// to the project the caller writes for: the expected project of [ExpectProject],
// and the project of an Owner without a policies capability ([AsProjectOwner]).
// The stored name names the project already, so this holds for every policy the
// name finds; the check stays as the guard of the lock. Another project's policy
// answers 404 as a missing one does, a global policy 403 to an Owner (Q-AUTHZ-199).
func ownsPolicy(o writeOpts, current Policy) error {
	if o.expectSet && !sameProject(current.Project, o.expectProject) {
		return notFoundPolicy(current.Name)
	}
	switch {
	case o.owner == "":
		return nil
	case current.Project == nil:
		return errs.E(errs.Forbidden, errs.CodeForbidden, "A global policy needs the capability policies:write.")
	case *current.Project != o.owner:
		return notFoundPolicy(current.Name)
	}
	return nil
}

// Delete removes a policy with its selectors, patterns and entries. name is the
// stored name ([StoredPolicyName]); expected follows the rules of Update.
func (s *Service) Delete(ctx context.Context, actor Subject, name string, expected int64, opts ...WriteOption) (int64, error) {
	o := collectOptions(opts)
	var epoch int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if err := qs.AuthzLockPolicy(ctx, name); err != nil {
			return err
		}
		current, err := s.load(ctx, qs, name)
		if err != nil {
			return err
		}
		if err := ownsPolicy(o, current); err != nil {
			return err
		}
		before, err := s.snapshotState(ctx, qs, current)
		if err != nil {
			return err
		}
		rows, err := qs.AuthzDeletePolicy(ctx, q.AuthzDeletePolicyParams{Name: name, Expected: expected})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			cur, verr := qs.AuthzPolicyVersion(ctx, name)
			if verr != nil {
				return verr
			}
			if len(cur) == 0 {
				return notFoundPolicy(name)
			}
			return staleVersion(cur[0])
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "policy", ID: rows[0].ID}); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, policyEvent(EventPolicyDeleted, actor, current, before, nil, epoch, true))
		})
		return nil
	})
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// Get returns a policy with its warnings (R-AUTHZ-021); name is the stored name
// ([StoredPolicyName]).
func (s *Service) Get(ctx context.Context, name string) (Policy, error) {
	var p Policy
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		p, err = s.load(ctx, qs, name)
		return err
	})
	if err != nil {
		return Policy{}, err
	}
	if rp, problems, rerr := s.resolve(ctx, p.PolicyInput); rerr == nil && len(problems) == 0 {
		p.Warnings = nonNilStrings(rp.warnings)
	}
	return p, nil
}

// sameProject compares two project references of a policy: both global, or the
// same key.
func sameProject(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// List returns every policy sorted by stored name, without warnings. The cursor
// paging and the filters of R-AUTHZ-021 are the handler's.
func (s *Service) List(ctx context.Context) ([]Policy, error) {
	var out []Policy
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.AuthzListPolicies(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			p, err := s.assemble(ctx, qs, policyHead{
				ID: r.ID, Name: r.Name, Description: r.Description, ResourceType: r.ResourceType, Version: r.Version, Project: r.ProjectKey,
				CreatedUS: r.CreatedUs, UpdatedUS: r.UpdatedUs,
				CreatedBy: refOfRow(r.CreatedByKind, r.CreatedByName), UpdatedBy: refOfRow(r.UpdatedByKind, r.UpdatedByName),
			})
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

type policyHead struct {
	ID, Name, Description, ResourceType string
	// Project is the key of the policy's project, "" for a global policy.
	Project              string
	Version              int64
	CreatedUS, UpdatedUS int64
	CreatedBy, UpdatedBy string
}

func (s *Service) load(ctx context.Context, qs *q.Queries, name string) (Policy, error) {
	rows, err := qs.AuthzGetPolicy(ctx, name)
	if err != nil {
		return Policy{}, err
	}
	if len(rows) == 0 {
		return Policy{}, notFoundPolicy(name)
	}
	r := rows[0]
	return s.assemble(ctx, qs, policyHead{
		ID: r.ID, Name: r.Name, Description: r.Description, ResourceType: r.ResourceType, Version: r.Version, Project: r.ProjectKey,
		CreatedUS: r.CreatedUs, UpdatedUS: r.UpdatedUs,
		CreatedBy: refOfRow(r.CreatedByKind, r.CreatedByName), UpdatedBy: refOfRow(r.UpdatedByKind, r.UpdatedByName),
	})
}

// assemble reads the children of a policy row; the name is the stored name (Q-AUTHZ-209).
func (s *Service) assemble(ctx context.Context, qs *q.Queries, h policyHead) (Policy, error) {
	p := Policy{ID: h.ID, Version: h.Version, Warnings: []string{}}
	p.Name, p.Description, p.ResourceType = h.Name, h.Description, h.ResourceType
	if h.Project != "" {
		key := h.Project
		p.Project = &key
	}
	p.CreatedAt, p.UpdatedAt = time.UnixMicro(h.CreatedUS).UTC(), time.UnixMicro(h.UpdatedUS).UTC()
	p.CreatedBy, p.UpdatedBy = h.CreatedBy, h.UpdatedBy
	p.Resources = Resources{Repositories: []string{}, Packages: PatternLists{Include: []string{}, Exclude: []string{}}, Paths: PatternLists{Include: []string{}, Exclude: []string{}}}
	p.Entries = []Entry{}

	sels, err := qs.AuthzPolicySelectors(ctx, h.ID)
	if err != nil {
		return Policy{}, err
	}
	for _, r := range sels {
		in := instanceContext
		if p.Project != nil {
			in = projectContext(*p.Project)
		}
		sel, err := selectorOfRow(q.AuthzBuildSelectorsRow{Kind: r.Kind, RepositoryID: r.RepositoryID, KeyPattern: r.KeyPattern, RepoClass: r.RepoClass, Format: r.Format}, in)
		if err != nil {
			return Policy{}, err
		}
		sel.key, sel.project = r.RepositoryKey, r.RepositoryProjectKey
		p.Resources.Repositories = append(p.Resources.Repositories, sel.text(nil))
	}
	p.Inert = len(sels) == 0
	pats, err := qs.AuthzPolicyPatterns(ctx, h.ID)
	if err != nil {
		return Policy{}, err
	}
	for _, r := range pats {
		var l *PatternLists
		switch r.Dimension {
		case "package":
			l = &p.Resources.Packages
		case "path":
			l = &p.Resources.Paths
		default:
			continue
		}
		if r.Mode == "include" {
			l.Include = append(l.Include, r.Pattern)
		} else {
			l.Exclude = append(l.Exclude, r.Pattern)
		}
	}
	ents, err := qs.AuthzPolicyEntries(ctx, h.ID)
	if err != nil {
		return Policy{}, err
	}
	for _, r := range ents {
		acts, err := actionsFromStored(r.Actions)
		if err != nil {
			return Policy{}, err
		}
		p.Entries = append(p.Entries, Entry{Principal: refOfRow(r.PrincipalKind, r.PrincipalName), Actions: acts.Strings()})
	}
	return p, nil
}

// snapshotState reads the access-bearing state of a stored policy as the
// revocation journal carries it, identified by IDs (R-AUTHZ-031). It runs in the
// transaction that changes the policy, before the change.
func (s *Service) snapshotState(ctx context.Context, qs *q.Queries, p Policy) (*journalState, error) {
	st := &journalState{
		ResourceType: p.ResourceType, Packages: normLists(p.Resources.Packages), Paths: normLists(p.Resources.Paths),
		Selectors: []journalSelector{}, Entries: []journalEntry{},
	}
	if p.Project != nil {
		st.Project = *p.Project
	}
	sels, err := qs.AuthzPolicySelectors(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range sels {
		st.Selectors = append(st.Selectors, journalSelector{
			Kind: r.Kind, RepositoryID: r.RepositoryID, Key: r.RepositoryKey, Pattern: r.KeyPattern, Class: r.RepoClass, Format: r.Format,
		})
	}
	ents, err := qs.AuthzPolicyEntries(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range ents {
		acts, err := actionsFromStored(r.Actions)
		if err != nil {
			return nil, err
		}
		st.Entries = append(st.Entries, journalEntry{PrincipalID: r.PrincipalID, Principal: refOfRow(r.PrincipalKind, r.PrincipalName), Actions: acts.Strings()})
	}
	sort.Slice(st.Entries, func(i, j int) bool { return st.Entries[i].PrincipalID < st.Entries[j].PrincipalID })
	return st, nil
}

func writeChildren(ctx context.Context, qs *q.Queries, id string, rp *resolvedPolicy) error {
	for i, sel := range rp.selectors {
		p := q.AuthzInsertSelectorParams{PolicyID: id, Ordinal: int16(i), Kind: sel.kind, Format: sel.format} //nolint:gosec // bounded by the selector limit
		switch sel.kind {
		case selKey:
			p.RepositoryID = sel.repoID
		case selPattern:
			p.KeyPattern = sel.pattern
		case selClass:
			p.RepoClass = sel.class
		}
		if err := qs.AuthzInsertSelector(ctx, p); err != nil {
			return err
		}
	}
	for _, l := range []struct {
		dim, mode string
		list      []string
	}{
		{"package", "include", rp.in.Resources.Packages.Include},
		{"package", "exclude", rp.in.Resources.Packages.Exclude},
		{"path", "include", rp.in.Resources.Paths.Include},
		{"path", "exclude", rp.in.Resources.Paths.Exclude},
	} {
		seen := map[string]bool{}
		for _, pat := range l.list {
			if seen[pat] {
				continue
			}
			seen[pat] = true
			if err := qs.AuthzInsertPattern(ctx, q.AuthzInsertPatternParams{PolicyID: id, Dimension: l.dim, Mode: l.mode, Pattern: pat}); err != nil {
				return err
			}
		}
	}
	for _, e := range rp.entries {
		if rp.project != "" {
			// The grants of a project policy never go into authz_policy_entry, which
			// the previous release reads (Q-AUTHZ-174).
			err := qs.AuthzInsertProjectEntry(ctx, q.AuthzInsertProjectEntryParams{
				PolicyID: id, ProjectID: rp.project, PrincipalID: e.id, Actions: e.actions.Strings(),
			})
			if err != nil {
				return err
			}
			continue
		}
		if err := qs.AuthzInsertEntry(ctx, q.AuthzInsertEntryParams{PolicyID: id, PrincipalID: e.id, Actions: e.actions.Strings()}); err != nil {
			if postgres.IsState(err, stateAccountOutsideProject) {
				// The trigger the validation precedes: a project service account in a
				// global policy (Q-AUTHZ-175, Q-AUTHZ-198).
				return problemError([]ProblemField{field(e.pointer+"/principal", errs.CodePrincipalOutsideProject,
					fmt.Sprintf("%s belongs to a project and is granted by its policies only.", e.ref))})
			}
			return err
		}
	}
	return nil
}

// policyEvent builds the audit event of a policy change. Updated and deleted
// events carry the journal entry of R-AUTHZ-031: the policy's ID and name and the
// access-bearing state before and after.
func policyEvent(name string, actor Subject, p Policy, before, after *journalState, epoch int64, journal bool) audit.Event {
	details := map[string]any{"epoch": epoch, "before": before, "after": after, "version": p.Version}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success,
		Subject: audit.Subject{Kind: "permission_policy", ID: p.ID, Name: p.Name},
		Details: mustJSON(details),
	}
	if p.Project != nil {
		// The event names the project with the name (Q-AUTHZ-209).
		details["project"] = *p.Project
		ev.Details, ev.Project = mustJSON(details), *p.Project
	}
	if journal {
		j := map[string]any{"policyId": p.ID, "name": p.Name, "before": before, "after": after}
		if p.Project != nil {
			j["project"] = *p.Project
		}
		ev.Journal = mustJSON(j)
	}
	return ev
}
