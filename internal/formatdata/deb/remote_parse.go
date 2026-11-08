package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
)

// RunRemoteParseJob is the handler of the job deb-remote-parse (R-DEB-023, Q-DEB-36): it reads the hash the stored release names for
// its index path when it starts, fetches the full index through the background path when it is not stored, and replaces the rows of
// the index path in one transaction. It is the stub the work package of the remote class replaces (apt-work-packages.md, WP6).
func RunRemoteParseJob(context.Context, *jobs.Run) error {
	return jobs.Permanent(errNotBuilt("the parse of a remote index"))
}
