package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ReinstateInput names the manifest `trove admin oci reinstate` puts back (R-OCI-033).
type ReinstateInput struct {
	// Project and Repository are the keys of the repository, Name the image name as the administrator typed it, Digest the
	// manifest's digest.
	Project, Repository, Name string
	Digest                    [32]byte
	// Actor is the administrator's command.
	Actor audit.Actor
}

// Reinstated is what a reinstate did.
type Reinstated struct {
	// Revision is packages.rev_seq after the change.
	Revision int64
}

// Reinstate reverses the withdrawal of a manifest that a restore's journal replay withheld (R-OCI-033, Q-OCI-33). It removes the
// manifest's mark only while the manifest row and its item are stored with the digest, so it brings back the same bytes and never
// admits new ones; it raises packages.rev_seq and writes oci.reinstate, all in one transaction.
//
// Every refusal changes nothing: a manifest without a mark (nothing is withheld), and a manifest whose row or item an administrator
// deleted to confirm the withdrawal (the bytes are gone, and the mark stays so the digest stays refused).
func (s *Store) Reinstate(ctx context.Context, in ReinstateInput) (Reinstated, error) {
	var out Reinstated
	digest := "sha256:" + hex.EncodeToString(in.Digest[:])
	label := in.Name + "@" + digest
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = Reinstated{}
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		repos, err := qs.BackupRepositoryByRef(ctx, q.BackupRepositoryByRefParams{ProjectKey: in.Project, RepoKey: in.Repository})
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "There is no repository "+in.Project+"/"+in.Repository+".")
		}
		repo := repos[0]
		if repo.Format != "oci" || repo.Class != "local" {
			return errs.E(errs.Invalid, errs.CodeInvalid, in.Project+"/"+in.Repository+" is not a local OCI repository.")
		}
		if _, err := qs.InsertPackage(ctx, q.InsertPackageParams{RepoID: repo.ID, Name: in.Name, DisplayName: in.Name}); err != nil {
			return err
		}
		pkgs, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: repo.ID, Name: in.Name})
		if err != nil {
			return err
		}
		if len(pkgs) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The repository holds no image name "+in.Name+".")
		}
		if _, err := qs.OCITombstoneRead(ctx, q.OCITombstoneReadParams{RepoID: repo.ID, Name: in.Name, Digest: in.Digest[:]}); err != nil {
			if isNoRows(err) {
				return errs.E(errs.NotFound, errs.CodeNotFound, label+" is not withheld: it has no mark.")
			}
			return err
		}
		if _, err := qs.OCIGetManifest(ctx, q.OCIGetManifestParams{RepoID: repo.ID, Name: in.Name, Digest: in.Digest[:]}); err != nil {
			if isNoRows(err) {
				return errs.E(errs.Conflict, errs.CodeConflict, label+" is no longer stored: its withdrawal was confirmed by deleting it, and its bytes are gone.")
			}
			return err
		}
		stored, err := qs.OCIManifestItemStored(ctx, q.OCIManifestItemStoredParams{
			RepoID: repo.ID, Path: in.Name + "/" + dirManifests + "/" + digest, Digest: in.Digest[:],
		})
		if err != nil {
			return err
		}
		if !stored {
			return errs.E(errs.Conflict, errs.CodeConflict, label+" is no longer stored: its withdrawal was confirmed by deleting it, and its bytes are gone.")
		}
		n, err := qs.OCIDeleteTombstone(ctx, q.OCIDeleteTombstoneParams{RepoID: repo.ID, Name: in.Name, Digest: in.Digest[:]})
		if err != nil {
			return err
		}
		if n != 1 {
			return errs.E(errs.Conflict, errs.CodeConflict, label+" changed while it was reinstated; run the command again.")
		}
		rev, err := qs.BumpPackageRevision(ctx, pkgs[0].ID)
		if err != nil {
			return err
		}
		details, err := json.Marshal(map[string]any{"name": in.Name, "digest": digest})
		if err != nil {
			return err
		}
		ev := s.event(ctx, audit.EventOCIReinstate, in.Project+"/"+in.Repository, in.Name, "", details, nil)
		ev.Actor = in.Actor
		if err := s.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
			return err
		}
		out = Reinstated{Revision: rev.RevSeq}
		return nil
	})
	return out, err
}
