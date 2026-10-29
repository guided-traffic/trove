// Command coverage generates the coverage pages of docs/coverage from the
// feature catalog (docs/features) and the specification (docs/spec), enforces
// the rules of those pages, learns the implementation status from test
// reports (Q-ROAD-4) and checks the relative links and anchors of the
// repository's Markdown. See docs/developer/coverage.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	exitOK    = 0
	exitData  = 1 // rule violation, bad input, broken link
	exitStale = 2 // -check: a committed file differs from the regenerated one
	exitUsage = 64
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type options struct {
	Repo  string
	Check bool
	Links bool
	Tests []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var tests stringList
	opts := options{}
	fs.StringVar(&opts.Repo, "repo", "", "repository root (default: nearest parent directory holding go.mod)")
	fs.BoolVar(&opts.Check, "check", false, "write nothing; exit 2 when a generated file differs from what would be generated")
	fs.BoolVar(&opts.Links, "links", false, "check the relative links and anchors of every Markdown file instead of generating")
	fs.Var(&tests, "tests", "test report with requirement IDs in test names: `go test -json` stream or JUnit XML; "+
		"'-' reads stdin; repeatable, further file arguments count too")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: coverage [-repo dir] [-check] [-tests report]... [report...]\n"+
			"       coverage [-repo dir] -links\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	opts.Tests = append(append([]string{}, tests...), fs.Args()...)
	if opts.Repo == "" {
		root, err := findRepo()
		if err != nil {
			fmt.Fprintln(stderr, "coverage:", err)
			return exitUsage
		}
		opts.Repo = root
	}
	if opts.Links {
		if len(opts.Tests) > 0 || opts.Check {
			fmt.Fprintln(stderr, "coverage: -links does not combine with -check or -tests")
			return exitUsage
		}
		return runLinks(opts.Repo, stdout, stderr)
	}
	return runGenerate(opts, stdin, stdout, stderr)
}

func findRepo() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found above the working directory; pass -repo")
		}
		dir = parent
	}
}

func runLinks(repo string, stdout, stderr io.Writer) int {
	broken, checked, err := checkLinks(repo)
	if err != nil {
		fmt.Fprintln(stderr, "coverage:", err)
		return exitData
	}
	fmt.Fprintf(stdout, "relative links checked: %d, broken: %d\n", checked, len(broken))
	for _, b := range broken {
		fmt.Fprintln(stderr, "  "+b)
	}
	if len(broken) > 0 {
		return exitData
	}
	return exitOK
}

func fail(stderr io.Writer, errs []string) int {
	fmt.Fprintln(stderr, "coverage: error")
	for _, e := range errs {
		fmt.Fprintln(stderr, e)
	}
	return exitData
}

func runGenerate(opts options, stdin io.Reader, stdout, stderr io.Writer) int {
	docs := filepath.Join(opts.Repo, "docs")
	c, err := load(docs)
	if err != nil {
		fmt.Fprintln(stderr, "coverage:", err)
		return exitData
	}
	if len(c.Errors) > 0 {
		return fail(stderr, c.Errors)
	}
	known := map[string]bool{}
	for _, r := range c.Reqs {
		known[r.ID] = true
	}

	implPath := filepath.Join(opts.Repo, "docs", "coverage", "implemented.txt")
	committed, err := readImplemented(implPath, known)
	if err != nil {
		return fail(stderr, []string{err.Error()})
	}
	implemented := committed
	targets := map[string]string{}
	if len(opts.Tests) > 0 {
		ev := newEvidence()
		for _, path := range opts.Tests {
			if err := ev.readFile(path, stdin); err != nil {
				return fail(stderr, []string{err.Error()})
			}
		}
		ids, unknown := ev.implemented(known)
		if len(unknown) > 0 {
			return fail(stderr, []string{"test names carry requirement IDs that no spec page defines: " + strings.Join(unknown, ", ")})
		}
		implemented = ids
		targets[implPath] = renderImplemented(ids)
		fmt.Fprintf(stdout, "tests: %d results read, %d requirements with passing evidence, %d with a failing test\n",
			ev.Tests, len(ids), len(ev.Failed))
	}
	set := map[string]bool{}
	for _, id := range implemented {
		set[id] = true
	}

	order := pageOrder(filepath.Join(docs, "features"), c.Pages)
	m := newModel(c, order, set)
	if errs := m.checkAudiences(); len(errs) > 0 {
		return fail(stderr, errs)
	}
	anchors := map[string]bool{}
	var anchorErrs []string
	for _, p := range m.Pages {
		a := m.anchor(p)
		if anchors[a] || strings.Contains(a, "--") {
			anchorErrs = append(anchorErrs, "section anchor not unique or not host-independent: "+a)
		}
		anchors[a] = true
	}
	if len(anchorErrs) > 0 {
		return fail(stderr, anchorErrs)
	}

	checklist := renderChecklist(m)
	readmePath := filepath.Join(docs, "coverage", "README.md")
	readmeOld, err := os.ReadFile(readmePath) //nolint:gosec // G304: fixed path below the repository root given on the command line
	if err != nil {
		return fail(stderr, []string{"docs/coverage/README.md does not exist; it holds the prose around the generated blocks"})
	}
	readme, err := renderReadme(m, string(readmeOld))
	if err != nil {
		return fail(stderr, []string{err.Error()})
	}
	targets[filepath.Join(docs, "coverage", "checklist.md")] = checklist
	targets[readmePath] = readme
	if _, ok := targets[implPath]; !ok {
		targets[implPath] = renderImplemented(implemented)
	}

	printSummary(stdout, m, c)

	paths := make([]string, 0, len(targets))
	for p := range targets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if opts.Check {
		stale := 0
		for _, p := range paths {
			old, err := os.ReadFile(p) //nolint:gosec // G304: path below the repository root given on the command line
			if err != nil || string(old) != targets[p] {
				rel, _ := filepath.Rel(opts.Repo, p)
				fmt.Fprintf(stderr, "stale: %s (run the generator and commit the result)\n", filepath.ToSlash(rel))
				stale++
			}
		}
		if stale > 0 {
			return exitStale
		}
		return exitOK
	}
	for _, p := range paths {
		//nolint:gosec // G306: generated documentation is committed and must be world-readable like the other docs
		if err := os.WriteFile(p, []byte(targets[p]), 0o644); err != nil {
			fmt.Fprintln(stderr, "coverage:", err)
			return exitData
		}
		rel, _ := filepath.Rel(opts.Repo, p)
		fmt.Fprintf(stdout, "wrote %s (%s bytes)\n", filepath.ToSlash(rel), num(len(targets[p])))
	}
	return exitOK
}

// readImplemented reads docs/coverage/implemented.txt; a missing file is an
// empty list. Every ID in it must be a requirement.
func readImplemented(path string, known map[string]bool) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path below the repository root given on the command line
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids, err := parseImplemented(string(data))
	if err != nil {
		return nil, fmt.Errorf("docs/coverage/implemented.txt: %w", err)
	}
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("docs/coverage/implemented.txt: %s is not a requirement of the spec", id)
		}
	}
	return ids, nil
}

func printSummary(w io.Writer, m *model, c *corpus) {
	var parts []string
	for _, p := range priorities() {
		parts = append(parts, fmt.Sprintf("%s %d", p, m.countAll(m.Features, p)))
	}
	fmt.Fprintf(w, "catalog: %d pages, %d features (%s)\n", len(m.Pages), len(m.Features), strings.Join(parts, ", "))
	rp := map[string]int{}
	pages := map[string]bool{}
	refs := 0
	for _, r := range m.Reqs {
		rp[r.Priority]++
		pages[r.Page] = true
		refs += len(r.Covers)
	}
	var rparts []string
	for _, p := range priorities() {
		if rp[p] > 0 {
			rparts = append(rparts, fmt.Sprintf("%s %d", p, rp[p]))
		}
	}
	only := len(m.troveOnlyReqs())
	fmt.Fprintf(w, "spec: %d pages, %d requirements (%s); %d cover catalog features, %d %s; %d Covers references\n",
		len(pages), len(m.Reqs), strings.Join(rparts, ", "), len(m.Reqs)-only, only, troveOnly, refs)
	var cov []string
	for _, p := range priorities() {
		cov = append(cov, fmt.Sprintf("%s %d/%d", p, m.countSpecified(m.Features, p), m.countAll(m.Features, p)))
	}
	fmt.Fprintf(w, "coverage: %d specified, %d open; %s\n", m.countSpecified(m.Features), m.countOpen(m.Features), strings.Join(cov, "; "))
	fmt.Fprintf(w, "implemented: %d of %d requirements, %d of %d features\n",
		m.implementedReqs(), len(m.Reqs), m.countImplemented(m.Features), len(m.Features))
	fmt.Fprintf(w, "headings skipped inside code fences: %d\n", len(c.Fenced))
	for _, l := range c.Fenced {
		fmt.Fprintf(w, "  %s\n", l)
	}
}
