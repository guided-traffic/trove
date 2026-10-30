// Package packages holds the generic package rows of a repository: the packages,
// their versions, the links from items to versions and the namespaced item
// properties (R-ARCH-009, FMT-007, storage.md). The format contract offers them to
// a format as PackageStore and PackageWriter; internal/formathost adapts the types.
//
// The writer owns the one rule a format must not be able to forget: every write
// that changes the rows of a package raises packages.rev_seq of that package, once
// per transaction however many writes there are (R-ARCH-009, R-ARCH-012,
// Q-ARCH-212). The revision is what clients hold as an ETag and what the merged-
// metadata cache of a virtual repository compares, so a write without it would
// serve stale documents.
//
// The package reaches PostgreSQL through sqlc queries only
// (internal/meta/postgres/queries/packages.sql) and imports no driver type.
package packages

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("packages: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// Ref names a package row. Name is the normalized name.
type Ref struct {
	ID     int64
	RepoID int64
	Name   string
	// RevSeq is the revision as read under the lock.
	RevSeq int64
}

// Package is a package row.
type Package struct {
	Ref
	DisplayName string
	Attributes  json.RawMessage
}

// Version is a live version of a package. LeadItem is 0 for none.
type Version struct {
	ID             int64
	Version        string
	DisplayVersion string
	LeadItem       int64
	Attributes     json.RawMessage
	CreatedAt      time.Time
	// CreatedBy is a principal id, "" for none.
	CreatedBy string
}

// NewVersion is a version to insert. LeadItem is 0 for none.
type NewVersion struct {
	Version        string
	DisplayVersion string
	LeadItem       int64
	Attributes     json.RawMessage
	CreatedBy      string
}

// Store reads the package rows. It asks PostgreSQL every time: the rows are the
// source of the documents a format renders (R-ARCH-012).
type Store struct{ run meta.Runner }

// NewStore creates the store.
func NewStore(run meta.Runner) (*Store, error) {
	if run == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "packages: no transaction runner")
	}
	return &Store{run: run}, nil
}

func (s *Store) read(ctx context.Context, fn func(*q.Queries) error) error {
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	})
}

// Package returns the row of a normalized name, found false when there is none.
func (s *Store) Package(ctx context.Context, repoID int64, name string) (p Package, found bool, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetPackage(ctx, q.GetPackageParams{RepoID: repoID, Name: name})
		if err != nil || len(rows) == 0 {
			return err
		}
		r := rows[0]
		p = Package{Ref: Ref{ID: r.ID, RepoID: r.RepoID, Name: r.Name, RevSeq: r.RevSeq}, DisplayName: r.DisplayName, Attributes: r.Attributes}
		found = true
		return nil
	})
	return p, found, err
}

// Versions lists the live versions of a package with its revision; a package that
// does not exist has none and revision 0. The revision is read before the
// versions, so a concurrent write makes the rows newer than the revision, never
// older: a document rendered from them carries an ETag that is at worst too old,
// and a client that holds it is told so by the next change.
func (s *Store) Versions(ctx context.Context, repoID int64, name string) (vs []Version, rev int64, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetPackage(ctx, q.GetPackageParams{RepoID: repoID, Name: name})
		if err != nil || len(rows) == 0 {
			return err
		}
		rev = rows[0].RevSeq
		vrows, err := qs.ListLiveVersions(ctx, rows[0].ID)
		if err != nil {
			return err
		}
		vs = make([]Version, len(vrows))
		for i, r := range vrows {
			vs[i] = Version{
				ID: r.ID, Version: r.Version, DisplayVersion: r.DisplayVersion, LeadItem: r.LeadItemID,
				Attributes: r.Attributes, CreatedAt: r.CreatedAt.Time, CreatedBy: r.CreatedBy,
			}
		}
		return nil
	})
	return vs, rev, err
}

// RevSeqs returns packages.rev_seq of a package name per repository id; a
// repository without the package maps to 0 (R-ARCH-015).
func (s *Store) RevSeqs(ctx context.Context, repoIDs []int64, name string) (map[int64]int64, error) {
	out := make(map[int64]int64, len(repoIDs))
	for _, id := range repoIDs {
		out[id] = 0
	}
	if len(repoIDs) == 0 {
		return out, nil
	}
	err := s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.ListPackageRevisions(ctx, q.ListPackageRevisionsParams{RepoIds: repoIDs, Name: name})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out[r.RepoID] = r.RevSeq
		}
		return nil
	})
	return out, err
}
