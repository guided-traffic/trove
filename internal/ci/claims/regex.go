package claims

import "regexp"

// Regular expressions are opt-in (R-CI-010): only the `regex` operator and the `require` filter of a
// template (R-CI-014) take one. The dialect is Go's RE2, which runs in time linear in the input, and
// every expression is anchored as ^(?:…)$, so it must match the whole value.
//
// The save compiles every expression ([Node.Validate], [ParseTemplate]), so a stored one always
// compiles; an evaluation compiles it again, a few microseconds for the 512 characters an
// expression may have. The package keeps no compiled copy: it holds no state (R-ARCH-032).

// compileAnchored compiles an expression as ^(?:expr)$, with (?i) in front when fold is set (Unicode
// simple case folding, as the regexp package defines it). The expression must compile on its own
// first: otherwise `a)|(b` would turn the wrapper into an alternation of two unanchored halves.
func compileAnchored(expr string, fold bool) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(expr); err != nil {
		return nil, err
	}
	src := "^(?:" + expr + ")$"
	if fold {
		src = "(?i)" + src
	}
	return regexp.Compile(src)
}
