package authz

import (
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// AnonymousID is the identifier of the built-in principal anonymous, the actor of a request without a credential; the
// migration of the principal table seeds it and a database guard keeps it from changing (R-AUTHZ-003). No table that grants
// may name it (SQLSTATE AI009, Q-AUTHZ-279).
const AnonymousID = "00000000-0000-0000-0000-000000000001"

// The kinds of the principal table as the database stores them. They differ
// from principal.Kind only for CI identity mappings.
const (
	dbUser        = "user"
	dbGroup       = "group"
	dbServiceAcct = "service_account"
	dbCIMapping   = "ci_mapping"
	dbAnonymous   = "anonymous"
)

// kindAuthenticated is the kind of the reference "authenticated", which no longer names a principal (Q-AUTHZ-263). It is
// parsed only so that every write path refuses it with 422 principal_not_allowed and a detail that names the visibility
// levels, instead of an unknown kind (Q-AUTHZ-279).
const kindAuthenticated principal.Kind = "authenticated"

// Ref is a principal reference of the API: user:<name>, group:<name>, sa:<name> or
// sa:<projectKey>/<name> for a project's service account, ci:<issuer>/<mapping> or
// ci:<issuer>/<projectKey>/<mapping> for a mapping of a project's tier (R-AUTHZ-002, Q-AUTHZ-209, Q-AUTHZ-214). Name is
// the principal's stored name, the project key included. The references anonymous and authenticated parse but name nothing
// a grant can reach (IsLevelPrincipal).
type Ref struct {
	Kind principal.Kind
	Name string
}

// ParseRef parses a reference. The names are not validated beyond their shape:
// the principal table is the authority on which exist.
func ParseRef(s string) (Ref, error) {
	switch s {
	case "anonymous":
		return Ref{Kind: principal.KindAnonymous, Name: "anonymous"}, nil
	case "authenticated":
		return Ref{Kind: kindAuthenticated, Name: "authenticated"}, nil
	}
	prefix, name, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return Ref{}, fmt.Errorf("%q is not a principal reference (user:<name>, group:<name>, sa:<name>, sa:<project>/<name>, ci:<issuer>/<mapping>, ci:<issuer>/<project>/<name>)", s)
	}
	switch prefix {
	case "user":
		return Ref{Kind: principal.KindUser, Name: name}, nil
	case "group":
		return Ref{Kind: principal.KindGroup, Name: name}, nil
	case "sa":
		return Ref{Kind: principal.KindServiceAccount, Name: name}, nil
	case "ci":
		return Ref{Kind: principal.KindCI, Name: name}, nil
	case "bastion":
		// A Bastion registration is a principal that holds nothing (R-BAS-004): the reference parses so that
		// every write path refuses it with 422 principal_not_allowed instead of an unknown kind.
		return Ref{Kind: principal.KindBastion, Name: name}, nil
	}
	return Ref{}, fmt.Errorf("%q has the unknown principal kind %q", s, prefix)
}

// String renders the reference.
func (r Ref) String() string {
	switch r.Kind {
	case principal.KindAnonymous, kindAuthenticated:
		return string(r.Kind)
	case principal.KindServiceAccount:
		return "sa:" + r.Name
	case principal.KindCI:
		return "ci:" + r.Name
	}
	return string(r.Kind) + ":" + r.Name
}

// refOfRow renders the reference of a principal-table row (kind as stored).
func refOfRow(kind, name string) string {
	switch kind {
	case "":
		return ""
	case dbAnonymous:
		return kind
	case dbServiceAcct:
		return "sa:" + name
	case dbCIMapping:
		return "ci:" + name
	}
	return kind + ":" + name
}

// IsLevelPrincipal reports whether the reference is anonymous or authenticated: the two that a visibility level replaced and
// that no policy entry, role binding, project membership or owner record may name (R-AUTHZ-003, Q-AUTHZ-279).
func (r Ref) IsLevelPrincipal() bool {
	return r.Kind == principal.KindAnonymous || r.Kind == kindAuthenticated
}
