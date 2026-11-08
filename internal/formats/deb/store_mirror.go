package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// MirrorStore is the part of the data port that the mirror's lister reads (R-DEB-024, Mirror discovery): the package names and
// files its parsed indexes list in the scope of a pass. Built by the work package of the mirror.
type MirrorStore interface {
	// MirrorNames lists the distinct package names that the parsed indexes of the scope list, in byte order after the cursor, at
	// most limit.
	MirrorNames(ctx context.Context, scope rows.MirrorScope, after string, limit int) ([]string, error)
	// MirrorEntries lists the files the parsed indexes of the scope list for a package name: the plan of a pass.
	MirrorEntries(ctx context.Context, scope rows.MirrorScope, name string) ([]rows.Entry, error)
}
