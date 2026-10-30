package v1

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The one merge patch (R-API-013, Q-API-157). The RFC 7396 appendix A examples
// that the repository area tested for its own copy are here, next to the
// implementation every PATCH uses.

func TestR_API_013_MergePatchAppendixAExactOutput(t *testing.T) {
	for _, c := range []struct{ doc, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	} {
		t.Run("R-API-013 RFC 7396 "+c.patch, func(t *testing.T) {
			got, err := MergePatch([]byte(c.doc), []byte(c.patch))
			if err != nil || string(got) != c.want {
				t.Fatalf("got %s, %v; want %s", got, err, c.want)
			}
		})
	}
}

func TestR_API_013_MergePatchKeepsLargeIntegers(t *testing.T) {
	const big = "9007199254740993" // 2^53 + 1, the first integer a float64 cannot hold
	t.Run("R-API-013 an integer of the patch is stored as sent", func(t *testing.T) {
		got, err := MergePatch([]byte(`{"a":1}`), []byte(`{"limit":`+big+`}`))
		if err != nil || string(got) != `{"a":1,"limit":`+big+`}` {
			t.Fatalf("got %s, %v", got, err)
		}
	})
	t.Run("R-API-013 an integer of the stored document that the patch does not touch survives", func(t *testing.T) {
		got, err := MergePatch([]byte(`{"limit":`+big+`,"b":2}`), []byte(`{"b":3}`))
		if err != nil || string(got) != `{"b":3,"limit":`+big+`}` {
			t.Fatalf("got %s, %v", got, err)
		}
	})
	t.Run("R-API-013 a decimal keeps its literal", func(t *testing.T) {
		got, err := MergePatch([]byte(`{}`), []byte(`{"ratio":1.50,"exp":1e300}`))
		if err != nil || string(got) != `{"exp":1e300,"ratio":1.50}` {
			t.Fatalf("got %s, %v", got, err)
		}
	})
	t.Run("R-API-013 a typed resource receives the exact integer", func(t *testing.T) {
		type sized struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		got, err := ApplyMergePatch(sized{Name: "n", Size: 1}, []byte(`{"size":`+big+`}`))
		if err != nil || got.Size != 9007199254740993 || got.Name != "n" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-API-013 two different large integers are a change of an immutable member", func(t *testing.T) {
		type sized struct {
			Size int64 `json:"size"`
		}
		before, after := sized{Size: 9007199254740992}, sized{Size: 9007199254740993}
		st, code, f := problemOf(t, CheckImmutable(before, after, "size"))
		if st != 422 || code != "validation_failed" || len(f) != 1 || f[0].Pointer != "/size" {
			t.Fatalf("%d %s %+v", st, code, f)
		}
		if err := CheckImmutable(after, after, "size"); err != nil {
			t.Fatalf("an equal value is ignored: %v", err)
		}
	})
}

func TestR_API_013_MergePatchReadsThePatchStrictly(t *testing.T) {
	t.Run("R-API-013 R-API-003 a duplicate member of the patch is 400 with its pointer", func(t *testing.T) {
		_, err := MergePatch([]byte(`{}`), []byte(`{"a":{"b":1,"b":2}}`))
		st, code, f := problemOf(t, err)
		if st != 400 || code != "malformed_request" || len(f) != 1 || f[0].Pointer != "/a/b" || f[0].Code != "duplicate" {
			t.Fatalf("%d %s %+v", st, code, f)
		}
	})
	t.Run("R-API-013 R-API-017 a patch that nests beyond the bound is 400", func(t *testing.T) {
		deep := strings.Repeat(`{"a":`, maxBodyDepth+1) + `1` + strings.Repeat(`}`, maxBodyDepth+1)
		st, _, f := problemOf(t, patchErr(MergePatch([]byte(`{}`), []byte(deep))))
		if st != 400 || len(f) != 1 || f[0].Code != "depth" {
			t.Fatalf("%d %+v", st, f)
		}
	})
	t.Run("R-API-013 a patch that is not JSON is 400", func(t *testing.T) {
		for _, p := range []string{``, `{`, `{"a":}`, `{} {}`} {
			if st, _, _ := problemOf(t, patchErr(MergePatch([]byte(`{}`), []byte(p)))); st != 400 {
				t.Errorf("patch %q: status %d", p, st)
			}
		}
	})
	t.Run("R-API-013 the body of a PATCH must be an object without duplicate members", func(t *testing.T) {
		for _, p := range []string{`[]`, `"x"`, `null`, `{"a":1,"a":2}`, `{`} {
			r := httptest.NewRequest("PATCH", "/x", strings.NewReader(p))
			r.Header.Set("Content-Type", MediaMergePatch)
			if _, err := ReadMergePatch(httptest.NewRecorder(), r, 1024); err == nil {
				t.Errorf("patch %s accepted", p)
			} else if st, code, _ := problemOf(t, err); st != 400 || code != "malformed_request" {
				t.Errorf("patch %s: %d %s", p, st, code)
			}
		}
	})
}

// patchErr turns the result of MergePatch into its error, so that a test of the
// refusal reads like the others.
func patchErr(_ []byte, err error) error { return err }

func TestR_API_013_RepositoryPatcherIsTheMergeAppliedToTheStoredDocument(t *testing.T) {
	apply := MergePatcher([]byte(`{"description":null,"includePatterns":["a/**"],"limits":{"size":` + "9007199254740993" + `}}`))
	got, err := apply([]byte(`{"key":"k","description":"d","includePatterns":["x"]}`))
	want := `{"includePatterns":["a/**"],"key":"k","limits":{"size":9007199254740993}}`
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v; want %s", got, err, want)
	}
	if _, err := MergePatcher([]byte(`{"a":1,"a":2}`))([]byte(`{}`)); err == nil {
		t.Fatal("a patch with a duplicate member was applied")
	}
}
