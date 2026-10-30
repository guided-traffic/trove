package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("repository: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package
// writes no SQL: its statements live in
// internal/meta/postgres/queries/repository.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// readTx runs fn in a read-only use of a transaction of the runner.
func readRows(ctx context.Context, run meta.Runner, fn func(*q.Queries) ([]q.Repository, error)) ([]q.Repository, error) {
	var rows []q.Repository
	err := run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err = fn(qs)
		return err
	})
	return rows, err
}

// toRepository converts a row to the read model and compiles the repository's
// patterns. A row whose stored document does not decode, or whose patterns do not
// compile, cannot have been written by this package; it is loaded disabled, so
// that it takes no part in resolution, download or deploy (fail closed), and the
// returned error says why.
func toRepository(r q.Repository, cat *catalog.Registry) (Repository, error) {
	out := Repository{
		ID: strconv.FormatInt(r.ID, 10), Key: r.Key, Class: Class(r.Class), Format: r.Format,
		HideExistence: HideInherit, Lifecycle: Lifecycle(r.Lifecycle), Version: r.ConfigVersion,
		Config: r.Config, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.LastActivityAt.Valid {
		out.LastActivityAt = r.LastActivityAt.Time
	}
	var s Settings
	if err := json.Unmarshal(r.Config, &s); err != nil {
		out.Disabled = true
		return out, err
	}
	s.normalize()
	out.Disabled, out.RequireAuthentication, out.HideExistence = s.Disabled, s.RequireAuthentication, s.HideExistence
	subject := SubjectPath
	if info, ok := cat.Lookup(r.Format); ok {
		subject = info.PatternSubject
	}
	adm, err := NewAdmission(subject, s.IncludePatterns, s.ExcludePatterns)
	if err != nil {
		out.Disabled = true
		return out, err
	}
	out.adm = adm
	return out, nil
}

// toDocument renders the stored row as the configuration document.
func toDocument(r q.Repository, cat *catalog.Registry) (Document, error) {
	d := Document{Key: r.Key, Class: Class(r.Class), Format: r.Format}
	if err := json.Unmarshal(r.Config, &d.Settings); err != nil {
		return Document{}, err
	}
	d.normalize()
	if info, ok := cat.Lookup(r.Format); ok {
		d.Layout = info.Layout
	}
	return d, nil
}

func notFound(key string) *errs.Error {
	return errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository has the key "+strconv.Quote(key))
}
