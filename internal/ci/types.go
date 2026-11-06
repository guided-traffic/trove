package ci

import (
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
)

// Reason is a reason name of the exchange or a management `code` (internal/ci/reason).
type Reason = reason.Reason

// Kinds of CI issuer (R-CI-001). github and generic are P1 (R-CI-025, R-CI-026): a create with them
// answers 422 kind_not_available.
const (
	KindGitLab  = "gitlab"
	KindGitHub  = "github"
	KindGeneric = "generic"
)

// Sources of a CI issuer (R-CI-001, Q-CI-16).
const (
	SourceAPI           = "api"
	SourceConfiguration = "configuration"
)

// The tier of administrator mappings; P1 adds "delegation:<uuid>" (R-CI-023).
const TierSystem = "system"

// ProjectTier is the tier of the mappings of one project (Q-CI-26): "project:<key>". Tiers combine by
// union and each is first-match inside, so a project's mapping can neither shadow a system mapping nor
// another project's.
func ProjectTier(key string) string { return "project:" + key }

// The subject kinds of a mapping (R-CI-009); job_token is P2 (R-CI-022).
const (
	SubjectIDToken  = "id_token"
	SubjectJobToken = "job_token"
)

// JobTokenFallback is the job_token_fallback setting of an issuer (R-CI-022, P2). The MVP stores it
// and refuses enabled: true with 422 job_token_fallback_not_available, so the column keeps room for
// the fallback without a migration.
type JobTokenFallback struct {
	Enabled bool `json:"enabled"`
}

// Issuer is a CI issuer as the API and the services read it (R-CI-001, R-CI-002). Durations are
// integer seconds on the wire (ClockSkewSeconds, MaxTTLSeconds); the store keeps intervals.
type Issuer struct {
	ID          string
	Name        string
	Kind        string
	Issuer      string
	Description string
	Enabled     bool

	// DiscoveryURL and JWKSURI are nil for the defaults (<issuer>/.well-known/openid-configuration,
	// the discovery document's jwks_uri).
	DiscoveryURL *string
	JWKSURI      *string

	AllowedAudiences         []string
	ExclusiveAudience        bool
	AllowedAlgorithms        []string
	AllowedTyp               []string
	AllowMissingTyp          bool
	ClockSkewSeconds         int
	RequiredClaims           []string
	PublicIssuer             bool
	PinClaims                []string
	SingleUseSubjectTokens   bool
	MaxTTLSeconds            *int64
	CABundleRef              *string
	UseOutboundProxy         bool
	JobTokenFallback         JobTokenFallback
	Source                   string
	RemovedFromConfiguration bool

	// Version is the ETag (R-CI-002). MappingsVersion is internal and never shown in the API; the
	// exchange compares it (R-CI-007, Q-CI-15).
	Version         int64
	MappingsVersion int64
	CreatedAt       time.Time
	UpdatedAt       time.Time

	// MappingCount and Keys are filled by List and Get only.
	MappingCount int
	Keys         *KeySummary
}

// Settings is the part of the issuer the subject-token validation reads (R-CI-004).
func (i Issuer) Settings() subject.Settings {
	return subject.Settings{
		Issuer: i.Issuer, AllowedAudiences: i.AllowedAudiences, ExclusiveAudience: i.ExclusiveAudience,
		AllowedAlgorithms: i.AllowedAlgorithms, AllowedTyp: i.AllowedTyp, AllowMissingTyp: i.AllowMissingTyp,
		ClockSkew: time.Duration(i.ClockSkewSeconds) * time.Second, RequiredClaims: i.RequiredClaims,
	}
}

// IssuerInput is the request body of POST and PUT of an issuer (R-CI-002): the full representation.
// A nil optional field takes its default, so a PUT is a full replacement; name and issuer of a PUT
// must equal the stored ones. The read-only members of a GET body are ignored by the decoder.
type IssuerInput struct {
	Name        string
	Kind        string
	Issuer      string
	Description string
	Enabled     *bool

	DiscoveryURL *string
	JWKSURI      *string

	// AllowedAudiences nil: [<server.base_url>, "npm:<host>"] filled at create time (R-CI-001).
	AllowedAudiences       []string
	ExclusiveAudience      *bool
	AllowedAlgorithms      []string
	AllowedTyp             []string
	AllowMissingTyp        *bool
	ClockSkewSeconds       *int
	RequiredClaims         []string
	PublicIssuer           *bool
	PinClaims              []string
	SingleUseSubjectTokens *bool
	MaxTTLSeconds          *int64
	CABundleRef            *string
	UseOutboundProxy       *bool
	JobTokenFallback       *JobTokenFallback
}

// DeclaredIssuer is an issuer of ci.issuers in the system configuration (R-CI-001, Q-CI-16): the
// same fields in snake_case, applied by `trove migrate` with source "configuration".
type DeclaredIssuer = IssuerInput

// KeyInfo is one key of a stored key set as the key status shows it (R-CI-002).
type KeyInfo struct {
	KID string
	KTY string
	Alg string
	// Ignored keys do not meet the key rules of R-CI-003 and are reported with the reason.
	Ignored       bool
	IgnoredReason string
}

// KeySummary is the short form on the issuer list: the count and the last fetch or error.
type KeySummary struct {
	Usable      int
	Ignored     int
	FetchedAt   *time.Time
	LastError   string
	LastErrorAt *time.Time
}

// KeyStatus is the answer of GET …/keys and POST …/keys/refresh (R-CI-002, R-CI-003). It holds the
// failure class, the HTTP status and the host of a failed fetch, never bytes of a response body.
type KeyStatus struct {
	Issuer        string
	JWKSURI       string
	Keys          []KeyInfo
	FetchedAt     *time.Time
	NextRefreshAt *time.Time
	LastError     string
	LastErrorAt   *time.Time
}

// RefreshTrigger is the trigger label of trove_ci_signing_keys_refresh_total (R-CI-020).
type RefreshTrigger string

// The triggers.
const (
	TriggerScheduled  RefreshTrigger = "scheduled"
	TriggerUnknownKID RefreshTrigger = "unknown_kid"
	TriggerAdmin      RefreshTrigger = "admin"
)

// Mapping is an identity mapping (R-CI-009, R-CI-019). Match, Grants and Options are the stored
// forms of ci_mappings.match, .grants and .options (lowerCamelCase JSON, the wire form).
type Mapping struct {
	ID          string
	IssuerID    string
	Issuer      string // the issuer's name
	PrincipalID string
	// Ref is "ci:<issuer>/<mapping>" (R-CI-009, Q-CI-10).
	Ref  string
	Name string
	Tier string
	// Project is the project key of a project-tier mapping and ProjectID its ID, "" otherwise.
	Project       string
	ProjectID     string
	Priority      int
	Enabled       bool
	SubjectKinds  []string
	Match         claims.Node
	Grants        claims.Spec
	MaxTTLSeconds *int64
	Options       claims.Options
	Description   string
	Version       int64
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PrincipalName is the name of the mapping's principal: "<issuer>/<name>" for a mapping of the system
// tier, "<issuer>/<project>/<name>" for one of a project's tier (R-CI-009, Q-AUTHZ-214).
func (m Mapping) PrincipalName() string {
	return principalNameOf(m.Issuer, m.Project, m.Name)
}

// ShortName is the mapping's name without its issuer, as the exchange response and the issuance filters
// write it: "<name>", or "<project>/<name>" for a mapping of a project's tier (R-CI-007).
func (m Mapping) ShortName() string {
	if m.Project == "" {
		return m.Name
	}
	return m.Project + "/" + m.Name
}

// MappingOptions are the options of a mapping (R-CI-011, R-CI-012).
type MappingOptions = claims.Options

// MappingInput is the request body of POST and PUT of a mapping (R-CI-019). A nil optional field
// takes its default (enabled true, subjectKinds ["id_token"], options deny and false); the name of a
// PUT must equal the stored one.
type MappingInput struct {
	// Issuer names the issuer of a project mapping's create (Q-CI-26); the issuer routes carry it in the
	// path and leave it empty.
	Issuer        string
	Name          string
	Description   string
	Priority      int
	Enabled       *bool
	SubjectKinds  []string
	Match         claims.Node
	Grants        claims.Spec
	MaxTTLSeconds *int64
	Options       *MappingOptions
}

// MappingResult is a saved mapping with the warnings of its save (R-CI-010, R-CI-011).
type MappingResult struct {
	Mapping  Mapping
	Warnings []claims.Warning
}

// IssuerChoice is what a project Owner sees of an issuer (Q-CI-26): enough to choose one.
type IssuerChoice struct {
	Name         string
	Kind         string
	Issuer       string
	PublicIssuer bool
}

// MappingSet is the mapping set of one issuer as a replica evaluates it: the enabled and the
// disabled mappings in evaluation order (tier, priority) with the issuer versions they were read at
// (R-CI-009, Q-CI-15). The exchange compares MappingsVersion with the issuer row it locks.
type MappingSet struct {
	IssuerID        string
	IssuerVersion   int64
	MappingsVersion int64
	Mappings        []Mapping

	// conds holds the compiled condition of every mapping by ID, and sorted says Mappings is in evaluation
	// order: the state the copy owns (newMappingSet, Q-CI-49, R-ARCH-032), so an exchange compiles and sorts
	// nothing. A set built by hand has neither; Select then compiles and sorts per call.
	conds  map[string]claims.Condition
	sorted bool
}

// newMappingSet is a mapping set as a replica holds it: the mappings in evaluation order, each condition compiled
// once (Q-CI-49).
func newMappingSet(issuerID string, issuerVersion, mappingsVersion int64, ms []Mapping) MappingSet {
	ordered := slices.Clone(ms)
	sortMappings(ordered)
	conds := make(map[string]claims.Condition, len(ordered))
	for _, m := range ordered {
		conds[m.ID] = claims.CompileCondition(m.Match)
	}
	return MappingSet{IssuerID: issuerID, IssuerVersion: issuerVersion, MappingsVersion: mappingsVersion, Mappings: ordered, conds: conds, sorted: true}
}

// ordered returns the mappings in evaluation order; the slice of a set that is sorted is shared and never changed.
func (s MappingSet) ordered() []Mapping {
	if s.sorted {
		return s.Mappings
	}
	out := slices.Clone(s.Mappings)
	sortMappings(out)
	return out
}

// condition returns the compiled condition of a mapping of the set, compiling it when the set holds none.
func (s MappingSet) condition(m Mapping) claims.Condition {
	if c, ok := s.conds[m.ID]; ok {
		return c
	}
	return claims.CompileCondition(m.Match)
}

// Actor is who performs a management write: the caller of the request, as the audit event records
// it, the same value the project service takes (internal/project).
type Actor = authz.Subject

// OnlyTier returns the set restricted to the mappings of one tier, for the dry run of a project
// administrator, who must not see the trace of any other tier (Q-CI-26).
func (s MappingSet) OnlyTier(tier string) MappingSet {
	out := s
	out.Mappings = nil
	for _, m := range s.Mappings {
		if m.Tier == tier {
			out.Mappings = append(out.Mappings, m)
		}
	}
	return out
}
