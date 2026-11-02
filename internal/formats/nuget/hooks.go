package nuget

import (
	"bytes"
	"context"
	"encoding/hex"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// pushCommit travels from the push handler through the item store to OnCommit as the
// payload of the lead's commit, and carries the result back: the handler reads the
// revision after the commit.
type pushCommit struct {
	in *incoming
	// origin is one of the rows.Origin values.
	origin string
	audit  rows.Audit
	// shadowing is what the shadowing check found of a first version (R-REPO-048).
	shadowing rows.Shadowing
	// shadowSkipped says that the shadowing check asked nothing because the repository
	// held a live version when it ran: the commit must not find the push to be the first
	// one then.
	shadowSkipped bool

	// Filled by the hook from the commit event.
	digests formats.Digests
	size    int64
	rev     formats.RevSeq
}

// nuspecCommit is the payload of the generated .nuspec, the companion of the package's
// commit: its hook links the item to the version the lead's hook created in the same
// transaction.
type nuspecCommit struct{ in *incoming }

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func storeOf(data any) (Store, error) {
	st, ok := data.(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The NuGet data port is not bound.")
	}
	return st, nil
}

// OnCommit runs in the transaction that wrote an item (R-NUGET-005 step 6).
//
// The package committed by a push creates the package row if absent (its display name is
// the ID casing of the first version, Q-NUGET-5), checks the version and the tombstone
// under the package row lock, claims the ID, stores the version with its NuGet row and
// writes the audit event; the generated .nuspec is linked to the version. Anything else
// is refused: a NuGet repository holds the versions that a push created, on their
// canonical paths, and a file that reached it any other way would be one no document
// lists. The upload of the management API, copy and move are not served yet (Q-NUGET-32);
// a remote fill is not either, because the remote class is not registered.
func (f *Format) OnCommit(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	switch p := c.Payload.(type) {
	case *pushCommit:
		return f.commitVersion(ctx, tx, c, p)
	case *nuspecCommit:
		return f.commitNuspec(ctx, c, p)
	case *symbolCommit:
		return f.commitSymbols(ctx, tx, c, p)
	}
	if c.Origin == formats.OriginFill {
		return nil
	}
	return notImplemented("the upload of a package through the management API; push it with a NuGet client")
}

func (f *Format) commitVersion(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *pushCommit) error {
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	in := p.in
	// The package row lock first: the version and tombstone checks below read only after it,
	// so a push racing a hard delete of the same number sees the version or its tombstone,
	// never neither (R-NUGET-006).
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, in.lowerID, in.declaredID)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version == in.lowerVersion {
			// The path of the item was free and the number is stored: another push took the
			// number meanwhile, with other bytes.
			return versionExists(in)
		}
	}
	reserved, err := f.tombstoneVerdict(ctx, tx, st, c.Repo.ID, in, c.Digests.SHA512)
	if err != nil {
		return err
	}
	first := len(live) == 0
	if first && p.shadowSkipped {
		// The last version went away (a hard delete) between the shadowing check and this
		// commit: the push gives the repository its first version of the ID, and R-REPO-048
		// has not been asked. The client retries, and the check then runs.
		return errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			in.lowerID+" lost its last version while this push ran; try again.", errs.WithRetryAfter(time.Second))
	}
	if err := c.Packages.Claim(ctx, formats.Target{Package: in.lowerID, Version: in.lowerVersion, Actions: []formats.Action{formats.ActionDeploy}},
		formats.OwnershipWrite{Publish: true, FirstVersion: first}); err != nil {
		return err
	}
	claim := ""
	if first {
		res, err := c.Packages.AutoClaim(ctx, in.lowerID)
		if err != nil {
			return err
		}
		claim = res.String()
	}
	vid, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{
		Version: in.lowerVersion, DisplayVersion: in.version.FullNormalized(), LeadItem: &c.Item, CreatedBy: c.Actor,
	})
	if err != nil {
		return err
	}
	rev, at, err := c.Packages.BumpRevision(ctx, pkg)
	if err != nil {
		return err
	}
	if err := c.Packages.SetProperties(ctx, c.Item, map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion}); err != nil {
		return err
	}
	p.digests, p.size = c.Digests, c.Size
	vi, err := in.versionInput(c.Repo, int64(vid), p)
	if err != nil {
		return err
	}
	vi.PublishedAt, vi.Claim = at, claim
	if !first {
		vi.Shadowing = rows.Shadowing{}
	}
	if err := st.InsertVersion(ctx, tx, vi); err != nil {
		return err
	}
	if reserved {
		// The bytes the restore's replay reserved the number for: the tombstone has done
		// its work (R-NUGET-006, Q-NUGET-10).
		gone, err := st.DeleteTombstone(ctx, tx, int64(vid))
		if err != nil {
			return err
		}
		if !gone {
			return errs.E(errs.Internal, errs.CodeInternal, "The reservation of a NuGet version number could not be released by the package it was kept for.")
		}
	}
	p.rev = rev
	return nil
}

// tombstoneVerdict applies the tombstone of the number to a push of the given bytes
// (R-NUGET-006): none lets it pass; a removal refuses it whatever the bytes are; a
// reservation admits the bytes it was made for (reserved is then true: the caller deletes
// the tombstone in the same transaction) and refuses any other.
func (f *Format) tombstoneVerdict(ctx context.Context, tx formats.Tx, st Store, repoID int64, in *incoming, sha512 []byte) (reserved bool, err error) {
	t, found, err := st.Tombstone(ctx, tx, repoID, in.lowerID, in.lowerVersion)
	switch {
	case err != nil:
		return false, err
	case !found:
		return false, nil
	case t.Kind == rows.TombstoneReserved && bytes.Equal(t.SHA512, sha512):
		return true, nil
	case t.Kind == rows.TombstoneReserved:
		return false, errs.E(errs.Conflict, errs.CodeNuGetVersionReserved,
			in.declaredID+" "+in.lowerVersion+" is reserved for the package that was published before a restore; a rebuilt package needs a new version.")
	}
	return false, versionDeleted(in)
}

func versionExists(in *incoming) error {
	return errs.E(errs.Conflict, errs.CodeNuGetVersionExists,
		in.declaredID+" "+in.lowerVersion+" is stored already; a version is never accepted again with other bytes.")
}

func versionDeleted(in *incoming) error {
	return errs.E(errs.Conflict, errs.CodeNuGetVersionDeleted,
		in.declaredID+" "+in.lowerVersion+" was deleted and is never accepted again; push the package under a new version.")
}

// commitNuspec links the generated .nuspec to the version of the package. The .nuspec is
// a companion of the package's commit, so its hook runs after the package's in the same
// transaction and the version is live by then; a version that is not is a conflict, and
// rolls the package back with it.
func (f *Format) commitNuspec(ctx context.Context, c formats.CommitEvent, p *nuspecCommit) error {
	in := p.in
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, in.lowerID, in.declaredID)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version != in.lowerVersion {
			continue
		}
		if err := c.Packages.LinkItem(ctx, c.Item, v.ID, false); err != nil {
			return err
		}
		return c.Packages.SetProperties(ctx, c.Item, map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion})
	}
	return versionExists(in)
}
