package repository

import (
	"log/slog"
	"slices"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// patNode is a node of the prefix trie of claim and pin-rule patterns: the path
// from the root spells the literal part of the pattern (R-REPO-023).
type patNode[T any] struct {
	next    map[byte]*patNode[T]
	entries []patEntry[T]
}

type patEntry[T any] struct {
	pat ClaimPattern
	val T
}

// patTrie finds the patterns that match a name without testing every pattern: a
// candidate is a pattern whose literal part is a prefix of the name, and the
// matcher of R-AUTHZ-005 confirms it, so `*` stops at `/` here exactly as it does
// in a permission. A trie is immutable once built.
type patTrie[T any] struct{ root patNode[T] }

func (t *patTrie[T]) add(pat ClaimPattern, v T) {
	n := &t.root
	for i := 0; i < len(pat.literal); i++ {
		if n.next == nil {
			n.next = map[byte]*patNode[T]{}
		}
		c := n.next[pat.literal[i]]
		if c == nil {
			c = &patNode[T]{}
			n.next[pat.literal[i]] = c
		}
		n = c
	}
	n.entries = append(n.entries, patEntry[T]{pat: pat, val: v})
}

// matches returns the entries whose pattern matches name.
func (t *patTrie[T]) matches(name string) []patEntry[T] {
	var out []patEntry[T]
	n := &t.root
	for i := 0; ; i++ {
		for _, e := range n.entries {
			if e.pat.Match(name) {
				out = append(out, e)
			}
		}
		if i == len(name) {
			return out
		}
		n = n.next[name[i]]
		if n == nil {
			return out
		}
	}
}

// pinIndex is the pin rules of one virtual repository, ready to select the rule
// of a name (R-REPO-023).
type pinIndex struct {
	rules []PinRule
	trie  patTrie[int]
}

// newPinIndex compiles the patterns of the rules, folded with the name rule of
// the format. A pattern that does not parse cannot have been stored by this
// package; the rule then selects nothing it names, which leaves the names to the
// claims, and the error is logged.
func newPinIndex(cat *catalog.Registry, format string, rules []PinRule, log *slog.Logger, key string) *pinIndex {
	p := &pinIndex{rules: rules}
	for i, r := range rules {
		for _, raw := range r.Packages {
			pat, err := parseFolded(cat, format, raw)
			if err != nil {
				log.Error("repository: a stored pin-rule pattern does not parse", "virtual", key, "pattern", raw, "error", err)
				continue
			}
			p.trie.add(pat, i)
		}
	}
	return p
}

// pinMatch is the rule a name selects.
type pinMatch struct {
	rule    PinRule
	pattern ClaimPattern
}

// best returns the rule whose pattern is the most specific match for name. Two
// rules never share a pattern (validation refuses it); if a stored pair did, the
// earlier rule wins.
func (p *pinIndex) best(name string) (pinMatch, bool) {
	if p == nil || name == "" {
		return pinMatch{}, false
	}
	var best patEntry[int]
	found := false
	for _, e := range p.trie.matches(name) {
		if !found || e.pat.moreSpecific(best.pat) || (e.pat.sameRank(best.pat) && e.val < best.val) {
			best, found = e, true
		}
	}
	if !found {
		return pinMatch{}, false
	}
	return pinMatch{rule: p.rules[best.val], pattern: best.pat}, true
}

// names reports whether the rule lists the member.
func (m pinMatch) names(memberKey string) bool { return slices.Contains(m.rule.Members, memberKey) }
