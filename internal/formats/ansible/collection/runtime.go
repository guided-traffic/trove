package collection

import (
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// MaxRuntimeNodes bounds the nodes of meta/runtime.yml that are looked at (R-ANS-010).
const MaxRuntimeNodes = 1000

// maxRequiresAnsible bounds the stored value of requires_ansible; a specifier is a few dozen bytes.
const maxRequiresAnsible = 256

// requiresAnsible reads the one scalar trove takes from meta/runtime.yml: the top-level key requires_ansible (R-ANS-010,
// Q-ANS-30). The document is decoded into a node tree, never into Go values, and the tree is walked and not interpreted: a document
// with an anchor, an alias, an explicit tag or a merge key, with more than [MaxRuntimeNodes] nodes, that is not a mapping at its top,
// or whose key is not a plain string is refused, so the parser's own expansion rules are never relied on. The caller turns the error
// into an empty field and a warning. found is false for a document without the key.
func requiresAnsible(doc []byte) (value string, found bool, err error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return "", false, fmt.Errorf("meta/runtime.yml is not valid YAML")
	}
	if root.Kind == 0 {
		return "", false, nil // an empty document
	}
	nodes := 0
	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if nodes++; nodes > MaxRuntimeNodes {
			return fmt.Errorf("meta/runtime.yml has more than %d nodes", MaxRuntimeNodes)
		}
		if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
			return errors.New("meta/runtime.yml uses an anchor, an alias or a tag")
		}
		if n.Kind == yaml.ScalarNode && n.Tag == "!!merge" {
			return errors.New("meta/runtime.yml uses a merge key")
		}
		for _, c := range n.Content {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&root); err != nil {
		return "", false, err
	}
	top := &root
	if top.Kind == yaml.DocumentNode {
		if len(top.Content) != 1 {
			return "", false, nil
		}
		top = top.Content[0]
	}
	if top.Kind != yaml.MappingNode {
		return "", false, errors.New("meta/runtime.yml is not a mapping")
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != "requires_ansible" {
			continue
		}
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || len(v.Value) > maxRequiresAnsible || !printable(v.Value) {
			return "", false, errors.New("requires_ansible is not a short plain string")
		}
		return v.Value, true, nil
	}
	return "", false, nil
}

// printable reports whether s holds no control character.
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
