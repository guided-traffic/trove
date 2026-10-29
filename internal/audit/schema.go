package audit

import (
	"encoding/json"
	"errors"
	"fmt"
)

// maxArrayItems is the bound every array of a data schema declares (R-OPS-014):
// templated webhooks rely on it (R-OPS-041).
const maxArrayItems = 1000

// checkSchema verifies that schema is a JSON object and that every array in it,
// at any depth, declares maxItems of at most [maxArrayItems].
func checkSchema(schema string) error {
	var doc any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return errors.New("not a JSON object")
	}
	return walkSchema(doc, "#")
}

func walkSchema(n any, path string) error {
	switch v := n.(type) {
	case map[string]any:
		if isArray(v["type"]) {
			m, ok := v["maxItems"].(float64)
			if !ok || m < 0 || m > maxArrayItems {
				return fmt.Errorf("the array at %s declares no maxItems of at most %d", path, maxArrayItems)
			}
		}
		for k, c := range v {
			if err := walkSchema(c, path+"/"+k); err != nil {
				return err
			}
		}
	case []any:
		for i, c := range v {
			if err := walkSchema(c, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// isArray reports whether a schema "type" is, or lists, "array".
func isArray(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "array"
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == "array" {
				return true
			}
		}
	}
	return false
}
