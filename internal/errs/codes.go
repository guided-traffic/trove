package errs

// Codes are stable snake_case identifiers and part of the API contract: a code
// is never reused or renamed within an API major version (R-ARCH-025). This file
// is the single registry; the problem catalog served under
// /api/v1/problems/<code> is generated from Codes().

// Generic codes, one per kind. A component that has no more specific code uses
// the one of its kind.
const (
	CodeInternal            = "internal"
	CodeInvalid             = "malformed_request"
	CodeValidation          = "validation_failed"
	CodeUnauthenticated     = "unauthenticated"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeMethodNotAllowed    = "method_not_allowed"
	CodeConflict            = "conflict"
	CodePreconditionFailed  = "precondition_failed"
	CodeTooLarge            = "payload_too_large"
	CodeUnsupported         = "unsupported_media_type"
	CodeRateLimited         = "rate_limited"
	CodeInsufficientStorage = "insufficient_storage"
	CodeUnavailable         = "unavailable"
	CodeUpstreamFailed      = "upstream_failed"
	CodeUpstreamTimeout     = "upstream_timeout"
)

// Specific codes named by the specification.
const (
	CodeRepositoryNotFound  = "repository_not_found"
	CodeDatabaseUnavailable = "database_unavailable"
	CodeEgressDenied        = "egress_denied"
	CodeKeyRingIncompatible = "key_ring_incompatible"
	CodeObjectStoreUnavail  = "object_store_unavailable"
	CodeShuttingDown        = "shutting_down"
	CodeServerBusy          = "server_busy"
	CodeDatabaseTimeout     = "database_timeout"
	CodeMisdirectedRequest  = "misdirected_request"
)

// Codes of the M1 areas. Each area adds its constants only inside its own
// markers and takes its range from docs/developer/m1-contracts.md; a code is
// never reused or renamed (R-ARCH-025). Write a `const (...)` block or single
// constants between the markers.

// >>> M1 audit

// CodeInvalidAuditRange is a query or export range the audit API refuses: a missing
// bound, an end before the start, or an export longer than 400 days (R-OPS-017).
const CodeInvalidAuditRange = "invalid_audit_range"

// <<< M1 audit
//
// >>> M1 api
// <<< M1 api
//
// >>> M1 identity
// <<< M1 identity
//
// >>> M1 login
// <<< M1 login
//
// >>> M1 authz
// <<< M1 authz
//
// >>> M1 repository
// Repository model (R-REPO-001 to R-REPO-013, R-ARCH-005). invalid_pattern,
// reserved_name and out_of_range are registered by the authz and api areas; the
// repository package names them as strings.
const (
	CodeRepositoryExists     = "repository_exists"
	CodeRepositoryDisabled   = "repository_disabled"
	CodeRepositoryReferenced = "repository_referenced"
	CodeImmutableField       = "immutable_field"
	CodeInvalidKey           = "invalid_key"
	CodeInvalidMember        = "invalid_member"
	CodeInvalidDocument      = "invalid_document"
	CodeNotApplicable        = "not_applicable"
	CodeUnknownFormat        = "unknown_format"
	CodeUnsupportedClass     = "unsupported_class"
	CodeUnsupportedLayout    = "unsupported_layout"
	CodeBatchTooLarge        = "batch_too_large"
	CodeNotAPackage          = "not_a_package"
	CodeNoDeployTarget       = "no_deploy_target"
	CodeInvalidPath          = "invalid_path"
	CodeNotAdmitted          = "not_admitted"
)

// <<< M1 repository
//
// >>> M1 files
// <<< M1 files
//
// >>> M1 endpoints
// <<< M1 endpoints

// CodeInfo documents one code for the problem catalog.
type CodeInfo struct {
	Code   string
	Kind   Kind
	Title  string
	Detail string // what the code means and what a client may do about it
}

// registry is frozen at start-up: it is filled by this literal and never written.
var registry = []CodeInfo{
	{CodeInternal, Internal, "Internal error", "The server failed in a way the client cannot fix. Quote the request ID when reporting it."},
	{CodeInvalid, Invalid, "Malformed request", "The request cannot be parsed: unparsable JSON, wrong types, an unknown or duplicate field, or an unknown query parameter."},
	{CodeValidation, Invalid, "Validation failed", "The request parsed but one or more fields are invalid; see errors."},
	{CodeUnauthenticated, Unauthenticated, "Authentication required", "The request carries no valid credential."},
	{CodeForbidden, Forbidden, "Forbidden", "The credential is valid but the operation is not permitted."},
	{CodeNotFound, NotFound, "Not found", "The resource does not exist or is not visible to the caller."},
	{CodeMethodNotAllowed, MethodNotAllowed, "Method not allowed", "The resource does not support the method; see the Allow header."},
	{CodeConflict, Conflict, "Conflict", "The request conflicts with the current state of the resource."},
	{CodePreconditionFailed, PreconditionFailed, "Precondition failed", "A conditional request header did not match."},
	{CodeTooLarge, TooLarge, "Payload too large", "The request body exceeds the permitted size."},
	{CodeUnsupported, Unsupported, "Unsupported media type", "The request body has a content type the endpoint does not accept."},
	{CodeRateLimited, RateLimited, "Too many requests", "Retry after the period in the Retry-After header."},
	{CodeInsufficientStorage, InsufficientStorage, "Insufficient storage", "A quota or capacity limit prevents the write."},
	{CodeUnavailable, Unavailable, "Service unavailable", "A dependency is temporarily unavailable; retry after the Retry-After period."},
	{CodeUpstreamFailed, UpstreamFailed, "Upstream failed", "The upstream registry answered with an error or could not be reached."},
	{CodeUpstreamTimeout, UpstreamTimeout, "Upstream timeout", "The upstream registry did not answer in time."},
	{CodeRepositoryNotFound, NotFound, "Repository not found", "No repository has the requested key."},
	{CodeDatabaseUnavailable, Unavailable, "Database unavailable", "PostgreSQL cannot be reached; retry after the Retry-After period."},
	{CodeEgressDenied, UpstreamFailed, "Egress denied", "The address policy of the outbound client refuses the upstream address."},
	{CodeKeyRingIncompatible, Conflict, "Key ring incompatible", "A live replica lacks a key that is active elsewhere or referenced by a sealed value."},
	{CodeObjectStoreUnavail, Unavailable, "Object store unavailable", "The S3-compatible object store cannot be reached."},
	{CodeShuttingDown, Unavailable, "Shutting down", "The replica is draining; retry on another replica."},
	{CodeServerBusy, Unavailable, "Server busy", "The replica is at server.max_in_flight or another concurrency bound; retry after the Retry-After period, the request can succeed on another replica."},
	{CodeDatabaseTimeout, Unavailable, "Database timeout", "A statement exceeded its time limit (statement_timeout or lock_timeout); retry after the Retry-After period."},
	{CodeMisdirectedRequest, Misdirected, "Misdirected request", "The host of the request is not one this endpoint answers for. Send the request to the endpoint that serves the host, on a new connection."},

	// >>> M1 audit
	{CodeInvalidAuditRange, Invalid, "Invalid audit range", "The time range of the audit query or export is missing, ends before it starts, or exceeds the 400 days an export may cover. Send from and to as RFC 3339 times, with to after from."},
	// <<< M1 audit
	//
	// >>> M1 api
	// <<< M1 api
	//
	// >>> M1 identity
	// <<< M1 identity
	//
	// >>> M1 login
	// <<< M1 login
	//
	// >>> M1 authz
	// <<< M1 authz
	//
	// >>> M1 repository
	{CodeRepositoryExists, Conflict, "Repository exists", "A repository with this key exists or is still being deleted; keys stay taken until the deletion job completes."},
	{CodeRepositoryDisabled, NotFound, "Repository disabled", "The repository is disabled and takes no part in resolution, download or deploy; the management API still works."},
	{CodeRepositoryReferenced, Conflict, "Repository referenced", "Virtual repositories list this repository as a member or deploy target; delete with detach=true or remove the references first."},
	{CodeImmutableField, Invalid, "Immutable field", "Key, class and format are fixed at creation; create a new repository instead."},
	{CodeInvalidKey, Invalid, "Invalid repository key", "A key matches ^[a-z0-9][a-z0-9._-]{1,63}$, contains no \"..\" and does not end in a dot."},
	{CodeInvalidMember, Invalid, "Invalid member", "A virtual repository member is missing, being deleted, of another format, duplicated or the repository itself."},
	{CodeInvalidDocument, Invalid, "Invalid document", "The configuration document is not valid JSON of the expected shape, or it names a member that does not exist."},
	{CodeNotApplicable, Invalid, "Not applicable", "The setting does not apply to the repository's class or format."},
	{CodeUnknownFormat, Invalid, "Unknown format", "The format is not in the format catalog; GET /api/v1/formats lists the formats."},
	{CodeUnsupportedClass, Invalid, "Unsupported class", "The class is unknown, or the format does not support it."},
	{CodeUnsupportedLayout, Invalid, "Unsupported layout", "A repository has the one layout of its format; it cannot be set to another."},
	{CodeBatchTooLarge, Invalid, "Batch too large", "A batch operation names more repositories than repositories.batch_limit."},
	{CodeNotAPackage, Invalid, "Not a package", "The format refuses content that is not one of its packages."},
	{CodeNoDeployTarget, MethodNotAllowed, "No deploy target", "The virtual repository has no default deployment target, so it takes no deploys."},
	{CodeInvalidPath, Invalid, "Invalid path", "The path has an empty, \".\" or \"..\" segment, a NUL, a backslash or a control character, is not valid UTF-8, or exceeds the length limits."},
	{CodeNotAdmitted, Forbidden, "Path not admitted", "The path does not match the repository's include patterns, or matches an exclude pattern."},
	// <<< M1 repository
	//
	// >>> M1 files
	// <<< M1 files
	//
	// >>> M1 endpoints
	// <<< M1 endpoints
}

// Codes returns a copy of the registry in declaration order.
func Codes() []CodeInfo {
	out := make([]CodeInfo, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the registry entry of a code.
func Lookup(code string) (CodeInfo, bool) {
	for _, c := range registry {
		if c.Code == code {
			return c, true
		}
	}
	return CodeInfo{}, false
}
