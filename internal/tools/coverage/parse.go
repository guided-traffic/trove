package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Priority values of the rubric, in rubric order.
const (
	prioMVP   = "MVP"
	prioP1    = "P1"
	prioP2    = "P2"
	prioLater = "Later"
	prioOut   = "Out of scope"

	troveOnly = "trove-only"
)

func priorities() []string { return []string{prioMVP, prioP1, prioP2, prioLater, prioOut} }

func inScopePriorities() []string { return []string{prioMVP, prioP1, prioP2, prioLater} }

const (
	idPattern   = `[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]+`
	prioPattern = `(MVP|P1|P2|Later|Out of scope)`
)

var (
	catHeading   = regexp.MustCompile(`^### (` + idPattern + `) · (\S.*?)\s*$`)
	catLookalike = regexp.MustCompile(`^### +[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]`)
	catPriority  = regexp.MustCompile(`^Priority: \*\*` + prioPattern + `\*\*\s*$`)
	reqHeading   = regexp.MustCompile(`^### (R-` + idPattern + `) · (\S.*?)\s*$`)
	reqLookalike = regexp.MustCompile(`^### +R-`)
	reqPriority  = regexp.MustCompile(`^Priority: \*\*` + prioPattern + `\*\* · Covers: (\S.*?)\s*$`)
	catalogID    = regexp.MustCompile(`^` + idPattern + `$`)
	qHeading     = regexp.MustCompile(`^### (Q-` + idPattern + `) · `)
	hHeading     = regexp.MustCompile(`^### (H-[0-9]+) · `)
	fenceLine    = regexp.MustCompile("^\\s*(`{3,}|~{3,})(.*)$")
	h1Line       = regexp.MustCompile(`^# (\S.*?)\s*$`)
)

type feature struct {
	ID, Title, Priority string
	Page                string // path below docs/features
	Line                int
}

type requirement struct {
	ID, Title, Priority string
	Covers              []string
	Page                string // path below docs/spec
	Line                int
}

type page struct {
	Rel, Title string
	Features   []feature
}

// corpus is everything read from docs/features and docs/spec plus the
// problems found while reading it.
type corpus struct {
	Pages  map[string]*page
	Reqs   []requirement
	Fenced []string // headings skipped inside code fences
	Errors []string
}

func (c *corpus) errorf(format string, args ...any) {
	c.Errors = append(c.Errors, fmt.Sprintf(format, args...))
}

// markdownFiles lists the .md files below dir in walk order (names sorted
// within each directory), as slash-separated paths relative to dir.
func markdownFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out, err
}

// readLines splits a file into lines and returns the indexes of the lines that
// lie outside fenced code blocks. Headings inside a fence are recorded in
// fenced; an unclosed fence is an error.
func readLines(path, shown string, c *corpus) (lines []string, visible []int, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path below the repository root given on the command line
	if err != nil {
		return nil, nil, err
	}
	lines = strings.Split(string(data), "\n")
	visible, fenceOpen := visibleLines(lines, func(idx int, line string) {
		c.Fenced = append(c.Fenced, fmt.Sprintf("%s:%d: %s", shown, idx+1, line))
	})
	if fenceOpen > 0 {
		c.errorf("%s:%d: code fence opened here is never closed", shown, fenceOpen)
	}
	return lines, visible, nil
}

// visibleLines returns the indexes of lines outside fenced code blocks. The
// callback receives each "### " line found inside a fence. The second result is
// the 1-based line of a fence that is never closed, or 0.
func visibleLines(lines []string, inFence func(idx int, line string)) ([]int, int) {
	var visible []int
	var fenceChar byte
	fenceLen, fenceAt := 0, 0
	for idx, line := range lines {
		m := fenceLine.FindStringSubmatch(line)
		if fenceChar == 0 {
			if m != nil && (m[1][0] != '`' || !strings.Contains(m[2], "`")) {
				fenceChar, fenceLen, fenceAt = m[1][0], len(m[1]), idx+1
				continue
			}
			visible = append(visible, idx)
			continue
		}
		if m != nil && m[1][0] == fenceChar && len(m[1]) >= fenceLen && strings.TrimSpace(m[2]) == "" {
			fenceChar = 0
			continue
		}
		if inFence != nil && strings.HasPrefix(line, "### ") {
			inFence(idx, line)
		}
	}
	if fenceChar != 0 {
		return visible, fenceAt
	}
	return visible, 0
}

func nextLine(lines []string, idx int) string {
	if idx+1 < len(lines) {
		return lines[idx+1]
	}
	return ""
}

func parseCatalog(docs string, c *corpus) error {
	dir := filepath.Join(docs, "features")
	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range files {
		lines, visible, err := readLines(filepath.Join(dir, filepath.FromSlash(rel)), "features/"+rel, c)
		if err != nil {
			return err
		}
		title := ""
		var feats []feature
		for _, idx := range visible {
			line := lines[idx]
			if title == "" {
				if m := h1Line.FindStringSubmatch(line); m != nil {
					title = m[1]
				}
			}
			if !strings.HasPrefix(line, "### ") {
				continue
			}
			where := fmt.Sprintf("features/%s:%d", rel, idx+1)
			m := catHeading.FindStringSubmatch(line)
			if m == nil {
				if catLookalike.MatchString(line) || strings.Contains(line, " · ") {
					c.errorf("%s: entry heading not in the form '### <ID> · <Title>': %s", where, line)
				}
				continue
			}
			p := catPriority.FindStringSubmatch(nextLine(lines, idx))
			if p == nil {
				c.errorf("%s: %s has no 'Priority: **<P>**' line directly below, with <P> one of %s",
					where, m[1], strings.Join(priorities(), ", "))
				continue
			}
			feats = append(feats, feature{ID: m[1], Title: m[2], Priority: p[1], Page: rel, Line: idx + 1})
		}
		if len(feats) == 0 {
			continue
		}
		if title == "" {
			c.errorf("features/%s: page has entries but no '# ' title", rel)
			title = rel
		}
		c.Pages[rel] = &page{Rel: rel, Title: title, Features: feats}
	}
	return nil
}

func parseSpec(docs string, c *corpus) error {
	dir := filepath.Join(docs, "spec")
	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range files {
		lines, visible, err := readLines(filepath.Join(dir, filepath.FromSlash(rel)), "spec/"+rel, c)
		if err != nil {
			return err
		}
		for _, idx := range visible {
			line := lines[idx]
			if !reqLookalike.MatchString(line) {
				continue
			}
			where := fmt.Sprintf("spec/%s:%d", rel, idx+1)
			m := reqHeading.FindStringSubmatch(line)
			if m == nil {
				c.errorf("%s: requirement heading not in the form '### R-<...>-<n> · <Title>': %s", where, line)
				continue
			}
			p := reqPriority.FindStringSubmatch(nextLine(lines, idx))
			if p == nil {
				c.errorf("%s: %s has no 'Priority: **<P>** · Covers: ...' line directly below, with <P> one of %s",
					where, m[1], strings.Join(priorities(), ", "))
				continue
			}
			var covers []string
			tokens := strings.Split(p[2], ",")
			for i := range tokens {
				tokens[i] = strings.TrimSpace(tokens[i])
			}
			for _, tok := range tokens {
				switch {
				case tok == troveOnly:
				case !catalogID.MatchString(tok):
					c.errorf("%s: %s Covers token is neither a catalog ID nor '%s': %q", where, m[1], troveOnly, tok)
				case contains(covers, tok):
					c.errorf("%s: %s lists %s twice in Covers", where, m[1], tok)
				default:
					covers = append(covers, tok)
				}
			}
			if contains(tokens, troveOnly) && len(tokens) > 1 {
				c.errorf("%s: %s mixes '%s' with catalog IDs", where, m[1], troveOnly)
			}
			c.Reqs = append(c.Reqs, requirement{
				ID: m[1], Title: m[2], Priority: p[1], Covers: covers, Page: rel, Line: idx + 1,
			})
		}
	}
	return nil
}

// checkQuestionsAndGaps scans every page below docs for decided questions
// (### Q-<AREA>-<n> · …) and security gaps (### H-<n> · …) and reports
// duplicates. The lines come from all pages, since a Q- heading lives on spec
// pages and on the roadmap, an H- heading on spec and security pages.
func checkQuestionsAndGaps(docs string, c *corpus) error {
	files, err := markdownFiles(docs)
	if err != nil {
		return err
	}
	seen := map[string][]string{}
	var order []string
	for _, rel := range files {
		if strings.HasPrefix(rel, "features/") {
			continue
		}
		scratch := &corpus{}
		lines, visible, err := readLines(filepath.Join(docs, filepath.FromSlash(rel)), rel, scratch)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(rel, "spec/") { // spec pages report their own fences
			c.Errors = append(c.Errors, scratch.Errors...)
		}
		for _, idx := range visible {
			var id string
			if m := qHeading.FindStringSubmatch(lines[idx]); m != nil {
				id = m[1]
			} else if m := hHeading.FindStringSubmatch(lines[idx]); m != nil {
				id = m[1]
			} else {
				continue
			}
			if _, ok := seen[id]; !ok {
				order = append(order, id)
			}
			seen[id] = append(seen[id], fmt.Sprintf("%s:%d", rel, idx+1))
		}
	}
	for _, id := range order {
		if len(seen[id]) > 1 {
			kind := "question"
			if strings.HasPrefix(id, "H-") {
				kind = "gap"
			}
			c.errorf("duplicate %s ID %s: %s", kind, id, strings.Join(seen[id], ", "))
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// load reads docs/features and docs/spec under the docs directory and applies
// the generator's rules. Problems accumulate in corpus.Errors.
func load(docs string) (*corpus, error) {
	c := &corpus{Pages: map[string]*page{}}
	if err := parseCatalog(docs, c); err != nil {
		return nil, err
	}
	if err := parseSpec(docs, c); err != nil {
		return nil, err
	}
	if err := checkQuestionsAndGaps(docs, c); err != nil {
		return nil, err
	}

	catalogAt := map[string][]string{}
	var catalogOrder []string
	rels := make([]string, 0, len(c.Pages))
	for rel := range c.Pages {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		for _, f := range c.Pages[rel].Features {
			if _, ok := catalogAt[f.ID]; !ok {
				catalogOrder = append(catalogOrder, f.ID)
			}
			catalogAt[f.ID] = append(catalogAt[f.ID], fmt.Sprintf("features/%s:%d", f.Page, f.Line))
		}
	}
	for _, id := range catalogOrder {
		if len(catalogAt[id]) > 1 {
			c.errorf("duplicate catalog ID %s: %s", id, strings.Join(catalogAt[id], ", "))
		}
	}
	reqAt := map[string][]string{}
	var reqOrder []string
	for _, r := range c.Reqs {
		if _, ok := reqAt[r.ID]; !ok {
			reqOrder = append(reqOrder, r.ID)
		}
		reqAt[r.ID] = append(reqAt[r.ID], fmt.Sprintf("spec/%s:%d", r.Page, r.Line))
	}
	for _, id := range reqOrder {
		if len(reqAt[id]) > 1 {
			c.errorf("duplicate requirement ID %s: %s", id, strings.Join(reqAt[id], ", "))
		}
	}
	for _, r := range c.Reqs {
		for _, fid := range r.Covers {
			if _, ok := catalogAt[fid]; !ok {
				c.errorf("spec/%s:%d: %s covers %s, which is not in the catalog", r.Page, r.Line, r.ID, fid)
			}
		}
	}
	return c, nil
}
