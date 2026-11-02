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
	// a mapping that did not match.
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

// Selection is the result of steps 4 to 7 of the exchange on one mapping set (R-CI-007): the
// classification, the trace, the contributing mappings with their rendered grants and caps, one
// contribution each. It is a pure function of the mapping set and the claims, so the dry run and the
// exchange share it.
type Selection struct {
	CrossProject bool
	Trace        []TraceEntry
	// Contributions are the contributing mappings in evaluation order, at most one per tier. There is no
	// union of their grants here: the bundle keeps them apart.
	Contributions []Contribution
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
// `read` for a read_only opt-in. It records the trace of every mapping it looked at.
//
// The first match of a tier decides the tier, also when it does not contribute: a cross-project token
// whose first match in a tier has the default `deny` gets nothing from that tier and does not fall
// through to a later, possibly broader mapping (R-CI-012, R-CI-009). The set's own order is not
// relied on.
func Select(set MappingSet, kind string, c claims.Claims) Selection {
	sel := Selection{CrossProject: c.CrossProject()}
	ordered := slices.Clone(set.Mappings)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Tier != ordered[j].Tier {
			return tierLess(ordered[i].Tier, ordered[j].Tier)
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	decided := map[string]bool{}
	matched := false
	for _, m := range ordered {
		if !m.Enabled || !slices.Contains(m.SubjectKinds, kind) || decided[m.Tier] {
			continue
		}
		ev := m.Match.Evaluate(c)
		entry := TraceEntry{Tier: m.Tier, Mapping: m.Name, Matched: ev.Matched, FailedLeaf: ev.FailedLeaf, Detail: ev.Detail, Leaves: ev.Leaves}
		if !ev.Matched {
			sel.Trace = append(sel.Trace, entry)
			continue
		}
		decided[m.Tier], matched = true, true
		var caps []claims.Cap
		if sel.CrossProject {
			if m.Options.CrossProjectSource != claims.CrossProjectReadOnly {
				sel.Trace = append(sel.Trace, entry)
				continue
			}
			caps = []claims.Cap{claims.CapRead}
		}
		entry.Contributes = true
		rendered, err := m.Grants.Render(c)
		if err != nil {
			entry.Detail = renderDetail(err)
			sel.Trace = append(sel.Trace, entry)
			sel.Failure, sel.Detail = reason.GrantRenderFailed, "the grants of the mapping "+m.Name+" cannot be rendered: "+entry.Detail
			return sel
		}
		sel.Trace = append(sel.Trace, entry)
		sel.Contributions = append(sel.Contributions, Contribution{Mapping: m, Rendered: rendered, Caps: caps})
	}
	switch {
	case len(sel.Contributions) > 0:
	case matched:
		sel.Failure, sel.Detail = reason.CrossProjectSourceDenied, "the matched mappings do not opt in to cross-project pipelines (options.crossProjectSource)"
	default:
		sel.Failure, sel.Detail = reason.NoMatchingMapping, "no enabled mapping of the issuer matches the claims"
	}
	return sel
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
