package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// rule is one parsed element of a `validate` tag.
type rule struct {
	op    string   // "min", "max" or "oneof"
	num   float64  // bound of min and max, in the unit of the type
	text  string   // the bound as written, for messages
	oneof []string // allowed values of oneof
}

func parseRules(t reflect.Type, tag string) (rules []rule, required bool, err error) {
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, arg, hasArg := strings.Cut(part, "=")
		switch name {
		case "required":
			if hasArg {
				return nil, false, fmt.Errorf("required takes no argument")
			}
			required = true
		case "min", "max":
			if !hasArg {
				return nil, false, fmt.Errorf("%s needs a bound", name)
			}
			n, err := boundOf(t, arg)
			if err != nil {
				return nil, false, fmt.Errorf("%s=%s: %w", name, arg, err)
			}
			rules = append(rules, rule{op: name, num: n, text: arg})
		case "oneof":
			if t.Kind() != reflect.String {
				return nil, false, fmt.Errorf("oneof needs a string field")
			}
			if !hasArg || arg == "" {
				return nil, false, fmt.Errorf("oneof needs values separated by |")
			}
			rules = append(rules, rule{op: "oneof", oneof: strings.Split(arg, "|")})
		default:
			return nil, false, fmt.Errorf("unknown rule %q", name)
		}
	}
	return rules, required, nil
}

// boundOf parses a bound in the unit its type uses: nanoseconds for durations, bytes for sizes, the number itself otherwise, a length for strings
// and collections.
func boundOf(t reflect.Type, arg string) (float64, error) {
	if t == durTyp || t == sizeTyp {
		pv, err := parseText(t, arg)
		if err != nil {
			return 0, err
		}
		return float64(pv.Int()), nil
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float64:
		pv, err := parseText(reflect.TypeOf(float64(0)), arg)
		if err != nil {
			return 0, err
		}
		return pv.Float(), nil
	case reflect.String, reflect.Slice, reflect.Map:
		pv, err := parseText(reflect.TypeOf(float64(0)), arg)
		if err != nil {
			return 0, err
		}
		return pv.Float(), nil
	}
	return 0, fmt.Errorf("min and max do not apply to %s", t)
}

// measure returns the number a min/max rule compares and a printable form of it.
func measure(v reflect.Value) (float64, string) {
	switch {
	case v.Type() == durTyp:
		return float64(v.Int()), formatDuration(time.Duration(v.Int()))
	case v.Type() == sizeTyp:
		return float64(v.Int()), ByteSize(v.Int()).String()
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		return float64(v.Int()), fmt.Sprint(v.Int())
	case reflect.Float64:
		return v.Float(), fmt.Sprint(v.Float())
	case reflect.String, reflect.Slice, reflect.Map:
		return float64(v.Len()), fmt.Sprint(v.Len())
	}
	return 0, ""
}

func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	}
	return v.IsZero()
}

// check evaluates the rules of key against value and returns one message per
// violation, each starting with the key path.
func (k Key) check(v reflect.Value) []string {
	var out []string
	if k.Required && isZero(v) {
		return []string{k.Path + " is required"}
	}
	for _, r := range k.Rules {
		switch r.op {
		case "oneof":
			s := v.String()
			if s == "" {
				continue // emptiness is the business of "required"
			}
			found := false
			for _, o := range r.oneof {
				if s == o {
					found = true
				}
			}
			if !found {
				out = append(out, fmt.Sprintf("%s must be one of %s (is %q)", k.Path, strings.Join(r.oneof, ", "), s))
			}
		case "min", "max":
			n, shown := measure(v)
			unit := ""
			if k.Type.Kind() == reflect.String || k.Type.Kind() == reflect.Slice || k.Type.Kind() == reflect.Map {
				unit = " entries or characters"
			}
			if r.op == "min" && n < r.num {
				out = append(out, fmt.Sprintf("%s must be at least %s%s (is %s)", k.Path, r.text, unit, shown))
			}
			if r.op == "max" && n > r.num {
				out = append(out, fmt.Sprintf("%s must be at most %s%s (is %s)", k.Path, r.text, unit, shown))
			}
		}
	}
	return out
}
