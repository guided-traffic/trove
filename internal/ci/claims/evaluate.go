package claims

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// LeafResult is the verdict of one leaf in a trace (R-CI-019 dry run, R-UI-036): which leaf, what it
// expected and what the claim held. It exists so the claim tester can mark each condition passed or
// failed with text and not only the first failure.
type LeafResult struct {
	// Pointer is the JSON pointer of the leaf inside the condition, "/all/1".
	Pointer  string `json:"pointer"`
	Claim    string `json:"claim"`
	Operator string `json:"operator"`
	// Expected is the literal(s) of the leaf as written; Actual is the normalized value(s) of the
	// claim, empty when it is absent. Neither is a secret: the dry run takes claims, not tokens.
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
//
// The deciding leaf of a failure is found from the root down: the first false child of an `all`,
// the first child of an `any` whose children all failed, the child of a `not` (whose detail starts
// with "not "). The pointer of a failed root leaf is "", the JSON pointer of the whole condition.
// The detail quotes the claim's value as the trace does, at most 128 characters of each; the
// exchange keeps it out of its error_description (R-CI-008). A node that Validate would refuse
// (empty, several combinators, no operator) is false.
func (n Node) Evaluate(c Claims) Evaluation {
	e := evaluator{claims: c, leaves: []LeafResult{}}
	r := e.eval(n, "")
	ev := Evaluation{Matched: r.value, Leaves: e.leaves}
	if !r.value {
		ev.FailedLeaf, ev.Detail = r.pointer, r.detail
	}
	return ev
}

// verdict is a node's value with the leaf that explains it.
type verdict struct {
	value   bool
	pointer string
	detail  string
}

type evaluator struct {
	claims Claims
	leaves []LeafResult
}

func (e *evaluator) eval(n Node, ptr string) verdict {
	switch {
	case n.combinators() > 1 || (n.combinators() == 1 && n.IsLeaf()):
		return verdict{pointer: ptr, detail: "the node is both a combinator and a leaf"}
	case n.IsLeaf():
		return e.leaf(n, ptr)
	case n.Not != nil:
		r := e.eval(*n.Not, ptr+"/not")
		return verdict{value: !r.value, pointer: r.pointer, detail: "not " + r.detail}
	case n.All != nil:
		return e.list(n.All, ptr+"/all", true)
	case n.Any != nil:
		return e.list(n.Any, ptr+"/any", false)
	}
	return verdict{pointer: ptr, detail: "the node has no leaf"}
}

// list evaluates every child of an `all` (conjunction) or an `any`. The verdict is explained by
// the first child that decides it: for an all the first false one, for an any the first true one;
// when none decides, the first child.
func (e *evaluator) list(children []Node, ptr string, conjunction bool) verdict {
	if len(children) == 0 {
		return verdict{pointer: ptr, detail: "an empty list matches nothing"}
	}
	var deciding *verdict
	var first verdict
	for i, ch := range children {
		r := e.eval(ch, ptr+"/"+strconv.Itoa(i))
		if i == 0 {
			first = r
		}
		if deciding == nil && r.value != conjunction {
			deciding = &r
		}
	}
	if deciding != nil {
		return *deciding
	}
	return first
}

func (e *evaluator) leaf(n Node, ptr string) verdict {
	values, present := e.claims.Normalized(n.claimName(), pathRest(n))
	op := n.operator()
	res := LeafResult{
		Pointer: ptr, Claim: n.display(), Operator: op,
		Expected: n.expected(), Actual: values, Absent: !present,
	}
	if res.Actual == nil {
		res.Actual = []string{}
	}
	res.Passed = n.test(values, present)
	e.leaves = append(e.leaves, res)
	return verdict{value: res.Passed, pointer: ptr, detail: leafDetail(res)}
}

func pathRest(n Node) []string {
	if n.Claim == "" && len(n.Path) > 1 {
		return n.Path[1:]
	}
	return nil
}

// test is the operator of a leaf on the claim's normalized values.
func (n Node) test(values []string, present bool) bool {
	switch {
	case n.Present != nil:
		return present == *n.Present
	case !present:
		return false
	case n.Equals != nil, n.In != nil:
		lits := n.expected()
		for _, v := range values {
			for _, l := range lits {
				if v == l || (n.IgnoreCase && strings.EqualFold(v, l)) {
					return true
				}
			}
		}
	case n.Glob != nil:
		for _, v := range values {
			if GlobMatch(*n.Glob, v, n.IgnoreCase) {
				return true
			}
		}
	case n.Regex != nil:
		re, err := compileAnchored(*n.Regex, n.IgnoreCase)
		if err != nil {
			return false
		}
		for _, v := range values {
			if re.MatchString(v) {
				return true
			}
		}
	}
	return false
}

// expected is the leaf's literal(s) in normalized form, the pattern of glob and regex, or "true" /
// "false" for present. A literal that is not a scalar has no normalized form and is left out, so it
// matches nothing.
func (n Node) expected() []string {
	switch {
	case n.Equals != nil:
		if v, ok := literalValue(n.Equals); ok {
			return []string{v}
		}
	case n.In != nil:
		out := make([]string, 0, len(n.In))
		for _, raw := range n.In {
			if v, ok := literalValue(raw); ok {
				out = append(out, v)
			}
		}
		return out
	case n.Glob != nil:
		return []string{*n.Glob}
	case n.Regex != nil:
		return []string{*n.Regex}
	case n.Present != nil:
		return []string{strconv.FormatBool(*n.Present)}
	}
	return []string{}
}

// maxDetailValue is the longest quoted value in a detail, in characters.
const maxDetailValue = 128

func quoteShort(s string) string {
	if utf8.RuneCountInString(s) > maxDetailValue {
		s = string([]rune(s)[:maxDetailValue]) + "…"
	}
	return strconv.Quote(s)
}

func quoteList(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = quoteShort(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// leafDetail is the sentence of the sketch of R-CI-019:
// `pipeline_source equals "merge_request_event": value "push"`.
func leafDetail(r LeafResult) string {
	var want string
	switch {
	case r.Operator == "in":
		want = quoteList(r.Expected)
	case r.Operator == "present" && len(r.Expected) == 1:
		want = r.Expected[0]
	case len(r.Expected) == 1:
		want = quoteShort(r.Expected[0])
	default:
		want = quoteList(r.Expected)
	}
	var got string
	switch {
	case r.Absent:
		got = "absent"
	case len(r.Actual) == 0:
		got = "a value that is not a string, an integer or a boolean"
	case len(r.Actual) == 1:
		got = "value " + quoteShort(r.Actual[0])
	default:
		got = "values " + quoteList(r.Actual)
	}
	return r.Claim + " " + r.Operator + " " + want + ": " + got
}
