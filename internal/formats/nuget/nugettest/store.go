// Package nugettest is the in-memory twin of the NuGet data port (Q-ARCH-193), for the
// conformance kit and the unit tests of the format. It implements the interface
// internal/formats/nuget declares (nuget.Store) over maps with the state the database rows
// hold, and enforces the rules the database enforces for the format: a tombstone refuses
// its number and is read again by the same key, a version is listed or not, a removal
// reads the SHA-512 of the removed package from the blob it names, and a write made in a
// transaction that is rolled back leaves no trace.
//
// It imports the rows leaf only, so the format's own tests and the tests of the packages
// that render documents can import it.
package nugettest

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// vkey names a version number of a repository.
type vkey struct {
	repo        int64
	id, version string
}

// verRec is the NuGet row of a version.
type verRec struct {
	in     rows.VersionInput
	listed bool
	// facts are what VersionFacts reports.
	publishedBy string
}

type tombRec struct {
	t  rows.Tombstone
	at time.Time
	by string
}

// Store is the in-memory data port.
type Store struct {
	mu       sync.Mutex
	versions map[vkey]*verRec
	byID     map[int64]*verRec
	tombs    map[vkey]tombRec
	blobs    map[string][]byte // hex SHA-256 -> SHA-512
	symbols  map[int64]int64
	revs     map[string]int64

	// Revisions answers packages.rev_seq and packages.display_name of a package for the
	// documents the twin returns, which the database reads in the same statement. A test
	// that runs the twin beside the kit's Host binds it to the Host's RevSeq; unbound, the
	// twin counts its own writes and the display name is the declared ID of the first
	// version.
	Revisions func(repo int64, lowerID string) (rev int64, display string)

	// Now is the clock of the twin; nil is the wall clock.
	Now func() time.Time

	// Published, Tombstones, Promoted and Listings log what was written, in order. A write
	// of a transaction that was rolled back is not in them.
	Published  []rows.VersionInput
	Tombstones []rows.TombstoneInput
	Promoted   []rows.ReservedInput
	Listings   []rows.ListingInput
	// Journal holds the journal entry of each published or deleted event, in order, as the
	// database store would pass it to the audit recorder.
	Journal []string
}

// New creates an empty store.
func New() *Store {
	return &Store{
		versions: map[vkey]*verRec{}, byID: map[int64]*verRec{}, tombs: map[vkey]tombRec{},
		blobs: map[string][]byte{}, symbols: map[int64]int64{}, revs: map[string]int64{},
	}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// undo registers fn to run when the transaction ends without a commit.
func undo(tx formats.Tx, fn func()) {
	if tx != nil {
		tx.OnRollback(fn)
	}
}

// Seed stores a version without a transaction, for a test that needs state to start from.
// A tombstone of the number is not consulted.
func (s *Store) Seed(in rows.VersionInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insert(in)
}

// SeedTombstone writes a tombstone without a transaction.
func (s *Store) SeedTombstone(repo int64, lowerID, lowerVersion string, sha512 []byte, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombs[vkey{repo, lowerID, lowerVersion}] = tombRec{t: rows.Tombstone{SHA512: sha512, Kind: kind}, at: s.now()}
}

func (s *Store) insert(in rows.VersionInput) *verRec {
	rec := &verRec{in: in, listed: true, publishedBy: in.Audit.Actor.ID}
	s.versions[vkey{in.RepoID, in.LowerID, in.LowerVersion}] = rec
	s.byID[in.VersionID] = rec
	for _, f := range in.Files {
		if f.SHA256 != "" && len(in.SHA512) > 0 {
			s.blobs[f.SHA256] = in.SHA512
		}
	}
	s.revs[fmt.Sprintf("%d/%s", in.RepoID, in.LowerID)]++
	return rec
}

// VersionFacts reads the facts of a stored version.
func (s *Store) VersionFacts(_ context.Context, repo int64, lowerID, lowerVersion string) (rows.VersionFacts, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.versions[vkey{repo, lowerID, lowerVersion}]
	if !ok {
		return rows.VersionFacts{}, false, nil
	}
	return rows.VersionFacts{
		Listed: rec.listed, SHA512: rec.in.SHA512, PublishedBy: rec.publishedBy, PublishedAt: rec.in.PublishedAt, Now: s.now(),
	}, true, nil
}

// Tombstone reads the tombstone of a number.
func (s *Store) Tombstone(_ context.Context, _ formats.Tx, repo int64, lowerID, lowerVersion string) (rows.Tombstone, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tombs[vkey{repo, lowerID, lowerVersion}]
	return t.t, ok, nil
}

// InsertVersion stores the row of a version.
func (s *Store) InsertVersion(_ context.Context, tx formats.Tx, in rows.VersionInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := vkey{in.RepoID, in.LowerID, in.LowerVersion}
	if _, dup := s.versions[k]; dup {
		return errs.E(errs.Conflict, errs.CodeNuGetVersionExists, "The version is stored already.")
	}
	if len(in.PackageTypes) == 0 {
		in.PackageTypes = []string{"dependency"}
	}
	s.insert(in)
	rev := fmt.Sprintf("%d/%s", in.RepoID, in.LowerID)
	published := len(s.Published)
	if in.Origin != rows.OriginCache {
		s.Published = append(s.Published, in)
		s.Journal = append(s.Journal, journal(in.RepoID, in.LowerID, in.LowerVersion, in.SHA512))
	}
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.versions, k)
		delete(s.byID, in.VersionID)
		s.revs[rev]--
		if in.Origin != rows.OriginCache {
			s.Published, s.Journal = s.Published[:published], s.Journal[:published]
		}
	})
	return nil
}

func journal(repo int64, id, version string, sha512 []byte) string {
	b, _ := json.Marshal(map[string]any{"repository": repo, "id": id, "version": version, "sha512": hex.EncodeToString(sha512)})
	return string(b)
}

// InsertTombstone writes the tombstone of a removed version. The SHA-512 comes from the
// blob the SHA-256 names, as the database store reads it.
func (s *Store) InsertTombstone(_ context.Context, tx formats.Tx, in rows.TombstoneInput) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum, ok := s.blobs[hex.EncodeToString(in.SHA256)]
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The blob of a removed NuGet package is not readable for its tombstone.")
	}
	k := vkey{in.RepoID, in.LowerID, in.LowerVersion}
	prev, had := s.tombs[k]
	if had {
		sum = prev.t.SHA512
	} else {
		s.tombs[k] = tombRec{t: rows.Tombstone{SHA512: sum, Kind: rows.TombstoneRemoved}, at: in.At, by: in.By}
	}
	// The removal of the core version row cascades to the NuGet row in the database; the
	// hook removes the version right after it writes the tombstone, so the twin drops the
	// row with it.
	gone := s.versions[k]
	delete(s.versions, k)
	if gone != nil {
		delete(s.byID, gone.in.VersionID)
	}
	n := len(s.Tombstones)
	s.Tombstones = append(s.Tombstones, in)
	s.Journal = append(s.Journal, journal(in.RepoID, in.LowerID, in.LowerVersion, sum))
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if had {
			s.tombs[k] = prev
		} else {
			delete(s.tombs, k)
		}
		s.Tombstones, s.Journal = s.Tombstones[:n], s.Journal[:len(s.Journal)-1]
		if gone != nil {
			s.versions[k], s.byID[gone.in.VersionID] = gone, gone
		}
	})
	return slices.Clone(sum), nil
}

// PromoteReserved turns a reserved tombstone into a removal.
func (s *Store) PromoteReserved(_ context.Context, tx formats.Tx, in rows.ReservedInput) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := vkey{in.RepoID, in.LowerID, in.LowerVersion}
	prev, ok := s.tombs[k]
	if !ok || prev.t.Kind != rows.TombstoneReserved {
		return nil, false, nil
	}
	s.tombs[k] = tombRec{t: rows.Tombstone{SHA512: prev.t.SHA512, Kind: rows.TombstoneRemoved}, at: s.now(), by: in.By}
	s.Promoted = append(s.Promoted, in)
	s.Journal = append(s.Journal, journal(in.RepoID, in.LowerID, in.LowerVersion, prev.t.SHA512))
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tombs[k] = prev
		s.Promoted, s.Journal = s.Promoted[:len(s.Promoted)-1], s.Journal[:len(s.Journal)-1]
	})
	return slices.Clone(prev.t.SHA512), true, nil
}

// DeleteTombstone removes the tombstone of a version stored again with the bytes the
// tombstone holds.
func (s *Store) DeleteTombstone(_ context.Context, tx formats.Tx, versionID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[versionID]
	if !ok {
		return false, nil
	}
	k := vkey{rec.in.RepoID, rec.in.LowerID, rec.in.LowerVersion}
	t, ok := s.tombs[k]
	if !ok || !bytes.Equal(t.t.SHA512, rec.in.SHA512) {
		return false, nil
	}
	delete(s.tombs, k)
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tombs[k] = t
	})
	return true, nil
}

// FindVersion reads the live version of a number.
func (s *Store) FindVersion(_ context.Context, _ formats.Tx, repo int64, lowerID, lowerVersion string) (rows.VersionRef, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.versions[vkey{repo, lowerID, lowerVersion}]
	if !ok {
		return rows.VersionRef{}, false, nil
	}
	return rows.VersionRef{VersionID: rec.in.VersionID, Listed: rec.listed}, true, nil
}

// SetListed unlists or relists a version.
func (s *Store) SetListed(_ context.Context, tx formats.Tx, in rows.ListingInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[in.VersionID]
	if !ok || rec.listed == in.Listed {
		return false, nil
	}
	rec.listed = in.Listed
	s.Listings = append(s.Listings, in)
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		rec.listed = !in.Listed
		s.Listings = s.Listings[:len(s.Listings)-1]
	})
	return true, nil
}

// ServeState reads what a GET of a file of a version decides on.
func (s *Store) ServeState(_ context.Context, repo int64, lowerID, lowerVersion string) (rows.ServeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := vkey{repo, lowerID, lowerVersion}
	rec, stored := s.versions[k]
	_, tomb := s.tombs[k]
	return rows.ServeState{Stored: stored, Listed: stored && rec.listed, Tombstoned: tomb}, nil
}

// InsertSymbolPackage records the symbol package of a version.
func (s *Store) InsertSymbolPackage(_ context.Context, tx formats.Tx, in rows.SymbolPackageInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.symbols[in.VersionID]; dup {
		return false, nil
	}
	s.symbols[in.VersionID] = in.ItemID
	undo(tx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.symbols, in.VersionID)
	})
	return true, nil
}

// entry is the list form of a stored version.
func entry(r *verRec) rows.VersionEntry {
	return rows.VersionEntry{
		VersionID: r.in.VersionID, LowerVersion: r.in.LowerVersion, FullVersion: fullVersion(r.in), DeclaredID: r.in.DeclaredID,
		Listed: r.listed, IsPrerelease: r.in.IsPrerelease, IsSemver2: r.in.IsSemver2, PublishedAt: r.in.PublishedAt,
	}
}

// fullVersion is the full normalized form the format handed over; a test that seeds a row
// without it gets the URL form.
func fullVersion(in rows.VersionInput) string {
	if in.FullVersion != "" {
		return in.FullVersion
	}
	return in.LowerVersion
}

// stored returns the versions of an ID ascending by sort key.
func (s *Store) stored(repo int64, lowerID string) []*verRec {
	var out []*verRec
	for k, r := range s.versions {
		if k.repo == repo && k.id == lowerID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].in.SortKey, out[j].in.SortKey) < 0 })
	return out
}

// Versions lists every stored version of an ID, ascending by sort key.
func (s *Store) Versions(_ context.Context, repo int64, lowerID string) ([]rows.VersionEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rows.VersionEntry
	for _, r := range s.stored(repo, lowerID) {
		out = append(out, entry(r))
	}
	return out, nil
}

// Registration reads what the registration documents of an ID render from.
func (s *Store) Registration(_ context.Context, repo int64, lowerID string) (rows.RegistrationPackage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := s.stored(repo, lowerID)
	if len(recs) == 0 {
		return rows.RegistrationPackage{}, false, nil
	}
	out := rows.RegistrationPackage{Rev: s.revs[fmt.Sprintf("%d/%s", repo, lowerID)], DisplayName: recs[0].in.DeclaredID}
	if s.Revisions != nil {
		out.Rev, out.DisplayName = s.Revisions(repo, lowerID)
	}
	for _, r := range recs {
		out.Versions = append(out.Versions, rows.RegistrationVersion{VersionEntry: entry(r), PackageTypes: slices.Clone(r.in.PackageTypes), Metadata: r.in.Metadata})
	}
	return out, true, nil
}

// Search matches the terms against the text parts of the packages' visible versions with
// a case-insensitive substring match: the twin has no text search configuration, so it
// answers what a test of the format needs (which packages, in which order) and not the
// ranking of PostgreSQL. It honours the page (Offset, Limit), KeysOnly and Only as the
// database does.
func (s *Store) Search(_ context.Context, q rows.SearchQuery) ([]rows.SearchHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(q.Only) > 0 {
		return s.only(q), nil
	}
	hits := s.matching(q)
	if q.Offset > 0 {
		hits = hits[min(q.Offset, len(hits)):]
	}
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

// SearchCount counts the packages the terms and the filters match.
func (s *Store) SearchCount(_ context.Context, q rows.SearchQuery) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.matching(q)), nil
}

// matching lists every matching package in ID order, unpaged. The caller holds the lock.
func (s *Store) matching(q rows.SearchQuery) []rows.SearchHit {
	byID := map[string][]*verRec{}
	for k, r := range s.versions {
		if k.repo == q.RepoID {
			byID[k.id] = append(byID[k.id], r)
		}
	}
	var ids []string
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var hits []rows.SearchHit
	for _, id := range ids {
		recs := byID[id]
		sort.Slice(recs, func(i, j int) bool { return bytes.Compare(recs[i].in.SortKey, recs[j].in.SortKey) < 0 })
		var visible []*verRec
		for _, r := range recs {
			if r.listed && (q.Prerelease || !r.in.IsPrerelease) && (q.SemVer2 || !r.in.IsSemver2) {
				visible = append(visible, r)
			}
		}
		if len(visible) == 0 {
			continue
		}
		latest := visible[len(visible)-1]
		if q.PackageType != "" && !slices.Contains(latest.in.PackageTypes, q.PackageType) {
			continue
		}
		if !matches(latest.in, id, q.Terms) {
			continue
		}
		hits = append(hits, hit(id, visible, q.KeysOnly))
	}
	return hits
}

// only builds the hits of the listed versions a scan found, each package with those versions alone.
func (s *Store) only(q rows.SearchQuery) []rows.SearchHit {
	byID := map[string][]*verRec{}
	var ids []string
	for _, ref := range q.Only {
		r, ok := s.versions[vkey{q.RepoID, ref.LowerID, ref.LowerVersion}]
		if !ok || !r.listed {
			continue
		}
		if _, seen := byID[ref.LowerID]; !seen {
			ids = append(ids, ref.LowerID)
		}
		byID[ref.LowerID] = append(byID[ref.LowerID], r)
	}
	var hits []rows.SearchHit
	for _, id := range ids {
		recs := byID[id]
		sort.Slice(recs, func(i, j int) bool { return bytes.Compare(recs[i].in.SortKey, recs[j].in.SortKey) < 0 })
		hits = append(hits, hit(id, recs, q.KeysOnly))
	}
	return hits
}

// hit builds the hit of a package from its visible versions, ascending.
func hit(id string, visible []*verRec, keysOnly bool) rows.SearchHit {
	latest := visible[len(visible)-1]
	h := rows.SearchHit{LowerID: id, DisplayName: latest.in.DeclaredID, Latest: rows.RegistrationVersion{VersionEntry: entry(latest), PackageTypes: slices.Clone(latest.in.PackageTypes)}}
	if !keysOnly {
		h.Latest.Metadata = latest.in.Metadata
	}
	for _, r := range visible {
		h.Listed = append(h.Listed, entry(r))
		h.ListedTypes = append(h.ListedTypes, slices.Clone(r.in.PackageTypes))
	}
	return h
}

func matches(in rows.VersionInput, id string, terms []rows.SearchTerm) bool {
	text := strings.ToLower(strings.Join([]string{id, in.IDTokens, in.Title, in.Tags, in.Authors, in.Summary, in.Description}, " "))
	for _, t := range terms {
		v := strings.ToLower(t.Value)
		if !strings.Contains(text, v) {
			return false
		}
	}
	return true
}
