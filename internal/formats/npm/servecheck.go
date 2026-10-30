package npm

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item of a local repository on any
// route: the npm tarball route and /files/ alike (R-NPM-003, R-NPM-015, R-NPM-047).
// An item of a version that was unpublished, moved out or withheld by a restore's
// replay is refused with the 404 of a missing resource: the item row says nothing of
// it, the tombstone does. A withheld version is stored and tombstoned, so this is the
// check that keeps its bytes from being served.
//
// It reads the tombstone through the data port, one indexed lookup per served file,
// and keeps no state of its own (R-ARCH-032): a tombstone written on any replica
// refuses the next request on every other.
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	if repo.Class != formats.Local {
		return nil
	}
	info := f.Classify(formats.Path{Path: it.Path})
	if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
		return nil
	}
	st, err := storeOf(h.FormatData(ID))
	if err != nil {
		return err
	}
	gone, err := st.Tombstoned(ctx, repo.ID, info.Package, info.Version)
	if err != nil {
		return err
	}
	if gone {
		return notFound()
	}
	return nil
}
