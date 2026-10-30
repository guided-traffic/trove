// Package npmtest holds the in-memory twin of the npm data port, for the
// conformance kit and for tests that need no PostgreSQL. It implements the
// interface internal/formats/npm declares (npm.Store) over maps and checks the
// rules the database enforces for the format: a tombstone refuses its number, a
// look-alike name is refused, the tags are stored as given.
//
// It imports the rows leaf only, so the format's own tests can import it.
package npmtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// Store is the in-memory data port.
type Store struct {
	mu         sync.Mutex
	folds      map[int64]map[string]int64 // repo -> fold -> package id
	tags       map[int64]map[string]string
	versions   map[string]rows.VersionFacts // repo/name/version
	tombstones map[string]bool

	// Published, Retries, Mismatches and TagChanges log what was written, in order.
	Published   []rows.PublishInput
	Retries     []rows.RetryInput
	Mismatches  []rows.MismatchInput
	TagChanges  []string
	Tombstoned  []rows.TombstoneInput
	FoldClashes int
}

// New creates an empty store.
func New() *Store {
	return &Store{
		folds: map[int64]map[string]int64{}, tags: map[int64]map[string]string{},
		versions: map[string]rows.VersionFacts{}, tombstones: map[string]bool{},
	}
}

func key(repo int64, name, version string) string {
	return fmt.Sprintf("%d/%s/%s", repo, name, version)
}

// MarkTombstoned marks a version as unpublished, as the database row would.
func (s *Store) MarkTombstoned(repo int64, name, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones[key(repo, name, version)] = true
}

// VersionFacts implements the read of the immutability rules.
func (s *Store) VersionFacts(_ context.Context, repo int64, name, version string) (rows.VersionFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.versions[key(repo, name, version)]
	f.Tombstoned = s.tombstones[key(repo, name, version)]
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
	for k, v := range s.tags[pkg] {
		out[k] = v
	}
	return out, nil
}

// Publish stores a version.
func (s *Store) Publish(_ context.Context, _ formats.Tx, in rows.PublishInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tombstones[key(in.RepoID, in.Name, in.Version)] {
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
	s.versions[key(in.RepoID, in.Name, in.Version)] = rows.VersionFacts{
		Live: true, Integrity: in.Integrity, PublishTag: in.Tag, PublishedBy: in.PublishedBy, PublishedAt: in.At,
	}
	s.tags[in.PackageID] = copyTags(in.Tags)
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
	s.tags[in.PackageID] = copyTags(in.Tags)
	s.TagChanges = append(s.TagChanges, "set "+in.Tag)
	return nil
}

// DeleteTag stores the tags of a removal.
func (s *Store) DeleteTag(_ context.Context, _ formats.Tx, in rows.DeleteTagInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tags[in.PackageID] = copyTags(in.Tags)
	s.TagChanges = append(s.TagChanges, "delete "+in.Tag)
	return nil
}

// Tombstone writes the tombstone of a live version.
func (s *Store) Tombstone(_ context.Context, _ formats.Tx, in rows.TombstoneInput) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(in.RepoID, in.Name, in.Version)
	f := s.versions[k]
	delete(s.versions, k)
	s.tombstones[k] = true
	s.tags[in.PackageID] = copyTags(in.Tags)
	s.Tombstoned = append(s.Tombstoned, in)
	return f.Integrity, nil
}

func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
