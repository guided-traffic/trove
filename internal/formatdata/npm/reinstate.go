package npm

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ReinstateInput names the version `trove admin npm reinstate` puts back (R-NPM-047).
type ReinstateInput struct {
	// Project and Repository are the keys of the repository, Name the package name as the administrator typed it
	// (it is normalized by the caller), Version the version.
	Project, Repository, Name, Version string
	// Actor is the administrator's command.
	Actor audit.Actor
}

// Reinstated is what a reinstate did.
type Reinstated struct {
	Integrity string
	// JournalEntry is the ID of the journal entry that withheld the version, "" when the tombstone does not name one.
	JournalEntry string
	// Revision is packages.rev_seq after the change.
	Revision int64
}

// Reinstate reverses the withdrawal of a version that a restore's journal replay withheld (R-NPM-047, Q-NPM-8). It
// removes the version's tombstone only when the tombstone has the source `journal` and the version is still stored with
// the integrity the tombstone holds, so it brings back the same bytes and never admits new ones. It gives a package that
// has no latest tag one (R-NPM-016), raises packages.rev_seq and writes npm.reinstate, all in one transaction.
//
// Every refusal changes nothing: a tombstone of an unpublish or a move (nothing is withheld; the number is gone for
// good, R-NPM-008), a version whose items an administrator deleted to confirm the withdrawal, a version stored with other
// bytes than the tombstone names.
func (s *Store) Reinstate(ctx context.Context, in ReinstateInput) (Reinstated, error) {
	var out Reinstated
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = Reinstated{}
		qs, err := queries(tx)
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
		if repo.Format != "npm" || repo.Class != "local" {
			return errs.E(errs.Invalid, errs.CodeInvalid, in.Project+"/"+in.Repository+" is not a local npm repository.")
		}
		pkgs, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: repo.ID, Name: in.Name})
		if err != nil {
			return err
		}
		label := in.Name + "@" + in.Version
		if len(pkgs) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The repository holds no package "+in.Name+".")
		}
		pkg := pkgs[0]
		tomb, err := qs.BackupNPMTombstone(ctx, q.BackupNPMTombstoneParams{RepoID: repo.ID, Name: in.Name, Version: in.Version})
		if err != nil {
			return err
		}
		switch {
		case len(tomb) == 0:
			return errs.E(errs.NotFound, errs.CodeNotFound, label+" is not withheld: it has no tombstone.")
		case tomb[0].Source != "journal":
			return errs.E(errs.Conflict, errs.CodeConflict, label+" was unpublished or moved out by a client or an administrator, not withheld by a journal replay; the version number is never accepted again with other bytes (R-NPM-008).")
		}
		stored, err := qs.BackupNPMStoredVersion(ctx, q.BackupNPMStoredVersionParams{PackageID: pkg.ID, Version: in.Version})
		if err != nil {
			return err
		}
		switch {
		case len(stored) == 0:
			return errs.E(errs.Conflict, errs.CodeConflict, label+" is no longer stored: its withdrawal was confirmed by deleting its items, and its bytes are gone.")
		case stored[0].Integrity != tomb[0].Integrity:
			return errs.E(errs.Conflict, errs.CodeConflict, label+" is stored with other bytes than its tombstone names; a reinstate brings back the withheld bytes and admits no others.")
		}
		n, err := qs.BackupNPMDeleteJournalTombstone(ctx, q.BackupNPMDeleteJournalTombstoneParams{
			RepoID: repo.ID, Name: in.Name, Version: in.Version, Integrity: tomb[0].Integrity,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return errs.E(errs.Conflict, errs.CodeConflict, label+" changed while it was reinstated; run the command again.")
		}
		// A package that has no latest tag gets one (R-NPM-016); the tags that pointed at the version were dropped
		// when it was withheld and stay dropped.
		tags, err := s.Tags(ctx, tx, pkg.ID)
		if err != nil {
			return err
		}
		if tags["latest"] == "" {
			live, err := qs.NPMLiveVersions(ctx, pkg.ID)
			if err != nil {
				return err
			}
			names := make([]string, len(live))
			for i, v := range live {
				names[i] = v.Version
			}
			if best, ok := semver.Highest(names); ok {
				tags["latest"] = best
			}
		}
		rev, err := qs.BumpPackageRevision(ctx, pkg.ID)
		if err != nil {
			return err
		}
		if err := setTags(ctx, qs, pkg.ID, tags, rev.At.Time); err != nil {
			return err
		}
		details := map[string]any{"package": in.Name, "version": in.Version, "integrity": tomb[0].Integrity}
		if tomb[0].JournalEntry != "" {
			details["journal_entry"] = tomb[0].JournalEntry
		}
		a := rows.Audit{
			RepoKey: in.Project + "/" + in.Repository,
			Actor:   rows.Actor{Kind: in.Actor.Kind, ID: in.Actor.ID, Name: in.Actor.Name, TokenID: in.Actor.TokenID, AuthMethod: in.Actor.AuthMethod},
		}
		if err := s.record(ctx, tx, audit.EventNPMReinstate, a, in.Name, details, "", nil); err != nil {
			return err
		}
		out = Reinstated{Integrity: tomb[0].Integrity, JournalEntry: tomb[0].JournalEntry, Revision: rev.RevSeq}
		return nil
	})
	return out, err
}
