package apitest

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ReservedNameProblems is the CI check of R-API-004: a literal path segment at
// the position where a collection's key or name sits (/users/me next to
// /users/{username}) is a reserved name of that collection, and the key schema
// lists it as x-trove-reserved-names. It derives every literal sibling of a
// {key} segment from the document and reports each that the parameter's schema
// does not list. A collection addressed by a server-generated identifier has no
// literal sibling and needs no list.
func ReservedNameProblems(spec []byte) ([]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, err
	}
	paths, _ := doc["paths"].(map[string]any)
	templates := make([]string, 0, len(paths))
	for p := range paths {
		templates = append(templates, p)
	}
	sort.Strings(templates)

	var problems []string
	for _, p := range templates {
		segs := strings.Split(strings.Trim(p, "/"), "/")
		for i, seg := range segs {
			if !strings.HasPrefix(seg, "{") {
				continue
			}
			literals := literalSiblings(templates, segs, i)
			if len(literals) == 0 {
				continue
			}
			listed := reservedList(doc, paths[p], strings.Trim(seg, "{}"))
			for _, l := range literals {
				if !contains(listed, l) {
					problems = append(problems, fmt.Sprintf("%s: the literal segment %q next to {%s} is not in x-trove-reserved-names of the parameter's schema",
						p, l, strings.Trim(seg, "{}")))
				}
			}
		}
	}
	return problems, nil
}

// literalSiblings returns the literal segments that stand at position i in
// other paths sharing the segments before i.
func literalSiblings(templates, segs []string, i int) []string {
	prefix := strings.Join(segs[:i], "/")
	set := map[string]bool{}
	for _, other := range templates {
		o := strings.Split(strings.Trim(other, "/"), "/")
		if len(o) <= i || strings.Join(o[:i], "/") != prefix || strings.HasPrefix(o[i], "{") {
			continue
		}
		set[o[i]] = true
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// reservedList returns the x-trove-reserved-names of the named path parameter of
// a path item (declared on the item or on one of its operations), following a
// $ref of the parameter or of its schema.
func reservedList(doc map[string]any, item any, name string) []string {
	m, _ := item.(map[string]any)
	var params []any
	if ps, ok := m["parameters"].([]any); ok {
		params = append(params, ps...)
	}
	for _, op := range m {
		if om, ok := op.(map[string]any); ok {
			if ps, ok := om["parameters"].([]any); ok {
				params = append(params, ps...)
			}
		}
	}
	for _, p := range params {
		pm := resolve(doc, p)
		if pm["in"] != "path" || pm["name"] != name {
			continue
		}
		schema := resolve(doc, pm["schema"])
		if raw, ok := schema["x-trove-reserved-names"].([]any); ok {
			var out []string
			for _, r := range raw {
				if s, ok := r.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// resolve follows local $refs ("#/components/...") of a node.
func resolve(doc map[string]any, node any) map[string]any {
	for range 8 {
		m, _ := node.(map[string]any)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		cur := any(doc)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cm, _ := cur.(map[string]any)
			cur = cm[part]
		}
		node = cur
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
