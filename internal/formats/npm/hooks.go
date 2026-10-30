package npm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/tarball"
)

// areaMetadataChanged is the signal that evicts merged metadata early after a
// change of a package (R-REPO-020). The payload is formats.PackageChanged of the
// repository and the name, which every change of the rows of a package attaches in
// its transaction; nothing's correctness depends on it (R-NPM-007 step 5).
const areaMetadataChanged = "metadata_changed"

// publishCommit travels from the publish handler through the item store to
// OnCommit as the commit's payload, and carries the result back: the handler reads
// the revision and the tags after the commit.
type publishCommit struct {
	name    name.Name
	version string
	tag     string
	// stored is the manifest to store (R-NPM-007), res the validated tarball and
	// installScript the rule of R-NPM-007 applied to it.
	stored        manifest.Manifest
	res           *tarball.Result
	installScript bool
	// principal is the id of the publishing principal, "" for none.
	principal string
	audit     rows.Audit
	// shadowing is what the shadowing check found of a first version (R-REPO-048).
	shadowing rows.Shadowing
	// shadowSkipped says that the shadowing check asked nothing because the repository held a
	// live version when it ran.
	shadowSkipped bool

	// Filled by the hook.
	pkg    formats.PackageID
	rev    formats.RevSeq
	latest string
	kept   bool
}

// sigstoreCommit is the payload of the commit of a provenance bundle, which links
// the item to the version the tarball commit created.
type sigstoreCommit struct {
	name    name.Name
	version string

	rev formats.RevSeq
}

// OnCommit runs in the transaction that wrote an item (R-NPM-007 step 5).
//
// A tarball committed by a publish creates the package rows, claims the name,
// stores the version with its tags and writes the audit event; a provenance bundle
// is linked to its version. Anything else is refused: an npm repository holds
// versions that a publish created, on their canonical paths, and a raw upload would
// put a file there that no document lists and that R-NPM-008 could not protect
// (raw deploy of a tarball without the client is R-NPM-036, P1). A fill (remote
// repositories) is not served yet.
func (f *Format) OnCommit(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	switch p := c.Payload.(type) {
	case *publishCommit:
		return f.commitVersion(ctx, tx, c, p)
	case *sigstoreCommit:
		return f.commitSigstore(ctx, c, p)
	}
	if c.Origin == formats.OriginFill {
		return nil
	}
	return manifest.BadPublish("A file reaches an npm repository through npm publish; a raw upload is not supported.")
}

func storeOf(data any) (Store, error) {
	st, ok := data.(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The npm data port is not bound.")
	}
	return st, nil
}

func (f *Format) commitVersion(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *publishCommit) error {
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	pkgName := p.name.String()
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, pkgName, pkgName)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version == p.version {
			// The commit wrote the item of a version that is stored: only a path that
			// was free can reach here, so another publish took the number meanwhile.
			return errs.E(errs.Conflict, errs.CodeNPMVersionExists, pkgName+"@"+p.version+" is published already; a version is never accepted again with other bytes.")
		}
	}
	first := len(live) == 0
	if first && p.shadowSkipped {
		// The last version went away (an unpublish) between the shadowing check and this commit:
		// the publish gives the repository its first version of the name, and R-REPO-048 has not
		// been asked. The client retries, and the check then runs.
		return errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			pkgName+" lost its last version while this publish ran; try again.", errs.WithRetryAfter(time.Second))
	}
	if err := f.claim(ctx, c.Packages, opPublish, pkgName, p.version, first); err != nil {
		return err
	}
	claim := ""
	if first {
		res, err := c.Packages.AutoClaim(ctx, pkgName)
		if err != nil {
			return err
		}
		claim = res.String()
	}
	vid, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{
		Version: p.version, DisplayVersion: p.version, LeadItem: &c.Item, CreatedBy: c.Actor,
	})
	if err != nil {
		return err
	}
	rev, at, err := c.Packages.BumpRevision(ctx, pkg)
	if err != nil {
		return err
	}
	props := map[string]string{"npm.name": pkgName, "npm.version": p.version}
	if p.name.IsScoped() {
		props["npm.scope"] = "@" + p.name.Scope()
	}
	if err := c.Packages.SetProperties(ctx, c.Item, props); err != nil {
		return err
	}
	stored, err := json.Marshal(p.stored)
	if err != nil {
		return err
	}
	cur, err := st.Tags(ctx, tx, int64(pkg.ID))
	if err != nil {
		return err
	}
	tags, kept := tagAfterPublish(cur, p.tag, p.version, true)
	err = st.Publish(ctx, tx, rows.PublishInput{
		RepoID: c.Repo.ID, PackageID: int64(pkg.ID), VersionID: int64(vid), Name: pkgName, NameFold: p.name.Fold(), Version: p.version,
		Manifest: stored, Integrity: p.res.Integrity(), Shasum: p.res.SHA1Hex(),
		FileCount: p.res.FileCount, UnpackedSize: p.res.UnpackedSize,
		HasInstallScript: p.installScript, HasShrinkwrap: p.res.HasShrinkwrap,
		Readme: p.res.Readme, ReadmeFilename: p.res.ReadmeFilename,
		Tag: p.tag, Tags: tags, LatestKept: kept, At: at, PublishedBy: c.Actor, Claim: claim, Audit: p.audit,
		Shadowing: shadowingOf(first, p.shadowing),
	})
	if err != nil {
		return err
	}
	if err := c.Packages.Notify(areaMetadataChanged, formats.PackageChanged(c.Repo.ID, pkgName)); err != nil {
		return err
	}
	p.pkg, p.rev, p.latest, p.kept = pkg.ID, rev, tags["latest"], kept
	return nil
}

// shadowingOf is what the commit records of the shadowing check: only the publish that
// gives the repository its first version of a name is a shadowing one.
func shadowingOf(first bool, s rows.Shadowing) rows.Shadowing {
	if !first {
		return rows.Shadowing{}
	}
	return s
}

// commitSigstore links the provenance bundle to the version of the tarball. The
// bundle is committed after the tarball, so the version is live by then; a version
// that was unpublished in between is a conflict.
func (f *Format) commitSigstore(ctx context.Context, c formats.CommitEvent, p *sigstoreCommit) error {
	pkgName := p.name.String()
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, pkgName, pkgName)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version != p.version {
			continue
		}
		if err := f.claim(ctx, c.Packages, opPublish, pkgName, p.version, false); err != nil {
			return err
		}
		if err := c.Packages.LinkItem(ctx, c.Item, v.ID, false); err != nil {
			return err
		}
		rev, _, err := c.Packages.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		p.rev = rev
		return c.Packages.Notify(areaMetadataChanged, formats.PackageChanged(c.Repo.ID, pkgName))
	}
	return errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished, pkgName+"@"+p.version+" is not published any more; its provenance bundle is not stored.")
}

// operation is a write whose ownership and action R-AUTHZ-007 fixes.
type operation int

const (
	opPublish operation = iota + 1
	opDistTag
	opUnpublish
	opDeprecate
)

// claim is the ownership check of a write in its transaction (R-AUTHZ-018, C-14),
// to be called under the package row lock EnsureLocked took. A publish adds a
// version, and the first version of a name claims it; a change of a dist-tag, an
// unpublish and a deprecation are writes by an owner, a holder of manage or
// content:bypass. The work packages that build the other three call it with their
// operation, so every write of npm asks the same question the same way.
func (f *Format) claim(ctx context.Context, w formats.PackageWriter, op operation, pkg, version string, firstVersion bool) error {
	t := formats.Target{Package: pkg, Version: version, Actions: []formats.Action{formats.ActionDeploy}}
	if op == opUnpublish {
		t.Actions = []formats.Action{formats.ActionDelete}
	}
	return w.Claim(ctx, t, formats.OwnershipWrite{Publish: op == opPublish, FirstVersion: firstVersion})
}
