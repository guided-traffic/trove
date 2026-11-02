package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The writes of an issuer (R-CI-002). Every write that changes an issuer runs in one READ COMMITTED
// transaction that takes the issuer row FOR UPDATE, moves its version, notifies the change area
// ci_mappings, records its audit event and, when the issuer is disabled or deleted, revokes its tokens
// in that same transaction. The network (discovery and keys) is touched before the transaction and never
// inside it (R-ARCH-019).

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil { // the values are strings, numbers, booleans and records of them
		panic(err)
	}
	return b
}

// issuerEvent builds the event of a change of an issuer: the full record before and after (R-CI-020).
// The journal entry of .updated, .disabled and .deleted carries what replay needs (R-HA-054).
func issuerEvent(name string, actor audit.Actor, before, after *Issuer, cascade bool, mappings int) audit.Event {
	subj := after
	if subj == nil {
		subj = before
	}
	details := map[string]any{
		"id": subj.ID, "name": subj.Name, "issuer": subj.Issuer, "version": subj.Version, "source": subj.Source,
		"before": recordPtr(before), "after": recordPtr(after),
	}
	if name == audit.EventCIIssuerDeleted {
		details["cascade"] = cascade
		details["mappingsDeleted"] = mappings
	}
	ev := audit.Event{
		Type: name, Actor: actor, Outcome: audit.Success,
		Subject: audit.Subject{Kind: "ci_issuer", ID: subj.ID, Name: subj.Name}, Details: mustJSON(details),
	}
	switch name {
	case audit.EventCIIssuerUpdated:
		ev.Journal = mustJSON(map[string]any{"issuerId": subj.ID, "name": subj.Name, "before": recordPtr(before), "after": recordPtr(after)})
	case audit.EventCIIssuerDisabled, audit.EventCIIssuerDeleted:
		ev.Journal = mustJSON(map[string]any{"issuerId": subj.ID, "name": subj.Name, "before": recordPtr(before)})
	}
	return ev
}

func recordPtr(is *Issuer) *record {
	if is == nil {
		return nil
	}
	return recordOf(*is)
}

// replaceEvent is the one event of a PUT, chosen by enabled (R-CI-020): .disabled when it changes true to
// false, .enabled when false to true, .updated otherwise.
func replaceEvent(before, after Issuer) string {
	switch {
	case before.Enabled && !after.Enabled:
		return audit.EventCIIssuerDisabled
	case !before.Enabled && after.Enabled:
		return audit.EventCIIssuerEnabled
	}
	return audit.EventCIIssuerUpdated
}

// revokeTokens ends the unexpired tokens issued through an issuer, in the caller's transaction. An issuer
// that never issued one has an empty issuance table and nothing to revoke; the revoker (work package W5)
// is asked only otherwise, so no window exists in which a token of a disabled or deleted issuer is usable
// and the service does not depend on the revoker before the first exchange can happen.
func (s *Service) revokeTokens(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, is Issuer) error {
	rows, err := qs.CiIssuerHasIssuances(ctx, is.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if s.o.Revoker == nil {
		return NotImplemented("W5")
	}
	_, err = s.o.Revoker.RevokeIssuerTx(ctx, tx, caller, is.ID, is.Name, CauseIssuer)
	return err
}

// Create creates an issuer (R-CI-002): 201 with the defaults filled in. With validate it first fetches the
// discovery document and the key set, outside any transaction, and stores the set with the issuer.
func (s *Service) Create(ctx context.Context, caller Actor, in IssuerInput, validate bool) (Issuer, error) {
	is, fields := buildIssuer(in, s.limits())
	if err := validationError(fields); err != nil {
		return Issuer{}, err
	}
	is.ID, is.Source = newID(), SourceAPI
	var res *fetched
	if validate {
		var err error
		if res, err = s.fetcher.fetch(ctx, is); err != nil {
			return Issuer{}, s.validationFailure(ctx, is, err)
		}
	}
	actor := actorOf(caller)
	var out Issuer
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.CiIssuerInsert(ctx, insertParams(is))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return s.createConflict(ctx, qs, is)
		}
		if err := s.notifyMappings(tx, is.ID); err != nil {
			return err
		}
		if res != nil {
			if _, err := s.storeKeysTx(ctx, tx, qs, is, res, TriggerAdmin, actor); err != nil {
				return err
			}
		}
		created, _, err := loadByID(ctx, qs, is.ID)
		if err != nil {
			return err
		}
		out = created
		return s.o.Recorder.RecordTx(ctx, tx, issuerEvent(audit.EventCIIssuerCreated, actor, nil, &out, false, 0))
	})
	if err != nil {
		return Issuer{}, err
	}
	return out, nil
}

// validationFailure turns a failed fetch of a create into its 422 (R-CI-002) and counts it. The failure
// is not stored: the issuer does not exist yet.
func (s *Service) validationFailure(ctx context.Context, is Issuer, err error) error {
	var fe *FetchError
	if !errors.As(err, &fe) {
		return err
	}
	s.count(is.Name, TriggerAdmin, OutcomeError)
	if fe.Class == classCanceled {
		return ctx.Err()
	}
	code := string(fe.code())
	return errs.E(errs.Invalid, code,
		"The discovery document or the key set of the issuer cannot be used: "+fe.Error()+".",
		errs.WithFields(errs.FieldError{Pointer: "/issuer", Code: code, Detail: fe.Error()}))
}

// createConflict names what exists: the name or the issuer URL.
func (s *Service) createConflict(ctx context.Context, qs *q.Queries, is Issuer) error {
	if rows, err := qs.CiIssuerByName(ctx, is.Name); err == nil && len(rows) > 0 {
		return errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("A CI issuer named %q exists.", is.Name))
	}
	if rows, err := qs.CiIssuerByIssuer(ctx, is.Issuer); err == nil && len(rows) > 0 {
		return errs.E(errs.Conflict, errs.CodeAlreadyExists,
			fmt.Sprintf("The issuer URL %q belongs to the CI issuer %q: an iss claim selects at most one issuer (R-CI-005).", is.Issuer, rows[0].Name))
	}
	return errs.E(errs.Conflict, errs.CodeAlreadyExists, "A CI issuer with this name or issuer URL exists.")
}

// lockIssuer takes the issuer row FOR UPDATE by name and checks the caller's version; it returns the
// locked row.
func lockIssuer(ctx context.Context, qs *q.Queries, name string, expected int64) (q.CiIssuerLockRow, error) {
	rows, err := qs.CiIssuerLock(ctx, name)
	if err != nil {
		return q.CiIssuerLockRow{}, err
	}
	if len(rows) == 0 {
		return q.CiIssuerLockRow{}, notFound(name)
	}
	if expected != errs.AnyRevision && expected != rows[0].Version {
		return q.CiIssuerLockRow{}, errs.Stale("The CI issuer", rows[0].Version)
	}
	return rows[0], nil
}

// managedByConfiguration is the 409 of a write of an issuer that ci.issuers declares (Q-CI-16).
func managedByConfiguration(is Issuer) error {
	return errs.E(errs.Conflict, errs.CodeIssuerManagedByConfiguration,
		fmt.Sprintf("The CI issuer %q is declared in ci.issuers of the system configuration; change or remove it there.", is.Name),
		errs.WithExtension("removedFromConfiguration", is.RemovedFromConfiguration))
}

// Replace replaces the full representation of an issuer under If-Match (R-CI-002). name, issuer and kind
// are immutable; an omitted optional member takes its default.
func (s *Service) Replace(ctx context.Context, caller Actor, name string, expected int64, in IssuerInput) (Issuer, error) {
	if in.Name == "" {
		in.Name = name
	}
	next, fields := buildIssuer(in, s.limits())
	if in.Name != name {
		fields = append(fields, field("/name", "immutable", "The member cannot be changed after the resource was created."))
	}
	var out Issuer
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		lock, err := lockIssuer(ctx, qs, name, expected)
		if err != nil {
			return err
		}
		before, _, err := loadByID(ctx, qs, lock.ID)
		if err != nil {
			return err
		}
		if before.Source == SourceConfiguration {
			return managedByConfiguration(before)
		}
		all := append([]errs.FieldError(nil), fields...)
		if in.Kind != before.Kind {
			all = append(all, field("/kind", "immutable", "The member cannot be changed after the resource was created."))
		}
		if in.Issuer != before.Issuer {
			all = append(all, field("/issuer", "immutable", "The member cannot be changed after the resource was created."))
		}
		if err := validationError(uniqueFields(all)); err != nil {
			return err
		}
		next.ID, next.Source, next.RemovedFromConfiguration = before.ID, before.Source, before.RemovedFromConfiguration
		out, err = s.writeUpdate(ctx, tx, qs, caller, before, next)
		return err
	})
	if err != nil {
		return Issuer{}, err
	}
	return out, nil
}

// uniqueFields removes a second field error of one pointer, so a kind that is both refused and
// immutable answers once.
func uniqueFields(fields []errs.FieldError) []errs.FieldError {
	seen := map[string]bool{}
	out := fields[:0:0]
	for _, f := range fields {
		if seen[f.Pointer] {
			continue
		}
		seen[f.Pointer] = true
		out = append(out, f)
	}
	return out
}

// writeUpdate updates the locked row, notifies, revokes when the issuer is disabled and records the one
// event of the change. before is the row as it was read under the lock.
func (s *Service) writeUpdate(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, before, next Issuer) (Issuer, error) {
	actor := actorOf(caller)
	if before.Enabled && !next.Enabled {
		if err := s.revokeTokens(ctx, tx, qs, caller, before); err != nil {
			return Issuer{}, err
		}
	}
	rows, err := qs.CiIssuerUpdate(ctx, updateParams(next))
	if err != nil {
		return Issuer{}, err
	}
	if len(rows) == 0 {
		return Issuer{}, notFound(before.Name)
	}
	if err := s.notifyMappings(tx, before.ID); err != nil {
		return Issuer{}, err
	}
	after, _, err := loadByID(ctx, qs, before.ID)
	if err != nil {
		return Issuer{}, err
	}
	if err := s.o.Recorder.RecordTx(ctx, tx, issuerEvent(replaceEvent(before, after), actor, &before, &after, false, 0)); err != nil {
		return Issuer{}, err
	}
	return after, nil
}

// Delete deletes an issuer (R-CI-002): it refuses while mappings exist unless cascade, which deletes the
// mappings and their principals in the same transaction, revokes the tokens of the issuer and records
// ci.issuer.deleted. A configuration issuer is deletable only once it is removed from the configuration.
func (s *Service) Delete(ctx context.Context, caller Actor, name string, cascade bool, expected int64) error {
	actor := actorOf(caller)
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		lock, err := lockIssuer(ctx, qs, name, expected)
		if err != nil {
			return err
		}
		before, _, err := loadByID(ctx, qs, lock.ID)
		if err != nil {
			return err
		}
		if before.Source == SourceConfiguration && !before.RemovedFromConfiguration {
			return managedByConfiguration(before)
		}
		count, err := qs.CiIssuerMappingCount(ctx, before.ID)
		if err != nil {
			return err
		}
		mappings := 0
		if len(count) > 0 {
			mappings = int(count[0])
		}
		if mappings > 0 {
			if !cascade {
				return errs.E(errs.Conflict, errs.CodeConflict,
					fmt.Sprintf("The CI issuer %q has %d mappings; delete them first or send cascade=true.", name, mappings))
			}
			if s.o.Mappings == nil {
				return NotImplemented("W3")
			}
			if mappings, err = s.o.Mappings.DeleteForIssuerTx(ctx, tx, caller, before.ID, before.Name); err != nil {
				return err
			}
		}
		if err := s.revokeTokens(ctx, tx, qs, caller, before); err != nil {
			return err
		}
		if err := qs.CiIssuerDelete(ctx, before.ID); err != nil {
			return err
		}
		if err := s.notifyMappings(tx, before.ID); err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, issuerEvent(audit.EventCIIssuerDeleted, actor, &before, nil, cascade, mappings))
	})
}
