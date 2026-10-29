package authz

import (
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// The identifiers of the two built-in principals; the migration of the
// principal table seeds them and a database guard keeps them from changing
// (R-AUTHZ-003, R-AUTHZ-030).
const (
	AnonymousID     = "00000000-0000-0000-0000-000000000001"
	AuthenticatedID = "00000000-0000-0000-0000-000000000002"
)

// The kinds of the principal table as the database stores them. They differ
// from principal.Kind only for CI identity mappings.
const (
	dbUser          = "user"
	dbGroup         = "group"
	dbServiceAcct   = "service_account"
	dbCIMapping     = "ci_mapping"
	dbAnonymous     = "anonymous"
	dbAuthenticated = "authenticated"
)

// Ref is a principal reference of the API: user:<name>, group:<name>,
// sa:<name>, ci:<issuer>/<mapping>, anonymous, authenticated (R-AUTHZ-002).
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
		return Ref{Kind: principal.KindAuthenticated, Name: "authenticated"}, nil
	}
	prefix, name, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return Ref{}, fmt.Errorf("%q is not a principal reference (user:<name>, group:<name>, sa:<name>, ci:<issuer>/<mapping>, anonymous, authenticated)", s)
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
	}
	return Ref{}, fmt.Errorf("%q has the unknown principal kind %q", s, prefix)
}

// String renders the reference.
func (r Ref) String() string {
	switch r.Kind {
	case principal.KindAnonymous, principal.KindAuthenticated:
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
	case dbAnonymous, dbAuthenticated:
		return kind
	case dbServiceAcct:
		return "sa:" + name
	case dbCIMapping:
		return "ci:" + name
	}
	return kind + ":" + name
}
