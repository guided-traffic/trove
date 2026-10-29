package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// parseText converts the text form of a value (default tag, environment
// variable, flag, YAML scalar) into a value of type t. Lists and maps are
// written as JSON, which is also valid YAML flow syntax, and decoded strictly.
func parseText(t reflect.Type, s string) (reflect.Value, error) {
	v := reflect.New(t).Elem()
	switch t {
	case durTyp:
		d, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return v, fmt.Errorf("invalid duration %q: want a Go duration such as 500ms, 10s or 1h30m", s)
		}
		v.SetInt(int64(d))
		return v, nil
	case sizeTyp:
		b, err := ParseByteSize(s)
		if err != nil {
			return v, err
		}
		v.SetInt(int64(b))
		return v, nil
	}
	switch t.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		switch strings.TrimSpace(s) {
		case "true":
			v.SetBool(true)
		case "false":
			v.SetBool(false)
		default:
			return v, fmt.Errorf("invalid boolean %q: want true or false", s)
		}
	case reflect.Int, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, t.Bits())
		if err != nil {
			return v, fmt.Errorf("invalid integer %q", s)
		}
		v.SetInt(n)
	case reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return v, fmt.Errorf("invalid number %q", s)
		}
		v.SetFloat(f)
	case reflect.Slice, reflect.Map:
		if strings.TrimSpace(s) == "" {
			return v, nil
		}
		ptr := reflect.New(t)
		if err := decodeYAML([]byte(s), ptr.Interface()); err != nil {
			return v, fmt.Errorf("invalid %s %q: want JSON (%w)", kindWord(t), s, err)
		}
		return ptr.Elem(), nil
	default:
		return v, fmt.Errorf("unsupported type %s", t)
	}
	return v, nil
}

func kindWord(t reflect.Type) string {
	if t.Kind() == reflect.Slice {
		return "list"
	}
	return "map"
}

// decodeYAML decodes strictly: an unknown field of a struct value is an error.
func decodeYAML(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// nodeValue converts a YAML node into a value of type t.
func nodeValue(t reflect.Type, n *yaml.Node) (reflect.Value, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			return reflect.Value{}, fmt.Errorf("must be a %s, not a scalar", kindWord(t))
		}
		return parseText(t, n.Value)
	case yaml.SequenceNode, yaml.MappingNode:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Map {
			return reflect.Value{}, fmt.Errorf("must be a scalar (%s), not a %s", t, map[yaml.Kind]string{yaml.SequenceNode: "list", yaml.MappingNode: "map"}[n.Kind])
		}
		raw, err := yaml.Marshal(n)
		if err != nil {
			return reflect.Value{}, err
		}
		ptr := reflect.New(t)
		if err := decodeYAML(raw, ptr.Interface()); err != nil {
			return reflect.Value{}, fmt.Errorf("invalid %s: %w", kindWord(t), err)
		}
		return ptr.Elem(), nil
	case yaml.AliasNode:
		return nodeValue(t, n.Alias)
	}
	return reflect.Value{}, fmt.Errorf("unsupported YAML node")
}

// displayValue is the typed form of a value in the effective-configuration
// view: durations and sizes as their text, everything else as itself.
func displayValue(v reflect.Value) any {
	switch v.Type() {
	case durTyp:
		return formatDuration(time.Duration(v.Int()))
	case sizeTyp:
		return ByteSize(v.Int()).String()
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		if v.IsNil() {
			if v.Kind() == reflect.Slice {
				return []string{}
			}
			return map[string]any{}
		}
	}
	if isStructList(v.Type()) {
		return elementMaps(v)
	}
	return v.Interface()
}

// elementMaps renders a list of structs with the yaml names of the fields, the
// names an operator writes, for the effective view and the JSON Schema default.
func elementMaps(v reflect.Value) []map[string]any {
	out := make([]map[string]any, v.Len())
	t := v.Type().Elem()
	for i := range out {
		m := map[string]any{}
		for j := 0; j < t.NumField(); j++ {
			name, _, _ := strings.Cut(t.Field(j).Tag.Get("yaml"), ",")
			m[name] = v.Index(i).Field(j).Interface()
		}
		out[i] = m
	}
	return out
}

// formatValue renders a value on one line for `admin config validate`.
func formatValue(v any) string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return `""`
		}
		return x
	case []string, map[string]string, map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return fmt.Sprint(v)
}

// formatDuration is time.Duration.String without the zero tails: 1m, not 1m0s.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
