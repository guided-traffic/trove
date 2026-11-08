package nuget

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// The operations on a stored version (R-NUGET-007): DELETE v3/package/{id}/{version}
// unlists the version or, in a repository whose deleteMode is delete, removes it for good;
// POST of the same address relists it. Both are repeatable: a client that repeats a request
// whose answer was lost gets the answer of the first (R-NUGET-032). The permission and the
// ownership are decided first, so that an answer that tells nothing new (the 204 of a
// version that is gone) tells nothing to a caller who could not have deleted it.

// handleDelete is DELETE v3/package/{id}/{version}.
//
//   - unlist mode (the default): the version is unlisted and stays downloadable; an
//     unlisted version is unlisted again with 204.
//   - delete mode: the files of the version go through the package writer and the hook
//     (hooks_remove.go) writes the tombstone and removes the rows. The version number is
//     never accepted again (R-NUGET-006).
//
// A version the repository does not hold is 404; one it holds a tombstone for is 204
// without change in either mode, except that in delete mode a reserved tombstone becomes
// a removal.
func (f *Format) handleDelete(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID, lowerVersion := rq.Params.Get("id"), rq.Params.Get("version")
	// The path is the version's lead item, the file the delete removes or the unlist marks (Q-AUTHZ-250).
	target := formats.Target{Package: lowerID, Version: lowerVersion, Path: NupkgPath(lowerID, lowerVersion), Actions: []formats.Action{formats.ActionDelete}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	hard := settingsOf(rq.Repo.Config).DeleteMode == DeleteModeDelete
	// An ID that has no package row and no tombstone has no version to find: 404 before a
	// lock is taken, so that a delete never creates a package row (R-NUGET-007).
	_, pkgFound, err := rq.Host.Packages().Package(ctx, repo, lowerID)
	if err != nil {
		return err
	}
	if !pkgFound {
		if _, tomb, err := st.Tombstone(ctx, nil, repo.ID, lowerID, lowerVersion); err != nil {
			return err
		} else if !tomb {
			return errNoSuchResource()
		}
	}
	audit := auditOf(rq)
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		var pkg formats.PackageRef
		if pkgFound {
			// The lock first; the version and the tombstone are read only after it
			// (R-NUGET-006).
			p, _, err := pw.EnsureLocked(ctx, repo, lowerID, lowerID)
			if err != nil {
				return err
			}
			pkg = p
		}
		if err := pw.Claim(ctx, target, formats.OwnershipWrite{}); err != nil {
			return err
		}
		ref, found, err := st.FindVersion(ctx, tx, repo.ID, lowerID, lowerVersion)
		if err != nil {
			return err
		}
		if !found {
			return f.deleteGone(ctx, tx, st, rq, hard, audit, lowerID, lowerVersion)
		}
		if !hard {
			changed, err := st.SetListed(ctx, tx, rows.ListingInput{
				VersionID: ref.VersionID, LowerID: lowerID, LowerVersion: lowerVersion, Listed: false, Audit: audit,
			})
			if err != nil || !changed {
				return err
			}
			_, _, err = pw.BumpRevision(ctx, pkg)
			return err
		}
		if err := pw.DeleteItems(ctx, repo, lowerID+"/"+lowerVersion); err != nil {
			return err
		}
		// The hook removed the version with its files. A version whose package file was not
		// there to remove would stay without a tombstone: it is not a state this code makes
		// and the tombstone cannot be written without the bytes, so the delete fails whole.
		if _, still, err := st.FindVersion(ctx, tx, repo.ID, lowerID, lowerVersion); err != nil {
			return err
		} else if still {
			return errs.E(errs.Internal, errs.CodeInternal, "The version has no package file to delete; it was not removed.")
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// deleteGone answers a delete of a version the repository holds no row for: 404 when
// there is no tombstone either, else 204 without change - and in delete mode a reserved
// tombstone becomes a removal, so that the journaled bytes are refused as well.
func (f *Format) deleteGone(ctx context.Context, tx formats.Tx, st Store, rq *formats.Request, hard bool, audit rows.Audit, lowerID, lowerVersion string) error {
	repo := rq.RepoRef()
	t, tomb, err := st.Tombstone(ctx, tx, repo.ID, lowerID, lowerVersion)
	if err != nil {
		return err
	}
	if !tomb {
		return errNoSuchResource()
	}
	if hard && t.Kind == rows.TombstoneReserved {
		by := rq.Subject.ID
		if by == "" {
			by = noActor
		}
		_, _, err := st.PromoteReserved(ctx, tx, rows.ReservedInput{
			RepoID: repo.ID, LowerID: lowerID, LowerVersion: lowerVersion, By: by, Audit: audit,
		})
		return err
	}
	return nil
}

// handleRelist is POST v3/package/{id}/{version}: the version is listed again, and a
// version that is listed answers 200 as well. A version the repository does not hold is
// 404, a tombstoned one included.
func (f *Format) handleRelist(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID, lowerVersion := rq.Params.Get("id"), rq.Params.Get("version")
	target := formats.Target{Package: lowerID, Version: lowerVersion, Path: NupkgPath(lowerID, lowerVersion), Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	if _, found, err := rq.Host.Packages().Package(ctx, repo, lowerID); err != nil {
		return err
	} else if !found {
		return errNoSuchResource()
	}
	audit := auditOf(rq)
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		pkg, _, err := pw.EnsureLocked(ctx, repo, lowerID, lowerID)
		if err != nil {
			return err
		}
		// Relisting changes what version selection picks, so it needs deploy and the
		// ownership of the ID (R-AUTHZ-007), not the annotate of a mere metadata change.
		if err := pw.Claim(ctx, target, formats.OwnershipWrite{}); err != nil {
			return err
		}
		ref, found, err := st.FindVersion(ctx, tx, repo.ID, lowerID, lowerVersion)
		if err != nil {
			return err
		}
		if !found {
			return errNoSuchResource()
		}
		changed, err := st.SetListed(ctx, tx, rows.ListingInput{
			VersionID: ref.VersionID, LowerID: lowerID, LowerVersion: lowerVersion, Listed: true, Audit: audit,
		})
		if err != nil || !changed {
			return err
		}
		_, _, err = pw.BumpRevision(ctx, pkg)
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}
