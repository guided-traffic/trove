package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
)

// RunIndexJob is the handler of the job deb-index (R-DEB-010): one build of the indexes of a local or virtual repository from the
// rows of a snapshot, signed and published through the publication port. It is the stub the work package of the index builder
// replaces (apt-work-packages.md, WP4).
func RunIndexJob(context.Context, *jobs.Run) error {
	return jobs.Permanent(errNotBuilt("the index build"))
}
