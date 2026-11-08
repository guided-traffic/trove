package deb

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
)

// The replay of deb.package.deleted and deb.coordinates.removed entries after a restore (R-DEB-032, Q-DEB-22, Q-DEB-34). The restore
// returned the repository to the backup's snapshot, so a package that was deleted afterwards, possibly because it was malicious, is
// stored again, and a coordinate that was removed (a package pulled out of a suite) is back. The replay only restricts: a deleted
// package is WITHHELD by a mark in deb_tombstones, and removed coordinates are removed again; a file left without any coordinate is
// withheld as well. Nothing is deleted (R-HA-054): a forged or mistaken entry costs availability until an administrator reinstates the
// file, never data, integrity or access. The serve check refuses what a mark names and the builds leave it out.
//
// An entry is a claim to be checked against the restored database, not a fact: it acts only on a file that a local Debian repository
// stores at the path with exactly the digest the entry names.

func init() {
	journal.Register(journal.Handler{Domain: "deb", Events: []string{audit.EventDebPackageDeleted}, Apply: replayPackageDeleted})
	journal.Register(journal.Handler{Domain: "deb", Events: []string{audit.EventDebCoordinatesRemoved}, Apply: replayCoordinatesRemoved})
}

type fileEntry struct {
	Repository int64       `json:"repository"`
	Path       string      `json:"path"`
	SHA256     string      `json:"sha256"`
	Removed    []coordJSON `json:"removed"`
	ActorID    string      `json:"actorId"`
}

var (
	sha256Text = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuidText   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// maxPathBytes is the bound of a canonical path (R-STOR-003), applied to an entry before it reaches a statement.
const maxPathBytes = 1024

// decodeFile parses an entry; the second result says why it is no entry the replay can act on, "" for a usable one.
func decodeFile(raw json.RawMessage) (fileEntry, [32]byte, string) {
	var e fileEntry
	var d [32]byte
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, d, "the entry is not a Debian file entry"
	}
	switch {
	case e.Repository <= 0:
		return e, d, "the entry is incomplete: no repository"
	case e.Path == "" || len(e.Path) > maxPathBytes:
		return e, d, "the entry is incomplete: no path"
	case !sha256Text.MatchString(e.SHA256):
		return e, d, "the entry is incomplete: no digest"
	}
	_, _ = hex.Decode(d[:], []byte(e.SHA256)) // sha256Text admits only 64 hexadecimal digits
	return e, d, ""
}

func skipped(res journal.Result, why string) journal.Result {
	res.Outcome = journal.Skipped
	res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
	return res
}

func outcomeOf(r rows.ReplayResult) journal.Outcome {
	switch r {
	case rows.Applied:
		return journal.Applied
	case rows.AlreadyInEffect:
		return journal.AlreadyInEffect
	}
	return journal.SubjectUnknown
}

// replayPackageDeleted applies one deb.package.deleted entry.
func replayPackageDeleted(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, digest, why := decodeFile(o.Entry)
	if why != "" {
		return skipped(res, why), nil
	}
	res.Subject = e.Path
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.DebReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	ref := repos[0].Ref
	res.Subject = ref + " " + e.Path
	by := ""
	if uuidText.MatchString(e.ActorID) {
		by = e.ActorID
	}
	r, err := withhold(ctx, qs, rows.Tombstone{RepoID: e.Repository, Path: e.Path, SHA256: digest, DeletedBy: by}, env.Now)
	if err != nil {
		return res, err
	}
	res.Outcome = outcomeOf(r)
	if r == rows.Applied {
		res.Detail, err = json.Marshal(map[string]string{"withheld": "true", "repository": ref, "path": e.Path, "sha256": e.SHA256})
	}
	return res, err
}

// replayCoordinatesRemoved applies one deb.coordinates.removed entry.
func replayCoordinatesRemoved(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, digest, why := decodeFile(o.Entry)
	if why == "" && (len(e.Removed) == 0 || len(e.Removed) > 1024) {
		why = "the entry is incomplete: no coordinates"
	}
	if why != "" {
		return skipped(res, why), nil
	}
	res.Subject = e.Path
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.DebReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	ref := repos[0].Ref
	res.Subject = ref + " " + e.Path
	in := rows.CoordinatesRemoved{RepoID: e.Repository, Path: e.Path, SHA256: digest}
	for _, c := range e.Removed {
		in.Removed = append(in.Removed, rows.Coordinate{Suite: c.Distribution, Component: c.Component})
	}
	r, err := removeCoordinates(ctx, qs, in)
	if err != nil {
		return res, err
	}
	res.Outcome = outcomeOf(r)
	if r == rows.Applied {
		res.Detail, err = json.Marshal(map[string]any{"repository": ref, "path": e.Path, "removed": e.Removed})
	}
	return res, err
}
