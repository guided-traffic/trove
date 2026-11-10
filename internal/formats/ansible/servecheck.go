package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item of a local repository on any route, /files/ and the download route included
// (R-ANS-029). An archive whose number a tombstone withholds - the archive a restore brought back after a journaled deletion - keeps its
// item and its bytes and is refused with the 404 of a missing file until an administrator clears the tombstone. A remote repository's
// cache has no tombstones. The check is one statement by primary key on the downloads of a local repository and keeps no state of its
// own (R-ARCH-032).
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	if repo.Class != formats.Local {
		return nil
	}
	info := f.Classify(formats.Path{Path: it.Path})
	if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
		return nil
	}
	st, ok := h.FormatData(ID).(Store)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The Ansible Galaxy data port is not bound.")
	}
	_, withheld, err := st.Tombstoned(ctx, repo.ID, info.Package, info.Version)
	if err != nil {
		return err
	}
	if withheld {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The collection archive is not available.")
	}
	return nil
}
