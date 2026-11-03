package ci

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// TraceEntry is one mapping in the decision trace of an evaluation (R-CI-019): the first failed leaf
// as the spec's sketch shows it, and every leaf for the claim tester (R-UI-036).
type TraceEntry struct {
	Tier    string
	Mapping string
	Matched bool
	// Contributes is false for a matched mapping whose cross_project_source is deny on a
	// cross-project token (R-CI-012): it is not one of the token's mappings. It is also false for
	// a mapping that did not match, and for a matched one whose grants did not render (Detail names
	// the failure; a project tier's is Selection.Dropped, Q-CI-47).
	Contributes bool
	FailedLeaf  string
	Detail      string
	Leaves      []claims.LeafResult
}

// Contribution is what one contributing mapping gives a token (R-CI-009, R-CI-012): the mapping, what
// its grants render to for the claims and the caps of its cross-project opt-in. The contributions stay
// separate because a cap bounds only its own mapping's grants and an inline grant is compiled against
// its own tier's project (Q-CI-17 as amended); [Contribution.Frozen] builds the entry of the token's
// bundle.
type Contribution struct {
	Mapping  Mapping
	Rendered claims.Rendered
	Caps     []claims.Cap
}

// Frozen is the contribution as the token's bundle carries it: the inline grants bounded by the caps,
// the groups, the caps themselves (claims.Freeze) and, for a project tier, the project.
func (c Contribution) Frozen() claims.FrozenMapping {
	var project *claims.FrozenProject
	if c.Mapping.Project != "" {
		project = &claims.FrozenProject{Key: c.Mapping.Project, ID: c.Mapping.ProjectID}
	}
	return claims.Freeze(c.Mapping.Ref, c.Mapping.ID, c.Mapping.Tier, project, c.Rendered, c.Caps)
}

// Dropped is a matched mapping of a project tier whose grants did not render (R-CI-009 step 6, Q-CI-47): its tier
// gives the token nothing and does not fall through to its next mapping, and the other tiers still give what they
// give. Detail names the pattern and the claim, never a claim value.
type Dropped struct {
	Mapping Mapping
	Reason  Reason
	Detail  string
}

// Selection is the result of steps 4 to 7 of the exchange on one mapping set (R-CI-007): the
// classification, the trace, the contributing mappings with their rendered grants and caps, one
// contribution each. It is a pure function of the mapping set and the claims, so the dry run and the
// exchange share it.
type Selection struct {
	CrossProject bool
	// Trace is the dry run's ([SelectTraced]); the exchange's selection has none ([Select], Q-CI-49).
	Trace []TraceEntry
	// Contributions are the contributing mappings in evaluation order, at most one per tier. There is no
	// union of their grants here: the bundle keeps them apart.
	Contributions []Contribution
	// Dropped are the project tiers whose match did not render (Q-CI-47), in evaluation order.
	Dropped []Dropped
	// Failure is no_matching_mapping, cross_project_source_denied or grant_render_failed, "" on a
	// success; Detail names the mapping or the claim and holds no claim value.
	Failure Reason
	Detail  string
}

// Mappings returns the contributing mappings, in evaluation order.
func (s Selection) Mappings() []Mapping {
	out := make([]Mapping, 0, len(s.Contributions))
	for _, c := range s.Contributions {
		out = append(out, c.Mapping)
	}
	return out
}

// tierLess orders the tiers of one evaluation: the administrators' tier first, then the projects' by
// key. The order only fixes the trace and the order of the bundle; tiers combine by union.
func tierLess(a, b string) bool {
	if (a == TierSystem) != (b == TierSystem) {
		return a == TierSystem
	}
	return a < b
}

// Select evaluates the mapping set for a subject token of the given kind (R-CI-009): the enabled
// mappings whose subject_kinds include it, per tier in ascending priority, the first match per tier;
// the cross-project classification and the per-mapping opt-in (R-CI-012); the rendering of the
// matched mappings' grants (R-CI-013, R-CI-014) without falling through on a render failure; the cap
// `read` for a read_only opt-in. It is the exchange's: the conditions the set holds compiled, evaluated
// with short-circuit and without a trace (Q-CI-49). [SelectTraced] is the same decision with the trace of
// every mapping it looked at, for the dry run.
//
// The first match of a tier decides the tier, also when it does not contribute: a cross-project token
// whose first match in a tier has the default `deny` gets nothing from that tier and does not fall
// through to a later, possibly broader mapping (R-CI-012, R-CI-009). The set's own order is not
// relied on.
//
// A render failure never falls through to the tier's next mapping (R-CI-009 step 6). What it fails is the
// tier's (Q-CI-47): a failure of the system tier, the administrators', fails the selection with
// grant_render_failed whatever the projects' tiers give, so a broken administrators' mapping is never
// silently replaced by what a project grants; a failure of a project's tier drops that tier's contribution
// (Selection.Dropped) and the selection fails with grant_render_failed only when no tier contributes.
func Select(set MappingSet, kind string, c claims.Claims) Selection {
	return selectOn(set, kind, c, false)
}

// SelectTraced is [Select] with the trace of the dry run (R-CI-019, R-UI-036): every leaf of every mapping it
// looked at.
func SelectTraced(set MappingSet, kind string, c claims.Claims) Selection {
	return selectOn(set, kind, c, true)
}

func selectOn(set MappingSet, kind string, c claims.Claims, traced bool) Selection {
	sel := Selection{CrossProject: c.CrossProject()}
	decided := map[string]bool{}
	matched := false
	trace := func(e TraceEntry) {
		if traced {
			sel.Trace = append(sel.Trace, e)
		}
	}
	for _, m := range set.ordered() {
		if !m.Enabled || !slices.Contains(m.SubjectKinds, kind) || decided[m.Tier] {
			continue
		}
		entry := TraceEntry{Tier: m.Tier, Mapping: m.Name}
		if traced {
			ev := m.Match.Evaluate(c)
			entry.Matched, entry.FailedLeaf, entry.Detail, entry.Leaves = ev.Matched, ev.FailedLeaf, ev.Detail, ev.Leaves
		} else {
			entry.Matched = set.condition(m).Matches(c)
		}
		if !entry.Matched {
			trace(entry)
			continue
		}
		decided[m.Tier], matched = true, true
		var caps []claims.Cap
		if sel.CrossProject {
			if m.Options.CrossProjectSource != claims.CrossProjectReadOnly {
				trace(entry)
				continue
			}
			caps = []claims.Cap{claims.CapRead}
		}
		rendered, err := m.Grants.Render(c)
		if err != nil {
			entry.Detail = renderDetail(err)
			trace(entry)
			if m.Tier == TierSystem {
				sel.Contributions, sel.Dropped = nil, nil
				sel.Failure, sel.Detail = reason.GrantRenderFailed, "the grants of the mapping "+m.Name+" cannot be rendered: "+entry.Detail
				return sel
			}
			sel.Dropped = append(sel.Dropped, Dropped{Mapping: m, Reason: reason.GrantRenderFailed, Detail: entry.Detail})
			continue
		}
		entry.Contributes = true
		trace(entry)
		sel.Contributions = append(sel.Contributions, Contribution{Mapping: m, Rendered: rendered, Caps: caps})
	}
	switch {
	case len(sel.Contributions) > 0:
	case len(sel.Dropped) > 0:
		d := sel.Dropped[0]
		sel.Failure, sel.Detail = reason.GrantRenderFailed, "the grants of the mapping "+d.Mapping.Name+" cannot be rendered: "+d.Detail
	case matched:
		sel.Failure, sel.Detail = reason.CrossProjectSourceDenied, "the matched mappings do not opt in to cross-project pipelines (options.crossProjectSource)"
	default:
		sel.Failure, sel.Detail = reason.NoMatchingMapping, "no enabled mapping of the issuer matches the claims"
	}
	return sel
}

// sortMappings orders mappings in evaluation order: the tiers by tierLess, each by ascending priority.
func sortMappings(ms []Mapping) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Tier != ms[j].Tier {
			return tierLess(ms[i].Tier, ms[j].Tier)
		}
		return ms[i].Priority < ms[j].Priority
	})
}

// renderDetail is the sentence of a render failure: the pointer of the pattern, then the claim it names.
func renderDetail(err error) string {
	var e *claims.Error
	if errors.As(err, &e) {
		if e.Pointer != "" {
			return "grants" + e.Pointer + ": " + e.Detail
		}
		return e.Detail
	}
	return strings.TrimSpace(err.Error())
}

// unionRendered is the display of a selection: the rendered grants of every contribution under its caps,
// policies and groups once each, in evaluation order. The token's bundle never uses it.
func unionRendered(contributions []Contribution) claims.Rendered {
	var out claims.Rendered
	for _, c := range contributions {
		r := claims.ApplyCaps(c.Rendered, c.Caps)
		for _, p := range r.Policies {
			if !slices.Contains(out.Policies, p) {
				out.Policies = append(out.Policies, p)
			}
		}
		for _, g := range r.Groups {
			if !slices.Contains(out.Groups, g) {
				out.Groups = append(out.Groups, g)
			}
		}
		out.Inline = append(out.Inline, r.Inline...)
	}
	return out
}
