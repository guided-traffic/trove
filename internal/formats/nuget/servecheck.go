package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item on any route: the flat-container
// routes, /files/ and the cache view alike (R-NUGET-007, R-NUGET-008).
//
// For a file of a version of a local repository it decides on the NuGet rows, which the
// item row does not carry: a listed version is served, an unlisted one is served as well
// (unlisting hides a version from search and leaves it restorable, R-NUGET-007), and one
// that is tombstoned, or that the repository holds no NuGet row for, is refused with the
// 404 of a missing resource. A version without its row can only be a file that reached the
// repository around the format, and fails closed.
//
// It reads through the data port, one indexed lookup per served file, and keeps no state
// of its own (R-ARCH-032): a removal that committed on any replica refuses the next
// request on every other, and a loss of Valkey's data changes nothing.
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	key := f.ServeFactsKey(repo, formats.Path{Path: it.Path})
	if key.Zero() {
		return nil
	}
	// The item lookup of the download read these rows with the item when the route asked for them (R-ARCH-027).
	if it.Facts.For(key) {
		if it.Facts.Tombstoned || !it.Facts.Stored {
			return errNoSuchResource()
		}
		return nil
	}
	st, err := storeOf(h.FormatData(ID))
	if err != nil {
		return err
	}
	state, err := st.ServeState(ctx, repo.ID, key.Package, key.Version)
	if err != nil {
		return err
	}
	if state.Tombstoned || !state.Stored {
		return errNoSuchResource()
	}
	return nil
}

// ServeFactsKey implements [formats.ServeFactsKeyer]: the rows ServeCheck decides on for a path of a local repository, which the
// item lookup of the download reads with the item, so that the check costs no statement of its own (R-ARCH-027). The zero key
// is a path ServeCheck lets through, and a class it does not check.
func (f *Format) ServeFactsKey(repo formats.RepoRef, p formats.Path) formats.ServeKey {
	if repo.Class != formats.Local {
		return formats.ServeKey{}
	}
	info := f.Classify(p)
	if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
		return formats.ServeKey{}
	}
	return formats.ServeKey{Kind: formats.ServeNuGetLocal, Package: info.Package, Version: info.Version}
}

var _ formats.ServeFactsKeyer = (*Format)(nil)
