package deb

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item of a local repository on any route, /files/ and the cache view included
// (R-DEB-032, Q-ARCH-381). A package file the journal replay withheld after a restore keeps its item and its bytes and is refused
// with the 404 of a missing file until an administrator reinstates it. Generated files are let through: they are not packages.
//
// The item lookup of the download read the mark with the item when the route asked for it ([Format.ServeFactsKey]), so the check
// costs no statement of its own on the hottest route (R-ARCH-027); it reads through the data port when the facts were not read for
// its key, and keeps no state of its own (R-ARCH-032). A remote repository's cache has no mark: its rows go with the entry.
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	key := f.ServeFactsKey(repo, formats.Path{Path: it.Path})
	if key.Zero() || key.Kind != formats.ServeDebLocal {
		return nil
	}
	if it.Facts.For(key) {
		if it.Facts.Tombstoned {
			return errWithheld()
		}
		return nil
	}
	st, ok := h.FormatData(ID).(Store)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The Debian data port is not bound.")
	}
	if _, withheld, err := st.Tombstone(ctx, repo.ID, it.Path); err != nil || withheld {
		if err != nil {
			return err
		}
		return errWithheld()
	}
	return nil
}

// ServeFactsKey implements [formats.ServeFactsKeyer]: the rows the item lookup of a download reads with the item (R-ARCH-027). A
// package file of a local repository (a path below pool/) reads the withheld mark (servefacts.DebLocal); a file of a remote
// repository that is no index and no release document reads the digest and size its newest parsed index lists for the path
// (servefacts.DebRemote), which the remote adapter's gate announces (R-DEB-022). The zero key is a path the check lets through.
func (f *Format) ServeFactsKey(repo formats.RepoRef, p formats.Path) formats.ServeKey {
	switch repo.Class {
	case formats.Local:
		if !strings.HasPrefix(p.Path, dirPool+"/") {
			return formats.ServeKey{}
		}
		return formats.ServeKey{Kind: formats.ServeDebLocal, Package: f.Classify(p).Package}
	case formats.Remote:
		if f.Classify(p).Kind != formats.Content || strings.HasPrefix(p.Path, dirDists+"/") {
			return formats.ServeKey{}
		}
		return formats.ServeKey{Kind: formats.ServeDebRemote}
	}
	return formats.ServeKey{}
}

var _ formats.ServeFactsKeyer = (*Format)(nil)

func errWithheld() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The package file is not available.")
}
