package ci

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ConfigurationActor is the caller of the writes `trove migrate` makes for the declared issuers: the
// audit events carry the actor system:configuration, as those of the declared role bindings do.
func ConfigurationActor() Actor { return authz.ConfigurationSubject() }

// ApplyDeclared makes the issuers with source configuration exactly the ones ci.issuers declares
// (R-CI-001, Q-CI-16), in the caller's transaction, which is the one of `trove migrate`: no replica
// calls it, so replicas of two releases during a rolling update never overwrite each other.
//
//   - A declared issuer is created, or replaced by name when the stored settings differ; one that the
//     configuration still declares exactly as stored is left alone, so a migration that changes nothing
//     writes nothing. A declared issuer that was removed and is declared again is enabled as declared and
//     loses removedFromConfiguration.
//   - A declared name or issuer URL that belongs to an issuer the API created stops the migration with an
//     error that names both; the transaction rolls back and nothing is applied.
//   - A configuration issuer that is no longer declared is disabled and flagged
//     removedFromConfiguration: its mappings and issuance records stay, its tokens are revoked.
//
// Every change notifies ci_mappings and records the same event the API writes, with the actor
// system:configuration.
func (s *Service) ApplyDeclared(ctx context.Context, tx meta.Tx, declared []DeclaredIssuer) error {
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	caller := ConfigurationActor()
	names := make([]string, 0, len(declared))
	for _, d := range declared {
		names = append(names, d.Name)
		if err := s.applyOne(ctx, tx, qs, caller, d); err != nil {
			return fmt.Errorf("ci.issuers entry %q: %w", d.Name, err)
		}
	}
	rows, err := qs.CiIssuerConfigured(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if slices.Contains(names, r.Name) || (!r.Enabled && r.RemovedFromConfiguration) {
			continue
		}
		if err := s.removeOne(ctx, tx, qs, caller, r.Name); err != nil {
			return fmt.Errorf("ci.issuers entry %q: %w", r.Name, err)
		}
	}
	return nil
}

func (s *Service) applyOne(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, d DeclaredIssuer) error {
	next, fields := buildIssuer(d, s.limits())
	if len(fields) > 0 {
		msgs := make([]string, 0, len(fields))
		for _, f := range fields {
			msgs = append(msgs, f.Pointer+": "+f.Detail)
		}
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	next.Source = SourceConfiguration
	rows, err := qs.CiIssuerLock(ctx, d.Name)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return s.insertDeclared(ctx, tx, qs, caller, next)
	}
	before, _, err := loadByID(ctx, qs, rows[0].ID)
	if err != nil {
		return err
	}
	if before.Source != SourceConfiguration {
		return fmt.Errorf("the name is also the name of the CI issuer created through the API for %s; delete or rename one of them", before.Issuer)
	}
	if other, err := qs.CiIssuerByIssuer(ctx, next.Issuer); err != nil {
		return err
	} else if len(other) > 0 && other[0].ID != before.ID {
		return fmt.Errorf("the issuer URL %s already belongs to the CI issuer %q (source %s)", next.Issuer, other[0].Name, other[0].Source)
	}
	next.ID, next.RemovedFromConfiguration = before.ID, false
	if sameSettings(before, next) {
		return nil
	}
	_, err = s.writeUpdate(ctx, tx, qs, caller, before, next)
	return err
}

func (s *Service) insertDeclared(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, next Issuer) error {
	if other, err := qs.CiIssuerByIssuer(ctx, next.Issuer); err != nil {
		return err
	} else if len(other) > 0 {
		return fmt.Errorf("the issuer URL %s already belongs to the CI issuer %q (source %s)", next.Issuer, other[0].Name, other[0].Source)
	}
	next.ID = newID()
	if _, err := qs.CiIssuerInsert(ctx, insertParams(next)); err != nil {
		return err
	}
	if err := s.notifyMappings(tx, next.ID); err != nil {
		return err
	}
	created, _, err := loadByID(ctx, qs, next.ID)
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, issuerEvent(audit.EventCIIssuerCreated, actorOf(caller), nil, &created, false, 0))
}

// removeOne disables a configuration issuer that is no longer declared and flags it, keeping its mappings
// and issuance records (Q-CI-16).
func (s *Service) removeOne(ctx context.Context, tx meta.Tx, qs *q.Queries, caller Actor, name string) error {
	rows, err := qs.CiIssuerLock(ctx, name)
	if err != nil || len(rows) == 0 {
		return err
	}
	before, _, err := loadByID(ctx, qs, rows[0].ID)
	if err != nil {
		return err
	}
	next := before
	next.Enabled, next.RemovedFromConfiguration = false, true
	_, err = s.writeUpdate(ctx, tx, qs, caller, before, next)
	return err
}
