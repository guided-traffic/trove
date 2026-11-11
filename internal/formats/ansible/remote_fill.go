package ansible

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The archives a remote repository caches (R-ANS-018, R-ANS-020). The announced digest (remote_gate.go) ties the bytes to the version
// document; the declared identity of the archive is the second check, that the upstream served the archive that was asked for: its
// MANIFEST.json must name the collection and the version of the path. The checks run before anything is stored, and no byte of an
// archive reaches a client before they ran (Q-ARCH-267): an archive that fails answers 502 to every request of the fetch and leaves no
// blob, no item and no row.

// InspectsFill declares the hook for the archive of a collection version and of a role version.
func (remoteAdapter) InspectsFill(p formats.Path) bool {
	t, err := parseTarget(p)
	return err == nil && (t.kind == kindArchive || t.kind == kindRoleArchive)
}

// InspectFill reads the spooled archive. A collection archive is read as an upload is (R-ANS-010, within the bounds of R-ANS-031)
// and its declared identity must be the path's: another namespace, name or version is 502 upstream_unavailable with the reason
// identity_mismatch, and an archive the reader refuses cannot be checked and is refused alike. A role archive has no manifest: it
// must be a gzip-compressed tar archive within the same bounds, and nothing else of it is claimed (R-ANS-020, H-840).
func (a remoteAdapter) InspectFill(ctx context.Context, _ formats.RemoteContext, p formats.Path, sp formats.Spooled) (*formats.Inspection, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	body, err := sp.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	lim := untrusted.DefaultLimits().Tighten(a.f.Descriptor().Limits)
	if t.kind == kindRoleArchive {
		return nil, checkRoleArchive(ctx, body, lim)
	}
	arc, err := collection.ReadArchive(ctx, body, lim)
	if err != nil {
		var ce *collection.Error
		if errors.As(err, &ce) {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
				"The upstream served an archive for "+t.ns+"."+t.name+" "+t.version+" that trove refuses (invalid_archive): "+ce.Detail, errs.WithCause(err))
		}
		return nil, err
	}
	if arc.Namespace != t.ns || arc.Name != t.name || arc.Version != t.version {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream served an archive for "+t.ns+"."+t.name+" "+t.version+" that declares "+arc.Namespace+"."+arc.Name+" "+arc.Version+" (identity_mismatch).")
	}
	return &formats.Inspection{
		Package: t.ns + "." + t.name, Version: t.version,
		Properties: map[string]string{
			"ansible.namespace": arc.Namespace, "ansible.name": arc.Name, "ansible.requires_ansible": arc.RequiresAnsible,
			"ansible.dependency_count": strconv.Itoa(len(arc.Dependencies)),
		},
		Payload: &inspected{archive: arc},
	}, nil
}

// CommitFill records the generic package and version rows of a collection archive a remote cached, in the transaction of the fill
// (FillCommitter, Q-ANS-76). The package screens list a remote repository's cache by these rows (R-UI-020), so a collection installed
// through a remote has a package, a version and the files of the cached archive, and the row of its manifest (putManifest) gives the page
// what a local collection's page has. A version that has a row already gets the item linked again, and a role archive has no inspection
// and is stored as it came.
func (a remoteAdapter) CommitFill(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	in := c.Inspection
	if in == nil || in.Package == "" || in.Version == "" {
		return nil
	}
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, in.Package, in.Package)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	linked := false
	at := time.Now()
	for _, v := range live {
		if version.Same(v.Version, in.Version) {
			if err := c.Packages.LinkItem(ctx, c.Item, v.ID, true); err != nil {
				return err
			}
			linked = true
			break
		}
	}
	if !linked {
		if _, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{Version: in.Version, DisplayVersion: in.Version, LeadItem: &c.Item}); err != nil {
			return err
		}
		_, bumped, err := c.Packages.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		at = bumped
	}
	props := map[string]string{}
	for k, v := range in.Properties {
		if v != "" {
			props[k] = v
		}
	}
	if err := c.Packages.SetProperties(ctx, c.Item, props); err != nil {
		return err
	}
	return a.putManifest(ctx, tx, c, at)
}

// putManifest records the manifest the inspection read, as a local publish records it (Q-ANS-76): the screens read the description, the
// licences, the dependencies, the links and Requires Ansible of a remote's collection from the same row as a local one's. The archive was
// read under the bounds of R-ANS-031 by InspectFill; nothing is read again.
func (a remoteAdapter) putManifest(ctx context.Context, tx formats.Tx, c formats.CommitEvent, at time.Time) error {
	ins, _ := c.Inspection.Payload.(*inspected)
	if ins == nil || ins.archive == nil {
		return nil
	}
	arc := ins.archive
	sv, err := version.Parse(arc.Version)
	if err != nil {
		return nil //nolint:nilerr // the reader accepted the version; a string that does not parse here is listed without a manifest
	}
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	return st.PutCachedVersion(ctx, tx, rows.VersionRow{
		RepoID: c.Repo.ID, Name: c.Inspection.Package, Version: arc.Version, SortKey: version.SortKey(sv), ItemID: c.Item.ID,
		SHA256: sumOf(c.Digests.SHA256), Size: c.Size, RequiresAnsible: arc.RequiresAnsible, Dependencies: arc.Dependencies,
		CollectionInfo: arc.Info, PublishedAt: at,
	})
}

// checkRoleArchive reads a role archive to its end within the bounds of R-ANS-031: a gzip-compressed tar archive without a link, a
// duplicate name, a bomb or data after its end.
func checkRoleArchive(ctx context.Context, body io.Reader, lim untrusted.Limits) error {
	ctx, cancel := lim.Context(ctx)
	defer cancel()
	tr := untrusted.NewTarGz(ctx, body, lim)
	for {
		e, err := tr.Next()
		if errors.Is(err, io.EOF) && !untrusted.IsRefused(err) && !untrusted.IsTooLarge(err) {
			return nil
		}
		if err != nil {
			if untrusted.IsRefused(err) || untrusted.IsTooLarge(err) {
				return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream served a role archive that trove refuses (invalid_archive): "+err.Error(), errs.WithCause(err))
			}
			return err
		}
		if e.Dir {
			continue
		}
		if _, err := tr.Stream(io.Discard); err != nil {
			if untrusted.IsRefused(err) || untrusted.IsTooLarge(err) {
				return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream served a role archive that trove refuses (invalid_archive): "+err.Error(), errs.WithCause(err))
			}
			return err
		}
	}
}
