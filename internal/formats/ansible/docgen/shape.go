package docgen

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// The decoding of DOCUMENTATION and RETURN (R-ANS-034, Q-ANS-58): the text is parsed into a YAML node tree and walked, never decoded into Go
// values by the parser, so the parser's expansion rules are not relied on. The refusals are those of meta/runtime.yml: an anchor, an alias, a tag
// or a merge key, more than maxYAMLNodes nodes or a nesting deeper than maxYAMLDepth. The result is shaped like the Galaxy's doc_strings
// (R-ANS-035): the options and the return values become lists of named objects, and only the keys of the contract are kept.

const (
	maxYAMLNodes = 20000
	maxYAMLDepth = 24
	// maxShortDescription is the longest short description kept, in characters.
	maxShortDescription = 300
)

// yamlNode and scalarKind keep the callers short.
type yamlNode = yaml.Node

const scalarKind = yaml.ScalarNode

var errYAMLRefused = errors.New("the YAML uses an anchor, an alias, a tag or a merge key, or is too large or too deep")

// parseYAML parses text and checks the tree against the refusals. An empty document is a nil node.
func parseYAML(text string) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil, errors.New("the text is not valid YAML")
	}
	if root.Kind == 0 {
		return nil, nil
	}
	nodes := 0
	var walk func(n *yaml.Node, depth int) error
	walk = func(n *yaml.Node, depth int) error {
		if nodes++; nodes > maxYAMLNodes || depth > maxYAMLDepth {
			return errYAMLRefused
		}
		if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 || n.Kind == yaml.ScalarNode && n.Tag == "!!merge" {
			return errYAMLRefused
		}
		for _, c := range n.Content {
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&root, 0); err != nil {
		return nil, err
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return nil, nil
		}
		return root.Content[0], nil
	}
	return &root, nil
}

// pairs lists the key and value nodes of a mapping whose keys are scalars, in document order.
func pairs(n *yaml.Node) (keys []string, vals []*yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Kind == yaml.ScalarNode {
			keys, vals = append(keys, k.Value), append(vals, n.Content[i+1])
		}
	}
	return keys, vals
}

// member returns the value of a key of a mapping.
func member(n *yaml.Node, key string) *yaml.Node {
	keys, vals := pairs(n)
	for i, k := range keys {
		if k == key {
			return vals[i]
		}
	}
	return nil
}

// scalarValue is the typed value of a scalar node: an integer, a float, a boolean or null keep their type, anything else is its text.
func scalarValue(n *yaml.Node) any {
	switch n.Tag {
	case "!!int":
		var v int64
		if n.Decode(&v) == nil {
			return v
		}
	case "!!float":
		var f float64
		if n.Decode(&f) == nil && f-f == 0 {
			return f
		}
	case "!!bool":
		var b bool
		if n.Decode(&b) == nil {
			return b
		}
	case "!!null":
		return nil
	}
	return n.Value
}

// generic is the JSON value of a node, to a nesting of maxYAMLDepth.
func generic(n *yaml.Node, depth int) any {
	if n == nil || depth > maxYAMLDepth {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return scalarValue(n)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, generic(c, depth+1))
		}
		return out
	case yaml.MappingNode:
		keys, vals := pairs(n)
		out := make(map[string]any, len(keys))
		for i, k := range keys {
			out[k] = generic(vals[i], depth+1)
		}
		return out
	}
	return nil
}

// text is the text of a scalar node, "" for anything else.
func text(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// textList is a scalar as a list of one string, or the scalars of a sequence; other nodes are left out.
func textList(n *yaml.Node) []string {
	switch {
	case n == nil:
		return nil
	case n.Kind == yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil
		}
		return []string{n.Value}
	case n.Kind == yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Tag != "!!null" {
				out = append(out, c.Value)
			}
		}
		return out
	}
	return nil
}

// plainShort is a short description as text: control characters removed, at most maxShortDescription characters.
func plainShort(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxShortDescription {
		s = string([]rune(s)[:maxShortDescription])
	}
	return s
}

// searchWords collects the words that make an entry findable: the names of its options and return values and the plain text of the prose.
type searchWords struct{ parts []string }

func (w *searchWords) add(s string) {
	if s = strings.TrimSpace(s); s != "" {
		w.parts = append(w.parts, s)
	}
}

// optionList shapes a mapping of options (or suboptions) into a list of named objects; names collects the option names.
func optionList(n *yaml.Node, w *searchWords, depth int) []any {
	keys, vals := pairs(n)
	if len(keys) == 0 || depth > maxYAMLDepth {
		return nil
	}
	out := make([]any, 0, len(keys))
	for i, name := range keys {
		spec := vals[i]
		o := map[string]any{"name": name}
		w.add(name)
		for _, f := range []string{"type", "elements", "version_added"} {
			if v := member(spec, f); v != nil && v.Kind == yaml.ScalarNode {
				o[f] = v.Value
			}
		}
		if v := member(spec, "required"); v != nil && v.Kind == yaml.ScalarNode {
			o["required"] = scalarValue(v)
		}
		for _, f := range []string{"default", "choices"} {
			if v := member(spec, f); v != nil {
				o[f] = generic(v, 0)
			}
		}
		if v := textList(member(spec, "aliases")); v != nil {
			o["aliases"] = v
		}
		if v := textList(member(spec, "description")); v != nil {
			o["description"] = v
			for _, d := range v {
				w.add(PlainText(d))
			}
		}
		if sub := optionList(member(spec, "suboptions"), w, depth+1); sub != nil {
			o["suboptions"] = sub
		}
		out = append(out, o)
	}
	return out
}

// returnList shapes the RETURN mapping into a list of named objects.
func returnList(n *yaml.Node, w *searchWords, depth int) []any {
	keys, vals := pairs(n)
	if len(keys) == 0 || depth > maxYAMLDepth {
		return nil
	}
	out := make([]any, 0, len(keys))
	for i, name := range keys {
		spec := vals[i]
		o := map[string]any{"name": name}
		w.add(name)
		for _, f := range []string{"type", "elements", "returned", "version_added"} {
			if v := member(spec, f); v != nil && v.Kind == yaml.ScalarNode {
				o[f] = v.Value
			}
		}
		if v := member(spec, "sample"); v != nil {
			o["sample"] = generic(v, 0)
		}
		if v := textList(member(spec, "description")); v != nil {
			o["description"] = v
			for _, d := range v {
				w.add(PlainText(d))
			}
		}
		if c := returnList(member(spec, "contains"), w, depth+1); c != nil {
			o["contains"] = c
		}
		out = append(out, o)
	}
	return out
}

// seeAlso keeps the scalar members of the entries of seealso that the page shows.
func seeAlso(n *yaml.Node) []any {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []any
	for _, e := range n.Content {
		item := map[string]any{}
		for _, f := range []string{"module", "name", "plugin", "plugin_type", "ref", "description", "link"} {
			if v := member(e, f); v != nil && v.Kind == yaml.ScalarNode {
				item[f] = v.Value
			}
		}
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
}

// shapeDoc turns the node of DOCUMENTATION into the doc member of doc_strings and returns the short description. It is nil when the node is
// not a mapping.
func shapeDoc(n *yaml.Node, contentType, contentName string, w *searchWords) (doc map[string]any, short string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, ""
	}
	doc = map[string]any{"plugin_type": contentType}
	module := text(member(n, "module"))
	if module == "" {
		module = text(member(n, "name"))
	}
	if module == "" {
		module = contentName
	}
	doc["module"] = module
	short = plainShort(text(member(n, "short_description")))
	doc["short_description"] = short
	w.add(PlainText(short))
	if v := text(member(n, "version_added")); v != "" {
		doc["version_added"] = v
	}
	for _, f := range []string{"description", "notes", "requirements", "author", "extends_documentation_fragment"} {
		if v := textList(member(n, f)); v != nil {
			doc[f] = v
		}
	}
	for _, d := range textList(member(n, "description")) {
		w.add(PlainText(d))
	}
	if o := optionList(member(n, "options"), w, 0); o != nil {
		doc["options"] = o
	}
	if s := seeAlso(member(n, "seealso")); s != nil {
		doc["seealso"] = s
	}
	if a := member(n, "attributes"); a != nil && a.Kind == yaml.MappingNode {
		doc["attributes"] = generic(a, 0)
	}
	if d := member(n, "deprecated"); d != nil && d.Kind == yaml.MappingNode {
		dep := map[string]any{}
		for _, f := range []string{"why", "alternative", "removed_in", "removed_at_date", "removed_from_collection"} {
			if v := member(d, f); v != nil && v.Kind == yaml.ScalarNode {
				dep[f] = v.Value
			}
		}
		doc["deprecated"] = dep
	}
	return doc, short
}
