package items

import (
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// checkPublishTarget is the rule of Q-STOR-180: generated items live in a local repository and in a virtual one, the one exception to a
// virtual repository holding no content, and nowhere else. A remote repository's items are its cache entries, which a generation must
// never overwrite or remove. A virtual repository accepts a generated item from the publication port and from nothing else: the files
// layer refuses a deploy to it (internal/files/deploy.go), the dispatcher sends a format's write to the deployment target, and a delete
// answers 405, so the port is the only writer by construction. Their usage is counted like any item's (R-STOR-003): the counters are kept
// by the items table whatever the class, so the usage figures of a virtual repository are those of its generated items.
func checkPublishTarget(repo RepoRef) error {
	switch repository.Class(repo.Class) {
	case repository.Local, repository.Virtual:
		return nil
	}
	return errs.E(errs.Internal, errs.CodeInternal, "items: generated items are published to a local or a virtual repository, not to a "+repo.Class+" one")
}
