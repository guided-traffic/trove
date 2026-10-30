package token

import "github.com/guided-traffic/trove/internal/auth/scope"

// The scope is defined once, in internal/auth/scope; these names are the same
// types and calls under the spelling the token area has always used, so the
// identity packages compile unchanged while they are under change. New code
// imports scope. Remove this file when no caller of the token package spells
// them any more.
type (
	Scope        = scope.Scope
	ScopeGrant   = scope.Grant
	Patterns     = scope.Patterns
	ScopeOptions = scope.Options
)

// DefaultScope is [scope.Default].
func DefaultScope() Scope { return scope.Default() }

// ParseScope is [scope.Parse].
func ParseScope(raw []byte, o ScopeOptions) (Scope, error) { return scope.Parse(raw, o) }

// ParseStoredScope is [scope.ParseStored].
func ParseStoredScope(raw []byte) (Scope, error) { return scope.ParseStored(raw) }
