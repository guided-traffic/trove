package oci

import (
	"context"
	"encoding/hex"
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// removedName groups what an event removed below one image name.
type removedName struct {
	// tags are the removed tag items by tag: the version row the tag held and the manifest it pointed at.
	tags map[string]formats.RemovedItem
	// manifests are the digests of the removed manifest items.
	manifests map[[32]byte]bool
	// blobs are the digests of the removed blob items.
	blobs map[[32]byte]bool
}

func (g *removedName) empty() bool {
	return len(g.tags) == 0 && len(g.manifests) == 0 && len(g.blobs) == 0
}

// digestFromText reads "sha256:<hex>" as the 32 bytes it names.
func digestFromText(s string) (d [32]byte, ok bool) {
	if !ValidDigest(s) {
		return d, false
	}
	_, err := hex.Decode(d[:], []byte(strings.TrimPrefix(s, "sha256:")))
	return d, err == nil
}

func digestText(d [32]byte) string { return "sha256:" + hex.EncodeToString(d[:]) }

// groupRemoved sorts the removed items of an event by image name. An item outside the layout, or below a name outside the grammar, is
// not the format's.
func groupRemoved(items []formats.RemovedItem) map[string]*removedName {
	out := map[string]*removedName{}
	for _, it := range items {
		name, kind, tail, ok := splitLayout(it.Item.Path)
		if !ok || !ValidName(name) {
			continue
		}
		g := out[name]
		if g == nil {
			g = &removedName{tags: map[string]formats.RemovedItem{}, manifests: map[[32]byte]bool{}, blobs: map[[32]byte]bool{}}
			out[name] = g
		}
		switch kind {
		case dirTags:
			g.tags[tail] = it
		case dirManifests:
			if d, ok := digestFromText(tail); ok {
				g.manifests[d] = true
			}
		case dirBlobs:
			if d, ok := digestFromText(tail); ok {
				g.blobs[d] = true
			}
		}
	}
	for name, g := range out {
		if g.empty() {
			delete(out, name)
		}
	}
	return out
}

// LockRemoval takes the package rows of the image names the files at paths belong to, in name order, before the item store deletes the
// rows of the files: a manifest push takes the package row first and then the rows of the layers it names (FOR SHARE), so a removal that
// deleted rows first and locked the package in the hook could wait for a push that waits for it (the order of Q-OCI-61, ADR-164).
func (f *Format) LockRemoval(ctx context.Context, _ formats.Tx, w formats.PackageWriter, repo formats.RepoRef, paths []string) error {
	if repo.Class != formats.Local {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, p := range paths {
		if name, kind, _, ok := splitLayout(p); ok && kind != dirBlobs && ValidName(name) && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if _, _, err := w.EnsureLocked(ctx, repo, name, name); err != nil {
			return err
		}
	}
	return nil
}

// OnItemsRemoved runs in the transaction that removed items of an OCI repository: the DELETE of a tag, a manifest or a blob, a raw
// delete through /files/ or the items API, a retention run, the sweep of unreferenced blobs and the cascade of a hook itself. It is
// the one road every removal takes (R-OCI-018, Q-OCI-24).
//
//   - A removed tag item takes its version row with it.
//   - A removed manifest item takes its row and the references it names, its tags, its referrers (the manifests whose subject it is,
//     unless an index of the name lists them) and, for an index, every child manifest that no other manifest and no tag of the name
//     references; the hook cascades through [formats.ItemsEvent.Cascade], which runs it again for what it removes. It records the
//     audit event oci.manifest.deleted with the journal entry a restore's replay withholds the manifest by (R-OCI-033), raises the
//     package's revision, and refuses, rolling the removal back, a manifest that an index of the name lists.
//   - A removed blob item is refused while a manifest of the name references the blob.
//
// A manifest or a blob that another removal of the same event takes as well (a folder deleted with its subtree) is no reference that
// refuses. The trash can does not exist yet (R-STOR-022): an event with the reason "trash" keeps the rows and cascades nothing, as the
// reason demands once it does, and the refusals apply to it.
//
// The ownership of the image name was decided before, in the same transaction, by the route and by the generic layer for a raw delete.
func (f *Format) OnItemsRemoved(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error {
	if e.Repo.Class != formats.Local && e.Repo.Class != formats.Remote {
		return nil
	}
	groups := groupRemoved(e.Items)
	if len(groups) == 0 {
		return nil
	}
	st, err := portOf(e.Data)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if e.Repo.Class == formats.Remote {
			err = f.onCacheRemoved(ctx, tx, st, e, name, groups[name])
		} else {
			err = f.onLocalRemoved(ctx, tx, st, e, name, groups[name])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// onCacheRemoved removes the rows of a remote repository's manifest with its alias item `<name>/_manifests/<digest>` (Q-OCI-34): the
// eviction of a metadata entry reaches no hook, the removal of that item does. A remote's tag documents and tags list own no row.
func (f *Format) onCacheRemoved(ctx context.Context, tx formats.Tx, st Store, e formats.ItemsEvent, name string, g *removedName) error {
	if len(g.manifests) == 0 {
		return nil
	}
	pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, name, name)
	if err != nil {
		return err
	}
	for _, d := range sortedDigests(g.manifests) {
		if err := st.RemoveManifest(ctx, tx, e.Repo.ID, name, d); err != nil {
			return err
		}
	}
	_, _, err = e.Packages.BumpRevision(ctx, pkg)
	return err
}

func (f *Format) onLocalRemoved(ctx context.Context, tx formats.Tx, st Store, e formats.ItemsEvent, name string, g *removedName) error {
	// The lock every writer of the name takes first (R-OCI-007), so that a manifest push and a removal of what it references are
	// ordered, and a refusal reads the references of a push that committed before. A name whose removal holds only blobs has no package
	// to lock and gets none: the blob rows arbitrate (a push holds them FOR SHARE), and the refusal below reads what the push committed.
	var pkg formats.PackageRef
	if len(g.tags) > 0 || len(g.manifests) > 0 {
		var err error
		if pkg, _, err = e.Packages.EnsureLocked(ctx, e.Repo, name, name); err != nil {
			return err
		}
	}
	// A tag item goes with its version row.
	tags := make([]string, 0, len(g.tags))
	for t := range g.tags {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	changed := false
	for _, t := range tags {
		if v := g.tags[t].Version; v != 0 {
			if err := e.Packages.TrashOrDeleteVersion(ctx, v, e.Reason != "trash"); err != nil {
				return err
			}
			changed = true
		}
	}
	// A blob that a manifest of the name still references is not removed. A manifest the same event removes is no reference.
	for _, d := range sortedDigests(g.blobs) {
		refs, err := st.ReferencedBy(ctx, tx, e.Repo.ID, name, d)
		if err != nil {
			return err
		}
		holders, scoped, err := outside(ctx, e, name, refs, g.manifests)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			return errs.E(errs.Conflict, errs.CodeOCIReferenced,
				"The blob "+digestText(d)+" is referenced by the manifest "+digestText(holders[0])+" of "+name+" and cannot be deleted.")
		}
		// A manifest the same delete takes in a later batch goes now, in this transaction: the batches of the job commit one by one, and a
		// batch that is refused or a job that is cancelled afterwards must not leave a manifest over a removed blob (Q-OCI-73).
		if err := f.removeWithThis(ctx, e, name, scoped); err != nil {
			return err
		}
	}
	for _, d := range sortedDigests(g.manifests) {
		if err := f.manifestRemoved(ctx, tx, st, e, name, d, g); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if _, _, err := e.Packages.BumpRevision(ctx, pkg); err != nil {
			return err
		}
	}
	return nil
}

// manifestRemoved handles the removal of the manifest item with digest d: the refusal, the row, the cascade and the event.
func (f *Format) manifestRemoved(ctx context.Context, tx formats.Tx, st Store, e formats.ItemsEvent, name string, d [32]byte, g *removedName) error {
	// The index that lists the manifest must go first (R-OCI-018): a removal of the manifest alone would leave a dangling reference.
	refs, err := st.ReferencedBy(ctx, tx, e.Repo.ID, name, d)
	if err != nil {
		return err
	}
	holders, scoped, err := outside(ctx, e, name, refs, g.manifests)
	if err != nil {
		return err
	}
	if len(holders) > 0 {
		return errs.E(errs.Conflict, errs.CodeOCIReferenced,
			"The manifest "+digestText(d)+" is listed by the index "+digestText(holders[0])+" of "+name+"; delete the index first.")
	}
	if err := f.removeWithThis(ctx, e, name, scoped); err != nil {
		return err
	}
	if e.Reason == "trash" {
		return nil // the rows stay while the item is in the trash (R-OCI-007), and so do the tags and the children
	}
	// What hangs on the manifest is read before its row goes, because the references go with the row.
	tagsOf, err := st.TagsPointingAt(ctx, tx, e.Repo.ID, name, d)
	if err != nil {
		return err
	}
	referrers, err := st.SubjectManifests(ctx, tx, e.Repo.ID, name, d)
	if err != nil {
		return err
	}
	children, err := st.Children(ctx, tx, e.Repo.ID, name, d)
	if err != nil {
		return err
	}
	if err := st.RemoveManifest(ctx, tx, e.Repo.ID, name, d); err != nil {
		return err
	}
	var paths []string
	for _, t := range tagsOf {
		paths = append(paths, TagPath(name, t))
	}
	// A referrer that an index of the name lists stays: the index must go first, and the subject's deletion does not fail for it.
	for _, r := range referrers {
		if g.manifests[r] {
			continue
		}
		rrefs, err := st.ReferencedBy(ctx, tx, e.Repo.ID, name, r)
		if err != nil {
			return err
		}
		held, _, err := outside(ctx, e, name, rrefs, g.manifests)
		if err != nil {
			return err
		}
		if len(held) == 0 {
			paths = append(paths, ManifestPath(name, digestText(r)))
		}
	}
	// A child goes with its index only when nothing else names it: no other manifest of the name and no tag.
	for _, c := range children {
		if g.manifests[c] {
			continue
		}
		crefs, err := st.ReferencedBy(ctx, tx, e.Repo.ID, name, c)
		if err != nil {
			return err
		}
		held, _, err := outside(ctx, e, name, crefs, g.manifests)
		if err != nil {
			return err
		}
		if len(held) > 0 {
			continue
		}
		ctags, err := st.TagsPointingAt(ctx, tx, e.Repo.ID, name, c)
		if err != nil {
			return err
		}
		if len(ctags) == 0 {
			paths = append(paths, ManifestPath(name, digestText(c)))
		}
	}
	paths = slices.Compact(paths)
	if len(paths) > 0 {
		if e.Cascade == nil {
			return errs.E(errs.Internal, errs.CodeInternal, "This host cannot cascade the removal of a manifest.")
		}
		if err := e.Cascade(ctx, paths...); err != nil {
			return err
		}
	}
	return st.RecordManifestDeleted(ctx, tx, rows.ManifestDeleted{
		RepoID: e.Repo.ID, RepoKey: e.Repo.Name.String(), Name: name, Digest: d, Deleted: len(paths), Actor: e.Actor,
	})
}

// outside sorts the manifests of refs that are not removed by this event: held are those that stay (they refuse), scoped are those whose
// item belongs to the removal of a subtree in another event or batch (ItemsEvent.InScope), because a delete of a whole image name reaches
// the hook in events of at most 5,000 files, or in the batches of a job, and the blobs of a name sort before its manifests.
func outside(ctx context.Context, e formats.ItemsEvent, name string, refs [][32]byte, removed map[[32]byte]bool) (held, scoped [][32]byte, err error) {
	for _, r := range refs {
		if removed[r] {
			continue
		}
		if e.InScope != nil {
			in, err := e.InScope(ctx, ManifestPath(name, digestText(r)))
			if err != nil {
				return nil, nil, err
			}
			if in {
				scoped = append(scoped, r)
				continue
			}
		}
		held = append(held, r)
	}
	return held, scoped, nil
}

// removeWithThis removes, in the transaction of the event, the manifests that the same delete takes later: a blob or a manifest that
// they reference goes now, so the transaction leaves no reference to it. The cascade runs the hook for them, with its own refusals.
func (f *Format) removeWithThis(ctx context.Context, e formats.ItemsEvent, name string, manifests [][32]byte) error {
	if len(manifests) == 0 {
		return nil
	}
	if e.Cascade == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "This host cannot cascade the removal of a manifest.")
	}
	paths := make([]string, len(manifests))
	for i, m := range manifests {
		paths[i] = ManifestPath(name, digestText(m))
	}
	return e.Cascade(ctx, paths...)
}

func sortedDigests(m map[[32]byte]bool) [][32]byte {
	out := make([][32]byte, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b [32]byte) int { return strings.Compare(string(a[:]), string(b[:])) })
	return out
}
