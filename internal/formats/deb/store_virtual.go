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
	// VirtualInputs reads, in the build's snapshot tx, the suites of a virtual repository and the stored state of its members in
	// member order: a local member's published generation, a remote member's stored releases, and the Date of each member's release
	// the previous generation merged (Q-DEB-43).
	VirtualInputs(ctx context.Context, tx formats.Tx, repoID int64) (rows.VirtualInputs, error)
	// RecordCheck records that a build checked its members without a change: the inputs it compared (the fingerprint of each
	// member's input, R-DEB-026) and the time, in deb_suites.inputs and checked_at, and nothing else.
	RecordCheck(ctx context.Context, tx formats.Tx, repoID int64, suite string, inputs json.RawMessage, at time.Time) error
}
