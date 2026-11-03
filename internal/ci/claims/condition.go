package claims

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

// Limits bound a condition (R-CI-010): the depth, the number of leaves, the size of an `in` list and
// the length of a regular expression. A condition over a limit is refused when the mapping is saved
// (422 condition_invalid), so the evaluation of a stored one needs no check of its own.
//
// The depth counts the nodes on the longest path from the root to a leaf, the leaf included: a
// root leaf has depth 1, `{"all": [leaf]}` depth 2, and all → any → not → leaf is the deepest
// condition of depth 4. A zero field takes its default.
type Limits struct {
	MaxDepth       int
	MaxLeaves      int
	MaxInValues    int
	MaxRegexLength int
}

// The size bounds of a condition (Q-CI-49): a stored condition is evaluated by every exchange of its issuer, so
// the bytes it compares are bounded at save beside the counts of Limits. A literal of equals or in has at most
// MaxLiteralLength characters (a glob MaxGlobLength, a regex Limits.MaxRegexLength), a claim name and a path member
// MaxClaimNameLength, a path MaxPathMembers members, and the JSON form of the whole condition MaxConditionBytes
// bytes. Over a bound the save is refused with 422 condition_invalid.
const (
	MaxLiteralLength   = 1024
	MaxClaimNameLength = 256
	MaxPathMembers     = 16
	MaxConditionBytes  = 64 << 10
)

// DefaultLimits are the limits of R-CI-010: depth 4, 64 leaves, 256 values, 512 characters.
func DefaultLimits() Limits {
	return Limits{MaxDepth: 4, MaxLeaves: 64, MaxInValues: 256, MaxRegexLength: 512}
}

func (l Limits) orDefault() Limits {
	d := DefaultLimits()
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxLeaves <= 0 {
		l.MaxLeaves = d.MaxLeaves
	}
	if l.MaxInValues <= 0 {
		l.MaxInValues = d.MaxInValues
	}
	if l.MaxRegexLength <= 0 {
		l.MaxRegexLength = d.MaxRegexLength
	}
	return l
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
//
// Decoding a Node with encoding/json applies [ParseCondition], so a condition read from the
// database or from a request body is held to the same rules wherever it is decoded.
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

// claimName is the top-level claim a leaf reads: Claim, or the first member of Path.
func (n Node) claimName() string {
	if n.Claim != "" {
		return n.Claim
	}
	if len(n.Path) > 0 {
		return n.Path[0]
	}
	return ""
}

// topClaim reports whether a leaf reads a top-level claim (Claim, or a Path of one member) and
// which: pins and the protected-ref rule count such leaves only.
func (n Node) topClaim() (string, bool) {
	switch {
	case n.Claim != "" && len(n.Path) == 0:
		return n.Claim, true
	case n.Claim == "" && len(n.Path) == 1:
		return n.Path[0], true
	}
	return "", false
}

// display is how a trace names the value a leaf reads: the claim, or the path joined with dots (for
// reading only; a claim name may itself contain a dot).
func (n Node) display() string {
	if n.Claim != "" {
		return n.Claim
	}
	return strings.Join(n.Path, ".")
}

// operator names the leaf's operator, "" when it has none (or several: Validate refuses both).
func (n Node) operator() string {
	ops := n.operators()
	if len(ops) != 1 {
		return ""
	}
	return ops[0]
}

func (n Node) operators() []string {
	var ops []string
	if n.Equals != nil {
		ops = append(ops, "equals")
	}
	if n.In != nil {
		ops = append(ops, "in")
	}
	if n.Glob != nil {
		ops = append(ops, "glob")
	}
	if n.Regex != nil {
		ops = append(ops, "regex")
	}
	if n.Present != nil {
		ops = append(ops, "present")
	}
	return ops
}

func (n Node) combinators() int {
	c := 0
	if n.All != nil {
		c++
	}
	if n.Any != nil {
		c++
	}
	if n.Not != nil {
		c++
	}
	return c
}

func (n Node) empty() bool {
	return n.combinators() == 0 && !n.IsLeaf() && len(n.operators()) == 0 && !n.IgnoreCase
}

// conditionInvalid is the refusal of a condition's shape or limits.
func conditionInvalid(pointer, detail string) *Error {
	return refuse(reason.ConditionInvalid, pointer, detail)
}

// maxParseNesting bounds the recursion of the parser; the depth limit of Validate is far below it.
const maxParseNesting = 64

// ParseCondition decodes a stored or submitted condition and refuses unknown members, a node that is
// both a combinator and a leaf, a leaf without or with several operators, an empty `all` or `any`
// and a value that is not a JSON scalar (422 condition_invalid with the pointer of the node). It
// does not apply the limits; [Node.Validate] does.
//
// The pointers are relative to the condition ("/all/1/in/0"); the mapping service prefixes the
// member that holds it ("/match"). An empty object `{}` parses as the empty node, which Validate
// refuses as catch_all_condition. A number literal must be a JSON integer (Q-CI-28), and
// `ignoreCase` goes only with equals, in, glob and regex.
func ParseCondition(b []byte) (Node, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Node{}, conditionInvalid("", "the condition is not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Node{}, conditionInvalid("", "data after the condition")
	}
	return parseNode(v, "", 0)
}

// UnmarshalJSON decodes a condition with [ParseCondition]. JSON null leaves the node empty.
func (n *Node) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		return nil
	}
	parsed, err := ParseCondition(b)
	if err != nil {
		return err
	}
	*n = parsed
	return nil
}

// nodeMember reports whether a member may appear in a condition node, in the wire spelling.
func nodeMember(name string) bool {
	switch name {
	case "all", "any", "not", "claim", "path", "equals", "in", "glob", "regex", "present", "ignoreCase":
		return true
	}
	return false
}

// pointerToken escapes a member name for a JSON pointer (RFC 6901 §3).
func pointerToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func parseNode(v any, ptr string, depth int) (Node, error) {
	if depth > maxParseNesting {
		return Node{}, conditionInvalid(ptr, "the condition is nested too deeply")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Node{}, conditionInvalid(ptr, "a condition node is a JSON object")
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !nodeMember(k) {
			return Node{}, conditionInvalid(ptr+"/"+pointerToken(k), "unknown member "+strconv.Quote(k)+" (a node has all, any, not, or claim or path with one of equals, in, glob, regex, present, and ignoreCase)")
		}
	}
	_, hasAll := m["all"]
	_, hasAny := m["any"]
	_, hasNot := m["not"]
	switch c := btoi(hasAll) + btoi(hasAny) + btoi(hasNot); {
	case c > 1:
		return Node{}, conditionInvalid(ptr, "a node has exactly one of all, any and not")
	case c == 1:
		if len(m) != 1 {
			return Node{}, conditionInvalid(ptr, "a combinator node has no claim, path, operator or ignoreCase beside it")
		}
		return parseCombinator(m, ptr, depth)
	case len(m) == 0:
		return Node{}, nil
	}
	return parseLeaf(m, ptr)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseCombinator(m map[string]any, ptr string, depth int) (Node, error) {
	if raw, ok := m["not"]; ok {
		child, err := parseNode(raw, ptr+"/not", depth+1)
		if err != nil {
			return Node{}, err
		}
		return Node{Not: &child}, nil
	}
	name := "all"
	raw, ok := m["all"]
	if !ok {
		name, raw = "any", m["any"]
	}
	list, isList := raw.([]any)
	if !isList {
		return Node{}, conditionInvalid(ptr+"/"+name, name+" holds a list of conditions")
	}
	if len(list) == 0 {
		return Node{}, conditionInvalid(ptr+"/"+name, "an empty "+name+" is invalid")
	}
	children := make([]Node, 0, len(list))
	for i, el := range list {
		child, err := parseNode(el, ptr+"/"+name+"/"+strconv.Itoa(i), depth+1)
		if err != nil {
			return Node{}, err
		}
		children = append(children, child)
	}
	if name == "all" {
		return Node{All: children}, nil
	}
	return Node{Any: children}, nil
}

func parseLeaf(m map[string]any, ptr string) (Node, error) {
	var n Node
	if err := parseLeafTarget(m, ptr, &n); err != nil {
		return Node{}, err
	}
	if err := parseOperators(m, ptr, &n); err != nil {
		return Node{}, err
	}
	if raw, ok := m["ignoreCase"]; ok {
		b, isBool := raw.(bool)
		if !isBool {
			return Node{}, conditionInvalid(ptr+"/ignoreCase", "ignoreCase is a boolean")
		}
		n.IgnoreCase = b
	}
	if n.IgnoreCase && n.Present != nil {
		return Node{}, conditionInvalid(ptr+"/ignoreCase", "ignoreCase goes with equals, in, glob and regex, not with present")
	}
	return n, nil
}

func parseLeafTarget(m map[string]any, ptr string, n *Node) error {
	rawClaim, hasClaim := m["claim"]
	rawPath, hasPath := m["path"]
	switch {
	case hasClaim && hasPath:
		return conditionInvalid(ptr, "a leaf names exactly one of claim and path")
	case !hasClaim && !hasPath:
		return conditionInvalid(ptr, "a leaf names a claim or a path")
	case hasClaim:
		s, ok := rawClaim.(string)
		if !ok || s == "" {
			return conditionInvalid(ptr+"/claim", "claim is a non-empty string")
		}
		n.Claim = s
		return nil
	}
	list, ok := rawPath.([]any)
	if !ok || len(list) == 0 {
		return conditionInvalid(ptr+"/path", "path is a non-empty list of member names")
	}
	for i, el := range list {
		s, ok := el.(string)
		if !ok || s == "" {
			return conditionInvalid(ptr+"/path/"+strconv.Itoa(i), "a path member is a non-empty string")
		}
		n.Path = append(n.Path, s)
	}
	return nil
}

func parseOperators(m map[string]any, ptr string, n *Node) error {
	count := 0
	if raw, ok := m["equals"]; ok {
		count++
		lit, err := literal(raw, ptr+"/equals")
		if err != nil {
			return err
		}
		n.Equals = lit
	}
	if raw, ok := m["in"]; ok {
		count++
		list, isList := raw.([]any)
		if !isList || len(list) == 0 {
			return conditionInvalid(ptr+"/in", "in is a non-empty list of literals")
		}
		n.In = make([]json.RawMessage, 0, len(list))
		for i, el := range list {
			lit, err := literal(el, ptr+"/in/"+strconv.Itoa(i))
			if err != nil {
				return err
			}
			n.In = append(n.In, lit)
		}
	}
	for _, op := range []string{"glob", "regex"} {
		raw, ok := m[op]
		if !ok {
			continue
		}
		count++
		s, isString := raw.(string)
		if !isString {
			return conditionInvalid(ptr+"/"+op, op+" is a string")
		}
		if op == "glob" {
			n.Glob = &s
		} else {
			n.Regex = &s
		}
	}
	if raw, ok := m["present"]; ok {
		count++
		b, isBool := raw.(bool)
		if !isBool {
			return conditionInvalid(ptr+"/present", "present is a boolean")
		}
		n.Present = &b
	}
	if count != 1 {
		return conditionInvalid(ptr, "a leaf has exactly one operator of equals, in, glob, regex and present")
	}
	return nil
}

// literal checks a literal of equals or in: a string, a boolean or a JSON integer (Q-CI-28).
func literal(v any, ptr string) (json.RawMessage, error) {
	switch t := v.(type) {
	case string, bool:
		b, _ := json.Marshal(t)
		return b, nil
	case json.Number:
		if _, ok := integerText(string(t)); !ok {
			return nil, conditionInvalid(ptr, "a number literal is an integer (claims are compared in decimal form; write 20, not 20.0 or 2e1)")
		}
		return json.RawMessage(t), nil
	}
	return nil, conditionInvalid(ptr, "a literal is a string, an integer or a boolean")
}

// literalValue is the normalized form of a raw literal; ok is false for one that is not a scalar.
func literalValue(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return "", false
	}
	return scalar(v)
}

// Validate applies the limits and the rules that need no issuer: the depth, the leaf count, the `in`
// size, valid glob patterns ([GlobValid]), a regular expression that compiles as `^(?:…)$` in RE2 and
// is within MaxRegexLength, and the rule that a condition has a leaf (422 catch_all_condition).
//
// It checks the shape again (exactly one combinator or a leaf with exactly one operator, literals
// that are scalars), so a node built in Go is held to the rules of ParseCondition too. A condition
// without any leaf is catch_all_condition before any other finding; the pointers are relative to
// the condition, as ParseCondition's.
func (n Node) Validate(l Limits) error {
	l = l.orDefault()
	if leafCount(n) == 0 {
		return refuse(reason.CatchAllCondition, "", "the condition has no leaf, so it would match every token")
	}
	v := validator{limits: l}
	if err := v.node(n, "", 1); err != nil {
		return err
	}
	if v.leaves > l.MaxLeaves {
		return conditionInvalid("", "the condition has "+strconv.Itoa(v.leaves)+" leaves, at most "+strconv.Itoa(l.MaxLeaves)+" are allowed")
	}
	if b, err := json.Marshal(n); err != nil || len(b) > MaxConditionBytes {
		return conditionInvalid("", "the condition is larger than "+strconv.Itoa(MaxConditionBytes)+" bytes")
	}
	return nil
}

func leafCount(n Node) int {
	if n.IsLeaf() {
		return 1
	}
	c := 0
	for _, ch := range n.All {
		c += leafCount(ch)
	}
	for _, ch := range n.Any {
		c += leafCount(ch)
	}
	if n.Not != nil {
		c += leafCount(*n.Not)
	}
	return c
}

type validator struct {
	limits Limits
	leaves int
}

func (v *validator) node(n Node, ptr string, depth int) error {
	if depth > v.limits.MaxDepth {
		return conditionInvalid(ptr, "the condition is deeper than "+strconv.Itoa(v.limits.MaxDepth)+" levels")
	}
	comb := n.combinators()
	switch {
	case comb > 1 || (comb == 1 && (n.IsLeaf() || len(n.operators()) > 0 || n.IgnoreCase)):
		return conditionInvalid(ptr, "a node is exactly one of all, any, not or a leaf")
	case n.Not != nil:
		return v.node(*n.Not, ptr+"/not", depth+1)
	case n.All != nil:
		return v.children("all", n.All, ptr, depth)
	case n.Any != nil:
		return v.children("any", n.Any, ptr, depth)
	case n.empty():
		return conditionInvalid(ptr, "an empty node is neither a combinator nor a leaf")
	}
	v.leaves++
	return v.leaf(n, ptr)
}

func (v *validator) children(name string, list []Node, ptr string, depth int) error {
	if len(list) == 0 {
		return conditionInvalid(ptr+"/"+name, "an empty "+name+" is invalid")
	}
	for i, ch := range list {
		if err := v.node(ch, ptr+"/"+name+"/"+strconv.Itoa(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) leaf(n Node, ptr string) error {
	switch {
	case !n.IsLeaf():
		return conditionInvalid(ptr, "a leaf names a claim or a path")
	case n.Claim != "" && len(n.Path) > 0:
		return conditionInvalid(ptr, "a leaf names exactly one of claim and path")
	case len(n.operators()) != 1:
		return conditionInvalid(ptr, "a leaf has exactly one operator of equals, in, glob, regex and present")
	case n.IgnoreCase && n.Present != nil:
		return conditionInvalid(ptr+"/ignoreCase", "ignoreCase goes with equals, in, glob and regex, not with present")
	}
	if err := checkTarget(n, ptr); err != nil {
		return err
	}
	switch {
	case n.Equals != nil:
		return checkLiteral(n.Equals, ptr+"/equals")
	case n.In != nil:
		return v.in(n.In, ptr)
	case n.Glob != nil:
		if err := GlobValid(*n.Glob); err != nil {
			return conditionInvalid(ptr+"/glob", "invalid glob: "+err.Error())
		}
	case n.Regex != nil:
		return v.regex(*n.Regex, n.IgnoreCase, ptr)
	}
	return nil
}

func (v *validator) in(list []json.RawMessage, ptr string) error {
	if len(list) == 0 {
		return conditionInvalid(ptr+"/in", "in is a non-empty list of literals")
	}
	if len(list) > v.limits.MaxInValues {
		return conditionInvalid(ptr+"/in", "in holds "+strconv.Itoa(len(list))+" values, at most "+strconv.Itoa(v.limits.MaxInValues)+" are allowed")
	}
	for i, raw := range list {
		if err := checkLiteral(raw, ptr+"/in/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}

// checkTarget bounds what a leaf reads (Q-CI-49): a claim name and each path member of at most MaxClaimNameLength
// characters, a path of at most MaxPathMembers members, no empty member.
func checkTarget(n Node, ptr string) error {
	if utf8.RuneCountInString(n.Claim) > MaxClaimNameLength {
		return conditionInvalid(ptr+"/claim", "the claim name has more than "+strconv.Itoa(MaxClaimNameLength)+" characters")
	}
	if len(n.Path) > MaxPathMembers {
		return conditionInvalid(ptr+"/path", "the path has "+strconv.Itoa(len(n.Path))+" members, at most "+strconv.Itoa(MaxPathMembers)+" are allowed")
	}
	for i, p := range n.Path {
		switch {
		case p == "":
			return conditionInvalid(ptr+"/path/"+strconv.Itoa(i), "a path member is a non-empty string")
		case utf8.RuneCountInString(p) > MaxClaimNameLength:
			return conditionInvalid(ptr+"/path/"+strconv.Itoa(i), "a path member has more than "+strconv.Itoa(MaxClaimNameLength)+" characters")
		}
	}
	return nil
}

// checkLiteral is one literal of equals or in: a scalar of at most MaxLiteralLength characters in its normalized
// form (Q-CI-49).
func checkLiteral(raw json.RawMessage, ptr string) error {
	v, ok := literalValue(raw)
	switch {
	case !ok:
		return conditionInvalid(ptr, "a literal is a string, an integer or a boolean")
	case utf8.RuneCountInString(v) > MaxLiteralLength:
		return conditionInvalid(ptr, "the literal has more than "+strconv.Itoa(MaxLiteralLength)+" characters")
	}
	return nil
}

func (v *validator) regex(expr string, fold bool, ptr string) error {
	if n := utf8.RuneCountInString(expr); n > v.limits.MaxRegexLength {
		return conditionInvalid(ptr+"/regex", "the regular expression has "+strconv.Itoa(n)+" characters, at most "+strconv.Itoa(v.limits.MaxRegexLength)+" are allowed")
	}
	if _, err := compileAnchored(expr, fold); err != nil {
		return conditionInvalid(ptr+"/regex", "the regular expression does not compile in RE2: "+err.Error())
	}
	return nil
}

// Pins reports whether the top level of the condition has a leaf with `equals` or `in` on one of the
// claims: the root leaf itself, or a child leaf of a root `all` (R-CI-005, R-CI-010). A leaf under
// `any`, `not` or a nested `all` does not count. The pin rule and the unpinned-write rule use it.
func (n Node) Pins(claims []string) bool {
	for _, leaf := range n.topLeaves() {
		name, top := leaf.topClaim()
		if !top || (leaf.Equals == nil && leaf.In == nil) {
			continue
		}
		for _, c := range claims {
			if c == name {
				return true
			}
		}
	}
	return false
}

// topLeaves are the leaves at the top level: the root leaf, or the leaf children of a root `all`.
func (n Node) topLeaves() []Node {
	if n.IsLeaf() {
		return []Node{n}
	}
	var out []Node
	for _, ch := range n.All {
		if ch.IsLeaf() {
			out = append(out, ch)
		}
	}
	return out
}

// RequiresProtectedRef reports whether the top-level `all` (or the root leaf) contains
// `{"claim": "ref_protected", "equals": true}` (R-CI-011).
func (n Node) RequiresProtectedRef() bool {
	for _, leaf := range n.topLeaves() {
		if name, top := leaf.topClaim(); !top || name != "ref_protected" || leaf.Equals == nil {
			continue
		}
		if v, ok := literalValue(leaf.Equals); ok && v == "true" {
			return true
		}
	}
	return false
}

// TestsPipelineSource reports whether the condition has a leaf on pipeline_source anywhere
// (R-CI-011 warning).
func (n Node) TestsPipelineSource() bool {
	found := false
	walkLeaves(n, "", func(leaf Node, _ string) {
		if leaf.claimName() == "pipeline_source" {
			found = true
		}
	})
	return found
}

// GuardedClaims are the claims a top-level `present: true` leaf guards, for the template warning of
// R-CI-010.
func (n Node) GuardedClaims() []string {
	var out []string
	for _, leaf := range n.topLeaves() {
		name, top := leaf.topClaim()
		if top && leaf.Present != nil && *leaf.Present {
			out = append(out, name)
		}
	}
	return out
}

// walkLeaves calls fn for every leaf with its pointer, in document order.
func walkLeaves(n Node, ptr string, fn func(Node, string)) {
	switch {
	case n.IsLeaf():
		fn(n, ptr)
	case n.Not != nil:
		walkLeaves(*n.Not, ptr+"/not", fn)
	case n.All != nil:
		for i, ch := range n.All {
			walkLeaves(ch, ptr+"/all/"+strconv.Itoa(i), fn)
		}
	case n.Any != nil:
		for i, ch := range n.Any {
			walkLeaves(ch, ptr+"/any/"+strconv.Itoa(i), fn)
		}
	}
}
