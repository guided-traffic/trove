package files

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// PathClass is what a format makes of a path of one of its repositories
// (R-REPO-003): how the request behaves and which package it belongs to. The
// generic layer decides a request on this coordinate (R-EP-014: package name and
// version for npm, the path alone for generic) and applies the repository's
// patterns to what they match for the format (R-REPO-005).
type PathClass struct {
	Kind    authz.RequestKind
	Package string
	Version string
	// ByName says the format's include and exclude patterns match package names
	// (its pattern subject): they are applied to Package, and a path the format
	// places in no package is package-less and admitted, as a protocol request
	// without a name is.
	ByName bool
}

// Removal is a removal of files a format is asked about (Formats.CheckRemoval):
// the files of one repository, and the index and the subject the request decided
// on, so that the format's rules are decided on what the request's actions were
// (R-AUTHZ-015).
type Removal struct {
	Repo repository.Repository
	// Files are the canonical paths of the files the removal takes away.
	Files   []string
	Index   *authz.Index
	Subject authz.Subject
}

// Formats is the seam to the formats from the generic layer: /files/ is the raw
// view of every repository, npm's included, so a format keeps a say about its own
// items there. The composition root injects an implementation over the format
// registry (internal/formathost.FilesSeam); a layer without one treats every
// repository as the generic format.
//
// The generic layer is a domain package and cannot import the edge-layer format
// contract; the seam is the interface it needs of it (Q-ARCH-206).
type Formats interface {
	// Classify classifies a canonical path of a repository.
	Classify(repo repository.Repository, p repository.Cleaned) PathClass
	// ServeCheck runs before a stored file is served: errs.NotFound refuses it
	// because of format state the item row does not carry (R-ARCH-008 "Serve-time
	// check"). It reads the stored marks only and never contacts an upstream.
	ServeCheck(ctx context.Context, repo repository.Repository, it items.Item) error
	// RawDeploy says whether /files/ deploys a file or a folder into a repository
	// of the format: nil for a format whose protocol is /files/ itself (the generic
	// format), else the refusal, 409 with the format's own way in (R-STOR-007,
	// R-API-025, Q-ARCH-223). It is asked before the body is read.
	RawDeploy(repo repository.Repository) error
	// CheckRemoval runs in the transaction that removes files of a repository, or
	// that queues the job removing them, before anything is removed: the format's
	// rules for removing its content, the ownership of R-AUTHZ-018 for the packages
	// the files belong to. An error stops the removal with nothing changed
	// (R-STOR-018, Q-AUTHZ-161).
	CheckRemoval(ctx context.Context, tx meta.Tx, rm Removal) error
}

// MaxUpload reads the repository's own upload limit, config.storage.maxUploadSize
// in bytes (R-STOR-004 admission); zero means none. A format's upload is spooled
// under the same limit.
func MaxUpload(repo repository.Repository) (int64, error) { return maxUpload(repo) }

// Classified completes a coordinate that names a canonical path of a repository
// with the package and the version the repository's format gives the path
// (R-EP-014), for a decision made before this layer is reached: the shared
// authorization step of the items operations of the management API, which
// decides on the path its declaration names. A coordinate that names a package
// already or names no path is returned as it is, and so is one whose repository
// the replica's snapshot does not hold, which the decision refuses. The caller
// checks that the path is canonical first.
func (s *Service) Classified(ctx context.Context, c authz.Coordinate) authz.Coordinate {
	if s.formats == nil || c.Path == "" || c.Package != "" {
		return c
	}
	repo, err := s.repos.ByKey(ctx, c.Repository)
	if err != nil {
		return c
	}
	pc := s.classify(repo, c.Path, false)
	c.Package, c.Version = pc.Package, pc.Version
	return c
}

// classify is what the repository's format makes of a path; the zero class (the
// generic format's) without a seam.
func (s *Service) classify(repo repository.Repository, path string, folder bool) PathClass {
	if s.formats == nil {
		return PathClass{}
	}
	return s.formats.Classify(repo, repository.Cleaned{Path: path, Folder: folder})
}

// classifier is the classification of the repository's paths as the decision
// point takes it for the entries of a listing and the items of a subtree
// (R-EP-014); nil without a seam.
func (s *Service) classifier(repo repository.Repository) authz.Classifier {
	if s.formats == nil {
		return nil
	}
	return func(path string) (string, string) {
		c := s.formats.Classify(repo, repository.Cleaned{Path: path})
		return c.Package, c.Version
	}
}

// admits applies the repository's include and exclude patterns (R-REPO-005) to
// what they match for its format: the package name the format gives the path when
// the patterns match names, else the path.
func admits(repo repository.Repository, path string, folder bool, c PathClass) bool {
	if c.ByName {
		return c.Package == "" || repo.Admits(c.Package, false)
	}
	return repo.Admits(path, folder)
}

// rawDeploy is the format's answer to a deploy through /files/; every deploy is
// accepted without a seam.
func (s *Service) rawDeploy(repo repository.Repository) error {
	if s.formats == nil {
		return nil
	}
	return s.formats.RawDeploy(repo)
}

// checkRemoval asks the format about the files a delete removes, in its
// transaction.
func (s *Service) checkRemoval(ctx context.Context, tx meta.Tx, c Caller, repo repository.Repository, files []string) error {
	if s.formats == nil || len(files) == 0 {
		return nil
	}
	return s.formats.CheckRemoval(ctx, tx, Removal{Repo: repo, Files: files, Index: c.Access.Index(), Subject: c.Subject})
}
