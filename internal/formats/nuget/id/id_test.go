package id

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

type idVec struct {
	Input    string `json:"input"`
	Local    bool   `json:"local"`
	Upstream bool   `json:"upstream"`
	Lower    string `json:"lower"`
}

// TestR_NUGET_003_GoldenIDs checks both rules and the lowercase against the vectors that
// tools/nugetvectors evaluates with .NET's regular expression engine.
func TestR_NUGET_003_GoldenIDs(t *testing.T) {
	raw, err := os.ReadFile("../version/testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		IDs []idVec `json:"ids"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.IDs) < 100 {
		t.Fatalf("%d ID vectors", len(f.IDs))
	}
	var local, upstream, nonASCII int
	for _, v := range f.IDs {
		if got := ValidateLocal(v.Input) == nil; got != v.Local {
			t.Errorf("ValidateLocal(%q) accepts = %v, want %v", v.Input, got, v.Local)
		}
		if got := ValidateUpstream(v.Input) == nil; got != v.Upstream {
			t.Errorf("ValidateUpstream(%q) accepts = %v, want %v", v.Input, got, v.Upstream)
		}
		if got := Lower(v.Input); got != v.Lower {
			t.Errorf("Lower(%q) = %q, want %q", v.Input, got, v.Lower)
		}
		if v.Local {
			local++
		}
		if v.Upstream && !v.Local {
			nonASCII++
		}
		if v.Upstream {
			upstream++
		}
	}
	if local < 15 || upstream < 40 || nonASCII < 20 {
		t.Fatalf("the vectors cover too little: %d local, %d upstream, %d upstream-only", local, upstream, nonASCII)
	}
}

// TestR_NUGET_003_LocalRule covers the acceptance examples of the spec.
func TestR_NUGET_003_LocalRule(t *testing.T) {
	good := []string{"Acme.Widgets", "ACME.Widgets", "a", "_", "_a", "a_b", "a-b.c_d", "0", "9.9", "System.Text.Json", strings.Repeat("a", 100)}
	bad := []string{"Müller.Logging", "Acme..Widgets", "", ".a", "a.", "-a", "a-", "a.-b", "a b", "a/b", "日本語", "a\n", strings.Repeat("a", 101), "\U00010400"}
	for _, in := range good {
		if err := ValidateLocal(in); err != nil {
			t.Errorf("ValidateLocal(%q): %v", in, err)
		}
	}
	for _, in := range bad {
		assertInvalid(t, ValidateLocal(in), in)
	}
}

// TestR_NUGET_003_UpstreamRule: legacy IDs outside ASCII stay valid, the shapes that no
// client sends do not.
func TestR_NUGET_003_UpstreamRule(t *testing.T) {
	good := []string{"Müller.Logging", "日本語", "Привет.Мир", "a‿b", "é", "٣a", "ß", strings.Repeat("ä", 100), "Acme.Widgets"}
	bad := []string{
		"", "Acme..Widgets", ".a", "a-", "a b", "a\u0903", "a\u00b7b", "\U00010400", "a\U0001f600", "a\n", "a\x00", "a\xffb",
		strings.Repeat("ä", 101), "a\u2122", "a\u200db",
	}
	for _, in := range good {
		if err := ValidateUpstream(in); err != nil {
			t.Errorf("ValidateUpstream(%q): %v", in, err)
		}
	}
	for _, in := range bad {
		assertInvalid(t, ValidateUpstream(in), in)
	}
}

func assertInvalid(t *testing.T, err error, in string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != CodeInvalid {
		t.Errorf("ID %q: %v, want an invalid-ID error", in, err)
		return
	}
	if len(e.Detail) > 400 || strings.ContainsAny(e.Detail, "\x00\n\x1b") {
		t.Errorf("ID %q: unsafe detail %q", in, e.Detail)
	}
}

// TestR_NUGET_003_LowerIsASCIIOnly: a non-ASCII ID is never lowercased by trove, so client and
// server cannot map it to different URLs.
func TestR_NUGET_003_LowerIsASCIIOnly(t *testing.T) {
	tests := map[string]string{
		"Acme.Widgets":   "acme.widgets",
		"ACME_W-1":       "acme_w-1",
		"Müller.Logging": "müller.logging",
		"MÜLLER":         "mÜller",
		"İstanbul":       "İstanbul",
		"Kelvin":         "Kelvin",
		"already.lower":  "already.lower",
		"":               "",
	}
	for in, want := range tests {
		if got := Lower(in); got != want {
			t.Errorf("Lower(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestR_NUGET_003_SameID(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"Acme.Widgets", "acme.widgets", true},
		{"Müller.Logging", "müller.logging", true},
		{"MÜLLER", "müller", true},
		{"Acme.Widgets", "Acme.Widget", false},
		{"a", "b", false},
	}
	for _, tt := range tests {
		if got := SameID(tt.a, tt.b); got != tt.want {
			t.Errorf("SameID(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// FuzzID: the local rule is a subset of the upstream rule, Lower is idempotent and keeps what
// it must, and nothing a rule accepts is empty or longer than MaxLength.
func FuzzID(f *testing.F) {
	for _, s := range []string{"Acme.Widgets", "a", "Müller.Logging", "a..b", "_", "日本語", "a‿b", "\U00010400", "A-B.C_D", "a\n", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		local, upstream := ValidateLocal(s), ValidateUpstream(s)
		if local == nil && upstream != nil {
			t.Fatalf("%q passes the local rule but not the upstream rule: %v", s, upstream)
		}
		lower := Lower(s)
		if Lower(lower) != lower || len(lower) != len(s) {
			t.Fatalf("Lower(%q) = %q", s, lower)
		}
		if (ValidateLocal(lower) == nil) != (local == nil) || (ValidateUpstream(lower) == nil) != (upstream == nil) {
			t.Fatalf("lowercasing %q changes validity", s)
		}
		if upstream == nil && (s == "" || len([]rune(s)) > MaxLength || strings.HasSuffix(s, "\n")) {
			t.Fatalf("upstream rule accepted %q", s)
		}
		if !SameID(s, lower) {
			t.Fatalf("%q and its lowercase are not the same ID", s)
		}
	})
}
