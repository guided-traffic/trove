package repository

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// The reference matcher below is the allocating implementation the decision
// path used before matching was made allocation-free (R-AUTHZ-015); the tests
// hold the two to the same answers.

func refGlobMatch(toks []tok, s []rune) bool {
	n := len(s)
	cur := make([]bool, n+1)
	cur[0] = true
	for _, t := range toks {
		next := make([]bool, n+1)
		switch t.kind {
		case tokLit:
			for j := 0; j < n; j++ {
				if cur[j] && s[j] == t.r {
					next[j+1] = true
				}
			}
		case tokOne:
			for j := 0; j < n; j++ {
				if cur[j] && s[j] != '/' {
					next[j+1] = true
				}
			}
		case tokStar:
			run := false
			for k := 0; k <= n; k++ {
				if cur[k] {
					run = true
				}
				next[k] = run
				if k < n && s[k] == '/' {
					run = false
				}
			}
		case tokAny:
			run := false
			for k := 0; k <= n; k++ {
				if cur[k] {
					run = true
				}
				next[k] = run
			}
		}
		cur = next
	}
	return cur[n]
}

func refStates(pat []patSeg, in []string) []bool {
	reach := make([]bool, len(pat)+1)
	reach[0] = true
	closure(pat, reach)
	for _, seg := range in {
		next := make([]bool, len(pat)+1)
		for i, ok := range reach {
			if !ok || i == len(pat) {
				continue
			}
			if pat[i].globstar {
				next[i] = true
			} else if refGlobMatch(pat[i].toks, []rune(seg)) {
				next[i+1] = true
			}
		}
		closure(pat, next)
		reach = next
	}
	return reach
}

func refMatch(p *Pattern, s string) bool {
	if p.subject == SubjectName {
		return refGlobMatch(p.segs[0].toks, []rune(s))
	}
	if s == "" {
		return false
	}
	return refStates(p.segs, strings.Split(s, "/"))[len(p.segs)]
}

func refMayContain(p *Pattern, folder string) bool {
	if p.subject == SubjectName {
		return refMatch(p, folder)
	}
	var in []string
	if folder != "" {
		in = strings.Split(folder, "/")
	}
	for _, ok := range refStates(p.segs, in)[:len(p.segs)] {
		if ok {
			return true
		}
	}
	return false
}

func randomText(r *rand.Rand, alphabet []string, max int) string {
	var b strings.Builder
	for range r.IntN(max + 1) {
		b.WriteString(alphabet[r.IntN(len(alphabet))])
	}
	return b.String()
}

// TestR_AUTHZ_015_PatternMatchAgreesWithTheReference compares the matcher with
// the reference on random patterns and inputs, including inputs longer than the
// stack buffers.
func TestR_AUTHZ_015_PatternMatchAgreesWithTheReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	patAlpha := []string{"a", "b", "é", "\uFFFD", "*", "*", "**", "?", "/", "/", `\*`, "-"}
	inAlpha := []string{"a", "b", "é", "\xff", "/", "/", "-", "*", "ab"}
	for _, subject := range []PatternSubject{SubjectName, SubjectPath} {
		checked := 0
		for range 20000 {
			src := randomText(r, patAlpha, 8)
			p, err := CompilePattern(subject, src)
			if err != nil {
				continue
			}
			checked++
			for range 6 {
				in := randomText(r, inAlpha, 12)
				if r.IntN(50) == 0 {
					in = strings.Repeat("ab/", 60) + in // beyond the stack buffers
				}
				if r.IntN(80) == 0 {
					in = strings.Repeat("é", 200) + in
				}
				if got, want := p.Match(in), refMatch(p, in); got != want {
					t.Fatalf("%v %q Match(%q) = %v, reference %v", subject, src, in, got, want)
				}
				if got, want := p.MayContain(in), refMayContain(p, in); got != want {
					t.Fatalf("%v %q MayContain(%q) = %v, reference %v", subject, src, in, got, want)
				}
			}
		}
		if checked < 1000 {
			t.Fatalf("only %d of the random patterns compiled for %v", checked, subject)
		}
	}
	long, err := CompilePattern(SubjectPath, strings.Repeat("a/", 40)+"**")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{strings.Repeat("a/", 40) + "z", strings.Repeat("a/", 39), strings.Repeat("a/", 41) + "b"} {
		if got, want := long.Match(in), refMatch(long, in); got != want {
			t.Fatalf("40 segments: Match(%q) = %v, reference %v", in, got, want)
		}
	}
}

// TestR_AUTHZ_015_PatternMatchAllocatesNothing is the per-change guard of the
// 1 ms target of R-AUTHZ-015 at 1,000 rules: the cost of a decision is its
// number of allocations, which does not depend on the load of the machine.
func TestR_AUTHZ_015_PatternMatchAllocatesNothing(t *testing.T) {
	compile := func(subject PatternSubject, src string) *Pattern {
		p, err := CompilePattern(subject, src)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	path, mid, name := compile(SubjectPath, "dir0001/**"), compile(SubjectPath, "a/*/b/**"), compile(SubjectName, "@acme/*")
	for label, f := range map[string]func(){
		"path":        func() { path.Match("nowhere/x/y.bin") },
		"inner":       func() { mid.Match("a/q/b/c/d.bin") },
		"name":        func() { name.Match("@acme/sdk") },
		"may contain": func() { mid.MayContain("a/q") },
	} {
		if n := testing.AllocsPerRun(100, f); n != 0 {
			t.Errorf("%s: %v allocations per match, want 0", label, n)
		}
	}
}

func FuzzPatternMatch(f *testing.F) {
	f.Add("a/*/b/**", "a/x/b/c")
	f.Add("@a/*", "@a/b")
	f.Add("\uFFFD*", "\xffa")
	f.Fuzz(func(t *testing.T, src, in string) {
		for _, subject := range []PatternSubject{SubjectName, SubjectPath} {
			p, err := CompilePattern(subject, src)
			if err != nil {
				continue
			}
			if p.Match(in) != refMatch(p, in) || p.MayContain(in) != refMayContain(p, in) {
				t.Fatalf("%v %q on %q differs from the reference", subject, src, in)
			}
		}
	})
}
