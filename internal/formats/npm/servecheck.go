package npm

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item on any route: the npm tarball
// route, /files/, the cache view and the tiers of a virtual repository alike
// (R-NPM-003, R-NPM-015, R-NPM-026, R-NPM-047).
//
// For a local repository, an item of a version that was unpublished, moved out or
// withheld by a restore's replay is refused with the 404 of a missing resource: the
// item row says nothing of it, the tombstone does. A withheld version is stored and
// tombstoned, so this is the check that keeps its bytes from being served.
//
// For a remote repository, the stored marks decide and no upstream is asked (the route
// that may ask revalidated the document before, [remoteAdapter.BeforeArtifact]): a
// version the upstream's document stopped listing, and every version of a package the
// upstream withdrew, are refused unless the repository serves removed versions
// (R-NPM-026).
//
// Both read through the data port, one indexed lookup per served file, and keep no state
// of their own (R-ARCH-032): a mark written on any replica refuses the next request on
// every other, and a loss of Valkey's data changes nothing.
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	if repo.Class != formats.Local && repo.Class != formats.Remote {
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
	if repo.Class == formats.Remote {
		facts, err := st.TarballFacts(ctx, repo.ID, info.Package, info.Version)
		if err != nil {
			return err
		}
		return refuseRemoved(facts)
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
