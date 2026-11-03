package iface

// The problem codes the two sides exchange or produce for the interface
// (R-BAS-008, R-BAS-009, R-BAS-014, R-BAS-029). The main installation answers the
// first four as problem details of the management API, also on a package route;
// the Bastion produces the rest itself. internal/errs registers every one of them
// with its kind, which gives the HTTP status.
const (
	// CodeTokenInvalid (401): the Bastion token on a request is invalid, revoked,
	// expired or the registration is disabled. It concerns the Bastion, never the
	// caller, and the Bastion never passes it on (R-BAS-008, R-BAS-027).
	CodeTokenInvalid = "bastion_token_invalid"
	// CodeInterfaceMismatch (400): the request's [HeaderInterface] differs from
	// the main installation's, or is missing. The problem carries
	// [ProblemMemberInterfaceVersion] (R-BAS-009).
	CodeInterfaceMismatch = "bastion_interface_mismatch"
	// CodeReadOnly (403): a write through a Bastion; the detail names the main
	// installation's URL of the addressed repository (R-BAS-014).
	CodeReadOnly = "bastion_read_only"
	// CodeNotServed (404): a path outside the mirrored families, answered by the
	// Bastion itself (R-BAS-003, R-BAS-018).
	CodeNotServed = "bastion_not_served"
	// CodeBaseURLUnknown (409): client-setup for a Bastion whose reported base URL
	// is not an entry of server.additional_base_urls (R-BAS-007).
	CodeBaseURLUnknown = "bastion_base_url_unknown"
	// CodeNotImplemented (501): an operation of the Bastion work that is not built
	// yet; it leaves with the last stub.
	CodeNotImplemented = "bastion_not_implemented"
)

// The codes of the Bastion's own answers (R-BAS-029). The statuses are in the
// table of R-BAS-029; a 401 carries the challenge of R-AUTH-002 for the path
// family and every answer carries Cache-Control: no-store.
const (
	CodeMainFailed        = "main_failed"          // 502
	CodeMainUnreachable   = "main_unreachable"     // 503, Retry-After: 30
	CodeBusy              = "bastion_busy"         // 503, Retry-After: 1
	CodeMainTimeout       = "main_timeout"         // 504
	CodeAuthTimeout       = "auth_timeout"         // 429
	CodeAddressBlocked    = "address_blocked"      // 429
	CodeInvalidToken      = "invalid_token"        // 401, shared with the main installation's registry
	CodeCredentialsNeeded = "credentials_required" // 401, shared with the main installation's registry
	CodeRateLimited       = "rate_limited"         // 429, shared
	CodeMethodNotAllowed  = "method_not_allowed"   // 405, shared
	CodePayloadTooLarge   = "payload_too_large"    // 413, shared
	CodeMisdirected       = "misdirected_request"  // 421, shared
)

// ProblemMemberInterfaceVersion is the member of the problem details of a
// [CodeInterfaceMismatch] answer that carries the main installation's version
// (R-BAS-009).
const ProblemMemberInterfaceVersion = "interfaceVersion"

// The problem media type of every error answer of the interface.
const ProblemMediaType = "application/problem+json"
