package formathost

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// BackgroundUpstream returns a [discover.Upstream] for one job run of one remote repository: the egress, credential, background
// share (R-REPO-055) and throttling (R-REPO-054) rules of a mirror pass, without a pass, its fence or its window (Q-REPO-272). Its
// Document and Artifact behave as they do for a pass. The jobs of the Debian format use it: the parse of a remote's indexes
// (R-DEB-023), the fetch of the release documents a virtual repository's build merges (R-DEB-026).
//
// Not built yet: the work package of the background fetches (apt-work-packages.md, WP13) replaces this body. The declaration is the
// contract the packages that call it build against.
func (h *Host) BackgroundUpstream(context.Context, formats.RepoRef) (discover.Upstream, error) {
	return nil, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The background upstream of a remote repository is not built yet.")
}
