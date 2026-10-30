// Package npm is the data port of the npm format (Q-ARCH-193, D-1): the statements
// on the npm tables (internal/meta/postgres/queries/npm_local.sql), behind the
// Store interface that internal/formats/npm declares. The format runs no SQL; this
// package holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values
// that cross the port are those of internal/formats/npm/rows, a leaf both import. It
// persists what the format decided (the dist-tags it computed with the SemVer
// order) and enforces what the database can: the unique name fold of
// R-NPM-007, the tombstone of R-NPM-008, and the audit event written in the
// transaction of the change (R-OPS-015).
package npm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("npm: the transaction is not one of internal/meta/postgres")

// maxReadme is the size the readme column takes (R-NPM-006).
const maxReadme = 64 << 10

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions and the
	// manifest mismatch asynchronously.
	Recorder audit.Recorder
	// Source gives the source of an audit event for the request in ctx; nil gives none.
	Source func(ctx context.Context) audit.Source
}

// Store implements the data port of the npm format.
type Store struct{ o Options }

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "npm: no transaction runner")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "npm: no audit recorder")
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

// VersionFacts implements the read of the immutability and retry rules.
func (s *Store) VersionFacts(ctx context.Context, repoID int64, name, version string) (rows.VersionFacts, error) {
	var out rows.VersionFacts
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = rows.VersionFacts{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		live, err := qs.NPMVersionFacts(ctx, q.NPMVersionFactsParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		if len(live) > 0 {
			r := live[0]
			out = rows.VersionFacts{
				Live: true, Integrity: r.Integrity, PublishTag: r.PublishTag, PublishedBy: r.PublishedBy,
				PublishedAt: r.PublishedAt.Time, Age: r.Now.Time.Sub(r.PublishedAt.Time),
			}
		}
		out.Tombstoned, err = qs.NPMTombstoned(ctx, q.NPMTombstonedParams{RepoID: repoID, Name: name, Version: version})
		return err
	})
	return out, err
}

// Tags reads the dist-tags of a package under its row lock.
func (s *Store) Tags(ctx context.Context, tx meta.Tx, packageID int64) (map[string]string, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	rs, err := qs.NPMReadTags(ctx, packageID)
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	if len(rs) > 0 {
		if err := json.Unmarshal([]byte(rs[0]), &tags); err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
		}
	}
	return tags, nil
}

// Publish stores the npm rows of a new version.
func (s *Store) Publish(ctx context.Context, tx meta.Tx, in rows.PublishInput) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	tombstoned, err := qs.NPMTombstoned(ctx, q.NPMTombstonedParams{RepoID: in.RepoID, Name: in.Name, Version: in.Version})
	if err != nil {
		return err
	}
	if tombstoned {
		return errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished,
			in.Name+"@"+in.Version+" was unpublished and is never accepted again; publish a new version.")
	}
	if _, err := qs.NPMInsertPackage(ctx, q.NPMInsertPackageParams{PackageID: in.PackageID, RepoID: in.RepoID, NameFold: in.NameFold, At: stamp(in.At)}); err != nil {
		return err
	}
	// The row is ours, or it was never created because another package of the
	// repository holds the fold (R-NPM-002): the unique index serialised the two.
	own, err := qs.NPMReadTags(ctx, in.PackageID)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		return errs.E(errs.Invalid, errs.CodeNPMInvalidName,
			"The name "+in.Name+" is too similar to a package that exists in this repository: names that differ only in case, \"-\", \"_\" and \".\" are not told apart.")
	}
	if err := qs.NPMInsertVersion(ctx, q.NPMInsertVersionParams{
		VersionID: in.VersionID, Manifest: string(in.Manifest), Integrity: in.Integrity, Shasum: in.Shasum,
		FileCount: clampInt32(in.FileCount), UnpackedSize: in.UnpackedSize,
		HasInstallScript: in.HasInstallScript, HasShrinkwrap: in.HasShrinkwrap,
		Readme: clean(in.Readme, maxReadme), ReadmeFilename: clean(in.ReadmeFilename, 255),
		PublishTag: in.Tag, PublishedAt: stamp(in.At),
	}); err != nil {
		return err
	}
	if err := s.setTags(ctx, qs, in.PackageID, in.Tags, in.At); err != nil {
		return err
	}
	details := map[string]any{"package": in.Name, "version": in.Version, "integrity": in.Integrity, "tag": in.Tag, "latest_guard": in.LatestKept}
	if in.Claim != "" {
		details["claim"] = in.Claim
	}
	return s.record(ctx, tx, audit.EventNPMPublish, in.Audit, in.Name, details, "", nil)
}

// RecordRetry records the acknowledged retry of a publish.
func (s *Store) RecordRetry(ctx context.Context, tx meta.Tx, in rows.RetryInput) error {
	details := map[string]any{"package": in.Name, "version": in.Version, "integrity": in.Integrity, "tag": in.Tag}
	return s.record(ctx, tx, audit.EventNPMPublish, in.Audit, in.Name, details, audit.ReasonRetryAcknowledged, nil)
}

// RecordMismatch records npm.publish.manifest_mismatch asynchronously.
func (s *Store) RecordMismatch(ctx context.Context, in rows.MismatchInput) error {
	ev, err := s.event(ctx, audit.EventNPMManifestMismatch, in.Audit, in.Name,
		map[string]any{"package": in.Name, "version": in.Version, "fields": in.Fields, "mode": in.Mode}, "", nil)
	if err != nil {
		return err
	}
	if in.Mode == "reject" {
		ev.Outcome, ev.Reason = audit.Failure, "manifest_mismatch"
	}
	s.o.Recorder.Record(ctx, ev)
	return nil
}

// SetTag stores the dist-tags a change of a tag produced.
func (s *Store) SetTag(ctx context.Context, tx meta.Tx, in rows.SetTagInput) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	if err := s.setTags(ctx, qs, in.PackageID, in.Tags, in.At); err != nil {
		return err
	}
	return s.record(ctx, tx, audit.EventNPMDistTagSet, in.Audit, in.Name,
		map[string]any{"package": in.Name, "tag": in.Tag, "version": in.Version, "previous": in.Previous}, "", nil)
}

// DeleteTag stores the dist-tags a removal of a tag produced.
func (s *Store) DeleteTag(ctx context.Context, tx meta.Tx, in rows.DeleteTagInput) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	if err := s.setTags(ctx, qs, in.PackageID, in.Tags, in.At); err != nil {
		return err
	}
	return s.record(ctx, tx, audit.EventNPMDistTagDelete, in.Audit, in.Name,
		map[string]any{"package": in.Name, "tag": in.Tag, "previous": in.Previous}, "", nil)
}

// Tombstone writes the tombstone of a live version and stores the tags without it.
func (s *Store) Tombstone(ctx context.Context, tx meta.Tx, in rows.TombstoneInput) (string, error) {
	qs, err := queries(tx)
	if err != nil {
		return "", err
	}
	integrity, err := qs.NPMInsertTombstone(ctx, q.NPMInsertTombstoneParams{
		RepoID: in.RepoID, Name: in.Name, Source: in.Source, At: stamp(in.At), By: in.By, VersionID: in.VersionID,
	})
	if err != nil {
		return "", err
	}
	if err := s.setTags(ctx, qs, in.PackageID, in.Tags, in.At); err != nil {
		return "", err
	}
	// The entry a restore's replay applies (Q-NPM-12, R-HA-054): the repository, the
	// package, the version, its integrity and the actor, and no secret.
	journal, err := json.Marshal(map[string]any{
		"repositoryId": in.RepoID, "package": in.Name, "version": in.Version, "integrity": integrity, "actorId": in.By,
	})
	if err != nil {
		return "", err
	}
	return integrity, s.record(ctx, tx, audit.EventNPMUnpublish, in.Audit, in.Name,
		map[string]any{"package": in.Name, "version": in.Version, "integrity": integrity}, "", journal)
}

// setTags stores the complete dist-tags map.
func (s *Store) setTags(ctx context.Context, qs *q.Queries, packageID int64, tags map[string]string, at time.Time) error {
	if tags == nil {
		tags = map[string]string{}
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	n, err := qs.NPMSetTags(ctx, q.NPMSetTagsParams{PackageID: packageID, DistTags: string(raw), At: stamp(at)})
	if err != nil {
		return err
	}
	if n != 1 {
		return errs.E(errs.Internal, errs.CodeInternal, "The npm row of the package is missing.")
	}
	return nil
}

// record writes a transactional event.
func (s *Store) record(ctx context.Context, tx meta.Tx, typ string, a rows.Audit, pkg string, details map[string]any, reason string, journal []byte) error {
	ev, err := s.event(ctx, typ, a, pkg, details, reason, journal)
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, ev)
}

func (s *Store) event(ctx context.Context, typ string, a rows.Audit, pkg string, details map[string]any, reason string, journal []byte) (audit.Event, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return audit.Event{}, err
	}
	ev := audit.Event{
		Type: typ, Outcome: audit.Success, Reason: reason,
		Actor:      audit.Actor{Kind: a.Actor.Kind, ID: a.Actor.ID, Name: a.Actor.Name, TokenID: a.Actor.TokenID, AuthMethod: a.Actor.AuthMethod},
		Subject:    audit.Subject{Kind: "package", Name: a.RepoKey + "/" + pkg},
		Repository: a.RepoKey, Path: pkg, Endpoint: a.Endpoint, Details: raw, Journal: journal,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	return ev, nil
}

// clean makes text fit a text column: valid UTF-8, no NUL, at most max bytes cut at
// a character boundary. The README of a tarball is publisher-controlled.
func clean(s string, max int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// clampInt32 narrows a count to the column's integer.
func clampInt32(n int64) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n) //nolint:gosec // G115: n is at most MaxInt32 here; a count is not negative
}
