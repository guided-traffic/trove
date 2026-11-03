package ci

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The writes of a mapping (R-CI-009, R-CI-017, R-CI-019, R-CI-020). Every write runs in one READ COMMITTED
// transaction that takes the issuer row FOR UPDATE first (a project's create takes the project row before
// it), increments the issuer's mappings_version, notifies the change area ci_mappings, keeps the mapping's
// principal and the policies' principal lists in step, revokes the tokens of a mapping that is disabled or
// deleted and records its audit event, all in that transaction.

// tierRef is the tier a save addresses: the administrators' or one project's.
type tierRef struct {
	tier    string
	project string // the key, "" for the system tier
}

func systemTier() tierRef { return tierRef{tier: TierSystem} }

func projectTier(key string) tierRef { return tierRef{tier: ProjectTier(key), project: key} }

// mappingByName reads one mapping inside a write, with its policies; ok is false when there is none.
func mappingByName(ctx context.Context, qs *q.Queries, issuer, name string) (Mapping, bool, error) {
	rows, err := qs.CiMappingByName(ctx, q.CiMappingByNameParams{Issuer: issuer, Name: name})
	if err != nil || len(rows) == 0 {
		return Mapping{}, false, err
	}
	ms, err := readMappings(ctx, qs, listRows(rows[:1]))
	if err != nil {
		return Mapping{}, false, err
	}
	return ms[0], true, nil
}

func mappingByID(ctx context.Context, qs *q.Queries, id string) (Mapping, error) {
	rows, err := qs.CiMappingByID(ctx, id)
	if err != nil {
		return Mapping{}, err
	}
	if len(rows) == 0 {
		return Mapping{}, fmt.Errorf("ci: the mapping %s vanished inside its transaction", id)
	}
	ms, err := readMappings(ctx, qs, listRows(rows[:1]))
	if err != nil {
		return Mapping{}, err
	}
	return ms[0], nil
}

// saved is what the rules of a save come to: the checked mapping, its warnings and the bindings to write.
type saved struct {
	checked  claims.Checked
	warnings []claims.Warning
	grants   resolvedGrants
	enabled  bool
	kinds    []string
}

// check applies every save-time rule to an input inside the transaction: the policies and groups exist
// and, in a project, are the project's; the rules of claims.Check with the actions the policies and groups
// give; the repositories of a project's inline grants are the project's.
func (s *MappingService) check(ctx context.Context, qs *q.Queries, is Issuer, tr tierRef, projectID, principalID string, in MappingInput) (saved, error) {
	enabled, kinds, opts := withDefaults(in)
	rg, fields, err := s.resolveGrants(ctx, qs, tr.project, principalID, in.Grants)
	if err != nil {
		return saved{}, err
	}
	if err := mappingViolation(fields); err != nil {
		return saved{}, err
	}
	facts := claims.IssuerFacts{Kind: is.Kind, PublicIssuer: is.PublicIssuer, PinClaims: is.PinClaims, RequiredClaims: is.RequiredClaims, Project: tr.project}
	checked, warnings, err := claims.Check(facts, claims.SaveInput{
		Name: in.Name, Match: in.Match, Grants: in.Grants, Options: opts, SubjectKinds: in.SubjectKinds, PolicyActions: rg.actions,
	}, claims.DefaultLimits())
	if err != nil {
		if fe := fieldOfCheck(err); fe != nil {
			return saved{}, mappingViolation(fe)
		}
		return saved{}, err
	}
	if tr.project != "" {
		fe, err := outsideFields(ctx, qs, tr.project, projectID, checked.Grants)
		if err != nil {
			return saved{}, err
		}
		if err := mappingViolation(fe); err != nil {
			return saved{}, err
		}
	}
	return saved{checked: checked, warnings: warnings, grants: rg, enabled: enabled, kinds: kinds}, nil
}

// Create creates a system-tier mapping and returns it with the warnings of the save.
func (s *MappingService) Create(ctx context.Context, caller Actor, issuer string, in MappingInput) (MappingResult, error) {
	return s.create(ctx, caller, issuer, systemTier(), in)
}

// CreateIn creates a mapping of the project's tier on the issuer named by in.Issuer (Q-CI-26).
func (s *MappingService) CreateIn(ctx context.Context, caller Actor, project string, in MappingInput) (MappingResult, error) {
	if in.Issuer == "" {
		return MappingResult{}, mappingViolation([]errs.FieldError{field("/issuer", "required", "A project's mapping names its issuer.")})
	}
	return s.create(ctx, caller, in.Issuer, projectTier(project), in)
}

func (s *MappingService) create(ctx context.Context, caller Actor, issuerName string, tr tierRef, in MappingInput) (MappingResult, error) {
	if err := mappingViolation(s.shapeFields(in)); err != nil {
		return MappingResult{}, err
	}
	actor := actorOf(caller)
	var out MappingResult
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		projectID := ""
		if tr.project != "" {
			ids, err := qs.CiProjectLock(ctx, tr.project)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return projectNotFound(tr.project)
			}
			projectID = ids[0]
			// The project's prefix before the name is looked up (H-413, Q-CI-51): a name outside it is refused whether or
			// not a mapping of another tier holds it, so the 409 of a taken name never answers for a name the caller may
			// not take. A project that does not exist is 404 first, as on every project route.
			if err := claims.CheckProjectName(tr.project, in.Name); err != nil {
				return mappingViolation(fieldOfCheck(err))
			}
		}
		lock, err := lockIssuer(ctx, qs, issuerName, errs.AnyRevision)
		if err != nil {
			return err
		}
		is, _, err := loadByID(ctx, qs, lock.ID)
		if err != nil {
			return err
		}
		if _, exists, err := mappingByName(ctx, qs, is.Name, in.Name); err != nil {
			return err
		} else if exists {
			return errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("A mapping named %q exists on the CI issuer %q.", in.Name, is.Name))
		}
		n, err := qs.CiMappingCount(ctx, is.ID)
		if err != nil {
			return err
		}
		if limit := s.o.Config.Mappings.MaxPerIssuer; limit > 0 && int(n) >= limit {
			return mappingViolation([]errs.FieldError{field("", errs.CodeMappingLimitReached, fmt.Sprintf("The issuer holds %d mappings, the limit ci.mappings.max_per_issuer.", limit))})
		}
		if err := s.checkProjectQuota(ctx, qs, is.ID, tr); err != nil {
			return err
		}
		sv, err := s.check(ctx, qs, is, tr, projectID, "", in)
		if err != nil {
			return err
		}
		id, principalID := newID(), newID()
		if err := s.checkPriority(ctx, qs, is.ID, tr.tier, in.Priority, id); err != nil {
			return err
		}
		if _, err := qs.CiMappingPrincipalInsert(ctx, q.CiMappingPrincipalInsertParams{ID: principalID, Name: is.Name + "/" + in.Name, Actor: principal.IDOrEmpty(caller.ID)}); err != nil {
			return err
		}
		rows, err := qs.CiMappingInsert(ctx, q.CiMappingInsertParams{
			ID: id, IssuerID: is.ID, PrincipalID: principalID, Tier: tr.tier, ProjectID: projectID, Name: in.Name,
			Priority: int32(in.Priority), Enabled: sv.enabled, SubjectKinds: sv.kinds, //nolint:gosec // 1 to 1000000, checked
			Match: conditionJSON(in.Match), Grants: storedGrants(sv.checked.Grants), MaxTtlSecs: derefInt(in.MaxTTLSeconds),
			Options: optionsJSON(sv.checked.Options), Description: in.Description, CreatedBy: mappingLabel(caller),
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return priorityTaken(in.Priority, tr.tier)
		}
		if !sv.enabled {
			if err := qs.CiMappingPrincipalSetStatus(ctx, q.CiMappingPrincipalSetStatusParams{Status: "disabled", ID: principalID}); err != nil {
				return err
			}
		}
		if _, _, err := s.applyBindings(ctx, tx, qs, principalID, sv.grants, in.Grants.Policies); err != nil {
			return err
		}
		if err := s.bump(ctx, tx, qs, is.ID); err != nil {
			return err
		}
		if err := s.notifyMappingState(tx, id); err != nil {
			return err
		}
		after, err := mappingByID(ctx, qs, id)
		if err != nil {
			return err
		}
		out = MappingResult{Mapping: after, Warnings: sv.warnings}
		return s.o.Recorder.RecordTx(ctx, tx, mappingEvent(audit.EventCIMappingCreated, actor, nil, &after, false))
	})
	if err != nil {
		return MappingResult{}, err
	}
	return out, nil
}

// bump increments the issuer's mappings_version and notifies the change area ci_mappings (R-CI-009).
func (s *MappingService) bump(ctx context.Context, tx meta.Tx, qs *q.Queries, issuerID string) error {
	if _, err := qs.CiIssuerBumpMappings(ctx, issuerID); err != nil {
		return err
	}
	return s.notifyMappings(tx, issuerID)
}

// checkProjectQuota is the 422 of a project's tier that holds ci.mappings.max_per_project mappings of the issuer
// (Q-CI-49): every exchange of the issuer evaluates every tier, so one project's mappings must neither fill the
// issuer's limit nor make the exchanges of every other project's jobs expensive. The issuer row is locked, so the
// count holds until the write commits.
func (s *MappingService) checkProjectQuota(ctx context.Context, qs *q.Queries, issuerID string, tr tierRef) error {
	limit := s.o.Config.Mappings.MaxPerProject
	if tr.project == "" || limit <= 0 {
		return nil
	}
	n, err := qs.CiMappingCountOfTier(ctx, q.CiMappingCountOfTierParams{IssuerID: issuerID, Tier: tr.tier})
	if err != nil {
		return err
	}
	if int(n) >= limit {
		return mappingViolation([]errs.FieldError{field("", errs.CodeMappingLimitReached,
			fmt.Sprintf("The project %q holds %d mappings of this issuer, the limit ci.mappings.max_per_project.", tr.project, limit))})
	}
	return nil
}

func priorityTaken(priority int, tier string) error {
	return errs.E(errs.Conflict, errs.CodePriorityTaken, fmt.Sprintf("Another mapping of the tier %q has the priority %d; priorities are unique per tier.", tier, priority))
}

// checkPriority is the 409 of a priority another mapping of the tier holds; the issuer row is locked, so
// nobody takes it between this read and the write.
func (s *MappingService) checkPriority(ctx context.Context, qs *q.Queries, issuerID, tier string, priority int, id string) error {
	holders, err := qs.CiMappingPriorityHolder(ctx, q.CiMappingPriorityHolderParams{IssuerID: issuerID, Tier: tier, Priority: int32(priority), ID: id}) //nolint:gosec // 1 to 1000000, checked
	if err != nil {
		return err
	}
	if len(holders) > 0 {
		return priorityTaken(priority, tier)
	}
	return nil
}

// revokeTokens ends the unexpired tokens issued through a mapping, in the caller's transaction. A mapping
// through which no token was ever issued has no issuance record and nothing to revoke; the revoker is asked
// only otherwise, so the service does not depend on it before the first exchange can happen.
func (s *MappingService) revokeTokens(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, m Mapping) error {
	rows, err := qs.CiMappingHasIssuances(ctx, q.CiMappingHasIssuancesParams{IssuerID: m.IssuerID, ID: m.ID})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if s.o.Revoker == nil {
		return NotImplemented("W5")
	}
	_, err = s.o.Revoker.RevokeMappingTx(ctx, tx, caller, m, CauseMapping)
	return err
}

// lockMapping takes the issuer row FOR UPDATE and reads the mapping under it; the tier check of a
// project's form is the caller's (`project` non-empty). expected is the version the client saw
// (errs.AnyRevision for none).
func (s *MappingService) lockMapping(ctx context.Context, qs *q.Queries, issuer, name, project string, expected int64) (Issuer, Mapping, error) {
	lock, err := lockIssuer(ctx, qs, issuer, errs.AnyRevision)
	if err != nil {
		return Issuer{}, Mapping{}, err
	}
	is, _, err := loadByID(ctx, qs, lock.ID)
	if err != nil {
		return Issuer{}, Mapping{}, err
	}
	m, ok, err := mappingByName(ctx, qs, issuer, name)
	if err != nil {
		return Issuer{}, Mapping{}, err
	}
	if !ok || (project != "" && m.Tier != ProjectTier(project)) {
		return Issuer{}, Mapping{}, mappingNotFound(issuer, name)
	}
	if expected != errs.AnyRevision && expected != m.Version {
		return Issuer{}, Mapping{}, errs.Stale("The CI mapping", m.Version)
	}
	return is, m, nil
}

// Replace is a full replacement under If-Match; name is immutable. One event, chosen by enabled. A mapping of
// a project's tier keeps the bounds of its tier when a system administrator replaces it.
func (s *MappingService) Replace(ctx context.Context, caller Actor, issuer, name string, expected int64, in MappingInput) (MappingResult, error) {
	return s.replace(ctx, caller, issuer, name, "", expected, in)
}

// ReplaceIn is the full replacement of a project's mapping under If-Match; name and issuer are immutable.
func (s *MappingService) ReplaceIn(ctx context.Context, caller Actor, project, issuer, name string, expected int64, in MappingInput) (MappingResult, error) {
	return s.replace(ctx, caller, issuer, name, project, expected, in)
}

func (s *MappingService) replace(ctx context.Context, caller Actor, issuer, name, project string, expected int64, in MappingInput) (MappingResult, error) {
	if in.Name == "" {
		in.Name = name
	}
	fields := s.shapeFields(in)
	if in.Name != name {
		fields = append(fields, field("/name", "immutable", "The member cannot be changed after the resource was created."))
	}
	if in.Issuer != "" && in.Issuer != issuer {
		fields = append(fields, field("/issuer", "immutable", "The member cannot be changed after the resource was created."))
	}
	if err := mappingViolation(uniqueFields(fields)); err != nil {
		return MappingResult{}, err
	}
	actor := actorOf(caller)
	var out MappingResult
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		is, before, err := s.lockMapping(ctx, qs, issuer, name, project, expected)
		if err != nil {
			return err
		}
		tr := tierRef{tier: before.Tier, project: before.Project}
		sv, err := s.check(ctx, qs, is, tr, before.ProjectID, before.PrincipalID, in)
		if err != nil {
			return err
		}
		if err := s.checkPriority(ctx, qs, is.ID, before.Tier, in.Priority, before.ID); err != nil {
			return err
		}
		if before.Enabled && !sv.enabled {
			if err := s.revokeTokens(ctx, tx, qs, caller, before); err != nil {
				return err
			}
		}
		if before.Enabled != sv.enabled {
			if err := s.setStatus(ctx, tx, qs, before.PrincipalID, sv.enabled); err != nil {
				return err
			}
			if err := s.notifyMappingState(tx, before.ID); err != nil {
				return err
			}
		}
		rows, err := qs.CiMappingUpdate(ctx, q.CiMappingUpdateParams{
			ID: before.ID, Priority: int32(in.Priority), Enabled: sv.enabled, SubjectKinds: sv.kinds, //nolint:gosec // 1 to 1000000, checked
			Match: conditionJSON(in.Match), Grants: storedGrants(sv.checked.Grants), MaxTtlSecs: derefInt(in.MaxTTLSeconds),
			Options: optionsJSON(sv.checked.Options), Description: in.Description,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return mappingNotFound(issuer, name)
		}
		if _, _, err := s.applyBindings(ctx, tx, qs, before.PrincipalID, sv.grants, in.Grants.Policies); err != nil {
			return err
		}
		if err := s.bump(ctx, tx, qs, is.ID); err != nil {
			return err
		}
		after, err := mappingByID(ctx, qs, before.ID)
		if err != nil {
			return err
		}
		out = MappingResult{Mapping: after, Warnings: sv.warnings}
		return s.o.Recorder.RecordTx(ctx, tx, mappingEvent(mappingReplaceEvent(before, after), actor, &before, &after, false))
	})
	if err != nil {
		return MappingResult{}, err
	}
	return out, nil
}

// setStatus mirrors enabled to the principal's status in the transaction (R-CI-017): token validation
// refuses every token of a disabled mapping with principal_disabled, also one that escaped the sweep.
func (s *MappingService) setStatus(ctx context.Context, tx meta.Tx, qs *q.Queries, principalID string, enabled bool) error {
	status := "disabled"
	if enabled {
		status = "active"
	}
	if err := qs.CiMappingPrincipalSetStatus(ctx, q.CiMappingPrincipalSetStatusParams{Status: status, ID: principalID}); err != nil {
		return err
	}
	return principal.NotifyPrincipals(s.o.Changes, tx, principal.ChangePrincipal, principalID)
}

// Delete deletes the mapping, revokes its tokens and deletes its principal. expected is optional.
func (s *MappingService) Delete(ctx context.Context, caller Actor, issuer, name string, expected int64) error {
	return s.delete(ctx, caller, issuer, name, "", expected)
}

// DeleteIn deletes a project's mapping, revokes its tokens and deletes its principal.
func (s *MappingService) DeleteIn(ctx context.Context, caller Actor, project, issuer, name string, expected int64) error {
	return s.delete(ctx, caller, issuer, name, project, expected)
}

func (s *MappingService) delete(ctx context.Context, caller Actor, issuer, name, project string, expected int64) error {
	actor := actorOf(caller)
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		is, before, err := s.lockMapping(ctx, qs, issuer, name, project, expected)
		if err != nil {
			return err
		}
		if err := s.revokeTokens(ctx, tx, qs, caller, before); err != nil {
			return err
		}
		if err := s.remove(ctx, tx, qs, before); err != nil {
			return err
		}
		if err := s.bump(ctx, tx, qs, is.ID); err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, mappingEvent(audit.EventCIMappingDeleted, actor, &before, nil, false))
	})
}

// remove deletes the mapping row and its principal; the policy entries, role bindings and owner entries
// of the principal go with it through their foreign keys (R-AUTHZ-002), and a mapping created later under
// the same name is a new principal.
func (s *MappingService) remove(ctx context.Context, tx meta.Tx, qs *q.Queries, m Mapping) error {
	if err := s.releaseBindings(ctx, tx, qs, m.PrincipalID); err != nil {
		return err
	}
	if err := qs.CiMappingDelete(ctx, m.ID); err != nil {
		return err
	}
	if err := qs.CiMappingPrincipalDelete(ctx, m.PrincipalID); err != nil {
		return err
	}
	if err := s.notifyMappingState(tx, m.ID); err != nil {
		return err
	}
	return principal.NotifyPrincipals(s.o.Changes, tx, principal.ChangePrincipal, m.PrincipalID)
}

// DeleteForIssuerTx deletes every mapping of an issuer and its principal in the caller's transaction, for
// the cascade of an issuer's deletion (R-CI-002): one ci.mapping.deleted per mapping with its journal entry.
// The mappings' tokens are left to the issuer's revocation in the same transaction, and the issuer row
// stays locked by the caller, so the service neither locks it nor bumps its mappings_version.
func (s *MappingService) DeleteForIssuerTx(ctx context.Context, tx meta.Tx, caller Actor, issuerID, _ string) (int, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return 0, err
	}
	rows, err := qs.CiMappingsOfIssuerID(ctx, issuerID)
	if err != nil {
		return 0, err
	}
	ms, err := readMappings(ctx, qs, listRows(rows))
	if err != nil {
		return 0, err
	}
	actor := actorOf(caller)
	for _, m := range ms {
		if err := s.remove(ctx, tx, qs, m); err != nil {
			return 0, err
		}
		if err := s.o.Recorder.RecordTx(ctx, tx, mappingEvent(audit.EventCIMappingDeleted, actor, &m, nil, true)); err != nil {
			return 0, err
		}
	}
	return len(ms), nil
}
