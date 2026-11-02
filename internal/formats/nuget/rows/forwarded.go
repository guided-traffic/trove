package rows

import "encoding/json"

// Forwarded is the part of an upstream's search result that a stored version has no column for
// (R-NUGET-016): the members the search protocol keeps for a package of a remote and that a local
// package omits (R-NUGET-010). A search result of a remote repository travels through the search
// and the merge of a virtual repository as a SearchHit whose Latest carries it, so it moves with
// the latest version of a union (R-NUGET-018 step 4, Q-NUGET-78).
type Forwarded struct {
	// Owners are the owners the upstream names, as a list; empty when it names none.
	Owners []string
	// Verified is the upstream's verified flag.
	Verified bool
	// Deprecation and Vulnerabilities are the upstream's members as received, nil when it sent
	// none; they describe the upstream's latest version.
	Deprecation, Vulnerabilities json.RawMessage
	// TotalDownloads is the upstream's total over every version of the package, or the sum
	// over the versions shown when some of them were removed for the caller.
	TotalDownloads int64
}
