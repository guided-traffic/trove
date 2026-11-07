package claims

import (
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/qname"
)

// ScopeItem is one item of the `scope` request parameter of the exchange (R-CI-007, Q-CI-2):
// `<action>` or `<action>:<repository selector>`. An item without selector stands for `@any`.
type ScopeItem struct {
	Action   Action
	Selector string
}

// String is the item in the request grammar.
func (i ScopeItem) String() string {
	if i.Selector == "" {
		return string(i.Action)
	}
	return string(i.Action) + ":" + i.Selector
}

// selector is the item's selector, "@any" for an item without one.
func (i ScopeItem) selector() string {
	if i.Selector == "" {
		return "@any"
	}
	return i.Selector
}

// MaxScopeItems is the number of distinct items a request may carry (the grant limit of R-AUTH-024).
const MaxScopeItems = 50

func scopeMalformed(detail string) *Error { return refuse(reason.ScopeMalformed, "", detail) }

// ParseScope parses the space-delimited scope string: items are case-sensitive and split at the first
// ":"; duplicates are dropped; an unknown action, `manage`, an empty item or more than MaxScopeItems
// items is an *Error with reason scope_malformed. The selector is not resolved here: the exchange
// checks that a key selector names an existing repository (scope_malformed).
//
// The empty string is no scope (nil items). An item holds only the characters of an OAuth scope
// token (RFC 6749 §3.3: printable ASCII without `"` and `\`), and its selector the syntax of
// R-AUTHZ-004 as every instance context writes it (`<project>/<key>`, `<project>/<key pattern>`, a class,
// a format filter on a pattern or class; a bare key is scope_malformed).
func ParseScope(s string) ([]ScopeItem, error) {
	if s == "" {
		return nil, nil
	}
	var out []ScopeItem
	seen := map[string]bool{}
	for i, raw := range strings.Split(s, " ") {
		item, err := parseScopeItem(raw, i)
		if err != nil {
			return nil, err
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, item)
		if len(out) > MaxScopeItems {
			return nil, scopeMalformed("the scope holds more than " + strconv.Itoa(MaxScopeItems) + " distinct items")
		}
	}
	return out, nil
}

func parseScopeItem(raw string, i int) (ScopeItem, error) {
	at := "scope item " + strconv.Itoa(i+1)
	if raw == "" {
		return ScopeItem{}, scopeMalformed(at + " is empty (items are separated by one space)")
	}
	for j := 0; j < len(raw); j++ {
		if c := raw[j]; c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return ScopeItem{}, scopeMalformed(at + " holds a character that is not allowed in an OAuth scope")
		}
	}
	action, selector, hasSelector := strings.Cut(raw, ":")
	a := Action(action)
	if !a.Valid() {
		return ScopeItem{}, scopeMalformed(at + " names the action " + strconv.Quote(action) + "; a CI token holds read, annotate, deploy, overwrite or delete, never manage")
	}
	if hasSelector {
		if _, err := parseSelector(selector, qname.Context{}); err != nil {
			return ScopeItem{}, scopeMalformed(at + " has an invalid repository selector: " + err.Error())
		}
	}
	return ScopeItem{Action: a, Selector: selector}, nil
}

// Narrow intersects the rendered grants with the requested items (R-CI-007 step 8): the result holds
// only the actions the items admit, an item that admits no action any grant holds is an *Error with
// reason scope_not_granted, and no partial token is issued. Policy and group grants are not known to
// this package, so their actions are asked through held: held(action, selector) reports whether the
// matched grants at exchange time hold the action on the selector. The returned Rendered keeps
// Policies and Groups and the narrowed inline grants; the stored token scope is built from the items
// by the exchange (`{"inherit": false, "repositories": [...]}`).
//
// An inline grant holds an item when it has the item's action on a selector that covers the item's
// as far as the text tells (the same selector, `@any`, a key pattern matching the item's key);
// everything the text cannot decide (a class or a format filter against a key) is held's to answer,
// with the repository index. An inline grant keeps, per selector, the actions of the items whose
// selector may overlap it; the stored scope is the exact ceiling at use time (R-AUTHZ-013). Caps
// apply before: narrow the result of [ApplyCaps]. No items is no narrowing.
func Narrow(r Rendered, items []ScopeItem, held func(Action, string) bool) (Rendered, error) {
	if len(items) == 0 {
		return r, nil
	}
	for _, it := range items {
		if !inlineHolds(r.Inline, it) && (held == nil || !held(it.Action, it.selector())) {
			return Rendered{}, notGranted(it)
		}
	}
	return Rendered{Policies: r.Policies, Groups: r.Groups, Inline: narrowInline(r.Inline, items)}, nil
}

func notGranted(it ScopeItem) *Error {
	return refuse(reason.ScopeNotGranted, "", "the scope item "+strconv.Quote(it.String())+" admits no action the matched grants hold")
}

// NarrowFrozen is Narrow over every contributing mapping of a bundle (R-CI-009 unites one match per
// tier): an item is granted when the inline grants of any mapping, under that mapping's caps, hold
// it, or held does; then each mapping's inline grants are narrowed as Narrow narrows them.
func NarrowFrozen(f Frozen, items []ScopeItem, held func(Action, string) bool) (Frozen, error) {
	if len(items) == 0 {
		return f, nil
	}
	for _, it := range items {
		granted := false
		for _, m := range f.Contributions {
			granted = granted || inlineHolds(ApplyCaps(Rendered{Inline: m.Inline}, m.Caps).Inline, it)
		}
		if !granted && (held == nil || !held(it.Action, it.selector())) {
			return Frozen{}, notGranted(it)
		}
	}
	out := f
	out.Contributions = make([]FrozenMapping, len(f.Contributions))
	for i, m := range f.Contributions {
		m.Inline = narrowInline(m.Inline, items)
		out.Contributions[i] = m
	}
	return out, nil
}

func inlineHolds(grants []InlineGrant, it ScopeItem) bool {
	for _, g := range grants {
		if !slices.Contains(g.Actions, it.Action) {
			continue
		}
		for _, s := range g.Repositories {
			if covers(s, it.selector()) {
				return true
			}
		}
	}
	return false
}

// narrowInline keeps, for each grant and each of its selectors, the grant's actions that an item
// with a possibly overlapping selector names. Selectors left with the same actions stay one grant;
// a selector left with none is dropped, and so is a grant left with no selector.
func narrowInline(grants []InlineGrant, items []ScopeItem) []InlineGrant {
	var out []InlineGrant
	for _, g := range grants {
		var groups [][]Action
		var sels [][]string
		for _, s := range g.Repositories {
			var kept []Action
			for _, a := range g.Actions {
				if !slices.Contains(kept, a) && slices.ContainsFunc(items, func(it ScopeItem) bool {
					return it.Action == a && !disjoint(s, it.selector())
				}) {
					kept = append(kept, a)
				}
			}
			if len(kept) == 0 {
				continue
			}
			i := slices.IndexFunc(groups, func(k []Action) bool { return slices.Equal(k, kept) })
			if i < 0 {
				groups, sels = append(groups, kept), append(sels, nil)
				i = len(groups) - 1
			}
			sels[i] = append(sels[i], s)
		}
		for i, kept := range groups {
			c := g.clone()
			c.Repositories, c.Actions = sels[i], kept
			out = append(out, c)
		}
	}
	return out
}

// FormatScope renders the `scope` member of the exchange response (R-CI-007): one item per action and
// repository selector of the effective grants after narrowing and caps, selectors as written in the
// grants, in a stable order. Package and path patterns are not shown.
//
// The items are those of the inline grants, grant by grant, selector by selector, in the grant's
// action order, each once. Policy and group grants are not known here: the exchange puts their items
// in front, as the example of R-CI-007 shows.
func FormatScope(r Rendered, caps []Cap) string {
	return strings.Join(scopeItems(nil, ApplyCaps(r, caps).Inline), " ")
}

// InlineScope is FormatScope over every contributing mapping of a bundle, each under its own caps,
// in the order of the contributions.
func (f Frozen) InlineScope() string {
	var items []string
	for _, m := range f.Contributions {
		items = scopeItems(items, ApplyCaps(Rendered{Inline: m.Inline}, m.Caps).Inline)
	}
	return strings.Join(items, " ")
}

func scopeItems(items []string, grants []InlineGrant) []string {
	for _, g := range grants {
		for _, s := range g.Repositories {
			for _, a := range g.Actions {
				item := string(a) + ":" + s
				if !slices.Contains(items, item) {
					items = append(items, item)
				}
			}
		}
	}
	return items
}

// Freeze builds the contribution of one matched mapping to a token (Q-CI-17): its reference, ID,
// tier and project, the groups it lists and its rendered inline grants bounded by its caps
// ([ApplyCaps]), so a frozen grant never holds an action its own cap removes; the caps stay recorded
// for the policy and group grants, which are bounded at use time.
func Freeze(ref, id, tier string, project *FrozenProject, r Rendered, caps []Cap) FrozenMapping {
	capped := ApplyCaps(r, caps)
	inline := make([]InlineGrant, 0, len(capped.Inline))
	for _, g := range capped.Inline {
		c := g.clone()
		if project != nil {
			// A project mapping writes bare keys; the token carries instance selectors, which the
			// engine and the scope items of the exchange read in one context (R-CI-007, Q-AUTHZ-216).
			for i, s := range c.Repositories {
				c.Repositories[i] = instanceForm(project.Key, s)
			}
		}
		inline = append(inline, c)
	}
	return FrozenMapping{
		Ref: ref, ID: id, Tier: tier, Project: project,
		Groups: slices.Clone(r.Groups), Inline: inline, Caps: slices.Clone(caps),
	}
}
