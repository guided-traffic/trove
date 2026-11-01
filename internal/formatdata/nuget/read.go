package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reads of the version list and of the registration documents (R-NUGET-008,
// R-NUGET-009). Each is one statement, so the revision and the versions of what it returns
// are one snapshot and a rendition keyed by that revision never holds the rows of another.

// Versions lists every stored version of an ID, listed or not, ascending by sort key.
func (s *Store) Versions(ctx context.Context, repoID int64, lowerID string) ([]rows.VersionEntry, error) {
	var out []rows.VersionEntry
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetVersions(ctx, q.NuGetVersionsParams{RepositoryID: repoID, LowerID: lowerID})
		if err != nil {
			return err
		}
		out = make([]rows.VersionEntry, len(rs))
		for i, r := range rs {
			out[i] = rows.VersionEntry{
				VersionID: r.VersionID, LowerVersion: r.Version, FullVersion: r.DisplayVersion, DeclaredID: r.DeclaredID,
				Listed: r.Listed, IsPrerelease: r.IsPrerelease, IsSemver2: r.IsSemver2, PublishedAt: r.PublishedAt.Time,
			}
		}
		return nil
	})
	return out, err
}

// Registration reads what the registration documents of an ID render from.
func (s *Store) Registration(ctx context.Context, repoID int64, lowerID string) (rows.RegistrationPackage, bool, error) {
	var out rows.RegistrationPackage
	var found bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out, found = rows.RegistrationPackage{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetRegistration(ctx, q.NuGetRegistrationParams{RepositoryID: repoID, LowerID: lowerID})
		if err != nil || len(rs) == 0 {
			return err
		}
		out = rows.RegistrationPackage{Rev: rs[0].RevSeq, DisplayName: rs[0].DisplayName, Versions: make([]rows.RegistrationVersion, len(rs))}
		for i, r := range rs {
			out.Versions[i] = rows.RegistrationVersion{
				VersionEntry: rows.VersionEntry{
					VersionID: r.VersionID, LowerVersion: r.Version, FullVersion: r.DisplayVersion, DeclaredID: r.DeclaredID,
					Listed: r.Listed, IsPrerelease: r.IsPrerelease, IsSemver2: r.IsSemver2, PublishedAt: r.PublishedAt.Time,
				},
				PackageTypes: r.PackageTypes, Metadata: []byte(r.Metadata),
			}
		}
		found = true
		return nil
	})
	return out, found, err
}
