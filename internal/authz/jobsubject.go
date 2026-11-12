package authz

import (
	"slices"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

// JobSubject is the subject a job carries for the request that queued it: who asked, with which credential, and on which endpoint,
// but none of what the request was decided with (Q-AUTHZ-260). The job that finishes a large delete (R-STOR-018) decides every batch
// on [JobSubject.Subject] with the [Standing] read at that batch, so a group the principal left, a revoked token and a disabled
// principal narrow or stop the job as they narrow or stop a request. It holds no secret.
type JobSubject struct {
	Anonymous bool           `json:"anonymous,omitempty"`
	Kind      principal.Kind `json:"kind,omitempty"`
	// ID is the principal-table ID; empty when anonymous.
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// TokenID and SessionID name the credential the request was authenticated with; at most one is set, neither for an anonymous
	// request. They are what the standing is read for.
	TokenID    string `json:"tokenId,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	AuthMethod string `json:"authMethod,omitempty"`
	// Endpoint and CacheFill are the request's endpoint and its cache_fill setting: the exposure ceiling stays the request's
	// (R-EP-013, H-166).
	Endpoint  string `json:"endpoint,omitempty"`
	CacheFill bool   `json:"cacheFill,omitempty"`
}

// JobSubjectOf is the job's form of the subject of a request: its identity, its credential and its endpoint. The groups, the token
// scope and the CI bundle are left out; the job reads them again for every batch ([Standing]).
func JobSubjectOf(s Subject) JobSubject {
	return JobSubject{
		Anonymous: s.Anonymous, Kind: s.Kind, ID: s.ID, Name: s.Name,
		TokenID: s.TokenID, SessionID: s.SessionID, AuthMethod: s.AuthMethod,
		Endpoint: s.Endpoint, CacheFill: s.CacheFill,
	}
}

// Standing is what the principal and the credential of a [JobSubject] hold at the moment it was read: the principal's kind and name
// and its groups as they are now (R-AUTHZ-002), and the token's scope and a CI token's frozen bundle as its row holds them. The
// authentication layer reads it and refuses to when the principal is no longer active, is no longer of the job's kind, or the
// credential was withdrawn (Q-AUTHZ-261, Q-AUTHZ-262). A session carries no scope and no bundle.
type Standing struct {
	// Kind and Name are the principal's as the principal table holds them now: a rename reaches the job, so an ownership that is
	// decided by name (R-AUTHZ-018) is decided on the current one. Empty for an anonymous caller.
	Kind   principal.Kind
	Name   string
	Groups []principal.GroupRef
	// External is the user's external flag as the principal cache holds it now (R-AUTH-044): a flag set after the request was
	// queued narrows the job as it narrows a request.
	External bool
	Scope    *scope.Scope
	CI       *claims.Frozen
}

// Subject is the subject a batch of the job decides on: the identity, credential and endpoint the job carries, with the kind, the
// name, the groups, the scope ceiling and the CI bundle of the standing read for the batch. A CI token's principal set is the
// mappings of its bundle, as for a request (R-AUTHZ-002).
func (j JobSubject) Subject(now Standing) Subject {
	kind, name := j.Kind, j.Name
	if now.Kind != "" {
		kind, name = now.Kind, now.Name
	}
	s := Subject{
		Anonymous: j.Anonymous, Kind: kind, ID: j.ID, Name: name, External: now.External,
		Groups: slices.Clone(now.Groups), Scope: now.Scope, CI: now.CI,
		TokenID: j.TokenID, SessionID: j.SessionID, AuthMethod: j.AuthMethod,
		Endpoint: j.Endpoint, CacheFill: j.CacheFill,
	}
	if now.CI != nil {
		s.Mappings = slices.Clone(now.CI.Mappings)
	}
	return s
}
