package files

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// PathClass is what a format makes of a path of one of its repositories
// (R-REPO-003): how the request behaves and which package it belongs to. The
// generic layer gives a /files/ request this coordinate (R-EP-014).
type PathClass struct {
	Kind    authz.RequestKind
	Package string
	Version string
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
}

// MaxUpload reads the repository's own upload limit, config.storage.maxUploadSize
// in bytes (R-STOR-004 admission); zero means none. A format's upload is spooled
// under the same limit.
func MaxUpload(repo repository.Repository) (int64, error) { return maxUpload(repo) }
