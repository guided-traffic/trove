package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The replay of oci.manifest.deleted entries after a restore (R-OCI-033, Q-OCI-30). The restore returned the repository to the
// backup's snapshot, so a manifest that was deleted afterwards, possibly because it was malicious, is stored again. The replay
// WITHHOLDS it: it writes the mark in oci_tombstones and raises the package's rev_seq, in the one transaction the journal gives a
// handler. It deletes nothing, neither the manifest row nor its items nor their bytes (R-HA-054): a forged or mistaken entry costs
// availability until an administrator reinstates the manifest or confirms the withdrawal by deleting it, never data, integrity or
// access. The serve check of the format (ServeCheck) refuses what the mark names.
//
// The entry is a claim to be checked against the restored database, not a fact. It acts only on a manifest that the repository
// stores with exactly the digest the entry names, and only in a local OCI repository.

func init() {
	journal.Register(journal.Handler{
		Domain: "oci",
		Events: []string{audit.EventOCIManifestDeleted},
		Apply:  replayManifestDeleted,
	})
}

// deletedEntry is the entry of oci.manifest.deleted (journalEntry): the repository, the image name, the digest and the principal.
type deletedEntry struct {
	Repository int64  `json:"repository"`
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	ActorID    string `json:"actorId"`
}

var (
	digestText = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	uuidText   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

var isNoRows = postgres.IsNoRows

// maxNameBytes is the bound of an image name (R-OCI-002), applied to an entry before it reaches a statement.
const maxNameBytes = 255

// decodeDeleted parses an entry; the second result says why it is no entry the replay can act on, "" for a usable one.
func decodeDeleted(raw json.RawMessage) (deletedEntry, [32]byte, string) {
	var e deletedEntry
	var d [32]byte
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, d, "the entry is not an oci.manifest.deleted entry"
	}
	switch {
	case e.Repository <= 0:
		return e, d, "the entry is incomplete: no repository"
	case e.Name == "" || len(e.Name) > maxNameBytes:
		return e, d, "the entry is incomplete: no image name"
	case !digestText.MatchString(e.Digest):
		return e, d, "the entry is incomplete: no digest"
	}
	_, _ = hex.Decode(d[:], []byte(e.Digest[len("sha256:"):])) // digestText admits only 64 hexadecimal digits
	return e, d, ""
}

// replayManifestDeleted applies one oci.manifest.deleted entry.
func replayManifestDeleted(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	res := journal.Result{ID: o.ID, Event: o.Event}
	e, digest, why := decodeDeleted(o.Entry)
	if why != "" {
		res.Outcome = journal.Skipped
		res.Detail, _ = json.Marshal(map[string]string{"reason": why}) //nolint:errcheck // a map of strings marshals
		return res, nil
	}
	res.Subject = e.Name + "@" + e.Digest
	qs, err := txQueries(tx)
	if err != nil {
		return res, err
	}
	repos, err := qs.OCIReplayRepository(ctx, e.Repository)
	if err != nil {
		return res, err
	}
	if len(repos) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	ref := repos[0].Ref
	res.Subject = ref + " " + res.Subject

	if _, err := qs.OCITombstoneRead(ctx, q.OCITombstoneReadParams{RepoID: e.Repository, Name: e.Name, Digest: digest[:]}); err == nil {
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	} else if !isNoRows(err) {
		return res, err
	}
	// The manifest is held when the restored repository has its row and its item with exactly these bytes.
	if _, err := qs.OCIGetManifest(ctx, q.OCIGetManifestParams{RepoID: e.Repository, Name: e.Name, Digest: digest[:]}); err != nil {
		if isNoRows(err) {
			res.Outcome = journal.SubjectUnknown
			return res, nil
		}
		return res, err
	}
	stored, err := qs.OCIManifestItemStored(ctx, q.OCIManifestItemStoredParams{
		RepoID: e.Repository, Path: e.Name + "/" + dirManifests + "/" + e.Digest, Digest: digest[:],
	})
	if err != nil {
		return res, err
	}
	if !stored {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	// The package row is locked first, as every writer of the name does, and its revision rises with the mark.
	if _, err := qs.InsertPackage(ctx, q.InsertPackageParams{RepoID: e.Repository, Name: e.Name, DisplayName: e.Name}); err != nil {
		return res, err
	}
	pkgs, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: e.Repository, Name: e.Name})
	if err != nil {
		return res, err
	}
	if len(pkgs) == 0 {
		res.Outcome = journal.SubjectUnknown
		return res, nil
	}
	by := ""
	if uuidText.MatchString(e.ActorID) {
		by = e.ActorID
	}
	n, err := qs.OCIPutTombstone(ctx, q.OCIPutTombstoneParams{
		RepoID: e.Repository, Name: e.Name, Digest: digest[:], WithheldAt: stamp(env.Now), DeletedBy: by,
	})
	if err != nil {
		return res, err
	}
	if n == 0 { // a concurrent writer withheld it between the check and the insert
		res.Outcome = journal.AlreadyInEffect
		return res, nil
	}
	if _, err := qs.BumpPackageRevision(ctx, pkgs[0].ID); err != nil {
		return res, err
	}
	res.Outcome = journal.Applied
	detail, err := json.Marshal(map[string]string{
		"withheld": "true", "repository": ref, "name": e.Name, "digest": e.Digest,
	})
	if err != nil {
		return res, err
	}
	res.Detail = detail
	return res, nil
}
