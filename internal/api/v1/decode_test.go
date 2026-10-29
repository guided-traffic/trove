package v1

import (
	"runtime"
	"strings"
	"testing"
)

// allocated returns the bytes the heap handed out while f ran.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestR_API_017_BodyNesting(t *testing.T) {
	nested := func(depth int) string {
		return strings.Repeat("[", depth) + strings.Repeat("]", depth)
	}
	t.Run("R-API-017 a body nested 64 levels deep is accepted", func(t *testing.T) {
		var v any
		if err := StrictUnmarshal([]byte(nested(maxBodyDepth)), &v); err != nil {
			t.Fatal(err)
		}
		obj := strings.Repeat(`{"a":`, maxBodyDepth-1) + `{}` + strings.Repeat("}", maxBodyDepth-1)
		if err := StrictUnmarshal([]byte(obj), &v); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-017 one level more answers 400 with the pointer of the container", func(t *testing.T) {
		var v any
		st, code, f := problemOf(t, StrictUnmarshal([]byte(nested(maxBodyDepth+1)), &v))
		want := strings.Repeat("/0", maxBodyDepth)
		if st != 400 || code != "malformed_request" || len(f) != 1 || f[0].Code != "depth" || f[0].Pointer != want {
			t.Fatalf("%d %s %+v", st, code, f)
		}
		obj := `{"x":[` + strings.Repeat(`{"a":`, maxBodyDepth) + `1` + strings.Repeat("}", maxBodyDepth) + `]}`
		_, _, f = problemOf(t, StrictUnmarshal([]byte(obj), &v))
		want = "/x/0" + strings.Repeat("/a", maxBodyDepth-2)
		if len(f) != 1 || f[0].Code != "depth" || f[0].Pointer != want {
			t.Fatalf("%+v, want pointer %s", f, want)
		}
	})
	t.Run("R-API-017 a merge patch and a repository document are bounded the same way", func(t *testing.T) {
		deep := `{"a":` + nested(maxBodyDepth) + `}`
		if _, err := stripReadOnly([]byte(deep)); err == nil {
			t.Fatal("deep document accepted")
		}
		if err := checkSyntax([]byte(deep)); err == nil {
			t.Fatal("deep patch accepted")
		}
	})
	t.Run("R-API-017 a body of 1 MiB of '[' is refused in bounded memory", func(t *testing.T) {
		body := []byte(strings.Repeat("[", 1<<20))
		var err error
		n := allocated(func() {
			var v any
			err = StrictUnmarshal(body, &v)
		})
		if _, _, f := problemOf(t, err); len(f) != 1 || f[0].Code != "depth" {
			t.Fatalf("%+v", f)
		}
		if n > 1<<20 {
			t.Fatalf("the refusal allocated %d bytes", n)
		}
	})
	t.Run("R-API-017 a wide array under a long member name builds no pointer per element", func(t *testing.T) {
		key := strings.Repeat("k", 64<<10)
		body := []byte(`{"` + key + `":[` + strings.TrimSuffix(strings.Repeat(`"v",`, 8192), ",") + `]}`)
		var err error
		n := allocated(func() {
			var m map[string][]string
			err = StrictUnmarshal(body, &m)
		})
		if err != nil {
			t.Fatal(err)
		}
		// The body is 96 KiB; one pointer string per element would be 512 MiB.
		if n > 32<<20 {
			t.Fatalf("decoding allocated %d bytes", n)
		}
	})
	t.Run("R-API-003 pointers of errors inside arrays and objects stay exact", func(t *testing.T) {
		var w widget
		_, _, f := problemOf(t, StrictUnmarshal([]byte(`{"tags":["a","b"],"labels":{"x":"1","x":"2"}}`), &w))
		if len(f) != 1 || f[0].Code != "duplicate" || f[0].Pointer != "/labels/x" {
			t.Fatalf("%+v", f)
		}
		var list []widget
		_, _, f = problemOf(t, StrictUnmarshal([]byte(`[{"name":"a"},{"name":"b","remote":{"u~/x":1}}]`), &list))
		if len(f) != 1 || f[0].Code != "unknown" || f[0].Pointer != "/1/remote/u~0~1x" {
			t.Fatalf("%+v", f)
		}
		_, _, f = problemOf(t, StrictUnmarshal([]byte(`{"tags":["a",}`), &w))
		if len(f) != 1 || f[0].Code != "syntax" || f[0].Pointer != "/tags/1" {
			t.Fatalf("%+v", f)
		}
	})
}
