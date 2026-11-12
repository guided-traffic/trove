// Package visibility holds the installation's default visibility level (R-API-056) and the rules about levels that the project
// and the repository services share (R-AUTHZ-033): the three levels and their order, the validation of a level named by a
// client, and the refusal of public while the default endpoint admits no request without a credential. It imports neither the
// authorization engine nor the repository package, so both can use it.
package visibility

import (
	"context"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The three levels as the API and the database spell them, from the most restrictive.
const (
	Private  = "private"
	Internal = "internal"
	Public   = "public"
)

// MaxPublicObjects is the most projects, and the most repositories, that publicObjects lists (R-API-056).
const MaxPublicObjects = 100

// Rank orders the levels: private 0, internal 1, public 2; -1 for a string that is no level.
func Rank(level string) int {
	switch level {
	case Private:
		return 0
	case Internal:
		return 1
	case Public:
		return 2
	}
	return -1
}

// Valid reports whether level is one of the three levels.
func Valid(level string) bool { return Rank(level) >= 0 }

// Lower returns the lower of two valid levels.
func Lower(a, b string) string {
	if Rank(b) < Rank(a) {
		return b
	}
	return a
}

// Above reports whether level a is more open than level b.
func Above(a, b string) bool { return Rank(a) > Rank(b) }

// PublicNotAllowed is the 409 of a write that sets public while the default endpoint admits no request without a credential
// (R-AUTHZ-033 rule 5).
func PublicNotAllowed() error {
	return errs.E(errs.Conflict, errs.CodePublicNotAllowed,
		"The level public needs the default endpoint to admit requests without a credential (endpoints.default.auth.anonymous).")
}

// InvalidLevel is the 422 field error of a member that names no level.
func InvalidLevel(pointer string) errs.FieldError {
	return errs.FieldError{Pointer: pointer, Code: "invalid_value", Detail: "The level is one of private, internal and public."}
}

// Queries binds the generated queries to the PostgreSQL transaction behind tx.
func Queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("visibility: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

// Default reads the installation's default level in the transaction that creates a project or a repository (R-AUTHZ-033 rule 1).
func Default(ctx context.Context, qs *q.Queries) (string, error) {
	rows, err := qs.VisibilityDefaultGet(ctx)
	if err != nil {
		return "", err
	}
	if len(rows) != 1 || !Valid(rows[0].DefaultLevel) {
		return "", errors.New("visibility: the settings row is missing")
	}
	return rows[0].DefaultLevel, nil
}

// Settings is the installation's visibility settings as the API shows them.
type Settings struct {
	DefaultLevel    string
	PublicAvailable bool
	PublicObjects   PublicObjects
	// Version is the ETag source; it moves with the default level.
	Version int64
}

// PublicObjects are the projects and the repositories whose artifacts are public, at most MaxPublicObjects of each in key order.
type PublicObjects struct {
	Projects     []string
	Repositories []string
	Truncated    bool
}

// Caller is who writes the settings and from where; it goes into the audit event.
type Caller struct {
	Actor  audit.Actor
	Source audit.Source
}

// Options configure [New].
type Options struct {
	// DB runs the writes in transactions.
	DB meta.Runner
	// Recorder records settings.visibility.changed in the transaction of the write.
	Recorder audit.Recorder
	// PublicAvailable reports whether the default endpoint admits requests without a credential
	// (endpoints.default.auth.anonymous, R-EP-010); nil means it does not.
	PublicAvailable func() bool
}

// Service reads and writes the installation's default level.
type Service struct {
	o Options
}

// New creates the service.
func New(o Options) *Service {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &Service{o: o}
}

func (s *Service) publicAvailable() bool { return s.o.PublicAvailable != nil && s.o.PublicAvailable() }

// Get returns the settings, read from PostgreSQL and not from a cache.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := Queries(tx)
		if err != nil {
			return err
		}
		out, err = s.read(ctx, qs)
		return err
	})
	return out, err
}

func (s *Service) read(ctx context.Context, qs *q.Queries) (Settings, error) {
	rows, err := qs.VisibilityDefaultGet(ctx)
	if err != nil {
		return Settings{}, err
	}
	if len(rows) != 1 {
		return Settings{}, errors.New("visibility: the settings row is missing")
	}
	out := Settings{DefaultLevel: rows[0].DefaultLevel, Version: rows[0].Version, PublicAvailable: s.publicAvailable()}
	projects, err := qs.VisibilityPublicProjects(ctx, MaxPublicObjects+1)
	if err != nil {
		return Settings{}, err
	}
	repos, err := qs.VisibilityPublicRepositories(ctx, MaxPublicObjects+1)
	if err != nil {
		return Settings{}, err
	}
	out.PublicObjects = PublicObjects{Projects: []string{}, Repositories: []string{}}
	if len(projects) > MaxPublicObjects {
		projects, out.PublicObjects.Truncated = projects[:MaxPublicObjects], true
	}
	if len(repos) > MaxPublicObjects {
		repos, out.PublicObjects.Truncated = repos[:MaxPublicObjects], true
	}
	out.PublicObjects.Projects = append(out.PublicObjects.Projects, projects...)
	out.PublicObjects.Repositories = append(out.PublicObjects.Repositories, repos...)
	return out, nil
}

// Update sets the default level (R-API-056) under the version check: expected is the version the client saw
// (errs.AnyRevision is the deliberate blind write). 422 validation_failed for a value that is no level, 409
// public_not_allowed while public is unavailable, 412 for a stale version. It records settings.visibility.changed with the
// default before and after, in the same transaction; setting the level it has changes nothing and records nothing. The change
// is not journaled and reaches no cache: the value is read in the transaction that creates a project or a repository.
func (s *Service) Update(ctx context.Context, c Caller, expected int64, level string) (Settings, error) {
	if !Valid(level) {
		return Settings{}, errs.E(errs.Invalid, errs.CodeValidation, "The level is one of private, internal and public.", errs.WithFields(InvalidLevel("/defaultLevel")))
	}
	var out Settings
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := Queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.VisibilityDefaultLock(ctx)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("visibility: the settings row is missing")
		}
		cur := rows[0]
		if expected != errs.AnyRevision && expected != cur.Version {
			return errs.Stale("The visibility settings", cur.Version)
		}
		if cur.DefaultLevel != level {
			if level == Public && !s.publicAvailable() {
				return PublicNotAllowed()
			}
			if _, err := qs.VisibilityDefaultSet(ctx, q.VisibilityDefaultSetParams{Level: level, Actor: principal.IDOrEmpty(c.Actor.ID)}); err != nil {
				return fmt.Errorf("visibility: set the default: %w", err)
			}
			ev := audit.Event{
				Type: "settings.visibility.changed", Actor: c.Actor, Source: c.Source, Outcome: audit.Success,
				Subject: audit.Subject{Kind: "settings", Name: "visibility"},
				Details: []byte(fmt.Sprintf(`{"before":{"defaultLevel":%q},"after":{"defaultLevel":%q}}`, cur.DefaultLevel, level)),
			}
			if err := s.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
				return err
			}
		}
		out, err = s.read(ctx, qs)
		return err
	})
	return out, err
}
