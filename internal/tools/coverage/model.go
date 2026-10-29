package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Feature status values. Implemented implies specified.
const (
	statusImplemented = "implemented"
	statusSpecified   = "specified"
	statusOpen        = "open"
)

var mdLink = regexp.MustCompile(`\]\(([^)#\s]+\.md)(?:#[^)]*)?\)`)

// model is the analysed corpus: pages in catalog-README order, the
// requirement-to-feature relation and the implementation evidence.
type model struct {
	Pages       []*page
	Features    []feature
	Reqs        []requirement
	coveredBy   map[string][]requirement
	implemented map[string]bool // requirement IDs with passing evidence
}

func newModel(c *corpus, order []string, implemented map[string]bool) *model {
	m := &model{Reqs: c.Reqs, coveredBy: map[string][]requirement{}, implemented: implemented}
	for _, rel := range order {
		p := c.Pages[rel]
		m.Pages = append(m.Pages, p)
		m.Features = append(m.Features, p.Features...)
	}
	for _, r := range c.Reqs {
		for _, fid := range r.Covers {
			m.coveredBy[fid] = append(m.coveredBy[fid], r)
		}
	}
	return m
}

// status of a feature: implemented when at least one requirement covers it and
// every covering requirement is implemented (Q-ROAD-4), specified when at
// least one requirement covers it, open otherwise.
func (m *model) status(f feature) string {
	reqs := m.coveredBy[f.ID]
	if len(reqs) == 0 {
		return statusOpen
	}
	for _, r := range reqs {
		if !m.implemented[r.ID] {
			return statusSpecified
		}
	}
	return statusImplemented
}

// isSpecified is true for specified and implemented features: a requirement
// lists them.
func (m *model) isSpecified(f feature) bool { return len(m.coveredBy[f.ID]) > 0 }

func (m *model) count(feats []feature, prios []string, pred func(feature) bool) int {
	n := 0
	for _, f := range feats {
		if prios != nil && !contains(prios, f.Priority) {
			continue
		}
		if pred != nil && !pred(f) {
			continue
		}
		n++
	}
	return n
}

func (m *model) countSpecified(feats []feature, prios ...string) int {
	return m.count(feats, prios, m.isSpecified)
}

func (m *model) countOpen(feats []feature, prios ...string) int {
	return m.count(feats, prios, func(f feature) bool { return !m.isSpecified(f) })
}

func (m *model) countImplemented(feats []feature, prios ...string) int {
	return m.count(feats, prios, func(f feature) bool { return m.status(f) == statusImplemented })
}

func (m *model) countAll(feats []feature, prios ...string) int { return m.count(feats, prios, nil) }

func (m *model) implementedReqs() int {
	n := 0
	for _, r := range m.Reqs {
		if m.implemented[r.ID] {
			n++
		}
	}
	return n
}

func (m *model) troveOnlyReqs() []requirement {
	var out []requirement
	for _, r := range m.Reqs {
		if len(r.Covers) == 0 {
			out = append(out, r)
		}
	}
	return out
}

func (m *model) prefixes(p *page) []string {
	var out []string
	for _, f := range p.Features {
		pre := f.ID[:strings.LastIndex(f.ID, "-")]
		if !contains(out, pre) {
			out = append(out, pre)
		}
	}
	return out
}

func (m *model) sectionHeading(p *page) string {
	return fmt.Sprintf("%s (%s)", p.Title, strings.Join(m.prefixes(p), ", "))
}

func (m *model) anchor(p *page) string { return slug(m.sectionHeading(p)) }

// slug is the heading anchor of the Markdown hosts the docs are read on:
// lower case, everything but letters, digits, underscore, hyphen and space
// removed, spaces turned into hyphens.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.M, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pageOrder is the order of the catalog README's page table. Format pages
// follow the order of first mention in formats/README.md; pages no README
// links come last, alphabetically (format pages after the last format page).
func pageOrder(featuresDir string, pages map[string]*page) []string {
	linked := func(readme, base string) []string {
		data, err := os.ReadFile(readme) //nolint:gosec // G304: path below the repository root given on the command line
		if err != nil {
			return nil
		}
		var order []string
		for _, mm := range mdLink.FindAllStringSubmatch(string(data), -1) {
			rel, err := filepath.Rel(featuresDir, filepath.Join(base, filepath.FromSlash(mm[1])))
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !contains(order, rel) {
				order = append(order, rel)
			}
		}
		return order
	}
	var formats []string
	for _, p := range linked(filepath.Join(featuresDir, "formats", "README.md"), filepath.Join(featuresDir, "formats")) {
		if strings.HasPrefix(p, "formats/") && p != "formats/README.md" {
			formats = append(formats, p)
		}
	}
	var order []string
	for _, rel := range linked(filepath.Join(featuresDir, "README.md"), featuresDir) {
		if contains(order, rel) {
			continue
		}
		order = append(order, rel)
		if rel == "formats/README.md" {
			for _, p := range formats {
				if !contains(order, p) {
					order = append(order, p)
				}
			}
		}
	}
	rest := make([]string, 0, len(pages))
	for rel := range pages {
		rest = append(rest, rel)
	}
	sort.Strings(rest)
	for _, rel := range rest {
		if contains(order, rel) {
			continue
		}
		at := len(order)
		if strings.HasPrefix(rel, "formats/") {
			for i, p := range order {
				if strings.HasPrefix(p, "formats/") {
					at = i + 1
				}
			}
		}
		order = append(order, "")
		copy(order[at+1:], order[at:])
		order[at] = rel
	}
	var out []string
	for _, rel := range order {
		if _, ok := pages[rel]; ok {
			out = append(out, rel)
		}
	}
	return out
}
