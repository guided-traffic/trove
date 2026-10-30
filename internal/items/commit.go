package items

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

const retryAfter = 2 * time.Second

// ErrOverwriteDenied is returned by [Store.Commit] when the link step finds other
// content at the path and the spec did not say the committer may replace it
// (CommitSpec.MayOverwrite). Nothing was written. The generic layer answers it
// with the decision it owes the caller; the proxy never meets it, because a fill
// sets MayOverwrite.
var ErrOverwriteDenied = errors.New("items: overwrite not granted")

// IngestOptions describe one body for [Store.Spool].
type IngestOptions struct {
	// ContentLength is the declared length, -1 when unknown (chunked).
	ContentLength int64
	// MaxSize bounds the body; 0 is no bound but the system's.
	MaxSize int64
	// Expect are the digests the committer announced; a body that differs is
	// refused (409) and nothing is stored.
	Expect Expected
}

// Spool reads a body into the replica-local spool and hashes it in one pass
// (R-STOR-004, R-STOR-005). Nothing reaches the object store before
// [Store.Commit]. The caller closes the returned body whatever happens next.
func (s *Store) Spool(ctx context.Context, body io.Reader, o IngestOptions) (*Spooled, error) {
	return s.blobs.Ingest(ctx, body, blob.IngestOptions{ContentLength: o.ContentLength, MaxSize: o.MaxSize, Expect: o.Expect})
}

// CommitSpec is one file to commit: the spooled body, the path it takes in a
// repository, and what is recorded with it. It is the one commit of the item
// store; the generic layer commits an upload with it, a format's route commits an
// upload through the host, and the proxy commits the artifact of a remote fill.
type CommitSpec struct {
	Repo repository.Repository
	// Path is the canonical path of a file; a folder path or the root is refused.
	Path    repository.Cleaned
	Spooled *Spooled
	// MediaType is stored with the item; "" stores none.
	MediaType string
	// Declared are the digests the committer announced, stored as the item's
	// declared digests.
	Declared Expected
	Origin   Origin
	// UpstreamURL is stored with the item (OriginFill); the store removes user info.
	UpstreamURL string
	// CreateOnly refuses a path that exists (If-None-Match: *, R-API-012): 412.
	CreateOnly bool
	// MayOverwrite says that other content at the path may be replaced. The
	// committer settled it before the transaction opened, outside it, so that no
	// decision waits for an index while the transaction holds the blob row
	// (R-STOR-007, R-ARCH-019). When it is false and the path holds other content
	// (a concurrent upload took it meanwhile) the commit returns
	// [ErrOverwriteDenied] and writes nothing.
	MayOverwrite bool
	// Actor, Source and Endpoint are what the audit event records.
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
	// Payload is handed to the storage hook in the CommitEvent.
	Payload any
	// Extra runs in the commit transaction right after the item row was written
	// and before the storage hook, with the item the row is: the generic layer
	// records the allowed write with it, the proxy upserts the cache entry that
	// references the item id. It runs for an identical re-commit as well, which
	// changes no row. An error rolls the whole commit back. It must be safe to run
	// again: the transaction may be restarted.
	Extra func(ctx context.Context, tx meta.Tx, it Ref) error
	// BeginCommit, when set, runs right before the commit protocol, once the body
	// has arrived: the server then lets the request finish past the shutdown
	// deadline (R-ARCH-024 step 5).
	BeginCommit func()
}

// CommitResult is the outcome of a commit.
type CommitResult struct {
	// Created is true for a new path and for a replacement (201), false for
	// identical content (200).
	Created bool
	// Overwritten says that different content replaced an existing file.
	Overwritten bool
	Item        Item
}

// Commit stores a spooled body as the file at a path (R-STOR-005, R-ARCH-014
// step 6): the blob goes to the object store, then one transaction holds the
// blob row's lock, creates the ancestor folders, serialises on the path and
// writes the item row, and then, in this order and in that same transaction,
// runs [CommitSpec.Extra], the storage hook ([Hooks.OnCommit]) and writes the
// audit row. A failure of any of them leaves no item. Identical content at the
// path changes no row: only Extra runs.
//
// Commit makes no decision. The committer decided before and settles the
// overwrite question in CommitSpec.MayOverwrite.
func (s *Store) Commit(ctx context.Context, spec CommitSpec) (CommitResult, error) {
	switch {
	case spec.Path.Path == "" || spec.Path.Folder:
		return CommitResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath, "A file is committed at a path that names a file.")
	case spec.Spooled == nil:
		return CommitResult{}, errs.E(errs.Internal, errs.CodeInternal, "items: no spooled body to commit")
	case spec.Origin != OriginUpload && spec.Origin != OriginFill:
		return CommitResult{}, errs.E(errs.Internal, errs.CodeInternal, "items: a commit has an origin")
	}
	repo := RepoOf(spec.Repo)
	spec.UpstreamURL = withoutUserInfo(spec.UpstreamURL)
	if spec.BeginCommit != nil {
		spec.BeginCommit()
	}
	var out linkOutcome
	_, err := s.blobs.Commit(ctx, spec.Spooled, func(ctx context.Context, tx meta.Tx) error {
		out = linkOutcome{}
		return s.linkFile(ctx, tx, repo, spec, &out)
	})
	if err != nil {
		return CommitResult{}, err
	}
	s.touch(repo.ID)
	sp := spec.Spooled
	it, found, err := s.Live(ctx, repo, spec.Path.Path)
	if err != nil {
		return CommitResult{}, err
	}
	if !found { // removed again between the commit and this read
		it = Item{Repository: repo.Key, Path: spec.Path.Path, Kind: KindFile, Size: sp.Size, Digest: sp.SHA256, MediaType: spec.MediaType}
	}
	return CommitResult{Created: out.created || out.overwritten, Overwritten: out.overwritten, Item: it}, nil
}

// withoutUserInfo removes the credentials a URL may carry (user:password@): the
// item keeps where its bytes came from, never how the proxy got in. A value that
// is not a URL is dropped.
func withoutUserInfo(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

// linkOutcome is what the link step of a file commit did.
type linkOutcome struct {
	created, overwritten bool
}

// maxLinkRounds bounds the retries of the link step when concurrent writers
// change the path or its parent between two statements.
const maxLinkRounds = 5

// linkFile is the link function of the commit protocol, in the transaction that
// holds the blob row's lock. It may run again (a restarted round), so it resets
// its outcome.
func (s *Store) linkFile(ctx context.Context, tx meta.Tx, repo RepoRef, spec CommitSpec, out *linkOutcome) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	sp, path := spec.Spooled, spec.Path.Path
	p := fileParams{rid: repo.ID, path: path, parent: ParentOf(path), spec: spec, actor: actorID(spec.Actor)}
	for range maxLinkRounds {
		if err := ensureParents(ctx, qs, repo.ID, p.actor, path); err != nil {
			return err
		}
		locked, err := lockAncestors(ctx, qs, repo.ID, path)
		if err != nil {
			return err
		}
		if !locked { // an ancestor was removed by a concurrent delete; create it again
			continue
		}
		rows, err := qs.LockLiveItem(ctx, q.LockLiveItemParams{RepoID: repo.ID, Path: path})
		if err != nil {
			return err
		}
		ref := Ref{Repo: repo, Path: path}
		switch {
		case len(rows) == 0:
			id, ok, err := insertFile(ctx, qs, p)
			if err != nil {
				return err
			}
			if !ok { // a concurrent upload took the path: lock it and treat ours as an overwrite
				continue
			}
			ref.ID = id
			out.created = true
		case rows[0].Kind != KindFile:
			return PathConflict("the path is a folder")
		case spec.CreateOnly:
			return CreateOnlyFailed()
		case bytes.Equal(rows[0].BlobSha256, sp.SHA256[:]):
			// Identical: nothing changes, so neither the hook nor an event; the
			// committer's own write still runs.
			ref.ID = rows[0].ID
			return s.extra(ctx, tx, spec, ref)
		default:
			if !spec.MayOverwrite {
				return ErrOverwriteDenied
			}
			if err := replaceFile(ctx, qs, rows[0].ID, p); err != nil {
				return err
			}
			ref.ID = rows[0].ID
			out.overwritten = true
		}
		if err := s.extra(ctx, tx, spec, ref); err != nil {
			return err
		}
		if err := s.hooks.OnCommit(ctx, tx, CommitEvent{
			Origin: spec.Origin, Repo: repo, Item: ref, Digests: sp.Digests, MediaType: spec.MediaType,
			UpstreamURL: spec.UpstreamURL, Payload: spec.Payload, Actor: p.actor, Replaced: out.overwritten,
		}); err != nil {
			return err
		}
		return s.recordCommitted(ctx, tx, repo.Key, spec, out.overwritten)
	}
	return errs.E(errs.Unavailable, errs.CodeUnavailable, "The path is being changed by concurrent requests; retry.", errs.WithRetryAfter(retryAfter))
}

func (s *Store) extra(ctx context.Context, tx meta.Tx, spec CommitSpec, ref Ref) error {
	if spec.Extra == nil {
		return nil
	}
	return spec.Extra(ctx, tx, ref)
}

type fileParams struct {
	rid                 int64
	path, parent, actor string
	spec                CommitSpec
}

func insertFile(ctx context.Context, qs *q.Queries, p fileParams) (id int64, ok bool, err error) {
	d := p.spec.Declared
	ids, err := qs.InsertFileItem(ctx, q.InsertFileItemParams{
		RepoID: p.rid, Path: p.path, Parent: p.parent, BlobSha256: p.spec.Spooled.SHA256[:], Size: p.spec.Spooled.Size,
		MediaType: p.spec.MediaType, DeclaredSha256: d.SHA256, DeclaredSha512: d.SHA512, DeclaredSha1: d.SHA1, DeclaredMd5: d.MD5,
		UpstreamUrl: p.spec.UpstreamURL, Actor: p.actor,
	})
	if err != nil || len(ids) != 1 {
		return 0, false, err
	}
	return ids[0], true, nil
}

func replaceFile(ctx context.Context, qs *q.Queries, id int64, p fileParams) error {
	d := p.spec.Declared
	_, err := qs.ReplaceFileContent(ctx, q.ReplaceFileContentParams{
		ID: id, BlobSha256: p.spec.Spooled.SHA256[:], Size: p.spec.Spooled.Size, MediaType: p.spec.MediaType,
		DeclaredSha256: d.SHA256, DeclaredSha512: d.SHA512, DeclaredSha1: d.SHA1, DeclaredMd5: d.MD5,
		UpstreamUrl: p.spec.UpstreamURL, Actor: p.actor,
	})
	return err
}

// ensureParents creates the missing ancestor folders of a path and refuses a path
// whose ancestor is a file (Q-GENERIC-3).
func ensureParents(ctx context.Context, qs *q.Queries, rid int64, actor, path string) error {
	chain := ancestors(path)
	if len(chain) == 0 {
		return nil
	}
	type folder struct {
		Path   string `json:"path"`
		Parent string `json:"parent"`
	}
	folders := make([]folder, len(chain))
	for i, a := range chain {
		folders[i] = folder{Path: a, Parent: ParentOf(a)}
	}
	raw, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	if _, err := qs.InsertImplicitFolders(ctx, q.InsertImplicitFoldersParams{RepoID: rid, Actor: actor, Folders: string(raw)}); err != nil {
		return err
	}
	kinds, err := qs.ListItemKinds(ctx, q.ListItemKindsParams{RepoID: rid, Paths: chain})
	if err != nil {
		return err
	}
	for _, k := range kinds {
		if k.Kind != KindFolder {
			return PathConflict("a file exists at " + k.Path)
		}
	}
	return nil
}

// lockAncestors holds every ancestor folder of the path FOR SHARE until the
// transaction ends, so that neither the removal of an empty implicit folder nor
// the removal of a folder with its subtree can take one from under the new item:
// they wait for this commit and then see its child. All of them, not the parent
// alone, because a delete of the subtree removes the ancestors that it empties
// and an ancestor that is gone would leave the item below missing folders
// (R-STOR-017). The locks are taken deepest first, the order of the delete. It
// reports false when one of them is gone, and the caller creates it again.
func lockAncestors(ctx context.Context, qs *q.Queries, rid int64, path string) (bool, error) {
	chain := ancestors(path)
	if len(chain) == 0 {
		return true, nil
	}
	rows, err := qs.LockFoldersShared(ctx, q.LockFoldersSharedParams{RepoID: rid, Paths: chain})
	return len(rows) == len(chain), err
}

// recordCommitted writes the audit row of a commit that changed a row. An upload
// is artifact.deployed; a fill has no event of its own yet (the cache event is
// webhook-only and the proxy raises it), so it writes none (Q-STOR-146).
func (s *Store) recordCommitted(ctx context.Context, tx meta.Tx, repoKey string, spec CommitSpec, overwritten bool) error {
	if spec.Origin != OriginUpload {
		return nil
	}
	sp := spec.Spooled
	raw, err := json.Marshal(map[string]any{"path": spec.Path.Path, "sha256": sp.SHA256.Hex(), "size": sp.Size, "overwritten": overwritten})
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventArtifactDeployed, Actor: spec.Actor, Outcome: audit.Success, Source: spec.Source, Endpoint: spec.Endpoint,
		Subject: audit.Subject{Kind: "repository", Name: repoKey}, Repository: repoKey, Path: spec.Path.Path, Details: raw,
	})
}

// FolderSpec is one explicit folder to create.
type FolderSpec struct {
	Repo repository.Repository
	// Path is the canonical path of the folder, not the root.
	Path string
	// CreateOnly refuses a folder that exists (If-None-Match: *): 412.
	CreateOnly bool
	Actor      audit.Actor
	// Extra runs in the transaction after the row was written; the generic layer
	// records the allowed write with it. An error rolls the folder back.
	Extra func(ctx context.Context, tx meta.Tx) error
}

// FolderResult is the outcome of [Store.CreateFolder].
type FolderResult struct {
	// Created is false for a folder that existed (it is marked explicit).
	Created bool
	Item    Item
}

// CreateFolder creates the explicit folder at a path with its missing ancestors,
// or marks an existing folder explicit (R-STOR-007). A file at the path or above
// it is a path conflict.
func (s *Store) CreateFolder(ctx context.Context, spec FolderSpec) (FolderResult, error) {
	if spec.Path == "" {
		return FolderResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath, "The repository root is neither a file nor a folder that can be created.")
	}
	repo := RepoOf(spec.Repo)
	path, parent, actor := spec.Path, ParentOf(spec.Path), actorID(spec.Actor)
	var created bool
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		created = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		for range maxLinkRounds {
			if err := ensureParents(ctx, qs, repo.ID, actor, path); err != nil {
				return err
			}
			if locked, err := lockAncestors(ctx, qs, repo.ID, path); err != nil {
				return err
			} else if !locked {
				continue
			}
			rows, err := qs.LockLiveItem(ctx, q.LockLiveItemParams{RepoID: repo.ID, Path: path})
			if err != nil {
				return err
			}
			switch {
			case len(rows) == 0:
				ids, err := qs.InsertExplicitFolder(ctx, q.InsertExplicitFolderParams{RepoID: repo.ID, Path: path, Parent: parent, Actor: actor})
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					continue
				}
				created = true
			case rows[0].Kind != KindFolder:
				return PathConflict("the path is a file")
			case spec.CreateOnly:
				return CreateOnlyFailed()
			default:
				if _, err := qs.MarkFolderExplicit(ctx, rows[0].ID); err != nil {
					return err
				}
			}
			if spec.Extra == nil {
				return nil
			}
			return spec.Extra(ctx, tx)
		}
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The path is being changed by concurrent requests; retry.", errs.WithRetryAfter(retryAfter))
	})
	if err != nil {
		return FolderResult{}, err
	}
	s.touch(repo.ID)
	it, found, err := s.Live(ctx, repo, path)
	if err != nil {
		return FolderResult{}, err
	}
	if !found {
		it = Item{Repository: repo.Key, Path: path, Kind: KindFolder, Explicit: true}
	}
	return FolderResult{Created: created, Item: it}, nil
}

// PathConflict is the 409 of a path that would be a file and a folder at once.
func PathConflict(why string) error {
	return errs.E(errs.Conflict, errs.CodePathConflict, "A path names a file or a folder, never both: "+why+".")
}

// CreateOnlyFailed is the 412 of a create-only request for a path that exists.
func CreateOnlyFailed() error {
	return errs.E(errs.PreconditionFailed, errs.CodePreconditionFailed, "The path exists and the request asked to create it only (If-None-Match: *).")
}
