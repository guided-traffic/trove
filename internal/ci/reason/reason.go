// Package reason holds the reason names of the CI exchange and of the CI management API
// (R-CI-004, R-CI-008, R-CI-010, R-CI-013, R-CI-014): the stable machine-readable values that
// appear in the exchange's OAuth error response, in the audit event of a denial, and as the `code`
// of a management error.
//
// It is a leaf of its own so that the claim engine (internal/ci/claims), the subject-token
// validator (internal/ci/subject) and the domain package (internal/ci) name one set without
// importing each other.
package reason

// Reason is one stable reason name. Names are a public contract and are never reused.
type Reason string

// String returns the name.
func (r Reason) String() string { return string(r) }

// The reasons of the exchange (R-CI-008). The comment names the step that raises it.
const (
	// The request (R-CI-007 step 1).
	MalformedRequest              Reason = "malformed_request"
	UnsupportedContentType        Reason = "unsupported_content_type"
	UnsupportedSubjectTokenType   Reason = "unsupported_subject_token_type"
	UnsupportedRequestedTokenType Reason = "unsupported_requested_token_type" //nolint:gosec // G101: a reason name, not a credential
	ActorTokenNotSupported        Reason = "actor_token_not_supported"
	GrantTypeUnsupported          Reason = "grant_type_unsupported"
	AudienceNotThisServer         Reason = "audience_not_this_server"
	ScopeMalformed                Reason = "scope_malformed"
	ScopeNotGranted               Reason = "scope_not_granted"

	// The subject token, in the order of R-CI-004.
	SubjectTokenMalformed Reason = "subject_token_malformed"  // steps 1 to 4
	WrongSubjectTokenType Reason = "wrong_subject_token_type" // step 2: a glcbt- job token
	AlgorithmNotAllowed   Reason = "algorithm_not_allowed"    // step 3
	TokenTypeNotAllowed   Reason = "token_type_not_allowed"   // step 3: typ
	UnknownIssuer         Reason = "unknown_issuer"           // step 5
	IssuerDisabled        Reason = "issuer_disabled"          // step 5
	IssuerMismatch        Reason = "issuer_mismatch"          // the ci_issuer parameter (R-CI-005); also a management code
	SignatureInvalid      Reason = "signature_invalid"        // step 6
	UnknownSigningKey     Reason = "unknown_signing_key"      // step 6
	AudienceInvalid       Reason = "audience_invalid"         // step 7
	AudienceNotExclusive  Reason = "audience_not_exclusive"   // step 7
	TokenExpired          Reason = "token_expired"            // step 8
	TokenNotYetValid      Reason = "token_not_yet_valid"      //nolint:gosec // G101: a reason name, not a credential      // step 8
	IssuedInFuture        Reason = "issued_in_future"         // step 8
	RequiredClaimMissing  Reason = "required_claim_missing"   // step 9; also single use without a jti (R-CI-018)

	// The exchange (R-CI-007 steps 3 to 10).
	SubjectTokenReplayed        Reason = "subject_token_replayed"
	SubjectTokenPredatesRestore Reason = "subject_token_predates_restore"
	CrossProjectSourceDenied    Reason = "cross_project_source_denied"
	NoMatchingMapping           Reason = "no_matching_mapping"
	GrantRenderFailed           Reason = "grant_render_failed"
	SubjectTokenExpiresTooSoon  Reason = "subject_token_expires_too_soon"
	// TTLBelowMinimum: the lifetime the request or a lifetime bound of the configuration leaves is below
	// ci.token.min_ttl (R-CI-016, Q-CI-41); the subject token's own remaining lifetime is
	// SubjectTokenExpiresTooSoon.
	TTLBelowMinimum        Reason = "ttl_below_minimum"
	JobTokenRejected       Reason = "job_token_rejected"       // P2 (R-CI-022)
	JobNotRunning          Reason = "job_not_running"          // P2 (R-CI-022)
	RateLimited            Reason = "rate_limited"             // R-CI-021
	SigningKeysUnavailable Reason = "signing_keys_unavailable" // R-CI-003
	GitLabUnreachable      Reason = "gitlab_unreachable"       // P2 (R-CI-022)
	DatabaseUnavailable    Reason = "database_unavailable"
	ConfigurationChanged   Reason = "configuration_changed" // R-CI-007: a second mismatch
	Internal               Reason = "internal"
)

// The `code` of a management error (R-CI-002, R-CI-010, R-CI-011, R-CI-013, R-CI-014, R-CI-022).
const (
	PinRequired                  Reason = "pin_required"
	PinClaimNotImmutable         Reason = "pin_claim_not_immutable"
	PublicIssuerRequired         Reason = "public_issuer_required"
	KindNotAvailable             Reason = "kind_not_available"
	IssuerManagedByConfiguration Reason = "issuer_managed_by_configuration"
	DiscoveryUnreachable         Reason = "discovery_unreachable"
	NoUsableKeys                 Reason = "no_usable_keys"
	AlgorithmsNotOffered         Reason = "algorithms_not_offered"
	CatchAllCondition            Reason = "catch_all_condition"
	UnprotectedWrite             Reason = "unprotected_write"
	UnpinnedWrite                Reason = "unpinned_write"
	ActionNotAllowed             Reason = "action_not_allowed"
	EmptyGrant                   Reason = "empty_grant"
	TemplateInvalid              Reason = "template_invalid"
	TemplateUnanchored           Reason = "template_unanchored"
	ConditionInvalid             Reason = "condition_invalid" // depth, leaves, list size, regex, glob (R-CI-010)
	JobTokenFallbackNotAvailable Reason = "job_token_fallback_not_available"
	MappingLimitReached          Reason = "mapping_limit_reached" // ci.mappings.max_per_issuer
	PriorityTaken                Reason = "priority_taken"        // 409 (R-CI-009)
	EgressDenied                 Reason = "egress_denied"         // the failure class of a fetch (R-CI-003)
	UnknownPolicy                Reason = "unknown_policy"
	UnknownGroup                 Reason = "unknown_group"
	InvalidSnippet               Reason = "invalid_snippet"
	// ProjectPrefixRequired: a project mapping's name does not start with "<project>-" (Q-CI-26).
	ProjectPrefixRequired Reason = "project_prefix_required"
	// InvalidPattern: a repository selector, package pattern or path pattern has invalid syntax or exceeds the
	// limits of R-AUTHZ-004 and R-AUTHZ-005 (1024 characters, 256 patterns per list).
	InvalidPattern Reason = "invalid_pattern"
	// InvalidValue: a member holds a value outside its closed set (an ownership, a crossProjectSource, a subject
	// kind) or an inline grant names both or neither of packages and paths.
	InvalidValue Reason = "invalid_value"
)

// The warnings a save returns beside its success (R-CI-010, R-CI-011).
const (
	WarnUnpinnedCondition              Reason = "unpinned_condition"
	WarnRepositoryTemplateUnconstraint Reason = "repository_template_unconstrained"
	WarnSharedOwnership                Reason = "shared_ownership"
	WarnSubjectPattern                 Reason = "sub_pattern"
	WarnMutableUserClaim               Reason = "mutable_user_claim"
	WarnUnguardedTemplateClaim         Reason = "unguarded_template_claim"
	WarnNoPipelineSource               Reason = "write_without_pipeline_source"
	WarnUnprotectedRefsAllowed         Reason = "unprotected_refs_allowed"
	WarnCrossProjectReadOnly           Reason = "cross_project_read_only"
)

// OAuth is the HTTP status and the OAuth `error` member of an exchange reason
// (R-CI-008's table). An unknown reason is 500 server_error.
func (r Reason) OAuth() (status int, oauthError string) {
	switch r {
	case GrantTypeUnsupported:
		return 400, "unsupported_grant_type"
	case ScopeMalformed, ScopeNotGranted:
		return 400, "invalid_scope"
	case AudienceNotThisServer:
		return 400, "invalid_target"
	case RateLimited:
		return 429, "slow_down"
	case SigningKeysUnavailable, GitLabUnreachable, DatabaseUnavailable, ConfigurationChanged:
		return 503, "temporarily_unavailable"
	case MalformedRequest, UnsupportedContentType, UnsupportedSubjectTokenType, UnsupportedRequestedTokenType,
		ActorTokenNotSupported, SubjectTokenMalformed, WrongSubjectTokenType, UnknownIssuer, IssuerDisabled,
		IssuerMismatch, AlgorithmNotAllowed, TokenTypeNotAllowed, UnknownSigningKey, SignatureInvalid,
		AudienceInvalid, AudienceNotExclusive, TokenExpired, TokenNotYetValid, IssuedInFuture,
		RequiredClaimMissing, SubjectTokenReplayed, SubjectTokenPredatesRestore, CrossProjectSourceDenied,
		NoMatchingMapping, GrantRenderFailed, SubjectTokenExpiresTooSoon, TTLBelowMinimum, JobTokenRejected, JobNotRunning:
		return 400, "invalid_request"
	}
	return 500, "server_error"
}

// The codes of a project administrator's mapping (Q-CI-26): groups are global grants, and a policy must
// be the project's own.
const (
	GroupsNotAllowedInProject Reason = "groups_not_allowed_in_project"
	PolicyOutsideProject      Reason = "policy_outside_project"
)
