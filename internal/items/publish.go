package items

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// GeneratedFile is one file of a generation that a format's index build publishes (ADR-170, Q-STOR-182): the file becomes a
// package-less generated item at Path, in a local or a virtual repository.
type GeneratedFile struct {
	Path repository.Cleaned
	// Body is read once; the port spools and hashes it.
	Body      io.Reader
	Size      int64
	MediaType string
	// ModifiedAt is the Release Date for every file of a generation, so that two generations never share a Last-Modified and a
	// client's If-Modified-Since never gets a stale 304 (R-DEB-010).
	ModifiedAt time.Time
}

// Publisher is the publication port of the item store (Q-STOR-182, ADR-170): the one way generated items are written. It is
// implemented for both classes by the work package of the Debian index builder; a virtual repository accepts generated items from
// this port and from nothing else.
type Publisher interface {
	// Publish stores every file's blob first, outside any transaction (R-STOR-005), then runs one transaction that upserts a
	// package-less generated item per file (an identical blob changes nothing but modified_at), deletes the generated items at
	// remove, and runs commit with the transaction for the caller's own rows. commit returning [ErrSuperseded] rolls everything
	// back and Publish returns it; the stored blobs are left to garbage collection. Quota admission does not apply (Q-STOR-181).
	Publish(ctx context.Context, repo RepoRef, files []GeneratedFile, remove []repository.Cleaned,
		commit func(ctx context.Context, tx meta.Tx) error) error
}

// ErrSuperseded is what the commit function of [Publisher.Publish] returns when a newer build has published already: the
// publication rolls back and returns it.
var ErrSuperseded = errors.New("items: superseded")

// Publish implements [Publisher]: not built yet. The work package of the Debian index builder replaces it (apt-work-packages.md,
// WP4); until then no format publishes generated items, so nothing calls it.
func (s *Store) Publish(context.Context, RepoRef, []GeneratedFile, []repository.Cleaned, func(context.Context, meta.Tx) error) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The publication of generated items is not built yet.")
}

var _ Publisher = (*Store)(nil)
