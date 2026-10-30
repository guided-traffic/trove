package name

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestParseEscaped(t *testing.T) {
	cases := []struct {
		name, in string
		want     string // canonical form; empty when the name must be refused
	}{
		// The forms R-NPM-002 lists for a scoped name reach the same identity.
		{"scoped with a slash", "@acme/ui", "@acme/ui"},
		{"scoped with %2f", "@acme%2fui", "@acme/ui"},
		{"scoped with %2F", "@acme%2Fui", "@acme/ui"},
		{"scoped with %40 and %2f", "%40acme%2fui", "@acme/ui"},
		{"scoped with %40 and %2F", "%40acme%2Fui", "@acme/ui"},
		{"scoped with %40 and a slash", "%40acme/ui", "@acme/ui"},
		{"unscoped", "left-pad", "left-pad"},
		{"unscoped escaped hyphen", "left%2Dpad", "left-pad"},
		{"legacy capitals are served unchanged", "JSONStream", "JSONStream"},
		{"legacy scoped capitals", "@Acme/UI", "@Acme/UI"},
		{"plus is not a space", "a+b", "a+b"},
		{"legacy name with a dot and an underscore", "_a.b", "_a.b"},
		{"a space is a legacy character", "a%20b", "a b"},
		{"non-ASCII legacy name", "caf%C3%A9", "café"},

		// Rejected before any lookup.
		{"empty", "", ""},
		{"dot", ".", ""},
		{"dot dot", "..", ""},
		{"escaped dot dot", "%2e%2e", ""},
		{"scope with an empty name", "@acme/", ""},
		{"scope with an empty name escaped", "@acme%2f", ""},
		{"empty scope", "@/ui", ""},
		{"scope alone", "@acme", ""},
		{"at sign alone", "@", ""},
		{"escaped scope alone", "%40acme", ""},
		{"traversal as the name", "@acme/..", ""},
		{"traversal as the scope", "@../ui", ""},
		{"dot as the name", "@acme/.", ""},
		{"more than one slash", "@acme/ui/extra", ""},
		{"more than one escaped slash", "@acme%2fui%2fextra", ""},
		{"slash without a scope", "acme/ui", ""},
		{"escaped slash without a scope", "acme%2fui", ""},
		{"leading slash", "/ui", ""},
		{"NUL", "a%00b", ""},
		{"backslash", "a%5Cb", ""},
		{"literal backslash", `a\b`, ""},
		{"second layer of escapes", "a%2525b", ""},
		{"escaped percent", "a%25b", ""},
		{"bare percent", "a%", ""},
		{"invalid escape", "a%zzb", ""},
		{"control character", "a%0ab", ""},
		{"DEL", "a%7fb", ""},
		{"invalid UTF-8", "a%ffb", ""},
		{"protocol operation prefix", "-", ""},
		{"too long", strings.Repeat("a", MaxLookupLength+1), ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-002 "+c.name, func(t *testing.T) {
			n, err := ParseEscaped(c.in)
			if c.want == "" {
				if err == nil {
					t.Fatalf("ParseEscaped(%q) = %q, want an error", c.in, n)
				}
				e, ok := errs.As(err)
				if !ok || e.Kind != errs.Invalid || e.Code != CodeInvalid {
					t.Fatalf("error = %#v, want an Invalid %s", err, CodeInvalid)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEscaped(%q): %v", c.in, err)
			}
			if n.String() != c.want {
				t.Fatalf("ParseEscaped(%q) = %q, want %q", c.in, n, c.want)
			}
		})
	}
	t.Run("R-NPM-002 a name at the lookup bound", func(t *testing.T) {
		if _, err := ParseEscaped(strings.Repeat("a", MaxLookupLength)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNormalizeAndEscape(t *testing.T) {
	t.Run("R-NPM-002 the three request forms share one identity", func(t *testing.T) {
		var first Name
		for i, in := range []string{"@acme%2fui", "@acme/ui", "%40acme%2Fui"} {
			n, err := ParseEscaped(in)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				first = n
			}
			if n != first || n.String() != "@acme/ui" {
				t.Fatalf("ParseEscaped(%q) = %#v, want %#v", in, n, first)
			}
		}
	})
	t.Run("R-NPM-002 Normalize is idempotent", func(t *testing.T) {
		for _, in := range []string{"@acme%2fui", "%40acme/ui", "JSONStream", "a%20b", "caf%C3%A9", "@a%2Fb"} {
			once, err := Normalize(in)
			if err != nil {
				t.Fatalf("Normalize(%q): %v", in, err)
			}
			twice, err := Normalize(once)
			if err != nil || twice != once {
				t.Fatalf("Normalize(Normalize(%q)) = %q, %v; want %q", in, twice, err, once)
			}
		}
	})
	t.Run("R-NPM-002 EscapedPath is the client form and parses back", func(t *testing.T) {
		cases := map[string]string{
			"@acme/ui":   "@acme%2fui",
			"left-pad":   "left-pad",
			"a b":        "a%20b",
			"@Acme/UI":   "@Acme%2fUI",
			"café":       "caf%C3%A9",
			"@a.b/c_d":   "@a.b%2fc_d",
			"a+b":        "a+b",
			"a?b#c":      "a%3Fb%23c",
			"@s c/n ame": "@s%20c%2fn%20ame",
		}
		for canon, want := range cases {
			n, err := Parse(canon)
			if err != nil {
				t.Fatalf("Parse(%q): %v", canon, err)
			}
			if got := n.EscapedPath(); got != want {
				t.Errorf("EscapedPath(%q) = %q, want %q", canon, got, want)
			}
			back, err := ParseEscaped(n.EscapedPath())
			if err != nil || back != n {
				t.Errorf("ParseEscaped(EscapedPath(%q)) = %#v, %v", canon, back, err)
			}
		}
	})
	t.Run("R-NPM-002 accessors", func(t *testing.T) {
		n, _ := Parse("@acme/ui")
		if n.Scope() != "acme" || n.Package() != "ui" || !n.IsScoped() {
			t.Fatalf("%#v", n)
		}
		u, _ := Parse("ui")
		if u.Scope() != "" || u.Package() != "ui" || u.IsScoped() {
			t.Fatalf("%#v", u)
		}
	})
}

func TestValidateNew(t *testing.T) {
	long := strings.Repeat("a", MaxNewLength)
	cases := []struct {
		name, in string
		valid    bool
	}{
		// Accepted.
		{"one letter", "a", true},
		{"hyphen", "left-pad", true},
		{"dot", "lodash.merge", true},
		{"underscore inside", "a_b", true},
		{"digits", "a1", true},
		{"digit first", "1a", true},
		{"scoped", "@acme/ui", true},
		{"scoped with dots", "@a.b/c.d", true},
		{"scope that looks like a core module", "@acme/http", true},
		{"core module name as a package of a scope", "@acme/fs", true},
		{"names that only contain a core module name", "http-server", true},
		{"names npm publishes that are not core modules", "test", true},
		{"sqlite", "sqlite", true},
		{"214 characters", long, true},
		{"214 characters scoped", "@" + strings.Repeat("s", 100) + "/" + strings.Repeat("n", 112), true},
		{"scoped node_modules is allowed", "@acme/node_modules", true},

		// Rejected.
		{"empty", "", false},
		{"215 characters", long + "a", false},
		{"215 characters scoped", "@" + strings.Repeat("s", 100) + "/" + strings.Repeat("n", 113), false},
		{"capitals", "Acme-UI", false},
		{"capital scope", "@Acme/ui", false},
		{"capital only in the name", "@acme/Ui", false},
		{"all capitals", "ACME", false},
		{"leading dot", ".a", false},
		{"leading underscore", "_a", false},
		{"leading hyphen", "-a", false},
		{"leading dot in the name of a scope", "@a/.b", false},
		{"leading underscore in the name of a scope", "@a/_b", false},
		{"leading hyphen in the name of a scope", "@a/-b", false},
		{"leading dot in the scope", "@.a/b", false},
		{"leading underscore in the scope", "@_a/b", false},
		{"leading hyphen in the scope", "@-a/b", false},
		{"leading space", " a", false},
		{"trailing space", "a ", false},
		{"inner space", "a b", false},
		{"tab", "a\tb", false},
		{"tilde", "a~b", false},
		{"apostrophe", "a'b", false},
		{"exclamation mark", "a!b", false},
		{"parentheses", "a(b)", false},
		{"asterisk", "a*b", false},
		{"colon", "a:b", false},
		{"at sign inside", "a@b", false},
		{"question mark", "a?b", false},
		{"hash", "a#b", false},
		{"percent", "a%b", false},
		{"plus", "a+b", false},
		{"comma", "a,b", false},
		{"non-ASCII", "café", false},
		{"non-ASCII scope", "@café/b", false},
		{"node_modules", "node_modules", false},
		{"favicon.ico", "favicon.ico", false},
		{"http", "http", false},
		{"fs", "fs", false},
		{"path", "path", false},
		{"events", "events", false},
		{"util", "util", false},
		{"process", "process", false},
		{"zlib", "zlib", false},
		{"punycode", "punycode", false},
		{"sys", "sys", false},
		{"wasi", "wasi", false},
		{"node: prefix", "node:fs", false},
		{"scope without a name", "@a/", false},
		{"two slashes", "@a/b/c", false},
		{"slash without a scope", "a/b", false},
		{"double at", "@@a/b", false},
	}
	for _, c := range cases {
		t.Run("R-NPM-002 new name "+c.name, func(t *testing.T) {
			n, perr := Parse(c.in)
			var err error
			if perr != nil {
				err = perr
			} else {
				err = ValidateNew(n)
			}
			if (err == nil) != c.valid {
				t.Fatalf("new name %q: error = %v, want valid = %v", c.in, err, c.valid)
			}
			if err != nil {
				if e, ok := errs.As(err); !ok || e.Kind != errs.Invalid || e.Code != CodeInvalid {
					t.Fatalf("error = %#v, want an Invalid %s", err, CodeInvalid)
				}
			}
			_, err = ParseNew(c.in)
			if (err == nil) != c.valid {
				t.Fatalf("ParseNew(%q) error = %v, want valid = %v", c.in, err, c.valid)
			}
		})
	}
	t.Run("R-NPM-002 the zero Name is not a name", func(t *testing.T) {
		if ValidateNew(Name{}) == nil {
			t.Fatal("zero Name accepted")
		}
	})
}

func TestCoreModules(t *testing.T) {
	core := []string{
		"assert", "async_hooks", "buffer", "child_process", "cluster", "console", "constants", "crypto",
		"dgram", "diagnostics_channel", "dns", "domain", "events", "fs", "http", "http2", "https",
		"inspector", "module", "net", "os", "path", "perf_hooks", "process", "punycode", "querystring",
		"readline", "repl", "stream", "string_decoder", "sys", "timers", "tls", "trace_events", "tty",
		"url", "util", "v8", "vm", "wasi", "worker_threads", "zlib",
	}
	for _, c := range core {
		t.Run("R-NPM-002 core module "+c, func(t *testing.T) {
			if !IsCoreModule(c) {
				t.Fatalf("%s is not recognized", c)
			}
			if _, err := ParseNew(c); err == nil {
				t.Fatalf("new name %s accepted", c)
			}
		})
	}
	for _, c := range []string{"test", "sqlite", "sea", "left-pad", "", "Fs"} {
		if IsCoreModule(c) {
			t.Errorf("%q recognized as a core module", c)
		}
	}
}

func TestFold(t *testing.T) {
	cases := []struct{ in, want string }{
		{"acme-ui", "acmeui"},
		{"acme_ui", "acmeui"},
		{"acme.ui", "acmeui"},
		{"Acme-UI", "acmeui"},
		{"@acme/ui-kit", "@acme/uikit"},
		{"@ac-me/ui", "@acme/ui"},
		{"a--b__c..d", "abcd"},
		{"", ""},
		{"-", ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-002 fold "+c.in, func(t *testing.T) {
			got := Fold(c.in)
			if got != c.want {
				t.Fatalf("Fold(%q) = %q, want %q", c.in, got, c.want)
			}
			if Fold(got) != got {
				t.Fatalf("Fold is not idempotent on %q", got)
			}
		})
	}
	t.Run("R-NPM-002 look-alikes", func(t *testing.T) {
		parse := func(s string) Name {
			n, err := Parse(s)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		if !LookAlike(parse("acme_ui"), parse("acme-ui")) {
			t.Fatal("acme_ui and acme-ui are look-alikes")
		}
		if !LookAlike(parse("@acme/ui.kit"), parse("@acme/ui-kit")) {
			t.Fatal("scoped look-alikes")
		}
		if LookAlike(parse("acme-ui"), parse("acme-ui")) {
			t.Fatal("a name is not a look-alike of itself")
		}
		if LookAlike(parse("acme-ui"), parse("acme-uix")) {
			t.Fatal("different names")
		}
		if LookAlike(parse("@acme/ui"), parse("acme-ui")) {
			t.Fatal("a scoped name keeps its scope in the fold")
		}
	})
}

func TestErrorDetail(t *testing.T) {
	t.Run("R-NPM-005 a detail carries no control characters and stays short", func(t *testing.T) {
		_, err := ParseEscaped("a%00" + strings.Repeat("é", 300) + "\x1b")
		e, ok := errs.As(err)
		if !ok {
			t.Fatalf("error = %v", err)
		}
		if len(e.Detail) > 200 {
			t.Fatalf("detail has %d bytes", len(e.Detail))
		}
		for i := 0; i < len(e.Detail); i++ {
			if e.Detail[i] < 0x20 || e.Detail[i] >= 0x7f {
				t.Fatalf("detail %q holds byte %#x", e.Detail, e.Detail[i])
			}
		}
	})
}

func FuzzName(f *testing.F) {
	for _, s := range []string{
		"", "a", "@acme/ui", "@acme%2fui", "%40acme%2Fui", "%40acme/ui", "JSONStream", "a%2525b", "a%", "%zz",
		"..", "@acme/..", "@/", "@a/b/c", "a%00b", "a%5cb", "caf%C3%A9", "a%20b", "-", "@@a/b", "node_modules",
		"http", "Acme-UI", "acme_ui", strings.Repeat("a", 215), "@" + strings.Repeat("s", 100) + "/" + strings.Repeat("n", 113),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		n, err := ParseEscaped(in)
		if err != nil {
			if e, ok := errs.As(err); !ok || e.Code != CodeInvalid || e.Kind != errs.Invalid {
				t.Fatalf("ParseEscaped(%q) error %v is not an Invalid %s", in, err, CodeInvalid)
			}
			return
		}
		canon := n.String()

		// The canonical form is a fixed point of normalization and of Parse.
		if strings.ContainsAny(canon, "%\\\x00") || len(canon) == 0 || len(canon) > MaxLookupLength {
			t.Fatalf("canonical form %q of %q is not canonical", canon, in)
		}
		again, err := Normalize(canon)
		if err != nil || again != canon {
			t.Fatalf("Normalize(%q) = %q, %v; want the identity", canon, again, err)
		}
		p, err := Parse(canon)
		if err != nil || p != n {
			t.Fatalf("Parse(%q) = %#v, %v; want %#v", canon, p, err, n)
		}
		// The client form parses back to the same name.
		back, err := ParseEscaped(n.EscapedPath())
		if err != nil || back != n {
			t.Fatalf("ParseEscaped(%q) = %#v, %v; want %#v", n.EscapedPath(), back, err, n)
		}
		// Shape: at most one slash, and a slash means a scope.
		if strings.Count(canon, "/") > 1 || (n.IsScoped() != strings.HasPrefix(canon, "@")) {
			t.Fatalf("shape of %q: %#v", canon, n)
		}
		if n.Package() == "" || n.Package() == "." || n.Package() == ".." || n.Scope() == "." || n.Scope() == ".." {
			t.Fatalf("traversal segment in %#v", n)
		}
		// The look-alike key is stable and ASCII-lowered.
		fold := Fold(canon)
		if Fold(fold) != fold || n.Fold() != fold {
			t.Fatalf("fold of %q: %q", canon, fold)
		}
		// A name that is valid for publishing is a valid lookup name, lower case,
		// within the length limit, and all of its parts are URL-safe.
		if ValidateNew(n) == nil {
			if canon != strings.ToLower(canon) || len(canon) > MaxNewLength {
				t.Fatalf("new name %q breaks the rules", canon)
			}
			if n.EscapedPath() != strings.ReplaceAll(canon, "/", "%2f") {
				t.Fatalf("new name %q escapes to %q", canon, n.EscapedPath())
			}
			if _, err := ParseNew(canon); err != nil {
				t.Fatalf("ParseNew(%q): %v", canon, err)
			}
		}
	})
}
