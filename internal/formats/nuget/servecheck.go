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
	state, err := st.ServeState(ctx, repo.ID, info.Package, info.Version)
	if err != nil {
		return err
	}
	if state.Tombstoned || !state.Stored {
		return errNoSuchResource()
	}
	return nil
}
