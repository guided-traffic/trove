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
const (
	CodeInvalidCursor        = "invalid_cursor"
	CodeInvalidHeader        = "invalid_header"
	CodeAlreadyExists        = "already_exists"
	CodeIdempotencyInFlight  = "idempotency_in_progress"
	CodeIdempotencyKeyReused = "idempotency_key_reused"
	CodePreconditionRequired = "precondition_required"
	CodeReservedName         = "reserved_name"
	CodeManagedByConfig      = "managed_by_config"
)

// <<< M1 api
//
// >>> M1 identity
const (
	CodeInvalidToken            = "invalid_token"
	CodeCredentialsRequired     = "credentials_required"
	CodeInvalidCredentials      = "invalid_credentials" //nolint:gosec // G101: an error code, not a credential
	CodePrincipalDisabled       = "principal_disabled"
	CodeInvalidScope            = "invalid_scope"
	CodeInvalidTTL              = "invalid_ttl"
	CodeTTLExceedsMaximum       = "ttl_exceeds_maximum"
	CodeDefaultAboveMaximum     = "default_above_maximum"
	CodeExceedsCeiling          = "exceeds_ceiling"
	CodeNonExpiringNotAllowed   = "non_expiring_not_allowed"
	CodeNonExpiringRestricted   = "non_expiring_restricted"
	CodePATNotAllowed           = "pat_not_allowed"
	CodeCITokenRestricted       = "ci_token_restricted" //nolint:gosec // G101: an error code, not a credential
	CodeTokenAlreadyIssued      = "token_already_issued"
	CodeTokenLimitReached       = "token_limit_reached" //nolint:gosec // G101: an error code, not a credential
	CodeDerivationExceedsParent = "derivation_exceeds_parent"
	CodeDescriptionRequired     = "description_required"
	CodeNotAToken               = "not_a_token"
	CodeManagedBySCIM           = "managed_by_scim"
	CodeLastAdmin               = "last_admin"
	CodeAnonymousNotAllowed     = "anonymous_not_allowed"
	CodeAutoJoinAdminConflict   = "auto_join_admin_conflict"
	CodeUsernameInvalid         = "username_invalid"
)

// <<< M1 identity
//
// >>> M1 login

// Codes of the login, the UI session and the break-glass account (R-AUTH-002,
// R-AUTH-005, R-AUTH-006, R-AUTH-011, R-AUTH-012, R-AUTH-014, R-AUTH-038).
const (
	CodeSessionExpired           = "session_expired"
	CodeReauthenticationRequired = "reauthentication_required"
	CodeCSRFFailed               = "csrf_failed"
	CodeBrowserMismatch          = "browser_mismatch"
	CodeFlowExpired              = "flow_expired"
	CodeProviderUnavailable      = "provider_unavailable"
	CodeBreakGlassRestricted     = "break_glass_restricted"
	CodeBreakGlassImmutable      = "break_glass_immutable"
	CodeBreakGlassRotating       = "break_glass_rotating"
	CodeLocalLoginDisabled       = "local_login_disabled"
	CodeBreakGlassLocked         = "break_glass_locked"
	CodeTooManyPendingLogins     = "too_many_pending_logins"
)

// <<< M1 login
//
// >>> M1 authz

// Codes of authorization (R-AUTHZ-016, R-AUTHZ-021, R-AUTHZ-022, R-AUTHZ-030,
// R-AUTHZ-032). The 422 codes name a rule of a policy entry or a role binding.
const (
	CodeAuthzStale                 = "authz_stale"
	CodeAuthzEpochUnknown          = "authz_epoch_unknown"
	CodeRoleNotAllowed             = "role_not_allowed"
	CodeUnknownRole                = "unknown_role"
	CodeAuthenticatedWriteAction   = "authenticated_write_action"
	CodeAnonymousWriteAction       = "anonymous_write_action"
	CodeAnonymousDynamicSelector   = "anonymous_dynamic_selector"
	CodeActionNotAllowed           = "action_not_allowed"
	CodeInvalidAction              = "invalid_action"
	CodeInvalidPattern             = "invalid_pattern"
	CodeUnknownPrincipal           = "unknown_principal"
	CodeUnknownRepository          = "unknown_repository"
	CodeUnknownFormat              = "unknown_format"
	CodeDuplicatePrincipal         = "duplicate_principal"
	CodeVirtualActionNotApplicable = "virtual_action_not_applicable"
)

// <<< M1 authz
//
// >>> M1 repository
// Repository model (R-REPO-001 to R-REPO-013, R-ARCH-005). invalid_pattern,
// reserved_name and out_of_range are registered by the authz and api areas; the
// repository package names them as strings.
const (
	CodeRepositoryDisabled   = "repository_disabled"
	CodeRepositoryReferenced = "repository_referenced"
	CodeInvalidKey           = "invalid_key"
	CodeInvalidMember        = "invalid_member"
	CodeInvalidDocument      = "invalid_document"
	CodeNotApplicable        = "not_applicable"
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
// Files transfer and items (R-STOR-007, R-STOR-012, R-GENERIC-002). invalid_path
// and not_admitted are registered with the repository codes above.
const (
	CodePathConflict        = "path_conflict"
	CodeContentUnavailable  = "content_unavailable"
	CodeRangeNotSatisfiable = "range_not_satisfiable"
)

// <<< M1 files
//
// >>> M1 endpoints

// Codes of endpoints and exposure rules (R-EP-012 to R-EP-016). The authz package
// names them as strings (internal/authz/exposure_service.go, errors.go).
const (
	CodeEndpointReadOnly      = "endpoint_read_only"
	CodeInvalidVersionRange   = "invalid_version_range"
	CodeDimensionNotSupported = "dimension_not_supported"
	CodeFormatNotExposable    = "format_not_exposable"
	CodeEndpointNotConfigured = "endpoint_not_configured"
	CodeExposureRuleLimit     = "exposure_rule_limit_reached"
)

// <<< M1 endpoints
//
// >>> M2 upstream

// Codes of the upstream clients of remote repositories (R-REPO-024, R-REPO-033,
// R-REPO-043). The engine of the pull-through proxy adds its own beside them.
const (
	CodeUpstreamSecretUnavailable = "upstream_secret_unavailable" //nolint:gosec // G101: an error code, not a credential
	CodeSourceNetworkUnavailable  = "source_network_unavailable"
	CodeForeignHostDenied         = "foreign_host_denied"
)

// <<< M2 upstream

// >>> M2 untrusted

// Code of package content whose structure the bounded readers refuse: ambiguous
// or forbidden (R-ARCH-028). A crossed bound answers too_large instead.
const CodeContentRefused = "content_refused"

// <<< M2 untrusted

// >>> M2 ownership

// Codes of package ownership and of the populate check (R-AUTHZ-008,
// R-AUTHZ-016, R-AUTHZ-018, R-AUTHZ-019). ci_token_restricted (identity) and
// already_exists (api) are registered above; the ownership operations answer
// with them as well.
const (
	CodePackageOwnedByOtherOwner = "package_owned_by_other_owner"
	CodeLastOwner                = "last_owner"
	CodePopulateNotPermitted     = "populate_not_permitted"
)

// <<< M2 ownership

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
	{CodeInvalidCursor, Invalid, "Invalid cursor", "The cursor was not issued by this server, is of an unsupported version, or was issued for other filters or another sort order. Restart the traversal without a cursor."},
	{CodeInvalidHeader, Invalid, "Invalid header", "A trove request header has an invalid value; the detail names the header."},
	{CodeAlreadyExists, Conflict, "Already exists", "A resource with this key or name exists already."},
	{CodeIdempotencyInFlight, Conflict, "Idempotent request in progress", "The first request with this Idempotency-Key has not finished. Retry after the Retry-After period; the retry replays its outcome."},
	{CodeIdempotencyKeyReused, Unprocessable, "Idempotency key reused", "The Idempotency-Key was used before with another method, route or body. Use a new key for a different request."},
	{CodePreconditionRequired, PreconditionRequired, "Precondition required", "The operation needs an If-Match header carrying the ETag the client loaded, or * to overwrite deliberately."},
	{CodeReservedName, Unprocessable, "Reserved name", "The name is reserved because a route of the collection uses it as a literal path segment."},
	{CodeManagedByConfig, Conflict, "Managed by configuration", "The value is set in the configuration file or an environment override and cannot be changed through the API; change it where it is set."},
	// <<< M1 api
	//
	// >>> M1 identity
	{CodeInvalidToken, Unauthenticated, "Invalid token", "The token is malformed, unknown, expired or revoked. The response does not say which; request a new token."},
	{CodeCredentialsRequired, Unauthenticated, "Credentials required", "The request carries no credential and anonymous access is off or does not cover it."},
	{CodeInvalidCredentials, Unauthenticated, "Invalid credentials", "The credentials were refused."},
	{CodePrincipalDisabled, Unauthenticated, "Principal disabled", "The credential is valid but its owner is disabled."},
	{CodeInvalidScope, Invalid, "Invalid scope", "The token scope has an unknown field or action, an unknown endpoint, or exceeds the size limits."},
	{CodeInvalidTTL, Invalid, "Invalid lifetime", "Give ttlSeconds (at least 60, or 0 for a non-expiring service-account token) or expiresAt, not both, and at least 60 seconds ahead."},
	{CodeTTLExceedsMaximum, Invalid, "Lifetime exceeds the maximum", "The requested lifetime is above the maximum for the token type; trove does not clamp it."},
	{CodeDefaultAboveMaximum, Invalid, "Default above maximum", "A default lifetime must not exceed its maximum."},
	{CodeExceedsCeiling, Invalid, "Exceeds the configured ceiling", "A runtime value must not exceed the ceiling set in the configuration."},
	{CodeNonExpiringNotAllowed, Invalid, "Non-expiring token not allowed", "Only a service-account token can be non-expiring, and only while the configuration allows it."},
	{CodeNonExpiringRestricted, Forbidden, "Non-expiring token restricted", "Only an administrator may create a non-expiring service-account token."},
	{CodePATNotAllowed, Forbidden, "Personal tokens not allowed", "Personal tokens are restricted to members of the configured groups."},
	{CodeCITokenRestricted, Forbidden, "CI token restricted", "A CI token cannot create or rotate tokens."},
	{CodeTokenAlreadyIssued, Conflict, "Token already issued", "The secret of this request was issued by an earlier identical request and is not shown again."},
	{CodeTokenLimitReached, Conflict, "Token limit reached", "The principal holds the maximum number of active tokens; revoke one first."},
	{CodeDerivationExceedsParent, Forbidden, "Derivation exceeds the presenting token", "A token created with a token must not outlive or outrank it."},
	{CodeDescriptionRequired, Invalid, "Description required", "A token needs a description of 1 to 1024 characters."},
	{CodeNotAToken, NotFound, "Not a token", "The request is not authenticated by a token."},
	{CodeManagedBySCIM, Conflict, "Managed by SCIM", "The object is owned by the identity provider and cannot be changed here."},
	{CodeLastAdmin, Conflict, "Last administrator", "The change would remove the last administrator."},
	{CodeAnonymousNotAllowed, Conflict, "Anonymous access not allowed", "The configuration forbids anonymous access."},
	{CodeAutoJoinAdminConflict, Conflict, "Auto-join conflicts with an administration role", "A group with an administration role binding cannot be an auto-join group."},
	{CodeUsernameInvalid, Invalid, "Invalid username", "The username does not match the name rule or is reserved."},
	// <<< M1 identity
	//
	// >>> M1 login
	{CodeSessionExpired, Unauthenticated, "Session expired", "The session cookie names a session that has ended (logout, timeout, revocation). Sign in again."},
	{CodeReauthenticationRequired, Unauthenticated, "Re-authentication required", "The action mints a long-lived credential and needs a recent sign-in at the identity provider; start the re-authentication round trip and retry."},
	{CodeCSRFFailed, Forbidden, "CSRF check failed", "A cookie-authenticated request that changes state needs the X-CSRF-Token header of the session and a same-origin Origin or Sec-Fetch-Site."},
	{CodeBrowserMismatch, Invalid, "Browser mismatch", "The login callback arrived in a browser other than the one that started the login. Start the login again in one browser."},
	{CodeFlowExpired, Invalid, "Login expired", "The login flow is unknown, used or older than ten minutes. Start the login again."},
	{CodeProviderUnavailable, Unavailable, "Login provider unavailable", "The identity provider's discovery document could not be loaded yet; retry after the Retry-After period."},
	{CodeBreakGlassRestricted, Forbidden, "Not allowed for the break-glass account", "The break-glass account can neither own nor create tokens nor approve command-line logins."},
	{CodeBreakGlassImmutable, Conflict, "Break-glass account is immutable", "The break-glass account cannot be deleted or renamed through the API; it is configured in the configuration file."},
	{CodeBreakGlassRotating, Unavailable, "Break-glass password rotation in progress", "The break-glass password was rotated a moment ago; local login resumes after the Retry-After period."},
	{CodeLocalLoginDisabled, Forbidden, "Local login disabled", "The break-glass account is disabled or has no password configured."},
	{CodeBreakGlassLocked, RateLimited, "Break-glass account locked", "Too many failed local logins; the account is locked until the Retry-After period has passed or an administrator unlocks it."},
	{CodeTooManyPendingLogins, Unavailable, "Too many pending logins", "More login flows are pending than auth.rate_limit.max_pending_flows allows; retry after the Retry-After period."},
	// <<< M1 login
	//
	// >>> M1 authz
	{CodeAuthzStale, Unavailable, "Authorization state not confirmed", "The replica could not confirm that its grant index is current within the wait of R-AUTHZ-028; retry after the Retry-After period, the request can succeed on another replica."},
	{CodeAuthzEpochUnknown, PreconditionFailed, "Authorization epoch unknown", "Trove-Authz-Min-Epoch names an epoch the database has not reached. Stop sending that epoch and read the changed object again."},
	{CodeRoleNotAllowed, Invalid, "Role not allowed", "System roles are bound to users, groups and service accounts only; never to anonymous, authenticated or CI identity mappings."},
	{CodeUnknownRole, Invalid, "Unknown role", "The role is not one of admin, security-admin, auditor, repository-admin, webhook-admin."},
	{CodeAuthenticatedWriteAction, Invalid, "Authenticated principal may only read", "Policy entries for the built-in principal authenticated hold only read."},
	{CodeAnonymousWriteAction, Invalid, "Anonymous principal may only read", "Policy entries for anonymous hold only read."},
	{CodeAnonymousDynamicSelector, Invalid, "Anonymous grants need explicit repositories", "A policy with entries for anonymous selects repositories by key only; patterns and classes would expose repositories created later."},
	{CodeActionNotAllowed, Invalid, "Action not allowed for this principal", "The principal kind cannot hold the action, for example manage for a CI identity mapping."},
	{CodeInvalidAction, Invalid, "Invalid action", "The name is neither one of the six repository actions nor a preset (read, write, maintain, full)."},
	{CodeInvalidPattern, Invalid, "Invalid pattern or selector", "A package-name pattern, path pattern or repository selector has invalid syntax or exceeds the limits of R-AUTHZ-005."},
	{CodeUnknownPrincipal, Invalid, "Unknown principal", "No principal of this reference exists."},
	{CodeUnknownRepository, Invalid, "Unknown repository", "A key selector names a repository that does not exist."},
	{CodeDuplicatePrincipal, Invalid, "Duplicate principal", "A policy holds at most one entry per principal."},
	{CodeVirtualActionNotApplicable, Invalid, "Action not applicable to a virtual repository", "A key selector that names a virtual repository accepts only read and manage."},
	// <<< M1 authz
	//
	// >>> M1 repository
	{CodeRepositoryDisabled, NotFound, "Repository disabled", "The repository is disabled and takes no part in resolution, download or deploy; the management API still works."},
	{CodeRepositoryReferenced, Conflict, "Repository referenced", "Virtual repositories list this repository as a member or deploy target; delete with detach=true or remove the references first."},
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
	{CodePathConflict, Conflict, "Path conflict", "A path names a file or a folder, never both: the deploy would put a file below an existing file, or a file or folder where the other kind exists."},
	{CodeContentUnavailable, Unavailable, "Content unavailable", "The stored object of the file is missing (503, retry: it can come back through a restore or a replication) or failed verification (500)."},
	{CodeRangeNotSatisfiable, RangeNotSatisfiable, "Range not satisfiable", "The Range header asks for bytes beyond the end of the file; Content-Range: bytes */<size> says how long it is."},
	// <<< M1 files
	//
	// >>> M1 endpoints
	{CodeEndpointReadOnly, Forbidden, "Endpoint is read-only", "This endpoint serves content under exposure rules and takes no writes; deploy through the default endpoint."},
	{CodeInvalidVersionRange, Invalid, "Invalid version range", "The format refuses the version range of an exposure rule, or the range is longer than 256 characters."},
	{CodeDimensionNotSupported, Invalid, "Dimension not supported", "The repository's format has no package names, versions or tags for this exposure rule dimension; generic repositories are exposed through paths."},
	{CodeFormatNotExposable, Invalid, "Format not exposable", "The repository's format cannot be served on an endpoint with exposure rules yet."},
	{CodeEndpointNotConfigured, Conflict, "Endpoint not configured", "The endpoint is not in the configuration; its exposure rules can be read and deleted, not created or replaced."},
	{CodeExposureRuleLimit, Conflict, "Exposure rule limit reached", "The endpoint holds 1,000 exposure rules, the most it may hold; delete a rule first."},
	// <<< M1 endpoints
	//
	// >>> M2 upstream
	{CodeUpstreamSecretUnavailable, UpstreamFailed, "Upstream secret unavailable", "A secret of the remote repository's upstream configuration cannot be unsealed or resolved on this replica, for example a reference to a file that is missing or a sealed value that was copied from another row. Other repositories are unaffected; the repository's administrator re-enters the secret or fixes the reference."},
	{CodeSourceNetworkUnavailable, UpstreamFailed, "Source network unavailable", "This replica has no interface address inside the remote repository's sourceNetwork, so it does not connect to the upstream. The failure does not count toward the circuit; another replica may have such an address."},
	{CodeForeignHostDenied, UpstreamFailed, "Foreign host denied", "The upstream document points to a host that is neither the upstream origin nor an entry of the remote repository's extraHosts; trove did not send a request."},
	// <<< M2 upstream
	// >>> M2 untrusted
	{CodeContentRefused, Unprocessable, "Content refused", "The package content has a structure trove does not read: duplicate names, link entries, a document type declaration, names two readers would resolve differently, or a document that is not well-formed."},
	// <<< M2 untrusted
	// >>> M2 ownership
	{CodePackageOwnedByOtherOwner, Forbidden, "Package owned by another owner", "The action is granted, but the package name is owned and the caller is none of its owners and holds no manage on it; an owner or a holder of manage adds the caller as an owner."},
	{CodeLastOwner, Conflict, "Last owner", "Removing the only owner of a package would leave it without one; add the new owner first, or release the name with release=true (needs manage)."},
	{CodePopulateNotPermitted, Forbidden, "Populate not permitted", "The caller may read the remote repository but not bring new content into its cache: the repository's cacheFillAction is not granted, the request came with the session cookie, or the endpoint does not fill caches. What the cache holds is still served."},
	// <<< M2 ownership
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
