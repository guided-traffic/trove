package ansible

import (
	"context"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// publishCommit travels from the publish handler through the item store to OnCommit as the payload of the commit of the archive.
type publishCommit struct {
	archive *collection.Archive
	audit   rows.Audit
	// info is what the shadowing check found of a first version (R-REPO-048).
	info rows.PublishInfo
	// shadowSkipped says that the repository held a live version when the check was due, so nothing was asked: the commit must not find
	// the publish to be the first one then.
	shadowSkipped bool
	// task is the import task the commit records, filled in by the handler with the id and the repository.
	task rows.Task
	// taskRecorded is set by the hook once it wrote the task; a commit of identical content runs no hook, and the handler then records the
	// task itself.
	taskRecorded bool
}

func storeOf(data any) (Store, error) {
	st, ok := data.(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The Ansible Galaxy data port is not bound.")
	}
	return st, nil
}

// FillCommitter is the optional interface of the remote adapter that records what a fill stored (R-ANS-018): OnCommit hands it the
// commit of an archive a remote repository cached, in the transaction of the fill. An adapter without it stores the archive as it came.
type FillCommitter interface {
	CommitFill(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error
}

// RemovalHook is the optional interface of the adapter of a class other than the local one that learns of the removal of its items
// (a remote repository's cache entry, R-ANS-018): OnItemsRemoved hands it the event in the transaction of the removal.
type RemovalHook interface {
	ItemsRemoved(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error
}

// OnCommit runs in the transaction that wrote an item (R-ANS-011).
//
// The archive committed by a publish creates the package row if absent, checks the number and its tombstone under the package row lock,
// claims the name, stores the version with its row, records the import task and writes the audit event. Anything else that reaches a
// local repository is refused: /files/ takes no content (R-STOR-007) and a file that reached the repository any other way would be one
// no document lists. What a remote repository cached (a fill) is the remote class's.
func (f *Format) OnCommit(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	if p, ok := c.Payload.(*publishCommit); ok {
		return f.commitVersion(ctx, tx, c, p)
	}
	if c.Origin == formats.OriginFill {
		if h, ok := f.remote.(FillCommitter); ok {
			return h.CommitFill(ctx, tx, c)
		}
		return nil
	}
	return errs.E(errs.Conflict, errs.CodeConflict,
		"A collection enters an Ansible Galaxy repository only through ansible-galaxy collection publish; it cannot be copied or moved in.")
}

func (f *Format) commitVersion(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *publishCommit) error {
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	a := p.archive
	name := a.Namespace + "." + a.Name
	// The package row lock first: the number and tombstone checks below read only after it, so a publish racing a deletion of the same
	// number sees the version or its tombstone, never neither.
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, name, name)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	if _, tombstoned, err := st.Tombstoned(ctx, c.Repo.ID, name, a.Version); err != nil {
		return err
	} else if tombstoned {
		return versionTombstoned(name, a.Version)
	}
	for _, v := range live {
		if version.Same(v.Version, a.Version) {
			// The path of the item was free and the number is stored: another publish took the number meanwhile, with other bytes.
			return versionExists(name, a.Version)
		}
	}
	first := len(live) == 0
	if first && p.shadowSkipped {
		// The last version went away between the shadowing check and this commit: the publish gives the repository its first version of
		// the name, and R-REPO-048 has not been asked. The client retries, and the check then runs.
		return errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			name+" lost its last version while this publish ran; try again.", errs.WithRetryAfter(time.Second))
	}
	target := formats.Target{Package: name, Version: a.Version, Path: ArtifactPath(a.Namespace, a.Name, a.Version), Actions: []formats.Action{formats.ActionDeploy}}
	if err := c.Packages.Claim(ctx, target, formats.OwnershipWrite{Publish: true, FirstVersion: first}); err != nil {
		return err
	}
	if first {
		res, err := c.Packages.AutoClaim(ctx, name)
		if err != nil {
			return err
		}
		p.info.Claim = res.String()
	} else {
		p.info.Shadowed, p.info.ShadowUndetermined = nil, false
	}
	if _, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{
		Version: a.Version, DisplayVersion: a.Version, LeadItem: &c.Item, CreatedBy: c.Actor,
	}); err != nil {
		return err
	}
	_, at, err := c.Packages.BumpRevision(ctx, pkg)
	if err != nil {
		return err
	}
	props := map[string]string{"ansible.namespace": a.Namespace, "ansible.name": a.Name}
	if a.RequiresAnsible != "" {
		props["ansible.requires_ansible"] = a.RequiresAnsible
	}
	if c.Inspection != nil {
		for k, v := range c.Inspection.Properties {
			if v != "" {
				props[k] = v
			}
		}
	}
	if err := c.Packages.SetProperties(ctx, c.Item, props); err != nil {
		return err
	}
	sv, err := version.Parse(a.Version)
	if err != nil {
		return versionInvalid(a.Version)
	}
	by := c.Actor
	if by == "" {
		by = "00000000-0000-0000-0000-000000000000"
	}
	if err := st.PutVersion(ctx, tx, rows.VersionRow{
		RepoID: c.Repo.ID, Name: name, Version: a.Version, SortKey: version.SortKey(sv), ItemID: c.Item.ID, SHA256: sumOf(c.Digests.SHA256), Size: c.Size,
		RequiresAnsible: a.RequiresAnsible, Dependencies: a.Dependencies, CollectionInfo: a.Info, PublishedBy: by, PublishedAt: at,
	}, p.audit, p.info); err != nil {
		return err
	}
	t := p.task
	t.Namespace, t.Name, t.Version = a.Namespace, a.Name, a.Version
	t.State, t.RepoID, t.CreatedBy = rows.TaskCompleted, c.Repo.ID, by
	t.CreatedAt, t.StartedAt, t.UpdatedAt, t.FinishedAt = at, at, at, at
	t.Messages = []rows.TaskMessage{{Level: "INFO", Message: "Collection " + name + " " + a.Version + " stored (sha256 " + hexOf(c.Digests.SHA256)[:12] + "…)", Time: at}}
	for _, w := range a.Warnings {
		t.Messages = append(t.Messages, rows.TaskMessage{Level: "WARNING", Message: w, Time: at})
	}
	if err := st.CreateTask(ctx, tx, t, p.audit); err != nil {
		return err
	}
	p.taskRecorded = true
	return nil
}

func sumOf(b []byte) (d [32]byte) {
	copy(d[:], b)
	return d
}

// OnItemsRemoved runs in the transaction that removed files of a repository: the deletion of a version through the management API or
// the items API, a raw delete through /files/ and a move out of the repository. The removal of the archive of a version is the removal
// of the version (R-ANS-012): its version row, its tombstone with the journaled event ansible.version.deleted, the generic version and
// the revision of the package. The ownership of the name was decided before, in the same transaction, by the generic layer, which asks
// the format seam for every package of the removed files.
func (f *Format) OnItemsRemoved(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error {
	if e.Repo.Class != formats.Local {
		var adapter any
		switch e.Repo.Class {
		case formats.Remote:
			adapter = f.remote
		case formats.Virtual:
			adapter = f.virtual
		}
		if h, ok := adapter.(RemovalHook); ok {
			return h.ItemsRemoved(ctx, tx, e)
		}
		return nil
	}
	groups := f.removedArchives(e.Items)
	if len(groups) == 0 {
		return nil
	}
	st, err := storeOf(e.Data)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	au := rows.Audit{RepoKey: e.Repo.Name.String(), Actor: rows.Actor{ID: e.Actor}}
	for _, name := range names {
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, name, name)
		if err != nil {
			return err
		}
		if _, _, err := e.Packages.BumpRevision(ctx, pkg); err != nil {
			return err
		}
		for _, r := range groups[name] {
			if r.item.Version != 0 {
				if err := e.Packages.TrashOrDeleteVersion(ctx, r.item.Version, e.Reason != "trash"); err != nil {
					return err
				}
			}
			if err := st.RemoveVersion(ctx, tx, e.Repo.ID, name, r.version, r.item.SHA256, true, au); err != nil {
				return err
			}
		}
	}
	return nil
}

type removedArchive struct {
	version string
	item    formats.RemovedItem
}

// removedArchives sorts the removed items of the event by collection: the archive at its canonical path is a version; any other item is
// not the format's.
func (f *Format) removedArchives(items []formats.RemovedItem) map[string][]removedArchive {
	out := map[string][]removedArchive{}
	for _, it := range items {
		info := f.Classify(formats.Path{Path: it.Item.Path})
		if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
			continue
		}
		ns, name, _ := collection.SplitName(info.Package)
		if it.Item.Path != ArtifactPath(ns, name, info.Version) {
			continue
		}
		out[info.Package] = append(out[info.Package], removedArchive{version: info.Version, item: it})
	}
	return out
}

func versionExists(name, ver string) error {
	return errs.E(errs.Conflict, errs.CodeAnsibleVersionExists,
		name+" "+ver+" is stored already with other bytes; a version is never accepted again with other bytes. Publish the collection under a new version.")
}

func versionTombstoned(name, ver string) error {
	return errs.E(errs.Conflict, errs.CodeAnsibleVersionTombstoned,
		name+" "+version.Number(ver)+" was deleted and is not accepted again until an administrator clears its tombstone; publish the collection under a new version.")
}

func versionInvalid(ver string) error {
	return errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidVersion, "The version "+ver+" is not a semantic version 2.0.0.")
}
