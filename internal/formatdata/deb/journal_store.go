package deb

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// journalStore is the group of the data port that writes the journaled events of files and applies their replay (R-DEB-032, Q-DEB-22,
// Q-DEB-34). The replay only restricts: it withholds a file and removes coordinates, and deletes no content.
type journalStore struct{ *group }

type coordJSON struct {
	Distribution string `json:"distribution"`
	Component    string `json:"component"`
}

func coordsJSON(cs []rows.Coordinate) []coordJSON {
	out := make([]coordJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, coordJSON{Distribution: c.Suite, Component: c.Component})
	}
	return out
}

// event completes an audit event of the data port: the repository, the path as the subject, the actor of the request when it is the
// principal with the id, and the source.
func (g *group) event(ctx context.Context, typ, repoKey, path, actorID string, details, journal []byte) audit.Event {
	ev := audit.Event{
		Type: typ, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "package", Name: repoKey + "/" + path},
		Repository: repoKey, Path: path, Details: details, Journal: journal,
	}
	if g.o.Source != nil {
		ev.Source = g.o.Source(ctx)
	}
	ev.Actor = g.actorOf(ctx, actorID)
	return ev
}

// actorOf describes the actor of an event the hook could name only by the principal's id: the actor of the request when it is the
// principal with that id, else a user with that id, else the system actor "removal".
func (g *group) actorOf(ctx context.Context, id string) audit.Actor {
	if id == "" {
		return audit.Actor{Kind: "system", Name: "removal"}
	}
	if g.o.Actor != nil {
		if a := g.o.Actor(ctx); a.Kind != "" && a.ID == id {
			return a
		}
	}
	return audit.Actor{Kind: "user", ID: id}
}

// RecordPackageDeleted writes deb.package.deleted with the journal entry a restore's replay withholds the file by (R-DEB-032).
func (s journalStore) RecordPackageDeleted(ctx context.Context, tx meta.Tx, in rows.PackageDeleted) error {
	sum := hex.EncodeToString(in.SHA256[:])
	details, err := json.Marshal(map[string]any{
		"path": in.Path, "package": in.Package, "version": in.Version, "architecture": in.Architecture, "sha256": sum,
	})
	if err != nil {
		return err
	}
	entry, err := json.Marshal(map[string]any{
		"repository": in.RepoID, "path": in.Path, "package": in.Package, "version": in.Version, "architecture": in.Architecture,
		"sha256": sum, "actorId": in.Actor,
	})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventDebPackageDeleted, in.RepoKey, in.Path, in.Actor, details, entry))
}

// RecordCoordinatesRemoved writes deb.coordinates.removed with its journal entry (Q-DEB-34).
func (s journalStore) RecordCoordinatesRemoved(ctx context.Context, tx meta.Tx, in rows.CoordinatesRemoved) error {
	sum := hex.EncodeToString(in.SHA256[:])
	details, err := json.Marshal(map[string]any{"path": in.Path, "sha256": sum, "removed": coordsJSON(in.Removed)})
	if err != nil {
		return err
	}
	entry, err := json.Marshal(map[string]any{
		"repository": in.RepoID, "path": in.Path, "sha256": sum, "removed": coordsJSON(in.Removed), "actorId": in.Actor,
	})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventDebCoordinatesRemoved, in.RepoKey, in.Path, in.Actor, details, entry))
}

// RecordCoordinatesChanged writes deb.coordinates.changed (R-DEB-009).
func (s journalStore) RecordCoordinatesChanged(ctx context.Context, tx meta.Tx, in rows.CoordinatesChangedEvent) error {
	details, err := json.Marshal(map[string]any{"path": in.Path, "added": coordsJSON(in.Added), "removed": coordsJSON(in.Removed)})
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventDebCoordinatesChanged, in.RepoKey, in.Path, in.Actor, details, nil))
}

// Withhold writes the mark of a file the replay withheld and raises change_seq of its suites (R-DEB-032).
func (s journalStore) Withhold(ctx context.Context, tx meta.Tx, in rows.Tombstone) (rows.ReplayResult, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return "", err
	}
	res, err := withhold(ctx, qs, in, time.Time{})
	if err == nil && res == rows.Applied {
		err = s.enqueueDefault(ctx, tx, in.RepoID)
	}
	return res, err
}

func withhold(ctx context.Context, qs *q.Queries, in rows.Tombstone, now time.Time) (rows.ReplayResult, error) {
	marks, err := qs.DebTombstoneRead(ctx, q.DebTombstoneReadParams{RepoID: in.RepoID, Path: in.Path})
	if err != nil {
		return "", err
	}
	if len(marks) > 0 {
		return rows.AlreadyInEffect, nil
	}
	stored, err := qs.DebFileStored(ctx, q.DebFileStoredParams{RepoID: in.RepoID, Path: in.Path, Sha256: in.SHA256[:]})
	if err != nil {
		return "", err
	}
	if !stored {
		return rows.SubjectUnknown, nil
	}
	list, err := qs.DebBinaryByPath(ctx, q.DebBinaryByPathParams{RepoID: in.RepoID, Path: in.Path})
	if err != nil {
		return "", err
	}
	if len(list) == 0 { // an item the format has no row for is no package file of it
		return rows.SubjectUnknown, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	n, err := qs.DebPutTombstone(ctx, q.DebPutTombstoneParams{RepoID: in.RepoID, Path: in.Path, Sha256: in.SHA256[:], WithheldAt: now.UTC().Format(time.RFC3339Nano), DeletedBy: in.DeletedBy})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return rows.AlreadyInEffect, nil
	}
	if err := bumpFile(ctx, qs, in.RepoID, list[0]); err != nil {
		return "", err
	}
	return rows.Applied, nil
}

// bumpFile raises change_seq of the suites of a file and the revision of its package, in the replay's transaction: the mark is a change
// of what the repository serves.
func bumpFile(ctx context.Context, qs *q.Queries, repoID int64, b q.DebBinaryByPathRow) error {
	cs, err := qs.DebCoordinatesOf(ctx, b.ItemID)
	if err != nil {
		return err
	}
	if err := raise(ctx, qs, repoID, suitesOf(toCoords(cs))); err != nil {
		return err
	}
	if _, err := qs.InsertPackage(ctx, q.InsertPackageParams{RepoID: repoID, Name: b.Package, DisplayName: b.Package}); err != nil {
		return err
	}
	pkgs, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: repoID, Name: b.Package})
	if err != nil || len(pkgs) == 0 {
		return err
	}
	_, err = qs.BumpPackageRevision(ctx, pkgs[0].ID)
	return err
}

// RemoveCoordinatesByJournal is the replay of deb.coordinates.removed: when the restored repository holds the file at the path with the
// digest, the coordinates are removed from it, and a file left without any is withheld by a mark, never deleted (Q-DEB-34).
func (s journalStore) RemoveCoordinatesByJournal(ctx context.Context, tx meta.Tx, in rows.CoordinatesRemoved) (rows.ReplayResult, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return "", err
	}
	return removeCoordinates(ctx, qs, in)
}

func removeCoordinates(ctx context.Context, qs *q.Queries, in rows.CoordinatesRemoved) (rows.ReplayResult, error) {
	stored, err := qs.DebFileStored(ctx, q.DebFileStoredParams{RepoID: in.RepoID, Path: in.Path, Sha256: in.SHA256[:]})
	if err != nil {
		return "", err
	}
	list, err := qs.DebBinaryByPath(ctx, q.DebBinaryByPathParams{RepoID: in.RepoID, Path: in.Path})
	if err != nil {
		return "", err
	}
	if !stored || len(list) == 0 {
		return rows.SubjectUnknown, nil
	}
	b := list[0]
	var touched []string
	for _, c := range in.Removed {
		n, err := qs.DebDeleteCoordinate(ctx, q.DebDeleteCoordinateParams{ItemID: b.ItemID, Suite: c.Suite, Component: c.Component})
		if err != nil {
			return "", err
		}
		if n == 1 && !slices.Contains(touched, c.Suite) {
			touched = append(touched, c.Suite)
		}
	}
	if len(touched) == 0 {
		return rows.AlreadyInEffect, nil
	}
	if err := raise(ctx, qs, in.RepoID, touched); err != nil {
		return "", err
	}
	left, err := qs.DebCoordinateCount(ctx, b.ItemID)
	if err != nil {
		return "", err
	}
	if left == 0 {
		if _, err := withhold(ctx, qs, rows.Tombstone{RepoID: in.RepoID, Path: in.Path, SHA256: in.SHA256}, time.Time{}); err != nil {
			return "", err
		}
	}
	return rows.Applied, nil
}

// ReinstateFile removes the mark of a withheld file, only while the file is stored with that digest, and raises change_seq of its
// suites; it reports whether a mark was there.
func (s journalStore) ReinstateFile(ctx context.Context, tx meta.Tx, repoID int64, path string, sha256 [32]byte) (bool, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return false, err
	}
	stored, err := qs.DebFileStored(ctx, q.DebFileStoredParams{RepoID: repoID, Path: path, Sha256: sha256[:]})
	if err != nil || !stored {
		return false, err
	}
	n, err := qs.DebDeleteTombstone(ctx, q.DebDeleteTombstoneParams{RepoID: repoID, Path: path, Sha256: sha256[:]})
	if err != nil || n == 0 {
		return false, err
	}
	list, err := qs.DebBinaryByPath(ctx, q.DebBinaryByPathParams{RepoID: repoID, Path: path})
	if err != nil || len(list) == 0 {
		return true, err
	}
	if err := bumpFile(ctx, qs, repoID, list[0]); err != nil {
		return true, err
	}
	return true, s.enqueueDefault(ctx, tx, repoID)
}

// Tombstone reads the mark of a path; found is false when there is none.
func (s journalStore) Tombstone(ctx context.Context, repoID int64, path string) (rows.Tombstone, bool, error) {
	var out rows.Tombstone
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.DebTombstoneRead(ctx, q.DebTombstoneReadParams{RepoID: repoID, Path: path})
		if err != nil || len(list) == 0 {
			return err
		}
		out = rows.Tombstone{RepoID: list[0].RepoID, Path: list[0].Path, DeletedBy: list[0].DeletedBy}
		copy(out.SHA256[:], list[0].Sha256)
		found = true
		return nil
	})
	return out, found, err
}
