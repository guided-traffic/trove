package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

const nugetLikeSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "deleteMode":    {"type": "string", "enum": ["unlist", "delete"], "x-trove-classes": ["local"]},
    "forwardSearch": {"type": "boolean", "x-trove-classes": ["remote"]}
  }
}`

func nugetValidate(t *testing.T, body string) (Document, error) {
	t.Helper()
	r := settingsCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "nuget", Classes: []Class{Local}, Layout: "nuget", ProtocolBasePath: "/api/nuget/{key}/",
		PatternSubject: SubjectName, PackageNames: true, SettingsSchema: []byte(nugetLikeSchema),
	}, Names: lower{}})
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(r, &d, true)
	return d, v.err()
}

// TestR_NUGET_007_TheNuGetMemberOfADocument: the member named after the format is a member of the
// document, validated against the format's schema and stored in its canonical form, and a member
// of another format is not applicable (R-REPO-004).
func TestR_NUGET_007_TheNuGetMemberOfADocument(t *testing.T) {
	if !HasFormatMember("nuget") || IsReservedMember("nuget") {
		t.Fatal("nuget is not a settings member of the repository document")
	}
	t.Run("the member is accepted and canonical", func(t *testing.T) {
		d, err := nugetValidate(t, `{"key":"nuget-local","class":"local","format":"nuget","nuget":{ "deleteMode" : "delete" }}`)
		if err != nil {
			t.Fatal(err)
		}
		if string(d.NuGet) != `{"deleteMode":"delete"}` {
			t.Fatalf("nuget = %s", d.NuGet)
		}
	})
	t.Run("a value outside the schema is refused at the member", func(t *testing.T) {
		_, err := nugetValidate(t, `{"key":"nuget-local","class":"local","format":"nuget","nuget":{"deleteMode":"purge"}}`)
		if got := fieldsOf(err)["/nuget/deleteMode"]; got != catalog.SettingInvalid {
			t.Fatalf("fields = %v (%v)", fieldsOf(err), err)
		}
	})
	t.Run("the member of another format is not applicable", func(t *testing.T) {
		_, err := nugetValidate(t, `{"key":"npm-local","class":"local","format":"npm","nuget":{"deleteMode":"delete"}}`)
		if got := fieldsOf(err)["/nuget"]; got != errs.CodeNotApplicable {
			t.Fatalf("fields = %v (%v)", fieldsOf(err), err)
		}
	})
}
