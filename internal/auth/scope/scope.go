// Package scope is the value type of a token's scope (R-AUTH-024, R-AUTHZ-013):
// the ceiling a token puts on its owner's rights. The authentication stage
// carries it from the token row to the request, the decision point applies it,
// and the token service parses, validates, stores and derives it; all of them
// use this one type. It is a leaf: it names no area, and the checks that need
// knowledge of one (the actions, the system items, the configured endpoints, the
// pattern syntax) arrive as [Options].
package scope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// Patterns is an include and exclude list of globs (R-AUTHZ-005).
type Patterns struct {
	Include []string
	Exclude []string
}

// Grant is one element of the repository part of a scope.
type Grant struct {
	// Repositories are keys or selector patterns (R-AUTHZ-004).
	Repositories []string
	Packages     Patterns
	Paths        Patterns
	// Actions are the repository actions of R-AUTHZ-006.
	Actions []string
}

// Scope is the validated scope of a token (R-AUTH-024). It narrows the owner's
// rights and never widens them. Builds join with P1.
type Scope struct {
	// Inherit admits every repository right of the owner.
	Inherit bool
	// Admin lets the token exercise the owner's system roles.
	Admin bool
	// Repositories are the restricted grants when Inherit is false.
	Repositories []Grant
	// System lists system capabilities (R-AUTHZ-010) from the closed list of R-AUTH-025.
	System []string
	// Endpoints binds the token to endpoint names (R-EP-011); empty means unbound.
	Endpoints []string
}

// Limits of a scope (R-AUTH-024).
const (
	maxGrants     = 50
	maxScopeBytes = 16 << 10
	maxEndpoints  = 16
)

// Options are the checks that need knowledge of other areas. A zero value
// accepts no action and no system item, and any syntactically valid endpoint
// name. The composition root builds them from the authorization area's lists and
// validators, so a scope is judged by the rules that apply it (R-AUTH-024).
type Options struct {
	// Action reports whether a name is one of the repository actions of
	// R-AUTHZ-006.
	Action func(action string) bool
	// SystemItem reports whether an item is a system capability of R-AUTHZ-010.
	SystemItem func(item string) bool
	// Endpoint reports whether an endpoint name is configured (R-EP-011); nil
	// accepts every name of the endpoint-name syntax.
	Endpoint func(name string) bool
	// Pattern validates a repository selector ("repositories"), a package-name
	// pattern ("packages") or a path pattern ("paths") with the syntax and the
	// length limit of R-AUTHZ-004 and R-AUTHZ-005; nil only checks that the text is
	// not empty and holds no control character.
	Pattern func(dimension, pattern string) error
}

// wire forms of the scope; they carry the JSON names of R-AUTH-024.
type wirePatterns struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type wireGrant struct {
	Repositories []string      `json:"repositories"`
	Packages     *wirePatterns `json:"packages,omitempty"`
	Paths        *wirePatterns `json:"paths,omitempty"`
	Actions      []string      `json:"actions"`
}

type wireScope struct {
	Inherit      bool        `json:"inherit"`
	Admin        bool        `json:"admin"`
	Repositories []wireGrant `json:"repositories"`
	System       []string    `json:"system"`
	Endpoints    []string    `json:"endpoints,omitempty"`
}

// Default is the scope of a token created without one: all rights of the
// owner (R-AUTH-024).
func Default() Scope { return Scope{Inherit: true} }

func invalidScope(format string, args ...any) error {
	return errs.E(errs.Invalid, errs.CodeInvalidScope, fmt.Sprintf("The scope is invalid: "+format, args...))
}

// Parse parses and validates the scope of a create request (R-AUTH-024):
// unknown fields and actions, an inherit scope with grants, more than 50 grants
// or 16 KiB, unknown endpoints and system items answer 400 invalid_scope. An
// empty document is the default scope.
func Parse(raw []byte, o Options) (Scope, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return Default(), nil
	}
	if len(raw) > maxScopeBytes {
		return Scope{}, invalidScope("larger than %d bytes", maxScopeBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wireScope
	if err := dec.Decode(&w); err != nil {
		return Scope{}, invalidScope("%v", err)
	}
	if dec.More() {
		return Scope{}, invalidScope("trailing data")
	}
	s := fromWire(w)
	if err := validate(s, o); err != nil {
		return Scope{}, err
	}
	if len(s.Marshal()) > maxScopeBytes {
		return Scope{}, invalidScope("larger than %d bytes", maxScopeBytes)
	}
	return s, nil
}

func validate(s Scope, o Options) error {
	if s.Inherit && len(s.Repositories) > 0 {
		return invalidScope("inherit excludes repository grants")
	}
	if len(s.Repositories) > maxGrants {
		return invalidScope("more than %d grants", maxGrants)
	}
	for i, g := range s.Repositories {
		if len(g.Repositories) == 0 {
			return invalidScope("grant %d names no repository", i)
		}
		for _, r := range g.Repositories {
			if err := checkPattern(o, "repositories", r); err != nil {
				return err
			}
		}
		if len(g.Actions) == 0 {
			return invalidScope("grant %d names no action", i)
		}
		for _, a := range g.Actions {
			if o.Action == nil || !o.Action(a) {
				return invalidScope("unknown action %q", a)
			}
		}
		for dim, p := range map[string]Patterns{"packages": g.Packages, "paths": g.Paths} {
			for _, pat := range append(slices.Clone(p.Include), p.Exclude...) {
				if err := checkPattern(o, dim, pat); err != nil {
					return err
				}
			}
		}
	}
	for _, item := range s.System {
		if o.SystemItem == nil || !o.SystemItem(item) {
			return invalidScope("unknown system item %q", item)
		}
	}
	if len(s.Endpoints) > maxEndpoints {
		return invalidScope("more than %d endpoints", maxEndpoints)
	}
	for _, e := range s.Endpoints {
		ok := config.ValidEndpointName(e)
		if o.Endpoint != nil {
			ok = o.Endpoint(e)
		}
		if !ok {
			return invalidScope("unknown endpoint %q", e)
		}
	}
	return nil
}

func checkPattern(o Options, dimension, p string) error {
	if p == "" || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return invalidScope("empty or invalid %s pattern", dimension)
	}
	if o.Pattern != nil {
		if err := o.Pattern(dimension, p); err != nil {
			return invalidScope("%s pattern %q: %v", dimension, p, err)
		}
	}
	return nil
}

func fromWire(w wireScope) Scope {
	s := Scope{Inherit: w.Inherit, Admin: w.Admin, System: w.System, Endpoints: w.Endpoints}
	for _, g := range w.Repositories {
		sg := Grant{Repositories: g.Repositories, Actions: g.Actions}
		if g.Packages != nil {
			sg.Packages = Patterns{Include: g.Packages.Include, Exclude: g.Packages.Exclude}
		}
		if g.Paths != nil {
			sg.Paths = Patterns{Include: g.Paths.Include, Exclude: g.Paths.Exclude}
		}
		s.Repositories = append(s.Repositories, sg)
	}
	return s
}

func toWire(s Scope) wireScope {
	w := wireScope{Inherit: s.Inherit, Admin: s.Admin, System: s.System, Endpoints: s.Endpoints, Repositories: []wireGrant{}}
	if w.System == nil {
		w.System = []string{}
	}
	for _, g := range s.Repositories {
		wg := wireGrant{Repositories: g.Repositories, Actions: g.Actions}
		if len(g.Packages.Include)+len(g.Packages.Exclude) > 0 {
			wg.Packages = &wirePatterns{Include: g.Packages.Include, Exclude: g.Packages.Exclude}
		}
		if len(g.Paths.Include)+len(g.Paths.Exclude) > 0 {
			wg.Paths = &wirePatterns{Include: g.Paths.Include, Exclude: g.Paths.Exclude}
		}
		w.Repositories = append(w.Repositories, wg)
	}
	return w
}

// Marshal renders the scope in its stored and returned form.
func (s Scope) Marshal() []byte {
	b, err := json.Marshal(toWire(s))
	if err != nil {
		panic(err) // strings and booleans only
	}
	return b
}

// ParseStored reads a scope from the database. It does not validate: the
// row was validated when it was written, and a scope an older build wrote must
// not turn a valid token away.
func ParseStored(raw []byte) (Scope, error) {
	var w wireScope
	if err := json.Unmarshal(raw, &w); err != nil {
		return Scope{}, err
	}
	return fromWire(w), nil
}

// Within reports whether s is no broader than parent, the derivation rule of
// R-AUTH-027: each grant must equal a presenting grant with a subset of its
// actions, and inherit, admin and system items may only be kept or dropped. A
// parent bound to endpoints requires a non-empty subset of them.
func (s Scope) Within(parent Scope) bool {
	if s.Inherit && !parent.Inherit {
		return false
	}
	if s.Admin && !parent.Admin {
		return false
	}
	if !subset(s.System, parent.System) && !parent.Admin {
		return false
	}
	if !parent.Inherit {
		for _, g := range s.Repositories {
			if !slices.ContainsFunc(parent.Repositories, func(pg Grant) bool { return grantWithin(g, pg) }) {
				return false
			}
		}
	}
	if len(parent.Endpoints) > 0 && (len(s.Endpoints) == 0 || !subset(s.Endpoints, parent.Endpoints)) {
		return false
	}
	return true
}

func grantWithin(g, parent Grant) bool {
	return slices.Equal(g.Repositories, parent.Repositories) &&
		slices.Equal(g.Packages.Include, parent.Packages.Include) && slices.Equal(g.Packages.Exclude, parent.Packages.Exclude) &&
		slices.Equal(g.Paths.Include, parent.Paths.Include) && slices.Equal(g.Paths.Exclude, parent.Paths.Exclude) &&
		subset(g.Actions, parent.Actions)
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
