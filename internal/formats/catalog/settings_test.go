package catalog

import (
	"strings"
	"testing"
)

const testSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "guard":    {"type": "boolean", "x-trove-classes": ["local"]},
    "strategy": {"type": "string", "enum": ["a", "b"], "x-trove-classes": ["virtual"]},
    "limit":    {"type": "integer", "minimum": 1}
  }
}`

func settingsEntry(t *testing.T, schema string) Entry {
	t.Helper()
	r := NewRegistry()
	e := valid("fmt")
	e.SettingsSchema = []byte(schema)
	r.Register(e)
	got, _ := r.Lookup("fmt")
	return got
}

func TestSettingsSchema(t *testing.T) {
	e := settingsEntry(t, testSchema)
	pointers := func(vs []SettingViolation) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Pointer+" "+v.Code)
		}
		return out
	}

	t.Run("R-ARCH-008 a document that matches the schema has no violation", func(t *testing.T) {
		if vs := e.ValidateSettings(Local, []byte(`{"guard": true, "limit": 3}`)); len(vs) != 0 {
			t.Fatalf("violations: %v", vs)
		}
		if vs := e.ValidateSettings(Local, nil); len(vs) != 0 {
			t.Fatalf("an empty member has violations: %v", vs)
		}
		if vs := e.ValidateSettings(Local, []byte(`null`)); len(vs) != 0 {
			t.Fatalf("a null member has violations: %v", vs)
		}
	})
	t.Run("R-ARCH-008 R-REPO-008 every violation is reported with the pointer of its member", func(t *testing.T) {
		vs := e.ValidateSettings(Virtual, []byte(`{"strategy": "c", "limit": 0, "unknown": 1}`))
		got := strings.Join(pointers(vs), ", ")
		for _, want := range []string{"/strategy invalid_value", "/limit invalid_value", " invalid_value"} {
			if !strings.Contains(got, want) {
				t.Errorf("violations %q lack %q", got, want)
			}
		}
		for _, v := range vs {
			if v.Detail == "" {
				t.Errorf("a violation without a detail: %+v", v)
			}
		}
	})
	t.Run("R-ARCH-008 a wrong type is a violation at the member", func(t *testing.T) {
		vs := e.ValidateSettings(Local, []byte(`{"guard": "yes"}`))
		if got := strings.Join(pointers(vs), ", "); got != "/guard invalid_value" {
			t.Fatalf("violations: %q", got)
		}
	})
	t.Run("R-REPO-004 a member of another class is not applicable", func(t *testing.T) {
		vs := e.ValidateSettings(Remote, []byte(`{"guard": true, "strategy": "a", "limit": 2}`))
		if got := strings.Join(pointers(vs), ", "); got != "/guard not_applicable, /strategy not_applicable" {
			t.Fatalf("violations: %q", got)
		}
	})
	t.Run("R-ARCH-008 content that is no object is refused", func(t *testing.T) {
		for _, raw := range []string{`[1]`, `"x"`, `{`} {
			if vs := e.ValidateSettings(Local, []byte(raw)); len(vs) == 0 {
				t.Errorf("%s was accepted", raw)
			}
		}
	})
	t.Run("R-ARCH-008 a format without a schema takes no settings", func(t *testing.T) {
		r := NewRegistry()
		r.Register(valid("plain"))
		plain, _ := r.Lookup("plain")
		if plain.HasSettings() {
			t.Fatal("HasSettings")
		}
		if vs := plain.ValidateSettings(Local, []byte(`{"a": 1}`)); len(vs) != 1 || vs[0].Code != SettingNotApplicable {
			t.Fatalf("violations: %v", vs)
		}
		if !e.HasSettings() {
			t.Fatal("a format with a schema reports none")
		}
	})
}

func TestSettingsSchemaRegistration(t *testing.T) {
	for name, schema := range map[string]string{
		"not json":                 `{`,
		"not an object schema":     `{"type": "string"}`,
		"unknown members allowed":  `{"type": "object", "properties": {}}`,
		"does not compile":         `{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "nope"}}}`,
		"a class that is none":     `{"type": "object", "additionalProperties": false, "properties": {"a": {"type": "string", "x-trove-classes": ["cloud"]}}}`,
		"additionalProperties yes": `{"type": "object", "additionalProperties": true}`,
	} {
		t.Run("R-ARCH-008 a settings schema that is "+name+" panics at registration", func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register accepted the schema")
				}
			}()
			e := valid("fmt")
			e.SettingsSchema = []byte(schema)
			NewRegistry().Register(e)
		})
	}
}
