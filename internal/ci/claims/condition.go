package claims

import "encoding/json"

// Limits bound a condition (R-CI-010): the depth, the number of leaves, the size of an `in` list and
// the length of a regular expression. A condition over a limit is refused when the mapping is saved
// (422 condition_invalid), so the evaluation of a stored one needs no check of its own.
type Limits struct {
	MaxDepth       int
	MaxLeaves      int
	MaxInValues    int
	MaxRegexLength int
}

// DefaultLimits are the limits of R-CI-010: depth 4, 64 leaves, 256 values, 512 characters.
func DefaultLimits() Limits {
	return Limits{MaxDepth: 4, MaxLeaves: 64, MaxInValues: 256, MaxRegexLength: 512}
}

// Node is one node of a condition tree (R-CI-010): a combinator (exactly one of All, Any, Not) or a
// leaf (Claim or Path with exactly one operator). The JSON form is the stored and the wire form of
// ci_mappings.match.
//
//	{"all": [
//	  {"claim": "namespace_id", "in": ["4711", "4712"]},
//	  {"claim": "ref_protected", "equals": true},
//	  {"not": {"claim": "environment_action", "in": ["prepare"]}}
//	]}
//
// Equals and In values are JSON scalars (string, number, boolean) kept raw so that the literal is
// normalized exactly as a claim value is (`true` equals "true", 20 equals "20").
type Node struct {
	All []Node `json:"all,omitempty"`
	Any []Node `json:"any,omitempty"`
	Not *Node  `json:"not,omitempty"`

	// Claim names a top-level claim; Path names a nested value by member names. Exactly one is set
	// on a leaf.
	Claim string   `json:"claim,omitempty"`
	Path  []string `json:"path,omitempty"`

	// The operators; exactly one is set on a leaf.
	Equals  json.RawMessage   `json:"equals,omitempty"`
	In      []json.RawMessage `json:"in,omitempty"`
	Glob    *string           `json:"glob,omitempty"`
	Regex   *string           `json:"regex,omitempty"`
	Present *bool             `json:"present,omitempty"`

	// IgnoreCase folds case (Unicode simple folding) for equals, in, glob and regex.
	IgnoreCase bool `json:"ignoreCase,omitempty"`
}

// IsLeaf reports whether the node tests a claim.
func (n Node) IsLeaf() bool { return n.Claim != "" || len(n.Path) > 0 }

// ParseCondition decodes a stored or submitted condition and refuses unknown members, a node that is
// both a combinator and a leaf, a leaf without or with several operators, an empty `all` or `any`
// and a value that is not a JSON scalar (422 condition_invalid with the pointer of the node). It
// does not apply the limits; [Node.Validate] does.
func ParseCondition(_ []byte) (Node, error) {
	panic("not implemented: W2")
}

// Validate applies the limits and the rules that need no issuer: the depth, the leaf count, the `in`
// size, valid glob patterns ([GlobValid]), a regular expression that compiles as `^(?:…)$` in RE2 and
// is within MaxRegexLength, and the rule that a condition has a leaf (422 catch_all_condition).
func (n Node) Validate(_ Limits) error {
	panic("not implemented: W2")
}

// LeafResult is the verdict of one leaf in a trace (R-CI-019 dry run, R-UI-036): which leaf, what it
// expected and what the claim held. It exists so the claim tester can mark each condition passed or
// failed with text and not only the first failure.
type LeafResult struct {
	// Pointer is the JSON pointer of the leaf inside the condition, "/all/1".
	Pointer  string `json:"pointer"`
	Claim    string `json:"claim"`
	Operator string `json:"operator"`
	// Expected is the literal(s) of the leaf as written; Actual is the normalized value(s) of the
	// claim, "" when it is absent. Neither is a secret: the dry run takes claims, not tokens.
	Expected []string `json:"expected"`
	Actual   []string `json:"actual"`
	Absent   bool     `json:"absent"`
	Passed   bool     `json:"passed"`
}

// Evaluation is the result of evaluating a condition with its trace.
type Evaluation struct {
	Matched bool `json:"matched"`
	// FailedLeaf is the pointer of the first leaf that decided the failure, "" on a match; Detail
	// reads like `pipeline_source equals "merge_request_event": value "push"`.
	FailedLeaf string       `json:"failedLeaf,omitempty"`
	Detail     string       `json:"detail,omitempty"`
	Leaves     []LeafResult `json:"leaves"`
}

// Evaluate evaluates the condition on the claims (R-CI-010): every leaf except `present: false` is
// false on a missing claim, so a `not` around a leaf on a missing claim is true; an array claim
// matches when any element does; a regular expression is anchored; the tree short-circuits only for
// the verdict, never for the trace, which lists every leaf.
func (n Node) Evaluate(_ Claims) Evaluation {
	panic("not implemented: W2")
}

// Pins reports whether the top level of the condition has a leaf with `equals` or `in` on one of the
// claims: the root leaf itself, or a child leaf of a root `all` (R-CI-005, R-CI-010). A leaf under
// `any`, `not` or a nested `all` does not count. The pin rule and the unpinned-write rule use it.
func (n Node) Pins(_ []string) bool {
	panic("not implemented: W2")
}

// RequiresProtectedRef reports whether the top-level `all` (or the root leaf) contains
// `{"claim": "ref_protected", "equals": true}` (R-CI-011).
func (n Node) RequiresProtectedRef() bool {
	panic("not implemented: W2")
}

// TestsPipelineSource reports whether the condition has a leaf on pipeline_source anywhere
// (R-CI-011 warning).
func (n Node) TestsPipelineSource() bool {
	panic("not implemented: W2")
}

// GuardedClaims are the claims a top-level `present: true` leaf guards, for the template warning of
// R-CI-010.
func (n Node) GuardedClaims() []string {
	panic("not implemented: W2")
}

// GlobValid refuses a pattern the mapping save must not accept (R-CI-010): `a**`, `**b`, and an
// unescaped `[`, `]`, `{` or `}`.
func GlobValid(_ string) error {
	panic("not implemented: W2")
}

// GlobMatch reports whether the whole value matches the pattern under the glob semantics of R-CI-010
// (separator `/`, `*` within a segment, `?` one character, `**` whole segments, `\x` the literal x).
// ignoreCase applies Unicode simple case folding. The pattern is assumed valid.
func GlobMatch(_, _ string, _ bool) bool {
	panic("not implemented: W2")
}
