package claims

import (
	"regexp"
	"strings"
)

// Condition is a condition compiled for the exchange (R-CI-010, Q-CI-49): its literals normalized and its regular
// expressions compiled once, evaluated without a trace and with short-circuit: an `all` stops at its first false
// child, an `any` at its first true one. The mapping set of a replica holds one per mapping (object state of the
// set, R-ARCH-032), so the exchange compiles nothing per request; the dry run keeps [Node.Evaluate] and its
// trace. Matches decides exactly as Node.Evaluate(c).Matched for every node and claim set, the nodes Validate
// refuses included (false); FuzzR_CI_010_Condition holds them equal. The zero Condition matches nothing.
type Condition struct {
	root cnode
}

// cop is the operation of a compiled node; the zero value is the node that is always false.
type cop uint8

const (
	opFalse cop = iota
	opNot
	opAll
	opAny
	opPresent
	opLiterals
	opGlob
	opRegex
)

type cnode struct {
	op       cop
	children []cnode
	// claim and rest address the value of a leaf (Claims.Normalized).
	claim string
	rest  []string
	// want is the expected presence of a present leaf; lits the normalized literals of equals and in; glob the
	// pattern; re the anchored expression (nil when it does not compile: the leaf is false, as in Evaluate).
	want bool
	lits []string
	glob string
	re   *regexp.Regexp
	fold bool
}

// CompileCondition compiles a condition. It never fails: a node that Evaluate would answer false (several
// combinators, a combinator beside a leaf, an empty list, a leaf without operator, a regular expression that does
// not compile) compiles to a node that is false.
func CompileCondition(n Node) Condition { return Condition{root: compileNode(n)} }

func compileNode(n Node) cnode {
	switch {
	case n.combinators() > 1 || (n.combinators() == 1 && n.IsLeaf()):
		return cnode{}
	case n.IsLeaf():
		return compileLeaf(n)
	case n.Not != nil:
		return cnode{op: opNot, children: []cnode{compileNode(*n.Not)}}
	case n.All != nil:
		return compileList(opAll, n.All)
	case n.Any != nil:
		return compileList(opAny, n.Any)
	}
	return cnode{}
}

func compileList(op cop, list []Node) cnode {
	if len(list) == 0 {
		return cnode{} // an empty list matches nothing
	}
	out := cnode{op: op, children: make([]cnode, 0, len(list))}
	for _, ch := range list {
		out.children = append(out.children, compileNode(ch))
	}
	return out
}

// compileLeaf follows the order of Node.test: present first, then equals and in, glob, regex.
func compileLeaf(n Node) cnode {
	out := cnode{claim: n.claimName(), rest: pathRest(n), fold: n.IgnoreCase}
	switch {
	case n.Present != nil:
		out.op, out.want = opPresent, *n.Present
	case n.Equals != nil, n.In != nil:
		out.op, out.lits = opLiterals, n.expected()
	case n.Glob != nil:
		out.op, out.glob = opGlob, *n.Glob
	case n.Regex != nil:
		out.op = opRegex
		if re, err := compileAnchored(*n.Regex, n.IgnoreCase); err == nil {
			out.re = re
		}
	default:
		out.op = opFalse
	}
	return out
}

// Matches evaluates the compiled condition on the claims.
func (c Condition) Matches(cl Claims) bool { return c.root.eval(cl) }

func (n *cnode) eval(cl Claims) bool {
	switch n.op {
	case opNot:
		return !n.children[0].eval(cl)
	case opAll:
		for i := range n.children {
			if !n.children[i].eval(cl) {
				return false
			}
		}
		return true
	case opAny:
		for i := range n.children {
			if n.children[i].eval(cl) {
				return true
			}
		}
		return false
	case opFalse:
		return false
	}
	return n.leaf(cl)
}

func (n *cnode) leaf(cl Claims) bool {
	values, present := cl.Normalized(n.claim, n.rest)
	switch {
	case n.op == opPresent:
		return present == n.want
	case !present:
		return false
	}
	for _, v := range values {
		switch n.op {
		case opLiterals:
			for _, l := range n.lits {
				if v == l || (n.fold && strings.EqualFold(v, l)) {
					return true
				}
			}
		case opGlob:
			if GlobMatch(n.glob, v, n.fold) {
				return true
			}
		case opRegex:
			if n.re != nil && n.re.MatchString(v) {
				return true
			}
		}
	}
	return false
}
