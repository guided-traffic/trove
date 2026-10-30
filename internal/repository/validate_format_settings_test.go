package repository

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

const npmLikeSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "latestGuard":          {"type": "boolean", "x-trove-classes": ["local"]},
    "serveRemovedVersions": {"type": "boolean", "x-trove-classes": ["remote"]},
    "latestStrategy":       {"type": "string", "enum": ["highest-member-latest", "newest-published"], "x-trove-classes": ["virtual"]}
  }
}`

// settingsCatalog holds an npm-like format with a settings schema and the generic format,
// which has none.
func settingsCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npm", Classes: []Class{Local, Remote, Virtual}, Layout: "npm", ProtocolBasePath: "/api/npm/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		SettingsSchema: []byte(npmLikeSchema),
	}, Names: lower{}})
	return r
}

func settingsValidate(t *testing.T, body string) (Document, error) {
	t.Helper()
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(settingsCatalog(), &d, true)
	return d, v.err()
}

func TestFormatSettingsMember(t *testing.T) {
	t.Run("R-REPO-004 the member named after the format is accepted and read back", func(t *testing.T) {
		d, err := settingsValidate(t, `{"key":"npm-local","class":"local","format":"npm","npm":{"latestGuard":false}}`)
		if err != nil {
			t.Fatal(err)
		}
		if string(d.NPM) != `{"latestGuard":false}` {
			t.Fatalf("npm = %s", d.NPM)
		}
	})
	t.Run("R-REPO-004 the stored form is canonical: key order and spacing do not make two documents", func(t *testing.T) {
		a, err := settingsValidate(t, `{"key":"npm-v","class":"virtual","format":"npm","npm":{ "latestStrategy" : "newest-published" }}`)
		if err != nil {
			t.Fatal(err)
		}
		b := Document{Settings: Settings{NPM: json.RawMessage(`{"latestStrategy":"newest-published"}`)}}
		b.normalize()
		if string(a.NPM) != string(b.NPM) {
			t.Fatalf("%s != %s", a.NPM, b.NPM)
		}
		stored, err := a.marshalConfig()
		if err != nil {
			t.Fatal(err)
		}
		var back Document
		if err := json.Unmarshal(stored, &back.Settings); err != nil {
			t.Fatal(err)
		}
		back.normalize()
		if string(back.NPM) != string(a.NPM) {
			t.Fatalf("read back %s, stored %s", back.NPM, a.NPM)
		}
	})
	t.Run("R-REPO-008 a value the schema refuses answers 422 with the pointer of the field", func(t *testing.T) {
		_, err := settingsValidate(t, `{"key":"npm-v","class":"virtual","format":"npm","npm":{"latestStrategy":"oldest"}}`)
		if e, ok := errs.As(err); !ok || e.Status() != 422 {
			t.Fatalf("err = %v", err)
		}
		if got := fieldsOf(err)["/npm/latestStrategy"]; got != catalog.SettingInvalid {
			t.Fatalf("fields = %v", fieldsOf(err))
		}
	})
	t.Run("R-REPO-008 an unknown setting and a wrong type are refused, every one reported", func(t *testing.T) {
		_, err := settingsValidate(t, `{"key":"npm-l","class":"local","format":"npm","npm":{"latestGuard":"yes","guard":true}}`)
		got := fieldsOf(err)
		if got["/npm/latestGuard"] != catalog.SettingInvalid || got["/npm"] != catalog.SettingInvalid {
			t.Fatalf("fields = %v", got)
		}
	})
	t.Run("R-REPO-004 a setting of another class is not applicable", func(t *testing.T) {
		_, err := settingsValidate(t, `{"key":"npm-r","class":"remote","format":"npm","remote":{"url":"https://registry.example.org/"},"npm":{"latestGuard":true}}`)
		if got := fieldsOf(err)["/npm/latestGuard"]; got != errs.CodeNotApplicable {
			t.Fatalf("fields = %v (%v)", fieldsOf(err), err)
		}
	})
	t.Run("R-REPO-004 the member of one format is not applicable to a repository of another", func(t *testing.T) {
		_, err := settingsValidate(t, `{"key":"gen","class":"local","format":"generic","npm":{"latestGuard":true}}`)
		if got := fieldsOf(err)["/npm"]; got != errs.CodeNotApplicable {
			t.Fatalf("fields = %v (%v)", fieldsOf(err), err)
		}
	})
	t.Run("R-REPO-011 a null member is an omitted one", func(t *testing.T) {
		d, err := settingsValidate(t, `{"key":"npm-l","class":"local","format":"npm","npm":null}`)
		if err != nil || len(d.NPM) != 0 {
			t.Fatalf("npm = %s, err = %v", d.NPM, err)
		}
	})
}

func TestFormatMemberNames(t *testing.T) {
	t.Run("R-REPO-004 a format's settings member is no reserved member", func(t *testing.T) {
		if IsReservedMember("npm") || !HasFormatMember("npm") {
			t.Fatal("npm must be the settings member of the npm format")
		}
	})
	t.Run("R-REPO-004 the other members of the document are reserved", func(t *testing.T) {
		for _, m := range []string{"key", "class", "format", "layout", "description", "local", "remote", "virtual", "storage", "hideExistence"} {
			if !IsReservedMember(m) {
				t.Errorf("%q is a member of the document and not reserved", m)
			}
		}
		if IsReservedMember("nuget") {
			t.Error("nuget is no member yet")
		}
	})
}
