//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// specValuesBlock returns the YAML of the values block of docs/spec/ha-and-helm.md (the fenced block under
// the summary deploy/helm/trove/values.yaml).
func specValuesBlock(t *testing.T) string {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join(repoRoot, "docs", "spec", "ha-and-helm.md"))
	if err != nil {
		t.Fatal(err)
	}
	var block []string
	state := 0
	for _, l := range strings.Split(string(spec), "\n") {
		switch {
		case state == 0 && strings.Contains(l, "<summary>deploy/helm/trove/values.yaml</summary>"):
			state = 1
		case state == 1 && strings.HasPrefix(l, "```yaml"):
			state = 2
		case state == 2 && strings.HasPrefix(l, "```"):
			state = 3
		case state == 2:
			block = append(block, l)
		}
	}
	if state != 3 || len(block) < 100 {
		t.Fatalf("the values block of docs/spec/ha-and-helm.md was not found (state %d, %d lines)", state, len(block))
	}
	return strings.Join(block, "\n")
}

// schemaAllows reports whether the JSON schema node declares the key path (a map-valued key whose schema has
// additionalProperties other than false accepts any child).
func schemaAllows(node map[string]any, path []string) bool {
	if len(path) == 0 {
		return true
	}
	if props, ok := node["properties"].(map[string]any); ok {
		if child, ok := props[path[0]].(map[string]any); ok {
			return schemaAllows(child, path[1:])
		}
	}
	switch ap := node["additionalProperties"].(type) {
	case map[string]any:
		return schemaAllows(ap, path[1:])
	case bool:
		return ap
	}
	// JSON schema: an object without additionalProperties accepts any further key.
	return node["type"] == "object"
}

// unbuiltPrefixes are the values of the spec block that the chart does not carry because the feature that owns
// them is not rendered yet: the chart-created Gateway (R-HA-053), the content origin
// (R-HA-055), the bootstrap ConfigMap, dual-stack and Service tuning (R-HA-044), trusted CA bundles (R-HA-043) and the
// extension points (R-HA-046). The keys under config belong to the binary's schema (R-HA-029) and are shown by the spec
// block as far as the pages name them. The backup and restore values (R-HA-060, R-HA-030) are the chart's and compared
// like every other value.
var unbuiltPrefixes = []string{
	"config.", "bootstrap.", "contentOrigin.", "gateway.create.",
	"secrets.s3.sessionTokenKey",
	"service.externalTrafficPolicy", "service.ipFamilyPolicy", "service.ipFamilies", "service.loadBalancerSourceRanges",
	"trustedCA.", "sidecars", "initContainers", "lifecycle",
}

func unbuiltValue(path string) bool {
	for _, p := range unbuiltPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// TestR_HA_031_ValuesReference (R-HA-031): the values block of the specification is the reference of the chart's
// values.yaml: every key of the chart is in it with the same default, every key of it is in the chart unless an
// unbuilt feature owns it (unbuiltPrefixes), and values.schema.json declares every key of values.yaml, so no
// documented value fails at helm and no value of the chart is undocumented.
func TestR_HA_031_ValuesReference(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("every key of values.yaml is in the spec block, and the spec block has no further key but those of features the chart does not render yet", func(t *testing.T) {
		got, want := sortedLeaves(t, string(raw)), sortedLeaves(t, specValuesBlock(t))
		inChart := map[string]bool{}
		for _, g := range got {
			inChart[g] = true
		}
		inSpec := map[string]bool{}
		for _, w := range want {
			inSpec[w] = true
			if !inChart[w] && !unbuiltValue(w) {
				t.Errorf("the spec block has %s, values.yaml does not, and no unbuilt feature owns it", w)
			}
		}
		for _, g := range got {
			if !inSpec[g] {
				t.Errorf("values.yaml has %s, the spec block does not", g)
			}
		}
	})
	t.Run("every default of values.yaml equals the spec block's", func(t *testing.T) {
		var got, want map[string]any
		if err := yaml.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal([]byte(specValuesBlock(t)), &want); err != nil {
			t.Fatal(err)
		}
		var diff func(path string, a, b any)
		diff = func(path string, a, b any) {
			am, aok := a.(map[string]any)
			bm, bok := b.(map[string]any)
			if aok && bok && len(am) > 0 && len(bm) > 0 {
				for k, av := range am {
					if bv, ok := bm[k]; ok {
						diff(path+"."+k, av, bv)
					}
				}
				return
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s: values.yaml %v, spec block %v", strings.TrimPrefix(path, "."), a, b)
			}
		}
		diff("", got, want)
	})
	t.Run("values.schema.json declares every key of values.yaml", func(t *testing.T) {
		schema := readJSONFile(t, filepath.Join(chartDir, "values.schema.json"))
		for _, leaf := range sortedLeaves(t, string(raw)) {
			if !schemaAllows(schema, strings.Split(leaf, ".")) {
				t.Errorf("values.schema.json does not declare %s", leaf)
			}
		}
	})
}
