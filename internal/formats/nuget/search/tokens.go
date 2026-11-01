package search

import (
	"strings"
	"unicode"
)

// IDTokens splits a package ID into the words search_doc carries with the highest weight
// (R-NUGET-010, Q-NUGET-18), space separated: at ".", "-" and "_", and at every change from
// a lower-case letter or a digit to an upper-case letter, so "Acme.Widgets" gives "Acme
// Widgets" and "NewtonsoftJson" gives "Newtonsoft Json". The case is kept; the statement
// that builds search_doc folds it. The caller passes the casing the .nuspec declares: the
// lower-case ID has no case changes to split at.
func IDTokens(declaredID string) string {
	var b strings.Builder
	var prev rune
	pending := false // a separator or a case change waits for the first rune of the next token
	for _, r := range declaredID {
		if r == '.' || r == '-' || r == '_' {
			pending = b.Len() > 0
			prev = r
			continue
		}
		if b.Len() > 0 && unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			pending = true
		}
		if pending {
			b.WriteByte(' ')
			pending = false
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}
