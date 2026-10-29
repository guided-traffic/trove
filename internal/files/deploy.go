package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// DeployRequest is one PUT of /files/ (R-STOR-007).
type DeployRequest struct {
	Repo string
	// Path is the canonical path; Folder says the request ended in a slash and
	// creates an explicit folder.
	Path repository.Cleaned
	Body io.Reader
	// ContentLength is the declared length, -1 when unknown.
	ContentLength int64
	// MediaType is stored with the item (Content-Type); "" stores none.
	MediaType string
	// Expect are the digests the client announced (Repr-Digest).
	Expect blob.Expected
	// CreateOnly is If-None-Match: * (R-API-012): an existing path answers 412.
	CreateOnly bool
	// BeginCommit, when set, runs right before the commit protocol, once the body
	// has arrived: the server then lets the request finish past the shutdown
	// deadline (R-ARCH-024 step 5).
	BeginCommit func()
}

// DeployResult is the outcome of a deploy.
type DeployResult struct {
	// Created is true for 201, false for 200 (an identical re-upload, or an
	// existing folder).
	Created bool
	// Overwritten says that different content replaced an existing file.
	Overwritten bool
	Item        Item
}

// Deploy implements R-STOR-007, R-ARCH-014 and R-GENERIC-004: the coarse check
// before the body is read, the admission of the path, the spool and the single
// hashing pass, the fine check (`overwrite` when different content replaces a
// file) before anything reaches the object store, and the commit protocol of
// R-STOR-005 with the item rows, the event and the audit of the allowed write in
// one transaction.
func (s *Service) Deploy(ctx context.Context, c Caller, req DeployRequest) (DeployResult, error) {
	if req.Path.Path == "" {
		return DeployResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath, "The repository root is neither a file nor a folder that can be created.")
	}
	dec, err := s.decide(ctx, c, req.Repo, req.Path.Path, authz.KindWrite, authz.Deploy)
	if err != nil {
		return DeployResult{}, err
	}
	repo, err := s.resolve(ctx, req.Repo, true)
	if err != nil {
		return DeployResult{}, err
	}
	if repo.Class != repository.Local {
		return DeployResult{}, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
			"A "+string(repo.Class)+" repository does not accept deploys.", errs.WithAllow("GET", "HEAD"))
	}
	if !repo.Admits(req.Path.Path, req.Path.Folder) {
		return DeployResult{}, errs.E(errs.Forbidden, errs.CodeNotAdmitted, "The path is not admitted by the include and exclude patterns of the repository.")
	}
	var res DeployResult
	if req.Path.Folder {
		res, err = s.deployFolder(ctx, c, repo, req, dec)
	} else {
		res, err = s.deployFile(ctx, c, repo, req, dec)
	}
	if err != nil {
		return DeployResult{}, err
	}
	if s.activity != nil {
		s.activity.Touch(repo.ID)
	}
	return res, nil
}

// maxUpload reads the repository's own upload limit, config.storage.maxUploadSize
// in bytes (R-STOR-004 admission); zero means none. A value that is not a
// non-negative integer cannot be a limit someone meant to lift: the repository
// refuses the upload (fails closed) instead of reading it as "none". The
// management API refuses to store such a value; this guards a document that
// predates the rule or was written around it.
func maxUpload(repo repository.Repository) (int64, error) {
	var doc struct {
		Storage struct {
			MaxUploadSize json.RawMessage `json:"maxUploadSize"`
		} `json:"storage"`
	}
	if len(repo.Config) == 0 {
		return 0, nil
	}
	if err := json.Unmarshal(repo.Config, &doc); err != nil {
		return 0, invalidUploadLimit()
	}
	raw := bytes.TrimSpace(doc.Storage.MaxUploadSize)
	if len(raw) == 0 {
		return 0, nil
	}
	n, err := strconv.ParseUint(string(raw), 10, 63)
	if err != nil {
		return 0, invalidUploadLimit()
	}
	return int64(n), nil
}

func invalidUploadLimit() error {
	return errs.E(errs.Internal, errs.CodeInternal, "The repository's upload limit (config.storage.maxUploadSize) is not a non-negative integer; uploads are refused until it is corrected.")
}

func (s *Service) deployFile(ctx context.Context, c Caller, repo repository.Repository, req DeployRequest, dec authz.Decision) (DeployResult, error) {
	limit, err := maxUpload(repo)
	if err != nil {
		return DeployResult{}, err
	}
	sp, err := s.blobs.Ingest(ctx, req.Body, blob.IngestOptions{
		ContentLength: req.ContentLength, MaxSize: limit, Expect: req.Expect,
	})
	if err != nil {
		return DeployResult{}, err
	}
	defer sp.Close()

	rid, path := repoID(repo), req.Path.Path
	// The fine check runs before the object is written (R-ARCH-014 step 5): an
	// existing item with other content needs `overwrite` as well.
	existing, found, err := s.liveItem(ctx, req.Repo, rid, path)
	if err != nil {
		return DeployResult{}, err
	}
	overwriteChecked := false
	if found {
		switch {
		case existing.Kind != KindFile:
			return DeployResult{}, pathConflict("the path is a folder")
		case req.CreateOnly:
			return DeployResult{}, createOnlyFailed()
		case existing.Digest != sp.SHA256:
			if dec, err = s.decide(ctx, c, req.Repo, path, authz.KindWrite, authz.Deploy, authz.Overwrite); err != nil {
				return DeployResult{}, err
			}
			overwriteChecked = true
		}
	}
	if !overwriteChecked {
		// The commit-time re-check of R-AUTHZ-015: the body may have taken minutes,
		// and a grant removed meanwhile stops the write, new file or identical
		// re-upload alike. It runs before the commit transaction opens, because a
		// stale index makes a decision wait (R-ARCH-019).
		if dec, err = s.decide(ctx, c, req.Repo, path, authz.KindWrite, authz.Deploy); err != nil {
			return DeployResult{}, err
		}
	}
	// A concurrent upload may create the path between the check above and the
	// link step, which then replaces content. Whether `overwrite` is held is
	// settled here, outside the transaction, so no decision waits for the index
	// inside it while it holds the blob row and the item (R-STOR-007, R-ARCH-019).
	overwriteOK := overwriteChecked
	if !overwriteOK {
		if overwriteOK, err = s.mayOverwrite(ctx, c, req.Repo, path); err != nil {
			return DeployResult{}, err
		}
	}

	if req.BeginCommit != nil {
		req.BeginCommit()
	}
	var out linkOutcome
	_, err = s.blobs.Commit(ctx, sp, func(ctx context.Context, tx meta.Tx) error {
		out = linkOutcome{}
		return s.linkFile(ctx, tx, c, repo, req, sp, dec, overwriteOK, &out)
	})
	if errors.Is(err, errOverwriteDenied) {
		// The path appeared meanwhile and the caller holds `deploy` only: the
		// refusal is decided, and audited, as any other.
		if _, err = s.decide(ctx, c, req.Repo, path, authz.KindWrite, authz.Deploy, authz.Overwrite); err == nil {
			err = errForbidden(authz.Overwrite, req.Repo, path)
		}
	}
	if err != nil {
		return DeployResult{}, err
	}
	it, found, err := s.liveItem(ctx, req.Repo, rid, path)
	if err != nil {
		return DeployResult{}, err
	}
	if !found { // removed again between the commit and this read
		it = Item{Repository: req.Repo, Path: path, Kind: KindFile, Size: sp.Size, Digest: sp.SHA256, MediaType: req.MediaType}
	}
	return DeployResult{Created: out.created || out.overwritten, Overwritten: out.overwritten, Item: it}, nil
}

// errOverwriteDenied is returned by the link step when it finds content to
// replace and the caller was not found to hold `overwrite` before the commit.
var errOverwriteDenied = errors.New("files: overwrite not granted")

// mayOverwrite says whether the caller holds `overwrite` on the path, without an
// audit event: it is asked in case a concurrent upload makes the deploy an
// overwrite, which usually does not happen. The decision point's PathsAllowed
// is the non-auditing evaluation; a decision point without it is asked with
// Decide, and a refusal then is read as "not held".
func (s *Service) mayOverwrite(ctx context.Context, c Caller, repo, path string) (bool, error) {
	if s.paths != nil {
		ok, err := s.paths.PathsAllowed(ctx, c.Subject, s.coordinate(c, repo, path, authz.KindWrite), authz.Overwrite, []string{path})
		if err != nil {
			return false, err
		}
		return len(ok) == 1 && ok[0], nil
	}
	d := s.authz.Decide(ctx, c.Subject, s.coordinate(c, repo, path, authz.KindWrite, authz.Deploy, authz.Overwrite))
	return d.Allowed(), nil
}

// linkOutcome is what the link step of a file deploy did.
type linkOutcome struct {
	created, overwritten bool
}

// maxLinkRounds bounds the retries of the link step when concurrent writers
// change the path or its parent between two statements.
const maxLinkRounds = 5

// linkFile is the link function of the commit protocol: in the transaction that
// holds the blob row's lock it creates the missing parent folders, serialises on
// the path, and inserts or replaces the file item, writes the event and records
// the allowed write. It may run again (a restarted round), so it resets its
// outcome.
func (s *Service) linkFile(ctx context.Context, tx meta.Tx, c Caller, repo repository.Repository, req DeployRequest,
	sp *blob.Spooled, dec authz.Decision, overwriteOK bool, out *linkOutcome,
) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	rid, path, parent := repoID(repo), req.Path.Path, parentOf(req.Path.Path)
	for range maxLinkRounds {
		if err := s.ensureParents(ctx, qs, rid, c, path); err != nil {
			return err
		}
		locked, err := lockAncestors(ctx, qs, rid, path)
		if err != nil {
			return err
		}
		if !locked { // an ancestor was removed by a concurrent delete; create it again
			continue
		}
		rows, err := qs.LockLiveItem(ctx, q.LockLiveItemParams{RepoID: rid, Path: path})
		if err != nil {
			return err
		}
		params := fileParams{rid: rid, path: path, parent: parent, sp: sp, req: req, actor: c.actorID()}
		switch {
		case len(rows) == 0:
			ok, err := s.insertFile(ctx, qs, params)
			if err != nil {
				return err
			}
			if !ok { // a concurrent upload took the path: lock it and treat ours as an overwrite
				continue
			}
			out.created = true
		case rows[0].Kind != KindFile:
			return pathConflict("the path is a folder")
		case req.CreateOnly:
			return createOnlyFailed()
		case bytes.Equal(rows[0].BlobSha256, sp.SHA256[:]):
			return s.recordAllowed(ctx, tx, c, req.Repo, path, dec, authz.Deploy) // identical: nothing changes
		default:
			if !overwriteOK {
				return errOverwriteDenied
			}
			if err := s.replaceFile(ctx, qs, rows[0].ID, params); err != nil {
				return err
			}
			out.overwritten = true
		}
		if err := s.recordAllowed(ctx, tx, c, req.Repo, path, dec, authz.Deploy); err != nil {
			return err
		}
		return s.recordDeployed(ctx, tx, c, req.Repo, path, sp, out.overwritten)
	}
	return errs.E(errs.Unavailable, errs.CodeUnavailable, "The path is being changed by concurrent requests; retry.", errs.WithRetryAfter(retryAfter))
}

type fileParams struct {
	rid                 int64
	path, parent, actor string
	sp                  *blob.Spooled
	req                 DeployRequest
}

func (p fileParams) declared() (sha256, sha512, sha1, md5 []byte) {
	e := p.req.Expect
	return e.SHA256, e.SHA512, e.SHA1, e.MD5
}

func (s *Service) insertFile(ctx context.Context, qs *q.Queries, p fileParams) (bool, error) {
	d256, d512, d1, dmd5 := p.declared()
	ids, err := qs.InsertFileItem(ctx, q.InsertFileItemParams{
		RepoID: p.rid, Path: p.path, Parent: p.parent, BlobSha256: p.sp.SHA256[:], Size: p.sp.Size,
		MediaType: p.req.MediaType, DeclaredSha256: d256, DeclaredSha512: d512, DeclaredSha1: d1, DeclaredMd5: dmd5, Actor: p.actor,
	})
	return len(ids) == 1, err
}

func (s *Service) replaceFile(ctx context.Context, qs *q.Queries, id int64, p fileParams) error {
	d256, d512, d1, dmd5 := p.declared()
	_, err := qs.ReplaceFileContent(ctx, q.ReplaceFileContentParams{
		ID: id, BlobSha256: p.sp.SHA256[:], Size: p.sp.Size, MediaType: p.req.MediaType,
		DeclaredSha256: d256, DeclaredSha512: d512, DeclaredSha1: d1, DeclaredMd5: dmd5, Actor: p.actor,
	})
	return err
}

// ensureParents creates the missing ancestor folders of a path and refuses a path
// whose ancestor is a file (Q-GENERIC-3).
func (s *Service) ensureParents(ctx context.Context, qs *q.Queries, rid int64, c Caller, path string) error {
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
		folders[i] = folder{Path: a, Parent: parentOf(a)}
	}
	raw, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	if _, err := qs.InsertImplicitFolders(ctx, q.InsertImplicitFoldersParams{RepoID: rid, Actor: c.actorID(), Folders: string(raw)}); err != nil {
		return err
	}
	kinds, err := qs.ListItemKinds(ctx, q.ListItemKindsParams{RepoID: rid, Paths: chain})
	if err != nil {
		return err
	}
	for _, k := range kinds {
		if k.Kind != KindFolder {
			return pathConflict("a file exists at " + k.Path)
		}
	}
	return nil
}

// lockAncestors holds every ancestor folder of the path FOR SHARE until the
// transaction ends, so that neither the removal of an empty implicit folder nor
// the removal of a folder with its subtree can take one from under the new item:
// they wait for this deploy and then see its child. All of them, not the parent
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

func (s *Service) recordDeployed(ctx context.Context, tx meta.Tx, c Caller, repo, path string, sp *blob.Spooled, overwritten bool) error {
	raw, err := json.Marshal(map[string]any{"path": path, "sha256": sp.SHA256.Hex(), "size": sp.Size, "overwritten": overwritten})
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventArtifactDeployed, Actor: c.Actor, Outcome: audit.Success, Source: c.Source, Endpoint: c.Endpoint,
		Subject: audit.Subject{Kind: "repository", Name: repo}, Repository: repo, Path: path, Details: raw,
	})
}

func (s *Service) deployFolder(ctx context.Context, c Caller, repo repository.Repository, req DeployRequest, dec authz.Decision) (DeployResult, error) {
	if req.ContentLength != 0 {
		var one [1]byte
		if n, err := io.ReadFull(req.Body, one[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			return DeployResult{}, errs.E(errs.Invalid, errs.CodeInvalid, "A folder is created with an empty body.")
		}
	}
	rid, path, parent := repoID(repo), req.Path.Path, parentOf(req.Path.Path)
	var created bool
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		created = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		for range maxLinkRounds {
			if err := s.ensureParents(ctx, qs, rid, c, path); err != nil {
				return err
			}
			if locked, err := lockAncestors(ctx, qs, rid, path); err != nil {
				return err
			} else if !locked {
				continue
			}
			rows, err := qs.LockLiveItem(ctx, q.LockLiveItemParams{RepoID: rid, Path: path})
			if err != nil {
				return err
			}
			switch {
			case len(rows) == 0:
				ids, err := qs.InsertExplicitFolder(ctx, q.InsertExplicitFolderParams{RepoID: rid, Path: path, Parent: parent, Actor: c.actorID()})
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					continue
				}
				created = true
			case rows[0].Kind != KindFolder:
				return pathConflict("the path is a file")
			case req.CreateOnly:
				return createOnlyFailed()
			default:
				if _, err := qs.MarkFolderExplicit(ctx, rows[0].ID); err != nil {
					return err
				}
			}
			return s.recordAllowed(ctx, tx, c, req.Repo, path, dec, authz.Deploy)
		}
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The path is being changed by concurrent requests; retry.", errs.WithRetryAfter(retryAfter))
	})
	if err != nil {
		return DeployResult{}, err
	}
	it, found, err := s.liveItem(ctx, req.Repo, rid, path)
	if err != nil {
		return DeployResult{}, err
	}
	if !found {
		it = Item{Repository: req.Repo, Path: path, Kind: KindFolder, Explicit: true}
	}
	return DeployResult{Created: created, Item: it}, nil
}

func pathConflict(why string) error {
	return errs.E(errs.Conflict, errs.CodePathConflict, "A path names a file or a folder, never both: "+why+".")
}

func createOnlyFailed() error {
	return errs.E(errs.PreconditionFailed, errs.CodePreconditionFailed, "The path exists and the request asked to create it only (If-None-Match: *).")
}
