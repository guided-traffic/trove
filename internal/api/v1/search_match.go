package v1

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/qname"
)

// How a suggestion matches and is ranked (R-API-062). The text is q trimmed and lower-cased; its terms are its parts between
// white space; its words are its runs of a to z and 0 to 9. A kind ranks its readable matches and cuts them at the limit, so the
// filter always runs before the cut (Q-API-276).

// suggestTerms are the parts of the text between white space.
func suggestTerms(text string) []string { return strings.Fields(text) }

// suggestWords are the runs of a to z and 0 to 9 of the text, the words a package name is matched by (Q-API-274). The
// generated column packages.name_words takes the words of a name by the same rule.
func suggestWords(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') })
}

// suggestTSQuery is the to_tsquery text of the words, each a prefix and all of them required: 'w1':* & 'w2':*. A word holds only
// a to z and 0 to 9, so no operator of the tsquery syntax reaches the database from a request (R-API-064). "" for no word.
func suggestTSQuery(words []string) string {
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, "'"+w+"':*")
	}
	return strings.Join(parts, " & ")
}

// prefixBounds are the bounds of the names that start with the text in byte order: low is the text, high the least string
// above every string that starts with it, the text with its last character raised by one (a character above which none
// exists is dropped first). ok is false when no such string exists, for a text of U+10FFFF characters only.
func prefixBounds(text string) (low, high string, ok bool) {
	rest := text
	for rest != "" {
		r, size := utf8.DecodeLastRuneInString(rest)
		rest = rest[:len(rest)-size]
		if next, ok := nextRune(r); ok {
			return text, rest + string(next), true
		}
	}
	return text, "", false
}

// nextRune is the code point after r that UTF-8 encodes, skipping the surrogates.
func nextRune(r rune) (rune, bool) {
	switch {
	case r >= unicode.MaxRune:
		return 0, false
	case r+1 == 0xD800:
		return 0xE000, true
	}
	return r + 1, true
}

// containsAll reports whether every term is part of one of the fields, each lower-cased.
func containsAll(terms []string, fields ...string) bool {
	for _, t := range terms {
		found := false
		for _, f := range fields {
			found = found || strings.Contains(f, t)
		}
		if !found {
			return false
		}
	}
	return true
}

// wordStarts reports whether the text, or one of its words between white space, starts with prefix.
func wordStarts(text, prefix string) bool {
	if strings.HasPrefix(text, prefix) {
		return true
	}
	for _, w := range strings.Fields(text) {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// repositoryRank is the rank of a repository (R-API-062): 0 the key or <project>/<key> equals the text; 1 the key starts with
// it; 2 <project>/<key> starts with it; 3 any other match. -1 when not every term is part of <project>/<key>.
func repositoryRank(text string, terms []string, name qname.Repo) int {
	full, key := name.String(), name.Key
	switch {
	case !containsAll(terms, full):
		return -1
	case key == text || full == text:
		return 0
	case strings.HasPrefix(key, text):
		return 1
	case strings.HasPrefix(full, text):
		return 2
	}
	return 3
}

// projectRank is the rank of a project (R-API-062): 0 the key equals the text; 1 the key starts with it; 2 the display name or
// one of its words starts with it; 3 any other match. -1 when not every term is part of the key or of the display name.
func projectRank(text string, terms []string, key, displayName string) int {
	display := strings.ToLower(displayName)
	switch {
	case !containsAll(terms, key, display):
		return -1
	case key == text:
		return 0
	case strings.HasPrefix(key, text):
		return 1
	case wordStarts(display, text):
		return 2
	}
	return 3
}

// ranked is a match of a kind with its rank and the text it is ordered by within the rank.
type ranked[T any] struct {
	rank  int
	order string
	item  T
}

// cutRanked orders the readable matches of a kind by rank, then by their text in byte order, and keeps limit of them; more
// reports that a readable match was left out (R-API-061, Q-API-276). The caller passes readable matches only.
func cutRanked[T any](matches []ranked[T], limit int) (items []T, more bool) {
	slices.SortStableFunc(matches, func(a, b ranked[T]) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), strings.Compare(a.order, b.order))
	})
	items = make([]T, 0, min(limit, len(matches)))
	for _, m := range matches[:min(limit, len(matches))] {
		items = append(items, m.item)
	}
	return items, len(matches) > limit
}
