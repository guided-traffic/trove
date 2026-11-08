package deb

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ReinstateInput names the file `trove admin deb reinstate` puts back (R-DEB-032).
type ReinstateInput struct {
	// Project and Repository are the keys of the repository, Path the canonical pool path of the file.
	Project, Repository, Path string
	// Actor is the administrator's command.
	Actor audit.Actor
}

// Reinstated is what a reinstate did.
type Reinstated struct {
	// SHA256 is the digest of the file that is served again.
	SHA256 [32]byte
}

// Reinstate reverses the withdrawal of a package file that a restore's journal replay withheld (R-DEB-032). It removes the file's mark
// only while the file is stored with the digest the mark holds, so it brings back the same bytes and never admits new ones; it raises
// change_seq of the file's suites and the revision of its package and writes deb.reinstate, all in one transaction.
//
// Every refusal changes nothing: a file without a mark (nothing is withheld), and a file that an administrator deleted to confirm the
// withdrawal (the bytes are gone, and the mark stays so the digest stays refused).
func (s *Store) Reinstate(ctx context.Context, in ReinstateInput) (Reinstated, error) {
	var out Reinstated
	g := s.localStore.group
	err := g.o.DB.InTx(ctx, func(tx meta.Tx) error {
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
		if repo.Format != "deb" || repo.Class != "local" {
			return errs.E(errs.Invalid, errs.CodeInvalid, in.Project+"/"+in.Repository+" is not a local Debian repository.")
		}
		marks, err := qs.DebTombstoneRead(ctx, q.DebTombstoneReadParams{RepoID: repo.ID, Path: in.Path})
		if err != nil {
			return err
		}
		if len(marks) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, in.Path+" is not withheld: it has no mark.")
		}
		var digest [32]byte
		copy(digest[:], marks[0].Sha256)
		ok, err := s.ReinstateFile(ctx, tx, repo.ID, in.Path, digest)
		if err != nil {
			return err
		}
		if !ok {
			return errs.E(errs.Conflict, errs.CodeConflict, in.Path+" is no longer stored: its withdrawal was confirmed by deleting it, and its bytes are gone.")
		}
		details, err := json.Marshal(map[string]any{"path": in.Path, "sha256": hex.EncodeToString(digest[:])})
		if err != nil {
			return err
		}
		ev := g.event(ctx, audit.EventDebReinstate, in.Project+"/"+in.Repository, in.Path, "", details, nil)
		ev.Actor = in.Actor
		if err := g.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
			return err
		}
		out = Reinstated{SHA256: digest}
		return nil
	})
	return out, err
}
