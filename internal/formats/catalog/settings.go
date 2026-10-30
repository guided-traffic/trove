package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The codes of a [SettingViolation]: the repository model answers them as the
// codes of its 422 field errors (R-REPO-008).
const (
	// SettingInvalid is a value the schema refuses: a wrong type, an unknown member,
	// a value outside an enum or a range.
	SettingInvalid = "invalid_value"
	// SettingNotApplicable is a member of the settings that the class of the
	// repository does not use (the schema's x-trove-classes).
	SettingNotApplicable = "not_applicable"
)

// SettingViolation is one finding of [Entry.ValidateSettings].
type SettingViolation struct {
	// Pointer is the JSON Pointer of the member inside the settings object ("" for the
	// object itself); the caller prefixes the name of the settings member.
	Pointer string
	Code    string
	Detail  string
}

// settingsSchema is the compiled form of [Facts.SettingsSchema].
type settingsSchema struct {
	schema *jsonschema.Schema
	// classes holds the classes of the members that name some (x-trove-classes); a member
	// that names none applies to every class.
	classes map[string][]Class
}

// compileSettings compiles the JSON Schema 2020-12 of a format's settings member
// (R-ARCH-008). The root must be an object that refuses members it does not list
// (additionalProperties false), because a settings document that silently drops a
// misspelt member leaves the operator believing it is in effect (R-REPO-008).
func compileSettings(id ID, raw []byte) (*settingsSchema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%q: the settings schema is no JSON: %w", id, err)
	}
	root, ok := doc.(map[string]any)
	if !ok || root["type"] != "object" || root["additionalProperties"] != false {
		return nil, fmt.Errorf(`%q: the settings schema must be an object schema with "additionalProperties": false`, id)
	}
	out := &settingsSchema{classes: map[string][]Class{}}
	props, _ := root["properties"].(map[string]any)
	for name, p := range props {
		decl, _ := p.(map[string]any)
		list, ok := decl["x-trove-classes"].([]any)
		if !ok {
			continue
		}
		for _, v := range list {
			c, _ := v.(string)
			if !Class(c).Valid() {
				return nil, fmt.Errorf("%q: the settings member %q applies to the class %q, which does not exist", id, name, c)
			}
			out.classes[name] = append(out.classes[name], Class(c))
		}
	}
	url := "trove:formats/" + string(id) + "/settings.json"
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("%q: the settings schema is refused: %w", id, err)
	}
	if out.schema, err = c.Compile(url); err != nil {
		return nil, fmt.Errorf("%q: the settings schema does not compile: %w", id, err)
	}
	return out, nil
}

// HasSettings reports whether the format declares a settings schema.
func (e Entry) HasSettings() bool { return e.settings != nil }

// ValidateSettings checks the settings member of a repository document of the given
// class against the format's schema (R-ARCH-008, R-REPO-008): every violation is
// reported, not the first. A member whose schema names classes (x-trove-classes) is
// not applicable to a repository of another class. A format without a settings schema
// takes no settings: any content is a violation. An empty member is valid.
func (e Entry) ValidateSettings(class Class, raw json.RawMessage) []SettingViolation {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if e.settings == nil {
		return []SettingViolation{{Code: SettingNotApplicable, Detail: fmt.Sprintf("the %s format has no settings", e.ID)}}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []SettingViolation{{Code: SettingInvalid, Detail: "must be a JSON object"}}
	}
	var out []SettingViolation
	if err := e.settings.schema.Validate(inst); err != nil {
		var verr *jsonschema.ValidationError
		if !errors.As(err, &verr) {
			return []SettingViolation{{Code: SettingInvalid, Detail: err.Error()}}
		}
		for _, u := range verr.BasicOutput().Errors {
			if u.Error != nil {
				out = append(out, SettingViolation{Pointer: u.InstanceLocation, Code: SettingInvalid, Detail: u.Error.String()})
			}
		}
	}
	if obj, ok := inst.(map[string]any); ok {
		names := make([]string, 0, len(obj))
		for name := range obj {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if cs, scoped := e.settings.classes[name]; scoped && !hasClass(cs, class) {
				out = append(out, SettingViolation{
					Pointer: "/" + name, Code: SettingNotApplicable,
					Detail: "the setting applies to " + joinClasses(cs) + " repositories",
				})
			}
		}
	}
	return out
}

func hasClass(list []Class, c Class) bool {
	for _, k := range list {
		if k == c {
			return true
		}
	}
	return false
}

func joinClasses(list []Class) string {
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = string(c)
	}
	return strings.Join(names, " and ")
}
