package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot finds the repository root from the package directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := findRepo()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func fixtureDocs(name string) string { return filepath.Join("testdata", name, "docs") }

func mustLoad(t *testing.T, docs string) *corpus {
	t.Helper()
	c, err := load(docs)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fixtureModel(t *testing.T, name string, implemented ...string) *model {
	t.Helper()
	c := mustLoad(t, fixtureDocs(name))
	if len(c.Errors) > 0 {
		t.Fatalf("fixture %s: %v", name, c.Errors)
	}
	set := map[string]bool{}
	for _, id := range implemented {
		set[id] = true
	}
	return newModel(c, pageOrder(filepath.Join(fixtureDocs(name), "features"), c.Pages), set)
}

func TestRealDocsReproduceTheMeasuredNumbers(t *testing.T) {
	c := mustLoad(t, filepath.Join(repoRoot(t), "docs"))
	if len(c.Errors) > 0 {
		t.Fatalf("the real docs violate the rules:\n%s", strings.Join(c.Errors, "\n"))
	}
	m := newModel(c, pageOrder(filepath.Join(repoRoot(t), "docs", "features"), c.Pages), map[string]bool{})

	// Catalog priority -> entries, listed in a Covers: line; measured 2026-10-31, after the page
	// vulnerability-scanning.md raised its features to MVP.
	want := map[string][2]int{
		prioMVP:   {379, 379},
		prioP1:    {517, 293},
		prioP2:    {895, 23},
		prioLater: {376, 0},
		prioOut:   {72, 0},
	}
	t.Run("E0.8 catalog counts per priority", func(t *testing.T) {
		for prio, w := range want {
			if got := m.countAll(m.Features, prio); got != w[0] {
				t.Errorf("%s entries: got %d, want %d", prio, got, w[0])
			}
			listed := m.countSpecified(m.Features, prio)
			if listed != w[1] {
				t.Errorf("%s listed in Covers: got %d, want %d", prio, listed, w[1])
			}
			if open := m.countOpen(m.Features, prio); open != w[0]-w[1] {
				t.Errorf("%s not listed: got %d, want %d", prio, open, w[0]-w[1])
			}
		}
	})
	t.Run("E0.8 catalog totals", func(t *testing.T) {
		if got := len(m.Features); got != 2239 {
			t.Errorf("catalog entries: got %d, want 2239", got)
		}
		if got := m.countSpecified(m.Features); got != 695 {
			t.Errorf("listed entries: got %d, want 695", got)
		}
		if got := m.countOpen(m.Features); got != 1544 {
			t.Errorf("not listed entries: got %d, want 1544", got)
		}
	})
	t.Run("E0.8 requirement counts", func(t *testing.T) {
		count := map[string]int{}
		for _, r := range m.Reqs {
			count[r.Priority]++
		}
		if len(m.Reqs) != 627 || count[prioMVP] != 473 || count[prioP1] != 149 || count[prioP2] != 5 {
			t.Errorf("requirements: got %d (MVP %d, P1 %d, P2 %d), want 627 (473, 149, 5)",
				len(m.Reqs), count[prioMVP], count[prioP1], count[prioP2])
		}
	})
	t.Run("E0.8 headings inside code fences are ignored", func(t *testing.T) {
		var sawRequirement, sawEntry bool
		for _, l := range c.Fenced {
			sawRequirement = sawRequirement || strings.Contains(l, "### R-NPM-012 ·")
			sawEntry = sawEntry || strings.Contains(l, "### RMT-004 ·")
		}
		if !sawRequirement || !sawEntry {
			t.Errorf("expected the format examples in the READMEs to be skipped, got %v", c.Fenced)
		}
	})
}

func TestCommittedOutputIsCurrent(t *testing.T) {
	root := repoRoot(t)
	t.Run("E0.8 regeneration changes no committed file", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", root, "-check"}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
		}
	})
}

func TestRuleViolations(t *testing.T) {
	cases := []struct {
		name, rule, want string
	}{
		{"bad-catalog-priority", "E0.8 catalog entry with a priority outside the rubric fails", "WID-002 has no 'Priority: **<P>**' line"},
		{"missing-catalog-priority", "E0.8 catalog entry without a Priority line fails", "WID-002 has no 'Priority: **<P>**' line"},
		{"bad-req-priority", "E0.8 requirement with a priority outside the rubric fails", "R-WID-001 has no 'Priority: **<P>** · Covers: ...' line"},
		{"missing-covers", "E0.8 requirement without Covers fails", "R-WID-001 has no 'Priority: **<P>** · Covers: ...' line"},
		{"unknown-covers", "E0.8 Covers ID missing from the catalog fails", "R-WID-001 covers WID-099, which is not in the catalog"},
		{"covers-mixed", "E0.8 trove-only mixed with IDs fails", "R-WID-001 mixes 'trove-only' with catalog IDs"},
		{"covers-bad-token", "E0.8 Covers token that is no ID fails", `neither a catalog ID nor 'trove-only': "widget two"`},
		{"dup-catalog-id", "E0.8 duplicate catalog ID fails", "duplicate catalog ID WID-001"},
		{"dup-req-id", "E0.8 duplicate requirement ID fails", "duplicate requirement ID R-WID-001"},
		{"dup-question-id", "E0.8 duplicate Q- ID fails", "duplicate question ID Q-WID-1"},
		{"dup-question-id-across-pages", "E0.8 duplicate Q- ID on two pages fails", "duplicate question ID Q-WID-1: spec/other.md:3, spec/widgets.md"},
		{"dup-gap-id", "E0.8 duplicate H- number fails", "duplicate gap ID H-1"},
		{"unclosed-fence", "E0.8 unclosed code fence fails", "code fence opened here is never closed"},
		{"malformed-heading", "E0.8 malformed entry heading fails", "entry heading not in the form '### <ID> · <Title>'"},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			c := mustLoad(t, fixtureDocs(tc.name))
			joined := strings.Join(c.Errors, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("errors do not contain %q:\n%s", tc.want, joined)
			}
		})
	}
	t.Run("E0.8 a valid fixture has no errors", func(t *testing.T) {
		if c := mustLoad(t, fixtureDocs("ok")); len(c.Errors) > 0 {
			t.Fatalf("unexpected errors: %v", c.Errors)
		}
	})
	t.Run("E0.8 headings inside fenced code blocks are ignored", func(t *testing.T) {
		c := mustLoad(t, fixtureDocs("fenced"))
		if len(c.Errors) > 0 {
			t.Fatalf("unexpected errors: %v", c.Errors)
		}
		if len(c.Fenced) != 4 {
			t.Errorf("skipped headings: got %d (%v), want 4", len(c.Fenced), c.Fenced)
		}
		if len(c.Reqs) != 2 {
			t.Errorf("requirements: got %d, want 2", len(c.Reqs))
		}
	})
}

func TestRuleViolationsFailTheCommand(t *testing.T) {
	t.Run("E0.8 command exits 1 on a rule violation", func(t *testing.T) {
		repo := t.TempDir()
		copyTree(t, filepath.Join("testdata", "dup-req-id", "docs"), filepath.Join(repo, "docs"))
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", repo}, strings.NewReader(""), &stdout, &stderr); code != exitData {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, exitData, stderr.String())
		}
		if !strings.Contains(stderr.String(), "duplicate requirement ID R-WID-001") {
			t.Errorf("stderr does not name the duplicate:\n%s", stderr.String())
		}
	})
}

func TestFeatureStatus(t *testing.T) {
	t.Run("Q-ROAD-4 feature is implemented only when every covering requirement is", func(t *testing.T) {
		// R-WID-001 covers WID-001 and WID-002; R-WID-002 covers nothing.
		m := fixtureModel(t, "ok", "R-WID-001")
		status := map[string]string{}
		for _, f := range m.Features {
			status[f.ID] = m.status(f)
		}
		want := map[string]string{"WID-001": statusImplemented, "WID-002": statusImplemented, "WID-003": statusOpen}
		for id, w := range want {
			if status[id] != w {
				t.Errorf("%s: got %s, want %s", id, status[id], w)
			}
		}
	})
	t.Run("Q-ROAD-4 feature with an unimplemented covering requirement stays specified", func(t *testing.T) {
		c := mustLoad(t, fixtureDocs("ok"))
		c.Reqs = append(c.Reqs, requirement{ID: "R-WID-003", Priority: prioMVP, Covers: []string{"WID-001"}, Page: "widgets.md"})
		m := newModel(c, []string{"widgets.md"}, map[string]bool{"R-WID-001": true})
		for _, f := range m.Features {
			switch f.ID {
			case "WID-001":
				if got := m.status(f); got != statusSpecified {
					t.Errorf("WID-001: got %s, want specified (R-WID-003 is not implemented)", got)
				}
			case "WID-002":
				if got := m.status(f); got != statusImplemented {
					t.Errorf("WID-002: got %s, want implemented", got)
				}
			}
		}
	})
	t.Run("Q-ROAD-4 no evidence leaves everything specified or open", func(t *testing.T) {
		m := fixtureModel(t, "ok")
		if got := m.countImplemented(m.Features); got != 0 {
			t.Errorf("implemented features: got %d, want 0", got)
		}
		if got := renderChecklist(m); !strings.Contains(got, "Nothing is implemented.") {
			t.Errorf("checklist should say nothing is implemented")
		}
	})
}

func readEvidence(t *testing.T, files ...string) *evidence {
	t.Helper()
	ev := newEvidence()
	for _, f := range files {
		if err := ev.readFile(filepath.Join("testdata", "evidence", f), strings.NewReader("")); err != nil {
			t.Fatal(err)
		}
	}
	return ev
}

func TestEvidence(t *testing.T) {
	known := map[string]bool{"R-WID-001": true, "R-WID-002": true, "R-WID-003": true}

	t.Run("Q-ROAD-4 passing test marks requirement implemented, failing test does not", func(t *testing.T) {
		ids, unknown := readEvidence(t, "pass-fail.json").implemented(known)
		if len(unknown) != 0 || strings.Join(ids, ",") != "R-WID-001" {
			t.Fatalf("got %v (unknown %v), want exactly R-WID-001", ids, unknown)
		}
	})
	t.Run("Q-ROAD-4 a failing test vetoes a passing one that names the same requirement", func(t *testing.T) {
		ids, _ := readEvidence(t, "veto.json").implemented(known)
		if strings.Join(ids, ",") != "R-WID-002" {
			t.Fatalf("got %v, want only R-WID-002 (R-WID-001 failed in package b, R-WID-003 was skipped)", ids)
		}
	})
	t.Run("Q-ROAD-4 JUnit reports are read like go test streams", func(t *testing.T) {
		ids, _ := readEvidence(t, "junit.xml").implemented(known)
		if strings.Join(ids, ",") != "R-WID-001" {
			t.Fatalf("got %v, want R-WID-001", ids)
		}
	})
	t.Run("Q-ROAD-4 reports combine and the veto holds across files", func(t *testing.T) {
		ids, _ := readEvidence(t, "pass-fail.json", "veto.json", "junit.xml").implemented(known)
		if len(ids) != 0 {
			t.Fatalf("got %v, want none: R-WID-001 fails in veto.json, R-WID-002 fails in pass-fail.json and junit.xml", ids)
		}
	})
	t.Run("Q-ROAD-4 an unknown ID in a test name is reported", func(t *testing.T) {
		_, unknown := readEvidence(t, "typo.json").implemented(known)
		if strings.Join(unknown, ",") != "R-WID-777" {
			t.Fatalf("unknown: got %v, want R-WID-777", unknown)
		}
	})
	t.Run("Q-ROAD-4 build chatter between JSON lines is skipped", func(t *testing.T) {
		ids, _ := readEvidence(t, "mixed-output.txt").implemented(known)
		if strings.Join(ids, ",") != "R-WID-001" {
			t.Fatalf("got %v, want R-WID-001", ids)
		}
	})
	t.Run("Q-ROAD-4 a file that is no report is an error", func(t *testing.T) {
		err := newEvidence().readFile(filepath.Join("testdata", "evidence", "not-a-report.json"), strings.NewReader(""))
		if err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("Q-ROAD-4 standard input is read for -", func(t *testing.T) {
		ev := newEvidence()
		if err := ev.readFile("-", strings.NewReader(`{"Action":"pass","Package":"p","Test":"TestX/R-WID-002_ok"}`+"\n")); err != nil {
			t.Fatal(err)
		}
		if ids, _ := ev.implemented(known); strings.Join(ids, ",") != "R-WID-002" {
			t.Fatalf("got %v", ids)
		}
	})
}

func TestIDsInTestNames(t *testing.T) {
	cases := map[string][]string{
		"TestX/R-ARCH-002_forbidden_import_names_the_edge": {"R-ARCH-002"},
		"TestX/R-NPM-007":                    {"R-NPM-007"},
		"TestX/R-NPM-007/2_second_criterion": {"R-NPM-007"},
		"TestX/R-A-1_and_R-B-22_both":        {"R-A-1", "R-B-22"},
		"TestX/R-A-1_R-B-2":                  {"R-A-1", "R-B-2"},
		"R-ARCH-002 spaces in JUnit titles":  {"R-ARCH-002"},
		"TestPR-ARCH-002":                    nil,
		"TestX/R-ARCH-002-suffix":            nil,
		"TestX/r-arch-002":                   nil,
		"TestX/Q-ROAD-4_is_no_requirement":   nil,
		"TestX/E0.8_only":                    nil,
	}
	// The test names must not carry the sample names: the generator reads IDs
	// from the names of the tests it is run with.
	n := 0
	for name, want := range cases {
		n++
		t.Run(fmt.Sprintf("Q-ROAD-4 test name case %d", n), func(t *testing.T) {
			got := idsIn(name)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

// copyTree copies a directory tree.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommandWithTestReports(t *testing.T) {
	root := repoRoot(t)
	setup := func(t *testing.T) string {
		repo := t.TempDir()
		copyTree(t, filepath.Join(root, "docs"), filepath.Join(repo, "docs"))
		return repo
	}
	stream := `{"Action":"pass","Package":"p","Test":"TestA/R-ARCH-002_passes"}` + "\n" +
		`{"Action":"fail","Package":"p","Test":"TestA/R-ARCH-003_fails"}` + "\n"
	write := func(t *testing.T, dir, content string) string {
		p := filepath.Join(dir, "report.json")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("E0.8 go test -json stream marks exactly the passing requirement implemented", func(t *testing.T) {
		repo := setup(t)
		report := write(t, t.TempDir(), stream)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", repo, "-tests", report}, strings.NewReader(""), &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d:\n%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(repo, "docs", "coverage", "implemented.txt"))
		if err != nil {
			t.Fatal(err)
		}
		ids, err := parseImplemented(string(data))
		if err != nil || strings.Join(ids, ",") != "R-ARCH-002" {
			t.Fatalf("implemented.txt lists %v (%v), want exactly R-ARCH-002", ids, err)
		}
		checklist, _ := os.ReadFile(filepath.Join(repo, "docs", "coverage", "checklist.md"))
		if !strings.Contains(string(checklist), "[R-ARCH-002](../spec/architecture.md) (implemented)") {
			t.Errorf("checklist does not mark R-ARCH-002 as implemented")
		}
		if strings.Contains(string(checklist), "[R-ARCH-003](../spec/architecture.md) (implemented)") {
			t.Errorf("checklist marks the failing R-ARCH-003 as implemented")
		}

		t.Run("Q-ROAD-4 -check with the same reports passes, with a changed report it is stale", func(t *testing.T) {
			var o, e bytes.Buffer
			if code := run([]string{"-repo", repo, "-check", "-tests", report}, strings.NewReader(""), &o, &e); code != exitOK {
				t.Fatalf("exit %d:\n%s", code, e.String())
			}
			none := write(t, t.TempDir(), `{"Action":"pass","Package":"p","Test":"TestA/plain"}`+"\n")
			o.Reset()
			e.Reset()
			if code := run([]string{"-repo", repo, "-check", "-tests", none}, strings.NewReader(""), &o, &e); code != exitStale {
				t.Fatalf("exit %d, want %d:\n%s", code, exitStale, e.String())
			}
			if !strings.Contains(e.String(), "stale: docs/coverage/implemented.txt") {
				t.Errorf("stderr: %s", e.String())
			}
		})
	})
	t.Run("Q-ROAD-4 a test naming an unknown requirement fails the command", func(t *testing.T) {
		repo := setup(t)
		report := write(t, t.TempDir(), `{"Action":"pass","Package":"p","Test":"TestA/R-ARCH-999_typo"}`+"\n")
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", repo, "-tests", report}, strings.NewReader(""), &stdout, &stderr); code != exitData {
			t.Fatalf("exit %d:\n%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "R-ARCH-999") {
			t.Errorf("stderr does not name R-ARCH-999:\n%s", stderr.String())
		}
	})
	t.Run("Q-ROAD-4 positional report arguments and standard input count as reports", func(t *testing.T) {
		repo := setup(t)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", repo, "-tests", "-"}, strings.NewReader(stream), &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d:\n%s", code, stderr.String())
		}
		data, _ := os.ReadFile(filepath.Join(repo, "docs", "coverage", "implemented.txt"))
		if !strings.Contains(string(data), "\nR-ARCH-002\n") || strings.Contains(string(data), "R-ARCH-003") {
			t.Errorf("implemented.txt:\n%s", data)
		}
	})
	t.Run("Q-ROAD-4 a requirement implemented in the committed list must exist", func(t *testing.T) {
		repo := setup(t)
		if err := os.WriteFile(filepath.Join(repo, "docs", "coverage", "implemented.txt"), []byte("R-NOPE-001\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", repo, "-check"}, strings.NewReader(""), &stdout, io.Discard); code != exitData {
			t.Fatalf("exit %d:\n%s", code, stderr.String())
		}
	})
}

func TestChecklistFormat(t *testing.T) {
	t.Run("E0.8 checklist lists every entry and requirement", func(t *testing.T) {
		m := fixtureModel(t, "ok")
		out := renderChecklist(m)
		for _, want := range []string{
			"| WID-001 | First widget | MVP | specified | [R-WID-001](../spec/widgets.md) |",
			"| WID-003 | Third widget | Out of scope | open | – |",
			"| R-WID-002 | Own design | P1 | [widgets.md](../spec/widgets.md) |",
			"## Widgets (WID)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("checklist lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("Q-ROAD-4 checklist shows implementation status", func(t *testing.T) {
		out := renderChecklist(fixtureModel(t, "ok", "R-WID-001", "R-WID-002"))
		for _, want := range []string{
			"| WID-001 | First widget | MVP | implemented | [R-WID-001](../spec/widgets.md) (implemented) |",
			"| R-WID-002 (implemented) | Own design |",
			"2 of 2 spec requirements are implemented",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("checklist lacks %q:\n%s", want, out)
			}
		}
	})
}

func TestLinks(t *testing.T) {
	write := func(t *testing.T, root, rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	write(t, root, "a.md", "# A\n\n## Same\n\n## Same\n\n[ok](b.md) [ok](b.md#target-section) [ok](#same-1) [ok](sub/c.md#c-title)\n"+
		"[gone](missing.md) [bad anchor](b.md#nope) [bad self](#nope) [web](https://example.com/x.md)\n"+
		"`[code](missing.md)`\n\n```\n[fenced](missing.md)\n```\n\n[abs](/sub/c.md)\n")
	write(t, root, "b.md", "# B\n\n## Target `Section`\n\n<a id=\"explicit\"></a>\n")
	write(t, root, "sub/c.md", "# C Title\n\n[up](../b.md#explicit) [up](../a.md#same)\n")
	write(t, root, "node_modules/x/README.md", "[skipped](nowhere.md)\n")

	broken, checked, err := checkLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("E0.8 link check finds exactly the broken links", func(t *testing.T) {
		want := []string{
			"a.md:8: b.md#nope (no such anchor in b.md)",
			"a.md:8: #nope (no such anchor in a.md)",
			"a.md:8: missing.md (target does not exist)",
		}
		got := strings.Join(broken, "\n")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("missing report %q in:\n%s", w, got)
			}
		}
		if len(broken) != len(want) {
			t.Errorf("got %d broken links, want %d:\n%s", len(broken), len(want), got)
		}
		if checked != 10 {
			t.Errorf("checked %d links, want 10", checked)
		}
	})
	t.Run("E0.8 -links exit code", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-repo", root, "-links"}, strings.NewReader(""), &stdout, &stderr); code != exitData {
			t.Fatalf("exit %d, want %d", code, exitData)
		}
	})
}
