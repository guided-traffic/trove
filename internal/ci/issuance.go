package ci

import (
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

// Issuance is the record of one exchange (R-CI-020): the link between a CI token and the verified
// job identity it was issued to. It holds an allowlisted claim snapshot, never a token or a hash.
type Issuance struct {
	TokenID      string
	Issuer       string // the issuer's name
	IssuerID     string
	MappingIDs   []string
	MappingNames []string
	SubjectKind  string
	JTI          string

	ProjectID      string
	ProjectPath    string
	NamespaceID    string
	PipelineID     string
	JobID          string
	Ref            string
	RefType        string
	RefProtected   *bool
	PipelineSource string
	Environment    string
	SHA            string
	UserID         string
	UserLogin      string

	Claims map[string]any
	// Grants is the frozen use-time bundle: the same value api_token.ci_context holds (Q-CI-17).
	Grants         claims.Frozen
	RequestedScope string
	GrantedScope   string
	CrossProject   bool
	IssuedAt       time.Time
	ExpiresAt      time.Time
	ClientIP       string
	UserAgent      string

	// Revoked and RevokedAt are read from the token row, which the record joins; a token row deleted
	// by its retention task reads as expired.
	RevokedAt *time.Time
}

// Active reports whether the record's token is unexpired and unrevoked at now.
func (i Issuance) Active(now time.Time) bool { return i.RevokedAt == nil && now.Before(i.ExpiresAt) }

// IssuanceFilter is the query of GET /api/v1/ci/issuances (R-CI-020), newest first.
type IssuanceFilter struct {
	Issuer      string
	ProjectID   string
	ProjectPath string
	PipelineID  string
	JobID       string
	Mapping     string
	// Active selects unexpired and unrevoked records (true) or the others (false); nil both.
	Active   *bool
	From, To *time.Time
	// Cursor and Limit page the list (R-API-005).
	Cursor string
	Limit  int
}

// IssuancePage is one page of a list.
type IssuancePage struct {
	Items      []Issuance
	NextCursor string
}

// Usage is the usage summary of one record, read from the audit store (R-CI-020).
type Usage struct {
	Requests         int64
	LastUsedAt       *time.Time
	PackagesDeployed []string
}

// RevokeFilter selects the tokens of the bulk revoke (R-CI-020): Issuer is required and at least one
// of ProjectID, PipelineID, JobID, Mapping.
type RevokeFilter struct {
	Issuer     string
	ProjectID  string
	PipelineID string
	JobID      string
	Mapping    string
}

// RevokeCause is the cause a ci.token.revoked event carries (R-CI-020).
type RevokeCause string

// The causes of a revocation of a CI token (R-CI-017).
const (
	CauseMapping RevokeCause = "mapping"
	CauseIssuer  RevokeCause = "issuer"
	CauseBulk    RevokeCause = "bulk"
	CauseSingle  RevokeCause = "single"
	CauseSelf    RevokeCause = "self"
)

// Revoked is the result of a revocation: the IDs of the tokens it ended.
type Revoked struct {
	TokenIDs []string
}
