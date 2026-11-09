package deb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// VirtualStore is the part of the data port that the build of a virtual repository's indexes reads and writes (R-DEB-025, R-DEB-026).
// Built by the work package of the virtual class.
type VirtualStore interface {
	// VirtualInputs reads, in the build's snapshot tx, the suites of a virtual repository with their last build's record (the inputs
	// and the Date of each remote member's release it merged, Q-DEB-43) and the published suites and index files of its local
	// members. A remote member's release is not read here: the build asks the remote engine for it.
	VirtualInputs(ctx context.Context, tx formats.Tx, repoID int64, localMembers []int64) (rows.VirtualInputs, error)
	// EnsureSuites creates the suite rows of a virtual repository that do not exist, in a transaction of their own.
	EnsureSuites(ctx context.Context, repoID int64, suites []string) error
	// VirtualsOfMember lists the virtual Debian repositories that list a repository as a member (the trigger of R-DEB-026).
	VirtualsOfMember(ctx context.Context, memberID int64) ([]int64, error)
	// LastBuild is the time of the last build or check of a virtual repository; the zero time when it has none.
	LastBuild(ctx context.Context, repoID int64) (time.Time, error)
	// ClaimsFingerprint is a digest of the claim revisions of the Debian repositories: part of the inputs a virtual build compares.
	ClaimsFingerprint(ctx context.Context) (string, error)
	// Repositories reads the key, class and upstream of repositories by id.
	Repositories(ctx context.Context, ids []int64) ([]rows.RepositoryMeta, error)
	// RecordCheck records that a build checked its members without a change: the inputs it compared (the fingerprint of each
	// member's input, R-DEB-026) and the time, in deb_suites.inputs and checked_at, and nothing else.
	RecordCheck(ctx context.Context, tx formats.Tx, repoID int64, suite string, inputs json.RawMessage, at time.Time) error
}
