package repository

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// fakeCatalog holds the generic format as the composition root derives it from
// the format registry, and an npm-like format with package names to exercise the
// members that the generic format refuses.
type fakeCatalog map[string]FormatInfo

func (c fakeCatalog) Format(name string) (FormatInfo, bool) { f, ok := c[name]; return f, ok }

func testCatalog() fakeCatalog {
	return fakeCatalog{
		"generic": {
			Name: "generic", Classes: []Class{Local}, Layout: "generic", ProtocolBasePath: "/files/{key}/",
			PatternSubject: SubjectPath,
		},
		"pkgfmt": {
			Name: "pkgfmt", Classes: []Class{Local, Remote, Virtual}, Layout: "pkgfmt", ProtocolBasePath: "/api/pkgfmt/{key}/",
			PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		},
	}
}

func decodeValidate(t *testing.T, body string, create bool) (Document, *validation, error) {
	t.Helper()
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		return Document{}, nil, err
	}
	_, v := validate(testCatalog(), &d, create)
	return d, v, v.err()
}

func fieldsOf(err error) map[string]string {
	out := map[string]string{}
	if e, ok := errs.As(err); ok {
		for _, f := range e.Fields {
			out[f.Pointer] = f.Code
		}
	}
	return out
}

func TestValidateKeys(t *testing.T) {
	for _, key := range []string{"Npm", "a", "-x", "a..b", "x.", "npm~cache", "a b", strings.Repeat("a", 65), ""} {
		t.Run("R-REPO-002 refuses "+key, func(t *testing.T) {
			_, _, err := decodeValidate(t, `{"key":`+quote(key)+`,"class":"local","format":"generic"}`, true)
			if got := fieldsOf(err)["/key"]; got != errs.CodeInvalidKey {
				t.Fatalf("code = %q (%v), want invalid_key", got, err)
			}
			if e, _ := errs.As(err); e.Status() != 422 || e.Code != errs.CodeValidation {
				t.Fatalf("status %d code %q, want 422 validation_failed", e.Status(), e.Code)
			}
		})
	}
	for _, key := range []string{"npm-local", "a1", "x.y_z-1", strings.Repeat("a", 64)} {
		t.Run("R-REPO-002 accepts "+key, func(t *testing.T) {
			if _, _, err := decodeValidate(t, `{"key":`+quote(key)+`,"class":"local","format":"generic"}`, true); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, key := range ReservedKeys() {
		t.Run("R-REPO-002 reserved name "+key, func(t *testing.T) {
			_, _, err := decodeValidate(t, `{"key":`+quote(key)+`,"class":"local","format":"generic"}`, true)
			if got := fieldsOf(err)["/key"]; got != "reserved_name" {
				t.Fatalf("code = %q, want reserved_name", got)
			}
		})
	}
	t.Run("R-REPO-002 an update does not look at the key", func(t *testing.T) {
		if _, _, err := decodeValidate(t, `{"key":"A","class":"local","format":"generic"}`, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-REPO-002 the key pattern of the contract is the one of INV-5", func(t *testing.T) {
		if KeyPattern != `^[a-z0-9][a-z0-9._-]{1,63}$` {
			t.Fatal(KeyPattern)
		}
	})
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestValidateClassAndFormat(t *testing.T) {
	t.Run("R-REPO-001 a create with class federated is unsupported_class", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"federated","format":"generic"}`, true)
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeValidation || fieldsOf(err)["/class"] != errs.CodeUnsupportedClass || e.Status() != 422 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-003 an unknown format is unknown_format", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"cobol"}`, true)
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeValidation || fieldsOf(err)["/format"] != errs.CodeUnknownFormat || e.Status() != 422 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-003 R-GENERIC-001 the generic format supports the local class only", func(t *testing.T) {
		for _, class := range []string{"remote", "virtual"} {
			_, _, err := decodeValidate(t, `{"key":"x1","class":"`+class+`","format":"generic"}`, true)
			if got := fieldsOf(err)["/class"]; got != errs.CodeUnsupportedClass {
				t.Fatalf("%s: code %q, err %v", class, got, err)
			}
		}
		if _, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic"}`, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-REPO-008 all violations come in one pass", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"A","class":"local","format":"cobol","includePatterns":["a//b"]}`, true)
		e, _ := errs.As(err)
		if e == nil || e.Code != errs.CodeValidation || len(e.Fields) != 2 {
			t.Fatalf("err = %#v", e)
		}
		got := fieldsOf(err)
		if got["/key"] != errs.CodeInvalidKey || got["/format"] != errs.CodeUnknownFormat {
			t.Fatalf("fields = %v", got)
		}
	})
	t.Run("R-REPO-006 R-GENERIC-003 layout is the format's and nothing else", func(t *testing.T) {
		d, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","layout":"generic"}`, true)
		if err != nil || d.Layout != "generic" {
			t.Fatalf("layout %q, err %v", d.Layout, err)
		}
		_, _, err = decodeValidate(t, `{"key":"x1","class":"local","format":"generic","layout":"maven-2"}`, true)
		if got := fieldsOf(err)["/layout"]; got != errs.CodeUnsupportedLayout {
			t.Fatalf("code %q, err %v", got, err)
		}
		d, _, _ = decodeValidate(t, `{"key":"x1","class":"local","format":"generic"}`, true)
		if d.Layout != "generic" {
			t.Fatalf("an omitted layout reads back as the format's, got %q", d.Layout)
		}
	})
}

func TestDocumentMembers(t *testing.T) {
	t.Run("R-REPO-008 an unknown member is 400 malformed_request", func(t *testing.T) {
		_, err := DecodeDocument([]byte(`{"key":"x1","class":"local","format":"generic","excludePattern":["a"]}`))
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeInvalid || e.Status() != 400 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-GENERIC-001 a generic member is an unknown member", func(t *testing.T) {
		_, err := DecodeDocument([]byte(`{"key":"x1","class":"local","format":"generic","generic":{}}`))
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeInvalid {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-008 malformed JSON, wrong types, duplicates and trailing data are 400 malformed_request", func(t *testing.T) {
		for name, body := range map[string]string{
			"syntax":           `{"key":`,
			"empty":            ``,
			"not an object":    `[]`,
			"wrong type":       `{"key":"x1","disabled":"yes"}`,
			"duplicate":        `{"key":"x1","key":"x2"}`,
			"nested duplicate": `{"key":"x1","local":{"autoClaim":true,"autoClaim":false}}`,
			"trailing":         `{"key":"x1"} {}`,
		} {
			_, err := DecodeDocument([]byte(body))
			if e, _ := errs.As(err); e == nil || e.Code != errs.CodeInvalid || e.Status() != 400 {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("R-REPO-004 defaults are filled and read back", func(t *testing.T) {
		d, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic"}`, true)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := d.marshalConfig()
		want := `{"description":"","adminNotes":"","includePatterns":[],"excludePatterns":[],"disabled":false,"requireAuthentication":false,"hideExistence":"inherit"}`
		if string(cfg) != want {
			t.Fatalf("config = %s\nwant     %s", cfg, want)
		}
	})
	t.Run("R-REPO-004 limits and enums", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","description":"`+strings.Repeat("d", 2001)+
			`","adminNotes":"`+strings.Repeat("n", 8001)+`","hideExistence":"maybe"}`, true)
		got := fieldsOf(err)
		if got["/description"] != "out_of_range" || got["/adminNotes"] != "out_of_range" || got["/hideExistence"] != "invalid_value" {
			t.Fatalf("fields = %v", got)
		}
		if _, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","description":"`+strings.Repeat("d", 2000)+`"}`, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-REPO-005 pattern limits and syntax answer invalid_pattern", func(t *testing.T) {
		many := make([]string, MaxPatterns+1)
		for i := range many {
			many[i] = "a"
		}
		list, _ := json.Marshal(many)
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","includePatterns":`+string(list)+`}`, true)
		if fieldsOf(err)["/includePatterns"] != "invalid_pattern" {
			t.Fatalf("err = %v", err)
		}
		_, _, err = decodeValidate(t, `{"key":"x1","class":"local","format":"generic","excludePatterns":["ok","a**b"]}`, true)
		if fieldsOf(err)["/excludePatterns/1"] != "invalid_pattern" {
			t.Fatalf("err = %v", err)
		}
		if _, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","includePatterns":["releases/**"],"excludePatterns":["**/*.pdb"]}`, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-REPO-004 a class section that does not match the class is not_applicable", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","virtual":{"members":[]},"remote":{"url":"https://example.org/"}}`, true)
		got := fieldsOf(err)
		if got["/virtual"] != errs.CodeNotApplicable || got["/remote"] != errs.CodeNotApplicable {
			t.Fatalf("fields = %v", got)
		}
	})
}

func TestGenericSettings(t *testing.T) {
	na := map[string]string{
		"priority member":   `"priorityMember":true`,
		"package ownership": `"local":{"packageOwnership":"enforce"}`,
		"ownership domain":  `"local":{"ownershipDomain":"team"}`,
		"auto claim":        `"local":{"autoClaim":true}`,
		"caps":              `"local":{"caps":{"maxVariantsPerGroup":5}}`,
	}
	pointers := map[string]string{
		"priority member": "/priorityMember", "package ownership": "/local/packageOwnership",
		"ownership domain": "/local/ownershipDomain", "auto claim": "/local/autoClaim", "caps": "/local/caps",
	}
	for name, member := range na {
		t.Run("R-GENERIC-008 "+name+" is not applicable to a generic repository", func(t *testing.T) {
			_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic",`+member+`}`, true)
			if got := fieldsOf(err)[pointers[name]]; got != errs.CodeNotApplicable {
				t.Fatalf("code %q, err %v", got, err)
			}
		})
	}
	t.Run("R-GENERIC-008 the shared members apply", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","description":"d","adminNotes":"n",
			"disabled":true,"requireAuthentication":true,"hideExistence":"false","storage":{"maxUploadBytes":10},
			"includePatterns":["a/**"],"excludePatterns":["b/**"]}`, true)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-GENERIC-008 storage must be an object", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","storage":"big"}`, true)
		if fieldsOf(err)["/storage"] == "" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-STOR-004 maxUploadSize must be a non-negative integer", func(t *testing.T) {
		for limit, ok := range map[string]bool{
			`0`: true, `1048576`: true, `1e9`: false, `1.5`: false, `-1`: false, `"10"`: false, `null`: false, `9223372036854775808`: false,
		} {
			_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"generic","storage":{"maxUploadSize":`+limit+`}}`, true)
			if got := fieldsOf(err)["/storage/maxUploadSize"] == ""; got != ok {
				t.Errorf("maxUploadSize %s: accepted = %v, want %v (err %v)", limit, got, ok, err)
			}
		}
	})
	t.Run("R-REPO-004 a format with package names takes the members the generic format refuses", func(t *testing.T) {
		_, _, err := decodeValidate(t, `{"key":"x1","class":"local","format":"pkgfmt","priorityMember":true,
			"local":{"packageOwnership":"enforce","autoClaim":true}}`, true)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = decodeValidate(t, `{"key":"x1","class":"local","format":"pkgfmt","local":{"packageOwnership":"sometimes"}}`, true)
		if fieldsOf(err)["/local/packageOwnership"] != "invalid_value" {
			t.Fatalf("err = %v", err)
		}
		_, _, err = decodeValidate(t, `{"key":"x1","class":"remote","format":"pkgfmt","local":{"packageOwnership":"off"}}`, true)
		if fieldsOf(err)["/local"] != errs.CodeNotApplicable {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestConsumerView(t *testing.T) {
	t.Run("R-REPO-010 the consumer view has identity, description, layout and addresses only", func(t *testing.T) {
		d, _, err := decodeValidate(t, `{"key":"files","class":"local","format":"generic","description":"d","adminNotes":"secret note","includePatterns":["a/**"]}`, true)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(d.ConsumerView("/files/{key}/"))
		want := `{"key":"files","class":"local","format":"generic","description":"d","layout":"generic","addresses":["/files/files/"]}`
		if string(raw) != want {
			t.Fatalf("view = %s", raw)
		}
		if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "includePatterns") {
			t.Fatal("the consumer view leaks configuration")
		}
		if d.WithoutAdminNotes().AdminNotes != "" || d.AdminNotes == "" {
			t.Fatal("WithoutAdminNotes must clear the copy only")
		}
	})
}

func TestMergePatch(t *testing.T) {
	// The examples of RFC 7396 appendix A.
	cases := []struct{ doc, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	}
	for _, c := range cases {
		t.Run("R-REPO-011 RFC 7396 "+c.patch, func(t *testing.T) {
			got, err := MergePatch([]byte(c.doc), []byte(c.patch))
			if err != nil || string(got) != c.want {
				t.Fatalf("got %s, %v; want %s", got, err, c.want)
			}
		})
	}
	t.Run("R-REPO-011 a patch that is not an object is invalid_document", func(t *testing.T) {
		for _, p := range []string{`[]`, `"x"`, `null`, `{"a":1,"a":2}`, `{`} {
			if _, err := MergePatch([]byte(`{}`), []byte(p)); err == nil {
				t.Errorf("patch %s accepted", p)
			}
		}
	})
}
