// Package nuget is the data port of the NuGet format (Q-ARCH-193, D-1): the statements on
// the NuGet tables (internal/meta/postgres/queries/nuget_local.sql), behind the Store
// interface that internal/formats/nuget declares. The format runs no SQL; this package
// holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that
// cross the port are those of internal/formats/nuget/rows, a leaf both import. It owns the
// four tables of migration 00017_nuget and registers them with the backup (tables.go), so
// that the package must be linked wherever the migrated schema is checked against the
// registry (cmd/trove/registry.go).
//
// The store persists what the format decided: the version order is the sort key the format
// computed, the ID and version arrive normalized, and the store never parses either. It
// enforces what the database can and writes the audit events of the changes in the
// transaction of the change (R-OPS-015): package.published, package.deleted and
// package.metadata_changed, the first two with the journal entry of R-NUGET-033.
package nuget

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("nuget: the transaction is not one of internal/meta/postgres")

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions.
	Recorder audit.Recorder
	// Source gives the source of an audit event for the request in ctx; nil gives none.
	Source func(ctx context.Context) audit.Source
	// Actor gives the actor of the request in ctx. A hook that learns of a removal from the
	// item store (a protocol delete, a raw delete) knows the principal's id and nothing
	// else; the actor of the request completes the event. nil keeps what the format passed.
	Actor func(ctx context.Context) audit.Actor
	// Blobs gives the package view ranged reads over a stored package, to read the README
	// file it holds (view.go). nil leaves the view without README text.
	Blobs BlobReader
}

// Store implements the data port of the NuGet format.
type Store struct{ o Options }

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: no transaction runner")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: no audit recorder")
	}
	return &Store{o: o}, nil
}

func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// stamp renders a time for a statement that casts it to timestamptz.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// noActor is the id a tombstone records for a removal that no principal asked for: the
// column is a uuid and not null, and the nil UUID is the one value that names nobody.
const noActor = "00000000-0000-0000-0000-000000000000"

func actorID(by string) string {
	if by == "" {
		return noActor
	}
	return by
}

// maxSearchText bounds each part of the search document: a tsvector holds at most 1 MiB,
// and a .nuspec may be that long (nuget.archive.max_nuspec_bytes), so an unbounded
// description could fail the whole push. The cut is at a character boundary; the words
// beyond it are not found by a search, and the package is complete in every other way.
const maxSearchText = 64 << 10

// cut makes text fit the search document: valid UTF-8, no NUL, at most maxSearchText bytes.
func cut(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) <= maxSearchText {
		return s
	}
	end := maxSearchText
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// VersionFacts reads what the immutability and the acknowledged-retry rules need.
func (s *Store) VersionFacts(ctx context.Context, repoID int64, lowerID, lowerVersion string) (rows.VersionFacts, bool, error) {
	var out rows.VersionFacts
	var found bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out, found = rows.VersionFacts{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetVersionFacts(ctx, q.NuGetVersionFactsParams{RepositoryID: repoID, LowerID: lowerID, LowerVersion: lowerVersion})
		if err != nil || len(rs) == 0 {
			return err
		}
		r := rs[0]
		out = rows.VersionFacts{
			Listed: r.Listed, SHA512: r.Sha512, PublishedBy: r.PublishedBy, PublishedAt: r.PublishedAt.Time, Now: r.Now.Time,
		}
		found = true
		return nil
	})
	return out, found, err
}

// Tombstone reads the tombstone of a number, in the transaction when there is one.
func (s *Store) Tombstone(ctx context.Context, tx meta.Tx, repoID int64, lowerID, lowerVersion string) (rows.Tombstone, bool, error) {
	var out rows.Tombstone
	var found bool
	read := func(tx meta.Tx) error {
		out, found = rows.Tombstone{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NuGetTombstone(ctx, q.NuGetTombstoneParams{RepositoryID: repoID, LowerID: lowerID, LowerVersion: lowerVersion})
		if err != nil || len(rs) == 0 {
			return err
		}
		out, found = rows.Tombstone{SHA512: rs[0].Sha512, Kind: rs[0].Kind}, true
		return nil
	}
	if tx != nil {
		return out, found, read(tx)
	}
	err := s.o.DB.InTx(ctx, read)
	return out, found, err
}

// InsertVersion stores the NuGet row of a version, and records package.published unless
// the version is a cache fill.
func (s *Store) InsertVersion(ctx context.Context, tx meta.Tx, in rows.VersionInput) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	if len(in.PackageTypes) == 0 {
		in.PackageTypes = []string{"dependency"}
	}
	if err := qs.NuGetInsertVersion(ctx, q.NuGetInsertVersionParams{
		VersionID: in.VersionID, RepositoryID: in.RepoID, LowerID: in.LowerID, DeclaredID: in.DeclaredID,
		OriginalVersion: in.OriginalVersion, SortKey: in.SortKey, IsPrerelease: in.IsPrerelease, IsSemver2: in.IsSemver2,
		PublishedAt: stamp(in.PublishedAt), PackageTypes: in.PackageTypes, Metadata: string(in.Metadata),
		IDTokens: cut(in.IDTokens), Title: cut(in.Title), Tags: cut(in.Tags), Authors: cut(in.Authors), Summary: cut(in.Summary),
		Description: cut(in.Description), Origin: in.Origin,
	}); err != nil {
		return err
	}
	if in.Origin == rows.OriginCache {
		return nil
	}
	files := make([]map[string]any, len(in.Files))
	for i, f := range in.Files {
		files[i] = map[string]any{"path": f.Path, "sha256": f.SHA256, "size": f.Size}
	}
	details := map[string]any{"package": in.LowerID, "version": in.LowerVersion, "files": files}
	if in.Claim != "" {
		details["claim"] = in.Claim
	}
	if in.Shadowing.Undetermined {
		details["shadowCheck"] = "undetermined"
	}
	journal, err := journalEntry(in.RepoID, in.LowerID, in.LowerVersion, in.SHA512)
	if err != nil {
		return err
	}
	if err := s.record(ctx, tx, audit.EventPackagePublished, in.Audit, in.LowerID, in.LowerVersion, details, journal); err != nil {
		return err
	}
	if len(in.Shadowing.Shadowed) == 0 {
		return nil
	}
	// A first version of an ID a remote member serves, published on the strength of
	// manage: the security event of R-REPO-048, in the commit transaction.
	exposure := make([]map[string]string, len(in.Shadowing.Shadowed))
	for i, p := range in.Shadowing.Shadowed {
		exposure[i] = map[string]string{"virtual": p.Virtual, "remote": p.Remote}
	}
	return s.record(ctx, tx, audit.EventRepositoryRemotePackageShadowed, in.Audit, in.LowerID, "",
		map[string]any{"package": in.LowerID, "exposure": exposure}, nil)
}

// journalEntry is the entry of R-NUGET-033 a restore's replay applies: the repository, the
// ID, the URL form of the version and the SHA-512 of the .nupkg, and no secret.
func journalEntry(repoID int64, lowerID, lowerVersion string, sha512 []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"repository": repoID, "id": lowerID, "version": lowerVersion, "sha512": hex.EncodeToString(sha512),
	})
}

// InsertTombstone writes the tombstone of a removed version and records package.deleted.
func (s *Store) InsertTombstone(ctx context.Context, tx meta.Tx, in rows.TombstoneInput) ([]byte, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	sum, err := qs.NuGetInsertTombstone(ctx, q.NuGetInsertTombstoneParams{
		RepositoryID: in.RepoID, LowerID: in.LowerID, LowerVersion: in.LowerVersion, At: stamp(in.At), By: actorID(in.By), Sha256: in.SHA256,
	})
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The blob of a removed NuGet package is not readable for its tombstone.", errs.WithCause(err))
	}
	return sum, s.recordDeleted(ctx, tx, in.Audit, in.RepoID, in.LowerID, in.LowerVersion, sum)
}

// PromoteReserved turns a reserved tombstone into a removal and records package.deleted.
func (s *Store) PromoteReserved(ctx context.Context, tx meta.Tx, in rows.ReservedInput) ([]byte, bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, false, err
	}
	sums, err := qs.NuGetPromoteReserved(ctx, q.NuGetPromoteReservedParams{
		RepositoryID: in.RepoID, LowerID: in.LowerID, LowerVersion: in.LowerVersion, By: actorID(in.By),
	})
	if err != nil || len(sums) == 0 {
		return nil, false, err
	}
	return sums[0], true, s.recordDeleted(ctx, tx, in.Audit, in.RepoID, in.LowerID, in.LowerVersion, sums[0])
}

func (s *Store) recordDeleted(ctx context.Context, tx meta.Tx, a rows.Audit, repoID int64, lowerID, lowerVersion string, sum []byte) error {
	journal, err := journalEntry(repoID, lowerID, lowerVersion, sum)
	if err != nil {
		return err
	}
	return s.record(ctx, tx, audit.EventPackageDeleted, a, lowerID, lowerVersion,
		map[string]any{"package": lowerID, "version": lowerVersion}, journal)
}

// DeleteTombstone removes the tombstone of a version stored again with its bytes.
func (s *Store) DeleteTombstone(ctx context.Context, tx meta.Tx, versionID int64) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.NuGetDeleteTombstone(ctx, versionID)
	return n > 0, err
}

// FindVersion reads the live version of a number under the package row lock.
func (s *Store) FindVersion(ctx context.Context, tx meta.Tx, repoID int64, lowerID, lowerVersion string) (rows.VersionRef, bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return rows.VersionRef{}, false, err
	}
	rs, err := qs.NuGetFindVersion(ctx, q.NuGetFindVersionParams{RepositoryID: repoID, LowerID: lowerID, LowerVersion: lowerVersion})
	if err != nil || len(rs) == 0 {
		return rows.VersionRef{}, false, err
	}
	return rows.VersionRef{VersionID: rs[0].VersionID, Listed: rs[0].Listed}, true, nil
}

// SetListed unlists or relists a version and records package.metadata_changed when the
// listing changed.
func (s *Store) SetListed(ctx context.Context, tx meta.Tx, in rows.ListingInput) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.NuGetSetListed(ctx, q.NuGetSetListedParams{Listed: in.Listed, VersionID: in.VersionID})
	if err != nil || n == 0 {
		return false, err
	}
	change := audit.ChangeUnlist
	if in.Listed {
		change = audit.ChangeRelist
	}
	return true, s.record(ctx, tx, audit.EventPackageMetadataChanged, in.Audit, in.LowerID, in.LowerVersion,
		map[string]any{"package": in.LowerID, "version": in.LowerVersion, "change": change}, nil)
}

// ServeState reads what a GET of a file of a version decides on.
func (s *Store) ServeState(ctx context.Context, repoID int64, lowerID, lowerVersion string) (rows.ServeState, error) {
	var out rows.ServeState
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		r, err := qs.NuGetServeState(ctx, q.NuGetServeStateParams{RepositoryID: repoID, LowerID: lowerID, LowerVersion: lowerVersion})
		if err != nil {
			return err
		}
		out = rows.ServeState{Stored: r.Stored, Listed: r.Listed, Tombstoned: r.Tombstoned}
		return nil
	})
	return out, err
}

// InsertSymbolPackage records the symbol package of a version.
func (s *Store) InsertSymbolPackage(ctx context.Context, tx meta.Tx, in rows.SymbolPackageInput) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.NuGetInsertSymbolPackage(ctx, q.NuGetInsertSymbolPackageParams{VersionID: in.VersionID, ItemID: in.ItemID})
	return n > 0, err
}

// record writes a transactional event of a package. version is "" for an event of the ID.
func (s *Store) record(ctx context.Context, tx meta.Tx, typ string, a rows.Audit, pkg, version string, details map[string]any, journal []byte) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	path := pkg
	if version != "" {
		path = pkg + "/" + version
	}
	ev := audit.Event{
		Type: typ, Outcome: audit.Success,
		Actor:      audit.Actor{Kind: a.Actor.Kind, ID: a.Actor.ID, Name: a.Actor.Name, TokenID: a.Actor.TokenID, AuthMethod: a.Actor.AuthMethod},
		Subject:    audit.Subject{Kind: "package", Name: a.RepoKey + "/" + pkg},
		Repository: a.RepoKey, Path: path, Endpoint: a.Endpoint, Details: raw, Journal: journal,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	if ev.Actor.Kind == "" {
		ev.Actor = s.actorOf(ctx, a.Actor.ID)
	}
	return s.o.Recorder.RecordTx(ctx, tx, ev)
}

// actorOf completes the actor of an event the format could not describe: the actor of the
// request when it is the principal with that id, else a user with that id, else none.
func (s *Store) actorOf(ctx context.Context, id string) audit.Actor {
	if s.o.Actor != nil {
		if a := s.o.Actor(ctx); a.Kind != "" && (id == "" || a.ID == id) {
			return a
		}
	}
	if id == "" {
		return audit.Actor{Kind: "anonymous"}
	}
	return audit.Actor{Kind: "user", ID: id}
}
