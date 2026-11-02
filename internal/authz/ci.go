package authz

import "github.com/guided-traffic/trove/internal/ci/claims"

// CI tokens in the evaluator (R-AUTHZ-014, R-CI-013, R-CI-015), the seam of Q-CI-17. Work package W5
// of docs/developer/ci-oidc.md fills the bodies; this file is the contract.
//
// What a CI token carries is [claims.Frozen], read by the token validator from api_token.ci_context
// into the token cache entry (internal/auth/token.Info.CI) and handed down as Subject.CI. The
// evaluator never queries PostgreSQL for it (R-AUTHZ-015). The rights of the subject are
//
//	(policy grants ∪ group grants ∪ frozen inline grants) ∩ caps ∩ scope
//
// where the policy grants are those of the principals Subject.Mappings name (the grant index maps
// "ci:<issuer>/<mapping>" to the principal row, Index.ci), the group grants are those of the groups
// Subject.CI.Groups name (resolved by name through the index, because the stage resolves no group
// membership for a mapping principal), and the inline grants are [CIGrantSet]. The scope is the
// credential's ceiling as for every token (R-AUTHZ-013). A CI token never has a user identity, never
// receives auto-join groups and never holds a system role (rolesOf already returns none for kindCI).

// CIProject is the project identity of a CI token: the owner identity "ci:<issuer>:project:<id>" of
// package ownership (R-CI-015, R-AUTHZ-018). Issuer is the issuer's name, IssuerID its immutable ID;
// the ownership store keeps the ID, the API shows the name.
type CIProject struct {
	Issuer      string
	IssuerID    string
	ProjectID   string
	ProjectPath string
}

// Ref is the reference of the owner identity, "ci:<issuer>:project:<project_id>".
func (p CIProject) Ref() string { return "ci:" + p.Issuer + ":project:" + p.ProjectID }

// CIProject returns the project identity of a CI subject; ok is false for every other subject and
// for a CI token without a project claim.
func (s Subject) CIProject() (CIProject, bool) {
	if s.CI == nil || s.CI.ProjectID == "" {
		return CIProject{}, false
	}
	return CIProject{Issuer: s.CI.Issuer, IssuerID: s.CI.IssuerID, ProjectID: s.CI.ProjectID, ProjectPath: s.CI.ProjectPath}, true
}

// CIGrantSet is the frozen inline grants of one CI subject compiled against the index of the request:
// selectors resolved to repositories, patterns compiled with the matcher of policies (R-AUTHZ-004,
// R-AUTHZ-005), the caps applied. It is built once per request and per subject, from the token's
// own data, with no query.
type CIGrantSet struct {
	frozen *claims.Frozen
}

// CIGrants compiles the frozen inline grants of a CI subject. A subject without Subject.CI yields an
// empty set that allows nothing. The stub (W5) allows nothing for every subject, which fails closed:
// until W5 builds it a CI token holds only the grants of its policies and groups.
func (x *Index) CIGrants(sub Subject) CIGrantSet { return CIGrantSet{frozen: sub.CI} }

// Allows reports whether a frozen inline grant gives the action on the coordinate of the repository
// with the given ID, under the caps of the token (the cap read removes every other action, R-CI-012).
// The coordinate's package and path are matched as for a policy grant. The stub allows nothing (W5).
func (g CIGrantSet) Allows(_ Action, _ int64, _ Coordinate) bool { return false }

// Claims reports the owners a claim by this subject records and the owner identities it holds for the
// ownership check (R-CI-015, R-AUTHZ-018 rules 1 and 2): the project identity always, and for an
// inline grant with ownership "none" the mapping, whose principal the claim records. A grant with
// ownership bind_project claims the project only.
func (g CIGrantSet) Claims(_ Coordinate) (project CIProject, mappings []string, ok bool) {
	return CIProject{}, nil, false
}
