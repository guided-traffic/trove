package v1

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// wikiSchemas are the response schemas that hold wiki content, a README or a documentation body (R-AUTHZ-034, R-API-058). The
// documentation operations of the Ansible format join with their requirement.
var wikiSchemas = map[string]bool{"PackageReadme": true, "PackageVersionDocs": true, "PackageDocEntry": true, "PackageDocFile": true}

// TestR_API_058_WikiOperationsAreMarked: every operation whose response holds a README or a documentation body carries
// x-trove-part: wiki, and the marked ones are read into Operation.Part; the extension is refused on anything but a public GET.
func TestR_API_058_WikiOperationsAreMarked(t *testing.T) {
	raw := EmbeddedYAML()
	doc, err := LoadDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	t.Run("R-API-058 an operation that returns a wiki schema is marked and read into Operation.Part", func(t *testing.T) {
		seen := 0
		for pattern, methods := range root.Paths {
			for method, node := range methods {
				var op struct {
					ID        string         `yaml:"operationId"`
					Part      string         `yaml:"x-trove-part"`
					Responses map[string]any `yaml:"responses"`
				}
				if err := node.Decode(&op); err != nil || op.ID == "" {
					continue
				}
				for _, r := range op.Responses {
					for name := range wikiSchemas {
						if strings.Contains(refsOf(r), "/schemas/"+name) {
							seen++
							if op.Part != "wiki" {
								t.Errorf("%s %s (%s) returns %s and lacks x-trove-part: wiki", strings.ToUpper(method), pattern, op.ID, name)
							}
						}
					}
				}
			}
		}
		if seen == 0 {
			t.Fatal("no operation returns a wiki schema: the walk checks nothing")
		}
		got := map[string]string{}
		for _, op := range doc.Operations() {
			got[op.ID] = op.Part
		}
		if got["getPackageVersionReadme"] != PartWiki || got["getPackageVersion"] != "" {
			t.Errorf("Operation.Part: readme %v, version %v", got["getPackageVersionReadme"], got["getPackageVersion"])
		}
	})
	t.Run("R-API-058 the extension on a PUT operation fails to load", func(t *testing.T) {
		bad := strings.Replace(string(raw), "      operationId: replaceProject\n", "      operationId: replaceProject\n      x-trove-part: wiki\n", 1)
		if _, err := LoadDocument([]byte(bad)); err == nil || !strings.Contains(err.Error(), "x-trove-part") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-API-058 the extension on a GET that is not public fails to load, and so does another value", func(t *testing.T) {
		bad := strings.Replace(string(raw), "      operationId: getAuthSettings\n", "      operationId: getAuthSettings\n      x-trove-part: wiki\n", 1)
		if _, err := LoadDocument([]byte(bad)); err == nil || !strings.Contains(err.Error(), "x-trove-part") {
			t.Fatalf("err = %v", err)
		}
		bad = strings.Replace(string(raw), "      x-trove-part: wiki\n", "      x-trove-part: artifacts\n", 1)
		if _, err := LoadDocument([]byte(bad)); err == nil || !strings.Contains(err.Error(), "x-trove-part") {
			t.Fatalf("err = %v", err)
		}
	})
}

// refsOf renders every $ref below v as one string.
func refsOf(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if k == "$ref" {
					if s, ok := e.(string); ok {
						b.WriteString(s + " ")
					}
					continue
				}
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}
