package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
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
	Expect items.Expected
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
	Item        items.Item
}

// Deploy implements R-STOR-007, R-ARCH-014 and R-GENERIC-004: the coarse check
// before the body is read, the format's answer to a raw deploy (a format with a
// protocol of its own takes nothing through /files/, Q-ARCH-223), the admission of
// the path, the spool and the single hashing pass, the fine check (`overwrite`
// when different content replaces a file) before anything reaches the object
// store, and the commit of the item store (R-STOR-005) with the item rows, the
// event and the audit of the allowed write in one transaction.
func (s *Service) Deploy(ctx context.Context, c Caller, req DeployRequest) (DeployResult, error) {
	if req.Path.Path == "" {
		return DeployResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath, "The repository root is neither a file nor a folder that can be created.")
	}
	t := s.locate(ctx, req.Repo, req.Path.Path, req.Path.Folder)
	dec, err := c.decide(ctx, req.Repo, req.Path.Path, t.class, authz.KindWrite, authz.Deploy)
	if err != nil {
		return DeployResult{}, err
	}
	repo, err := t.resolved(true)
	if err != nil {
		return DeployResult{}, err
	}
	if repo.Class != repository.Local {
		return DeployResult{}, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
			"A "+string(repo.Class)+" repository does not accept deploys.", errs.WithAllow("GET", "HEAD"))
	}
	// Before the body is read, so a refused upload spools nothing and sends no
	// 100 Continue; a folder is refused as a file is, because an empty folder at a
	// path the format writes would block its next write there.
	if err := s.rawDeploy(repo); err != nil {
		return DeployResult{}, err
	}
	if !admits(repo, req.Path.Path, req.Path.Folder, t.class) {
		return DeployResult{}, errs.E(errs.Forbidden, errs.CodeNotAdmitted, "The path is not admitted by the include and exclude patterns of the repository.")
	}
	if req.Path.Folder {
		return s.deployFolder(ctx, c, repo, req, t.class, dec)
	}
	return s.deployFile(ctx, c, repo, req, t.class, dec)
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

func (s *Service) deployFile(ctx context.Context, c Caller, repo repository.Repository, req DeployRequest, pc PathClass, dec authz.Decision) (DeployResult, error) {
	limit, err := maxUpload(repo)
	if err != nil {
		return DeployResult{}, err
	}
	sp, err := s.items.Spool(ctx, req.Body, items.IngestOptions{ContentLength: req.ContentLength, MaxSize: limit, Expect: req.Expect})
	if err != nil {
		return DeployResult{}, err
	}
	defer sp.Close()
	// The body may have taken minutes: what follows is decided on the index of
	// this moment (R-AUTHZ-015), not on the one the request began with.
	if c, err = c.afterBody(ctx, req.Repo); err != nil {
		return DeployResult{}, err
	}

	ref, path := items.RepoOf(repo), req.Path.Path
	// The fine check runs before the object is written (R-ARCH-014 step 5): an
	// existing item with other content needs `overwrite` as well.
	existing, found, err := s.items.Live(ctx, ref, path)
	if err != nil {
		return DeployResult{}, err
	}
	overwriteChecked := false
	if found {
		switch {
		case existing.Kind != items.KindFile:
			return DeployResult{}, items.PathConflict("the path is a folder")
		case req.CreateOnly:
			return DeployResult{}, items.CreateOnlyFailed()
		case existing.Digest != sp.SHA256:
			if dec, err = c.decide(ctx, req.Repo, path, pc, authz.KindWrite, authz.Deploy, authz.Overwrite); err != nil {
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
		if dec, err = c.decide(ctx, req.Repo, path, pc, authz.KindWrite, authz.Deploy); err != nil {
			return DeployResult{}, err
		}
	}
	// A concurrent upload may create the path between the check above and the
	// link step, which then replaces content. Whether `overwrite` is held is
	// settled here, outside the transaction, so no decision waits for the index
	// inside it while it holds the blob row and the item (R-STOR-007, R-ARCH-019).
	overwriteOK := overwriteChecked
	if !overwriteOK {
		if overwriteOK, err = c.mayOverwrite(ctx, req.Repo, path, pc); err != nil {
			return DeployResult{}, err
		}
	}

	res, err := s.items.Commit(ctx, items.CommitSpec{
		Repo: repo, Path: req.Path, Spooled: sp, MediaType: req.MediaType, Declared: req.Expect,
		Origin: items.OriginUpload, CreateOnly: req.CreateOnly, MayOverwrite: overwriteOK,
		Actor: c.Actor, Source: c.Source, Endpoint: c.Subject.Endpoint,
		Extra: func(ctx context.Context, tx meta.Tx, _ items.Ref) error {
			return c.Access.RecordAllowed(ctx, tx, coordinate(req.Repo, path, pc, authz.KindWrite, authz.Deploy), dec)
		},
		BeginCommit: req.BeginCommit,
	})
	if errors.Is(err, items.ErrOverwriteDenied) {
		// The path appeared meanwhile and the caller holds `deploy` only: the
		// refusal is decided, and audited, as any other.
		if _, err = c.decide(ctx, req.Repo, path, pc, authz.KindWrite, authz.Deploy, authz.Overwrite); err == nil {
			err = errForbidden(authz.Overwrite, req.Repo, path)
		}
	}
	if err != nil {
		return DeployResult{}, err
	}
	return DeployResult{Created: res.Created, Overwritten: res.Overwritten, Item: res.Item}, nil
}

// mayOverwrite says whether the caller holds `overwrite` on the path, without an
// audit event: it is asked in case a concurrent upload makes the deploy an
// overwrite, which usually does not happen. PathsAllowed is the non-auditing
// evaluation.
func (c Caller) mayOverwrite(ctx context.Context, repo, path string, pc PathClass) (bool, error) {
	same := func(string) (string, string) { return pc.Package, pc.Version }
	ok, err := c.Access.PathsAllowed(ctx, coordinate(repo, path, pc, authz.KindWrite), authz.Overwrite, []string{path}, same)
	if err != nil {
		return false, err
	}
	return len(ok) == 1 && ok[0], nil
}

func (s *Service) deployFolder(ctx context.Context, c Caller, repo repository.Repository, req DeployRequest, pc PathClass, dec authz.Decision) (DeployResult, error) {
	if req.ContentLength != 0 {
		var one [1]byte
		if n, err := io.ReadFull(req.Body, one[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			return DeployResult{}, errs.E(errs.Invalid, errs.CodeInvalid, "A folder is created with an empty body.")
		}
	}
	res, err := s.items.CreateFolder(ctx, items.FolderSpec{
		Repo: repo, Path: req.Path.Path, CreateOnly: req.CreateOnly, Actor: c.Actor,
		Extra: c.recorder(req.Repo, req.Path.Path, pc, dec, authz.Deploy),
	})
	if err != nil {
		return DeployResult{}, err
	}
	return DeployResult{Created: res.Created, Item: res.Item}, nil
}
