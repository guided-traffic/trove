package search

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// The fields a term can be limited to (R-NUGET-010, Q-NUGET-46). An unknown prefix is no
// field: the whole word is free text.
const (
	FieldID          = "id"
	FieldPackageID   = "packageid"
	FieldVersion     = "version"
	FieldTitle       = "title"
	FieldTags        = "tags"
	FieldAuthor      = "author"
	FieldDescription = "description"
	FieldSummary     = "summary"
	FieldOwner       = "owner"
)

// Bounds of a query: the statement scans the latest version of every package of a
// repository once per term, so the size of the text and the number of terms are bounded
// (Q-NUGET-46).
const (
	MaxQueryBytes = 1024
	MaxTerms      = 16
)

func isField(s string) bool {
	switch s {
	case FieldID, FieldPackageID, FieldVersion, FieldTitle, FieldTags, FieldAuthor, FieldDescription, FieldSummary, FieldOwner:
		return true
	}
	return false
}

// isSpace is ASCII white space. The text is UTF-8, and a byte of a multi-byte character
// must never be taken for a separator.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// Parse splits the text of the q parameter into terms (R-NUGET-010): words separated by
// white space, double quotes grouping a phrase, and "field:value" or "field:"phrase""
// limiting a term to one field. A quote in the middle of a word ends the word and opens a
// phrase; a quote that is never closed runs to the end of the text. A field prefix is
// matched case-insensitively and a prefix that is no field leaves the word as free text. A
// term without a value is dropped.
//
// The values come back folded: the ID rule of R-NUGET-003 lowercases them, white space in a
// phrase collapses to one space, and the value of a version term is the URL form of the
// version, or "" for a text that is no version, which matches nothing. more is true when
// the text holds more terms than [MaxTerms]; the terms returned are the first ones.
func Parse(q string) (terms []rows.SearchTerm, more bool) {
	emit := func(t rows.SearchTerm) {
		if strings.TrimSpace(t.Value) == "" {
			return
		}
		t = fold(t)
		if len(terms) == MaxTerms {
			more = true
			return
		}
		terms = append(terms, t)
	}
	i := 0
	for i < len(q) {
		switch {
		case isSpace(q[i]):
			i++
		case q[i] == '"':
			phrase, next := readPhrase(q, i+1)
			emit(rows.SearchTerm{Value: phrase, Phrase: true})
			i = next
		default:
			start := i
			for i < len(q) && q[i] != '"' && !isSpace(q[i]) {
				i++
			}
			word := q[start:i]
			if c := strings.IndexByte(word, ':'); c > 0 && isField(strings.ToLower(word[:c])) {
				field, value := strings.ToLower(word[:c]), word[c+1:]
				if value == "" && i < len(q) && q[i] == '"' {
					phrase, next := readPhrase(q, i+1)
					emit(rows.SearchTerm{Field: field, Value: phrase, Phrase: true})
					i = next
					continue
				}
				emit(rows.SearchTerm{Field: field, Value: value})
				continue
			}
			emit(rows.SearchTerm{Value: word})
		}
	}
	return terms, more
}

// readPhrase reads a quoted group whose opening quote precedes from; it returns the text
// and the index after the closing quote.
func readPhrase(q string, from int) (phrase string, next int) {
	end := strings.IndexByte(q[from:], '"')
	if end < 0 {
		return q[from:], len(q)
	}
	return q[from : from+end], from + end + 1
}

// fold applies the folding rules of the field to a term.
func fold(t rows.SearchTerm) rows.SearchTerm {
	t.Value = strings.Join(strings.FieldsFunc(t.Value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
	}), " ")
	if t.Field == FieldVersion {
		t.Value = versionForm(t.Value)
		return t
	}
	t.Value = id.Lower(t.Value)
	return t
}

// versionForm is the URL form of a version text, "" for a text that is no version.
func versionForm(s string) string {
	v, err := version.Parse(s)
	if err != nil {
		return ""
	}
	return v.URLForm()
}
