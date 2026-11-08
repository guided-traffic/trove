package authz

import (
	"slices"

	"github.com/guided-traffic/trove/internal/qname"
)

// UpperCasePath reports the first path pattern with an upper-case letter (HasUpperCase) among patterns when the
// selectors, read in the context in, cover a repository whose format stores lower-case paths on the installed index
// (Q-AUTHZ-253). The decision point compares such a pattern in lower case (Q-AUTHZ-254), so the refusal is feedback to
// the writer, who then sees the pattern as it is applied. The policy validation refuses it from the repositories it
// covers; a token scope and a CI inline grant, whose writers hold no list of repositories, are refused through this. A
// selector that does not parse covers nothing here; its writer refuses it on its own.
func (e *Engine) UpperCasePath(in qname.Context, selectors, patterns []string) (pattern string, repo qname.Repo, found bool) {
	i := slices.IndexFunc(patterns, HasUpperCase)
	if i < 0 {
		return "", qname.Repo{}, false
	}
	idx := e.Index()
	if idx == nil || e.o.Formats == nil {
		return "", qname.Repo{}, false
	}
	for _, r := range idx.repos {
		if en, ok := e.o.Formats.Lookup(r.Format); !ok || !en.LowerCasePaths {
			continue
		}
		if e.compile.selectorsMatch(selectors, in, r.repoView) {
			return patterns[i], r.Name, true
		}
	}
	return "", qname.Repo{}, false
}

// UpperCaseDetail is the message of a refused upper-case path pattern.
func UpperCaseDetail(pattern string, repo qname.Repo) string { return upperCaseDetail(pattern, repo) }
