package authz

import (
	"fmt"
	"sort"
)

// ActionSet is a set of the six repository actions of R-AUTHZ-006. No action
// implies another (R-AUTHZ-001): a set holds exactly the bits it was given.
type ActionSet uint8

const (
	bitRead ActionSet = 1 << iota
	bitAnnotate
	bitDeploy
	bitOverwrite
	bitDelete
	bitManage
)

// allActions lists the actions in the alphabetical order the API stores and
// returns them in (R-AUTHZ-001: "annotate", "deploy", "read").
func allActions() []Action {
	return []Action{Annotate, Delete, Deploy, Manage, Overwrite, Read}
}

func (a Action) bit() ActionSet {
	switch a {
	case Read:
		return bitRead
	case Annotate:
		return bitAnnotate
	case Deploy:
		return bitDeploy
	case Overwrite:
		return bitOverwrite
	case Delete:
		return bitDelete
	case Manage:
		return bitManage
	}
	return 0
}

// Valid reports whether a is one of the six actions.
func (a Action) Valid() bool { return a.bit() != 0 }

// Has reports whether the set holds a.
func (s ActionSet) Has(a Action) bool { return a.bit() != 0 && s&a.bit() == a.bit() }

// Contains reports whether the set holds every action of o.
func (s ActionSet) Contains(o ActionSet) bool { return s&o == o }

// Strings returns the actions in alphabetical order.
func (s ActionSet) Strings() []string {
	var out []string
	for _, a := range allActions() {
		if s.Has(a) {
			out = append(out, string(a))
		}
	}
	return out
}

// SetOf builds a set from actions.
func SetOf(actions ...Action) ActionSet {
	var s ActionSet
	for _, a := range actions {
		s |= a.bit()
	}
	return s
}

// virtualActions are the only actions with an effect on a virtual repository
// (R-AUTHZ-004, R-AUTHZ-009): the endpoint gate and the cache invalidation.
const virtualActions = bitRead | bitManage

// presets are the shorthands the API and the UI accept in place of action
// lists; they are expanded when a policy is written and never stored
// (R-AUTHZ-001).
func presets() map[string]ActionSet {
	return map[string]ActionSet{
		"read":     bitRead,
		"write":    bitRead | bitAnnotate | bitDeploy,
		"maintain": bitRead | bitAnnotate | bitDeploy | bitOverwrite | bitDelete,
		"full":     bitRead | bitAnnotate | bitDeploy | bitOverwrite | bitDelete | bitManage,
	}
}

// ParseActions expands a list of actions and presets into a set. A name that is
// neither is an invalid_action problem (R-AUTHZ-021); the second result names
// each offender.
func ParseActions(names []string) (ActionSet, []string) {
	var s ActionSet
	var bad []string
	p := presets()
	for _, n := range names {
		if set, ok := p[n]; ok {
			s |= set
			continue
		}
		if a := Action(n); a.Valid() {
			s |= a.bit()
			continue
		}
		bad = append(bad, n)
	}
	sort.Strings(bad)
	return s, bad
}

// actionsFromStored reads the stored form of an entry; a value outside the six
// actions cannot occur (the table's CHECK) and is reported as an error.
func actionsFromStored(names []string) (ActionSet, error) {
	var s ActionSet
	for _, n := range names {
		a := Action(n)
		if !a.Valid() {
			return 0, fmt.Errorf("authz: stored action %q is not one of the six", n)
		}
		s |= a.bit()
	}
	return s, nil
}
