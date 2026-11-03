package iface

// The routes of the interface on the main installation's default endpoint
// (R-BAS-008, Q-BAS-25). The Bastion token is the Authorization: Bearer
// credential on these routes.
const (
	// PathPrefix starts every interface route. A request below it is an interface
	// request; it is part of /api/v1/, so the Bastion itself never serves it
	// (R-BAS-003).
	PathPrefix = "/api/v1/bastion/"
	// PathHandshake is GET: interface version, identity and token expiry, the main
	// installation's state (R-BAS-010).
	PathHandshake = "/api/v1/bastion/handshake"
	// PathCheck is POST: a caller's credential and its read shape on repositories
	// (R-BAS-011).
	PathCheck = "/api/v1/bastion/check"
	// PathFeed is GET with the query parameters [QueryAfter] and [QueryLimit]: the
	// revocation feed (R-BAS-015).
	PathFeed = "/api/v1/bastion/feed"
	// PathDownloads is POST: download events served from the Bastion's cache
	// (R-BAS-016).
	PathDownloads = "/api/v1/bastion/downloads"
)

// The query parameters of [PathFeed].
const (
	QueryAfter = "after"
	QueryLimit = "limit"
)

// The operation names of the interface: the operation label of
// trove_bastion_interface_requests_total on the main installation (R-BAS-008) and
// the kind of trove_bastion_main_requests_total on the Bastion for the
// non-forwarded ones (R-BAS-040).
const (
	OperationHandshake = "handshake"
	OperationCheck     = "check"
	OperationFeed      = "feed"
	OperationDownloads = "downloads"
)

// The families of the mirrored URL space (R-BAS-003, R-BAS-018). The Bastion
// serves a path only below one of them: /api/<format>/<repoKey>/… (every path
// below /api/ except /api/v1/…) and /files/<repoKey>/….
const (
	// FilesPrefix is the raw transfer family of INV-6.
	FilesPrefix = "/files/"
	// APIPrefix is the family of the package protocols; its management API
	// sub-tree is [APIv1Prefix].
	APIPrefix = "/api/"
	// APIv1Prefix is the management API, which the Bastion never serves.
	APIv1Prefix = "/api/v1/"
)
