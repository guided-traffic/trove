package manifest

import (
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// zeros yields an endless stream of zero bytes and counts what was taken.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	clear(p)
	z.n += int64(len(p))
	return len(p), nil
}

func wantError(t *testing.T, err error, status int, code string) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("error = %v, want an *errs.Error", err)
	}
	if e.Status() != status || e.Code != code {
		t.Fatalf("error = %d %s (%s), want %d %s", e.Status(), e.Code, e.Detail, status, code)
	}
}

func TestParse(t *testing.T) {
	t.Run("R-NPM-007 a manifest keeps its numbers as written", func(t *testing.T) {
		m := mustParse(t, `{"name":"a","x":1.50,"big":12345678901234567890}`)
		if got := jsonOf(t, m); got != `{"big":12345678901234567890,"name":"a","x":1.50}` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("R-NPM-007 the top level must be one JSON object", func(t *testing.T) {
		for _, doc := range []string{``, `null`, `5`, `[]`, `"x"`, `{`, `{} {}`, `{} x`, `{"a":1,}`} {
			_, err := Parse([]byte(doc), DefaultLimits())
			wantError(t, err, 400, CodeBadPublish)
		}
	})
	t.Run("R-NPM-007 whitespace after the object is fine", func(t *testing.T) {
		if _, err := Parse([]byte("{}\n \t"), DefaultLimits()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-NPM-007 the last of two equal keys wins, as in JavaScript", func(t *testing.T) {
		m := mustParse(t, `{"a":1,"a":2}`)
		if jsonOf(t, m) != `{"a":2}` {
			t.Fatalf("got %s", jsonOf(t, m))
		}
	})
}

func TestBounds(t *testing.T) {
	t.Run("R-ARCH-028 a manifest of 1 MiB is read, one byte more is refused", func(t *testing.T) {
		pad := func(n int) string {
			doc := `{"a":"`
			return doc + strings.Repeat("x", n-len(doc)-2) + `"}`
		}
		if _, err := Decode(strings.NewReader(pad(DefaultMaxBytes)), DefaultLimits()); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
		_, err := Decode(strings.NewReader(pad(DefaultMaxBytes+1)), DefaultLimits())
		wantError(t, err, 413, CodeTooLarge)
	})

	t.Run("R-ARCH-028 a 2 GiB manifest is refused after one byte over the limit, without allocating it", func(t *testing.T) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		z := &zeros{}
		_, err := Decode(io.LimitReader(z, 2<<30), DefaultLimits())
		runtime.ReadMemStats(&after)
		wantError(t, err, 413, CodeTooLarge)
		if z.n > DefaultMaxBytes+64<<10 {
			t.Fatalf("read %d bytes from the source", z.n)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
			t.Fatalf("allocated %d bytes", grown)
		}
	})

	t.Run("R-ARCH-028 a JSON document nested 100,000 deep is refused before it is decoded", func(t *testing.T) {
		for _, open := range []string{"[", `{"a":`} {
			doc := `{"deps":` + strings.Repeat(open, 100000)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, err := Parse([]byte(doc), DefaultLimits())
			runtime.ReadMemStats(&after)
			wantError(t, err, 400, CodeBadPublish)
			if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
				t.Fatalf("allocated %d bytes", grown)
			}
		}
	})

	t.Run("R-ARCH-028 the depth limit counts the top-level object as depth 1", func(t *testing.T) {
		lim := Limits{MaxDepth: 4}
		ok := `{"a":{"b":{"c":1}}}`
		tooDeep := `{"a":{"b":{"c":{"d":{}}}}}`
		if _, err := Parse([]byte(ok), lim); err != nil {
			t.Fatalf("depth 3: %v", err)
		}
		if _, err := Parse([]byte(`{"a":[[[1]]]}`), lim); err != nil {
			t.Fatalf("depth 4: %v", err)
		}
		_, err := Parse([]byte(tooDeep), lim)
		wantError(t, err, 400, CodeBadPublish)
	})

	t.Run("R-ARCH-028 brackets inside strings and escaped quotes do not count as nesting", func(t *testing.T) {
		doc := `{"a":"` + strings.Repeat("[", 1000) + `\"` + strings.Repeat("{", 1000) + `"}`
		if _, err := Parse([]byte(doc), Limits{MaxDepth: 2}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-ARCH-028 Limits fall back to the defaults", func(t *testing.T) {
		if got := (Limits{}).WithDefaults(); got != DefaultLimits() {
			t.Fatalf("got %+v", got)
		}
		if got := (Limits{MaxBytes: 5, MaxDepth: 2}).WithDefaults(); got.MaxBytes != 5 || got.MaxDepth != 2 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("R-ARCH-028 Parse refuses a document over the byte limit on its own", func(t *testing.T) {
		_, err := Parse([]byte(`{"a":"`+strings.Repeat("x", 100)+`"}`), Limits{MaxBytes: 50})
		wantError(t, err, 413, CodeTooLarge)
	})
}

func TestClone(t *testing.T) {
	t.Run("R-NPM-007 a clone shares nothing with its source", func(t *testing.T) {
		m := mustParse(t, `{"a":{"b":[1,{"c":2}]}}`)
		c := m.Clone()
		c["a"].(map[string]any)["b"].([]any)[1].(map[string]any)["c"] = "changed"
		if jsonOf(t, m) != `{"a":{"b":[1,{"c":2}]}}` {
			t.Fatalf("the source changed: %s", jsonOf(t, m))
		}
	})
	t.Run("R-NPM-007 a nil manifest clones to nil", func(t *testing.T) {
		if Manifest(nil).Clone() != nil {
			t.Fatal("not nil")
		}
	})
}

func FuzzManifest(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"name":"a","version":"1.0.0"}`, `{"name":"a","bin":"x","bundledDependencies":true,"dependencies":["a@1","user/repo"]}`,
		`{"scripts":{"install":"node_modules/.bin/x"},"engines":["node >=1"],"directories":{"bin":"b"}}`,
		strings.Repeat("[", 200), `{"a":` + strings.Repeat("[", 80) + `1` + strings.Repeat("]", 80) + `}`,
		`{"dependencies":"a@1, b@2","optionalDependencies":[5,"c"],"bin":{"a/b":"..\\x:y"}}`,
	} {
		f.Add([]byte(seed))
	}
	facts := Facts{Files: []string{"binding.gyp", "b/x", "b/y/z", "a.gyp"}}
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxBytes: 1 << 16, MaxDepth: 32}
		m, err := Parse(data, lim)
		if err != nil {
			if _, ok := errs.As(err); !ok {
				t.Fatalf("not an *errs.Error: %v", err)
			}
			return
		}
		if int64(len(data)) > lim.MaxBytes {
			t.Fatalf("accepted %d bytes over the limit of %d", len(data), lim.MaxBytes)
		}
		if err := checkDepth(data, lim.MaxDepth); err != nil {
			t.Fatalf("accepted a document over the depth limit: %v", err)
		}
		once := Normalize(m, facts)
		twice := Normalize(once, facts)
		if jsonOf(t, once) != jsonOf(t, twice) {
			t.Fatalf("Normalize is not idempotent:\n once  %s\n twice %s", jsonOf(t, once), jsonOf(t, twice))
		}
		if d := Mismatches(m, m.Clone(), facts); len(d) != 0 {
			t.Fatalf("a manifest differs from its clone in %v", d)
		}
		Stored(m, ServerFields{Name: "a", Version: "1.0.0"})
	})
}
