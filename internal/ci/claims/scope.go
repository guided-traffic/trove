package claims

// ScopeItem is one item of the `scope` request parameter of the exchange (R-CI-007, Q-CI-2):
// `<action>` or `<action>:<repository selector>`. An item without selector stands for `@any`.
type ScopeItem struct {
	Action   Action
	Selector string
}

// MaxScopeItems is the number of distinct items a request may carry (the grant limit of R-AUTH-024).
const MaxScopeItems = 50

// ParseScope parses the space-delimited scope string: items are case-sensitive and split at the first
// ":"; duplicates are dropped; an unknown action, `manage`, an empty item or more than MaxScopeItems
// items is an *Error with reason scope_malformed. The selector is not resolved here: the exchange
// checks that a key selector names an existing repository (scope_malformed).
func ParseScope(_ string) ([]ScopeItem, error) {
	panic("not implemented: W4")
}

// Narrow intersects the rendered grants with the requested items (R-CI-007 step 8): the result holds
// only the actions the items admit, an item that admits no action any grant holds is an *Error with
// reason scope_not_granted, and no partial token is issued. Policy and group grants are not known to
// this package, so their actions are asked through held: held(action, selector) reports whether the
// matched grants at exchange time hold the action on the selector. The returned Rendered keeps
// Policies and Groups and the narrowed inline grants; the stored token scope is built from the items
// by the exchange (`{"inherit": false, "repositories": [...]}`).
func Narrow(_ Rendered, _ []ScopeItem, _ func(Action, string) bool) (Rendered, error) {
	panic("not implemented: W4")
}

// FormatScope renders the `scope` member of the exchange response (R-CI-007): one item per action and
// repository selector of the effective grants after narrowing and caps, selectors as written in the
// grants, in a stable order. Package and path patterns are not shown.
func FormatScope(_ Rendered, _ []Cap) string {
	panic("not implemented: W4")
}
