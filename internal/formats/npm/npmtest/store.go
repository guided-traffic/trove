// Package npmtest holds the in-memory twin of the npm data port, for the
// conformance kit and for tests that need no PostgreSQL. It implements the
// interface internal/formats/npm declares (npm.Store) over maps and checks the
// rules the database enforces for the format: a tombstone refuses its number, a
// look-alike name is refused, the tags are stored as given, and a document holds the
// versions that are neither removed nor tombstoned.
//
// It imports the rows leaf only, so the format's own tests can import it.
package npmtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// pkgRec is a package of the twin: the rows of npm_packages and npm_versions.
type pkgRec struct {
	id         int64
	repo       int64
	name       string
	createdAt  time.Time
	modifiedAt time.Time
	tags       map[string]string
	versions   []*verRec // in publish order
}

type verRec struct {
	id          int64
	version     string
	manifest    []byte
	deprecated  string
	publishedAt time.Time
	readme      string
	readmeFile  string
	integrity   string
	// facts are what VersionFacts reported while the version was live; a restore of a
	// trashed version (P1) brings them back.
	facts rows.VersionFacts
}

// Store is the in-memory data port.
type Store struct {
	mu         sync.Mutex
	folds      map[int64]map[string]int64 // repo -> fold -> package id
	pkgs       map[string]*pkgRec         // repo/name
	byID       map[int64]*pkgRec
	versions   map[string]rows.VersionFacts // repo/name/version
	tombstones map[string]string            // repo/name/version -> integrity

	// Revisions answers packages.rev_seq of a package for the documents the twin
	// returns, which the database reads in the same statement. A test that runs the
	// twin beside the kit's Host binds it to the Host's RevSeq; unbound, the twin
	// counts its own writes.
	Revisions func(name string) int64
	revs      map[int64]int64

	// Published, Retries, Mismatches and TagChanges log what was written, in order.
	Published   []rows.PublishInput
	Retries     []rows.RetryInput
	Mismatches  []rows.MismatchInput
	TagChanges  []string
	Tombstones  []rows.TombstoneInput
	Cleared     []string
	FoldClashes int
	// Fills logs the documents ApplyFill recorded, in order.
	Fills       []rows.RemoteFill
	remote      map[string]*remoteRec
	remoteState RemoteState
}

// New creates an empty store.
func New() *Store {
	return &Store{
		folds: map[int64]map[string]int64{}, pkgs: map[string]*pkgRec{}, byID: map[int64]*pkgRec{},
		versions: map[string]rows.VersionFacts{}, tombstones: map[string]string{}, revs: map[int64]int64{},
	}
}

// now is the clock of the twin's gone mark.
func (s *Store) now() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

func key(repo int64, name, version string) string {
	return fmt.Sprintf("%d/%s/%s", repo, name, version)
}

func pkgKey(repo int64, name string) string { return fmt.Sprintf("%d/%s", repo, name) }

// MarkTombstoned marks a version as unpublished, as the database row would.
func (s *Store) MarkTombstoned(repo int64, name, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones[key(repo, name, version)] = s.versions[key(repo, name, version)].Integrity
}

// VersionFacts implements the read of the immutability rules.
func (s *Store) VersionFacts(_ context.Context, repo int64, name, version string) (rows.VersionFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.versions[key(repo, name, version)]
	_, f.Tombstoned = s.tombstones[key(repo, name, version)]
	return f, nil
}

// RecordMismatch logs the mismatch.
func (s *Store) RecordMismatch(_ context.Context, in rows.MismatchInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Mismatches = append(s.Mismatches, in)
	return nil
}

// Tags returns the tags of a package.
func (s *Store) Tags(_ context.Context, _ formats.Tx, pkg int64) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	if p, ok := s.byID[pkg]; ok {
		for k, v := range p.tags {
			out[k] = v
		}
	}
	return out, nil
}

// Publish stores a version.
func (s *Store) Publish(_ context.Context, _ formats.Tx, in rows.PublishInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tombstones[key(in.RepoID, in.Name, in.Version)]; ok {
		return errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished, in.Name+"@"+in.Version+" was unpublished")
	}
	folds := s.folds[in.RepoID]
	if folds == nil {
		folds = map[string]int64{}
		s.folds[in.RepoID] = folds
	}
	if owner, ok := folds[in.NameFold]; ok && owner != in.PackageID {
		s.FoldClashes++
		return errs.E(errs.Invalid, errs.CodeNPMInvalidName, "the name is too similar to a package of the repository")
	}
	folds[in.NameFold] = in.PackageID
	p := s.pkgs[pkgKey(in.RepoID, in.Name)]
	if p == nil {
		p = &pkgRec{id: in.PackageID, repo: in.RepoID, name: in.Name, createdAt: in.At}
		s.pkgs[pkgKey(in.RepoID, in.Name)], s.byID[in.PackageID] = p, p
	}
	p.modifiedAt, p.tags = in.At, copyTags(in.Tags)
	p.versions = append(p.versions, &verRec{
		id: in.VersionID, version: in.Version, manifest: append([]byte(nil), in.Manifest...), publishedAt: in.At,
		readme: in.Readme, readmeFile: in.ReadmeFilename, integrity: in.Integrity,
	})
	s.revs[in.PackageID]++
	s.versions[key(in.RepoID, in.Name, in.Version)] = rows.VersionFacts{
		Live: true, Integrity: in.Integrity, PublishTag: in.Tag, PublishedBy: in.PublishedBy, PublishedAt: in.At,
	}
	s.Published = append(s.Published, in)
	return nil
}

// RecordRetry logs the acknowledgement.
func (s *Store) RecordRetry(_ context.Context, _ formats.Tx, in rows.RetryInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Retries = append(s.Retries, in)
	return nil
}

// SetTag stores the tags of a change.
func (s *Store) SetTag(_ context.Context, _ formats.Tx, in rows.SetTagInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setTags(in.PackageID, in.Tags, in.At)
	s.TagChanges = append(s.TagChanges, "set "+in.Tag)
	return nil
}

// DeleteTag stores the tags of a removal.
func (s *Store) DeleteTag(_ context.Context, _ formats.Tx, in rows.DeleteTagInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setTags(in.PackageID, in.Tags, in.At)
	s.TagChanges = append(s.TagChanges, "delete "+in.Tag)
	return nil
}

// setTags stores the tags and moves modified_at forward by at least a millisecond, as
// the statement does (R-NPM-012). The caller holds the lock.
func (s *Store) setTags(pkg int64, tags map[string]string, at time.Time) {
	p, ok := s.byID[pkg]
	if !ok {
		return
	}
	p.tags = copyTags(tags)
	p.modifiedAt = laterOf(p.modifiedAt.Add(time.Millisecond), at)
	s.revs[pkg]++
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Tombstone writes the tombstone of a live version and drops its rows.
func (s *Store) Tombstone(_ context.Context, _ formats.Tx, in rows.TombstoneInput) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(in.RepoID, in.Name, in.Version)
	f := s.versions[k]
	delete(s.versions, k)
	if _, ok := s.tombstones[k]; !ok {
		s.tombstones[k] = f.Integrity
	}
	if p, ok := s.byID[in.PackageID]; ok {
		// The record stays, as the row does while a version is trashed: a document
		// leaves out what has a tombstone.
		for _, v := range p.versions {
			if v.version == in.Version {
				v.facts = f
			}
		}
	}
	s.setTags(in.PackageID, in.Tags, in.At)
	s.Tombstones = append(s.Tombstones, in)
	return f.Integrity, nil
}

// LocalDocument returns the rows of a package document.
func (s *Store) LocalDocument(_ context.Context, repo int64, name string) (rows.LocalPackage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pkgs[pkgKey(repo, name)]
	if !ok {
		return rows.LocalPackage{}, false, nil
	}
	out := rows.LocalPackage{
		PackageID: p.id, RevSeq: s.rev(p), CreatedAt: p.createdAt, ModifiedAt: p.modifiedAt, Tags: copyTags(p.tags),
	}
	for _, v := range p.versions {
		if _, gone := s.tombstones[key(repo, name, v.version)]; gone {
			continue
		}
		out.Versions = append(out.Versions, rows.LocalVersion{Version: v.version, Manifest: v.manifest, Deprecated: v.deprecated, PublishedAt: v.publishedAt})
		if v.version == p.tags["latest"] {
			out.Readme, out.ReadmeFilename = v.readme, v.readmeFile
		}
	}
	return out, len(out.Versions) > 0, nil
}

// LocalVersion returns the one version a document holds under a number or a tag.
func (s *Store) LocalVersion(ctx context.Context, repo int64, name, version, tag string) (rows.LocalPackage, bool, error) {
	doc, found, err := s.LocalDocument(ctx, repo, name)
	if err != nil || !found {
		return rows.LocalPackage{}, false, err
	}
	if version == "" {
		version = doc.Tags[tag]
	}
	for _, v := range doc.Versions {
		if v.Version == version {
			doc.Versions, doc.Readme, doc.ReadmeFilename = []rows.LocalVersion{v}, "", ""
			return doc, true, nil
		}
	}
	return rows.LocalPackage{}, false, nil
}

// DistTags returns the tags of a package with its revision.
func (s *Store) DistTags(ctx context.Context, repo int64, name string) (rows.LocalTags, bool, error) {
	doc, found, err := s.LocalDocument(ctx, repo, name)
	if err != nil || !found {
		return rows.LocalTags{}, false, err
	}
	return rows.LocalTags{RevSeq: doc.RevSeq, Tags: doc.Tags}, true, nil
}

// LiveVersions lists the versions of a package that its document holds.
func (s *Store) LiveVersions(_ context.Context, _ formats.Tx, pkg int64) ([]rows.LiveVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[pkg]
	if !ok {
		return nil, nil
	}
	var out []rows.LiveVersion
	for _, v := range p.versions {
		if _, gone := s.tombstones[key(p.repo, p.name, v.version)]; !gone {
			out = append(out, rows.LiveVersion{ID: v.id, Version: v.version})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Tombstoned reports whether the number has a tombstone.
func (s *Store) Tombstoned(_ context.Context, repo int64, name, version string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tombstones[key(repo, name, version)]
	return ok, nil
}

// ClearTombstone removes the tombstone of a version that is stored again.
func (s *Store) ClearTombstone(_ context.Context, _ formats.Tx, repo int64, name string, versionID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pkgs[pkgKey(repo, name)]
	if p == nil {
		return false, nil
	}
	for _, v := range p.versions {
		k := key(repo, name, v.version)
		if want, ok := s.tombstones[k]; ok && v.id == versionID && want == v.integrity {
			delete(s.tombstones, k)
			s.versions[k] = v.facts
			s.Cleared = append(s.Cleared, name+"@"+v.version)
			return true, nil
		}
	}
	return false, nil
}

// rev is the revision a document is read at. The caller holds the lock.
func (s *Store) rev(p *pkgRec) int64 {
	if s.Revisions != nil {
		return s.Revisions(p.name)
	}
	return s.revs[p.id] + 1
}

func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
