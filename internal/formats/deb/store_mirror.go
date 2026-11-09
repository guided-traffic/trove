package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// MirrorStore is the part of the data port that the mirror's lister reads (R-DEB-024, Mirror discovery): the package names and
// files its parsed indexes list in the scope of a pass, and the step that brings the release documents and the indexes of the scope
// into the cache and into rows.
type MirrorStore interface {
	// MirrorSync is the index step of a pass (Q-DEB-91): for each suite of the scope it fetches the release document through the
	// pass's upstream, verifies it against the current keys and notes it when it is unverified or expired, fetches the index files
	// the scope keeps in every variant the release lists and parses the Packages and Sources indexes into rows in the pass. It
	// returns the suites whose release counts and the notes (suite_unavailable, release_unverified, release_expired, index_unlisted).
	MirrorSync(ctx context.Context, up discover.Upstream, scope rows.MirrorScope) (suites []string, notes []string, err error)
	// MirrorNames lists the distinct package names that the parsed indexes of the scope list, in byte order after the cursor, at
	// most limit.
	MirrorNames(ctx context.Context, scope rows.MirrorScope, after string, limit int) ([]string, error)
	// MirrorEntries lists the files the parsed indexes of the scope list for a package name: the plan of a pass.
	MirrorEntries(ctx context.Context, scope rows.MirrorScope, name string) ([]rows.Entry, error)
}
