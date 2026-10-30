package npm

import (
	"crypto/sha256"
	"fmt"

	"github.com/guided-traffic/trove/internal/formats"
)

// Rev is the `_rev` of a local package document and the `rev` of a successful write:
// "<rev_seq>-<first 32 hex characters of SHA-256 over the repository id, the
// package id and rev_seq>" (R-NPM-009). Clients treat it as opaque; the hash keeps
// a revision of one package from matching the same number of another, and a
// document of a restored installation from matching one of the old (R-NPM-047).
// rev_seq is packages.rev_seq, the one counter of the core.
func Rev(repoID int64, pkg formats.PackageID, rev formats.RevSeq) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d", repoID, pkg, rev)))
	return fmt.Sprintf("%d-%x", rev, sum[:16])
}
