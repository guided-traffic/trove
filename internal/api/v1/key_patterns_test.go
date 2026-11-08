package v1

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/qname"
)

const (
	repoKeyPattern    = `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`
	projectKeyPattern = `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$`
)

// walkSchemas calls fn for every mapping of the document that has a pattern.
func walkSchemas(node any, fn func(path string, m map[string]any)) {
	var walk func(path string, n any)
	walk = func(path string, n any) {
		switch v := n.(type) {
		case map[string]any:
			if _, ok := v["pattern"]; ok {
				fn(path, v)
			}
			for k, c := range v {
				walk(path+"/"+k, c)
			}
		case []any:
			for i, c := range v {
				walk(path+"/"+string(rune('0'+i%10)), c)
			}
		}
	}
	walk("", node)
}

// keyProbes are strings around every edge of the two grammars.
func keyProbes() []string {
	out := []string{
		"", "a", "ab", "a1", "0a", "docker-", "docker-local", "docker.hub", "a__b", "a___b", "a._b", "a.-b", "a_-b", "a--b", "-ab", "ab-", ".ab", "Ab",
		"web", "web-", "web--app", "web_app", "web.app", "1web", "a b", "a/b", "a~cache",
	}
	for _, n := range []int{31, 32, 33, 63, 64, 65} {
		out = append(out, "a"+strings.Repeat("b", n-1), strings.Repeat("a", n)+"-")
	}
	return out
}

// TestTheKeyPatternsOfTheDocumentAreTheGrammarsOfTheCode: every schema of api/openapi.yaml whose pattern is the repository key
// grammar or the project key grammar carries the bounds of the code (2 to 64, 2 to 32), and a string passes the schema exactly when
// qname accepts it. Without the bounds a generated client would accept a key of one character or of 200 (Q-OCI-3).
func TestTheKeyPatternsOfTheDocumentAreTheGrammarsOfTheCode(t *testing.T) {
	var doc any
	if err := yaml.Unmarshal(EmbeddedYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	re := map[string]*regexp.Regexp{repoKeyPattern: regexp.MustCompile(repoKeyPattern), projectKeyPattern: regexp.MustCompile(projectKeyPattern)}
	valid := map[string]func(string) bool{repoKeyPattern: qname.ValidKey, projectKeyPattern: qname.ValidProjectKey}
	bounds := map[string][2]int{repoKeyPattern: {2, 64}, projectKeyPattern: {2, 32}}
	found := map[string]int{}
	walkSchemas(doc, func(path string, m map[string]any) {
		pat, _ := m["pattern"].(string)
		r, ok := re[pat]
		if !ok {
			return
		}
		found[pat]++
		lo, hi := bounds[pat][0], bounds[pat][1]
		if m["minLength"] != lo || m["maxLength"] != hi {
			t.Errorf("%s: minLength %v, maxLength %v, want %d and %d", path, m["minLength"], m["maxLength"], lo, hi)
		}
		for _, s := range keyProbes() {
			if got := len(s) >= lo && len(s) <= hi && r.MatchString(s); got != valid[pat](s) {
				t.Errorf("%s: the schema says %v of %q, qname says %v", path, got, s, !got)
			}
		}
	})
	if found[repoKeyPattern] < 5 || found[projectKeyPattern] < 10 {
		t.Errorf("the document holds %d repository key schemas and %d project key schemas", found[repoKeyPattern], found[projectKeyPattern])
	}
}

// TestTheKeyPatternsOfTheUIAreTheGrammarsOfTheCode: the patterns the UI checks a key with live in TypeScript, where this test cannot
// run them; it reads the literals and holds them to the expression of the grammar with the bound written as a look-ahead.
func TestTheKeyPatternsOfTheUIAreTheGrammarsOfTheCode(t *testing.T) {
	for _, c := range []struct {
		file, name, grammar string
		lo, hi              int
	}{
		{"../../../web/src/app/features/repositories/repository-model.ts", "KEY_PATTERN", repoKeyPattern, 2, 64},
		{"../../../web/src/app/api/projects-api.ts", "PROJECT_KEY_PATTERN", projectKeyPattern, 2, 32},
	} {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`export const ` + c.name + ` = /(.*)/;`).FindSubmatch(raw)
		if m == nil {
			t.Fatalf("%s: no %s", c.file, c.name)
		}
		want := `^(?=.{` + strconv.Itoa(c.lo) + `,` + strconv.Itoa(c.hi) + `}$)` + strings.TrimPrefix(c.grammar, "^")
		if string(m[1]) != want {
			t.Errorf("%s = /%s/, want /%s/", c.name, m[1], want)
		}
	}
}
