package ansible

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
)

// patternChars are the characters of a name pattern (R-AUTHZ-005); a string that holds one is a pattern and is not checked against the
// grammar.
const patternChars = `*?[]{},!\`

// names is the package-name rule of the format (R-ANS-002, R-AUTHZ-005): a package is "<ns>.<name>". It implements the widest rule
// the format accepts anywhere: upper case is folded, because a remote may serve what the client's own grammar [a-zA-Z0-9_] accepts and
// a request for it must find the folded name; a publish refuses upper case in Inspect. A pattern is left as it is.
type names struct{}

func (names) Normalize(s string) (string, bool) {
	if strings.ContainsAny(s, patternChars) {
		return s, true
	}
	return normalizeName(s)
}

// normalizeName folds a request's "<ns>.<name>" to lower case and checks it.
func normalizeName(s string) (string, bool) {
	l := strings.ToLower(s)
	if _, _, ok := collection.SplitName(l); !ok {
		return "", false
	}
	return l, true
}
