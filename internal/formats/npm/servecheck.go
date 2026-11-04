package npm

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
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
	key := f.ServeFactsKey(repo, formats.Path{Path: it.Path})
	if key.Zero() {
		return nil
	}
	// The item lookup of the download read these rows with the item when the route asked for them (R-ARCH-027).
	have := it.Facts.For(key)
	if repo.Class == formats.Remote {
		if have {
			return refuseRemoved(tarballFactsOf(it.Facts))
		}
		st, err := storeOf(h.FormatData(ID))
		if err != nil {
			return err
		}
		facts, err := st.TarballFacts(ctx, repo.ID, key.Package, key.Version)
		if err != nil {
			return err
		}
		return refuseRemoved(facts)
	}
	gone := it.Facts.Tombstoned
	if !have {
		st, err := storeOf(h.FormatData(ID))
		if err != nil {
			return err
		}
		if gone, err = st.Tombstoned(ctx, repo.ID, key.Package, key.Version); err != nil {
			return err
		}
	}
	if gone {
		return notFound()
	}
	return nil
}

// ServeFactsKey implements [formats.ServeFactsKeyer]: the rows ServeCheck decides on for a path of a repository, which the item
// lookup of the download reads with the item, so that the check costs no statement of its own (R-ARCH-027). The zero key is a
// path ServeCheck lets through, and a class it does not check.
func (f *Format) ServeFactsKey(repo formats.RepoRef, p formats.Path) formats.ServeKey {
	var kind formats.ServeKind
	switch repo.Class {
	case formats.Local:
		kind = formats.ServeNPMLocal
	case formats.Remote:
		kind = formats.ServeNPMRemote
	default:
		return formats.ServeKey{}
	}
	info := f.Classify(p)
	if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
		return formats.ServeKey{}
	}
	return formats.ServeKey{Kind: kind, Package: info.Package, Version: info.Version}
}

var _ formats.ServeFactsKeyer = (*Format)(nil)

// tarballFactsOf is the npm remote part of facts as the rows of the data port spell it.
func tarballFactsOf(f formats.ServeFacts) rows.TarballFacts {
	n := f.NPMRemote
	out := rows.TarballFacts{
		Known: n.Known, Listed: n.Listed, Integrity: n.Integrity, Shasum: n.Shasum,
		DocStored: n.DocStored, DocCurrent: n.DocCurrent, ServeRemoved: n.ServeRemoved,
	}
	if n.DocGoneUs != 0 {
		out.DocGone = time.UnixMicro(n.DocGoneUs).UTC()
	}
	return out
}

// serveFactsOf is the facts a read of the data port stands for, under the key it was read for.
func serveFactsOf(key formats.ServeKey, t rows.TarballFacts) formats.ServeFacts {
	out := formats.ServeFacts{Key: key, NPMRemote: formats.ServeNPMRemoteFacts{
		Known: t.Known, Listed: t.Listed, Integrity: t.Integrity, Shasum: t.Shasum,
		DocStored: t.DocStored, DocCurrent: t.DocCurrent, ServeRemoved: t.ServeRemoved,
	}}
	if !t.DocGone.IsZero() {
		out.NPMRemote.DocGoneUs = t.DocGone.UnixMicro()
	}
	return out
}
