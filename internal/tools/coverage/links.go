package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	inlineCode   = regexp.MustCompile("`[^`]*`")
	inlineLink   = regexp.MustCompile(`\]\(<?([^)\s>]+)>?(?:\s+"[^"]*")?\)`)
	refDefinion  = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s+<?(\S+?)>?(?:\s+.*)?$`)
	atxHeading   = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)[ \t]*$`)
	closingHash  = regexp.MustCompile(`[ \t]+#+$`)
	headingLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag      = regexp.MustCompile(`<[^>]+>`)
	htmlAnchor   = regexp.MustCompile(`<a\s[^>]*?(?:id|name)="([^"]+)"`)
	urlScheme    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	skippedDirs  = map[string]bool{".git": true, "node_modules": true, "testdata": true}
	emphasisMark = strings.NewReplacer("`", "", "*", "")
)

type mdFile struct {
	Path    string // relative to the root, slash-separated
	Lines   []string
	Visible []int
	Anchors map[string]bool
}

// headingAnchors returns the anchors of the headings and explicit anchors in a
// file. Repeated headings get the -1, -2 suffixes of the Markdown hosts.
func headingAnchors(lines []string, visible []int) map[string]bool {
	anchors := map[string]bool{}
	used := map[string]int{}
	for _, idx := range visible {
		line := lines[idx]
		for _, m := range htmlAnchor.FindAllStringSubmatch(line, -1) {
			anchors[m[1]] = true
		}
		m := atxHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := closingHash.ReplaceAllString(m[2], "")
		text = headingLink.ReplaceAllString(text, "$1")
		text = htmlTag.ReplaceAllString(text, "")
		base := slug(strings.TrimSpace(emphasisMark.Replace(text)))
		a := base
		if n := used[base]; n > 0 {
			a = fmt.Sprintf("%s-%d", base, n)
		}
		used[base]++
		anchors[a] = true
	}
	return anchors
}

func collectMarkdown(root string) ([]*mdFile, error) {
	var files []*mdFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: p comes from walking the repository root given on the command line
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		lines := strings.Split(string(data), "\n")
		visible, _ := visibleLines(lines, nil)
		files = append(files, &mdFile{
			Path: filepath.ToSlash(rel), Lines: lines, Visible: visible, Anchors: headingAnchors(lines, visible),
		})
		return nil
	})
	return files, err
}

// checkLinks verifies every relative Markdown link below root: the target file
// must exist and, when the link carries a fragment and the target is a
// Markdown file, the fragment must be a heading or explicit anchor there.
// Fenced code blocks and inline code are not searched. It returns one line per
// broken link and the number of links checked.
func checkLinks(root string) (broken []string, checked int, err error) {
	files, err := collectMarkdown(root)
	if err != nil {
		return nil, 0, err
	}
	byPath := map[string]*mdFile{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, f := range files {
		for _, idx := range f.Visible {
			line := inlineCode.ReplaceAllString(f.Lines[idx], "")
			var targets []string
			for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
				targets = append(targets, m[1])
			}
			if m := refDefinion.FindStringSubmatch(line); m != nil {
				targets = append(targets, m[1])
			}
			for _, raw := range targets {
				if urlScheme.MatchString(raw) || strings.HasPrefix(raw, "//") {
					continue
				}
				checked++
				if msg := checkTarget(root, f, raw, byPath); msg != "" {
					broken = append(broken, fmt.Sprintf("%s:%d: %s (%s)", f.Path, idx+1, raw, msg))
				}
			}
		}
	}
	sort.Strings(broken)
	return broken, checked, nil
}

func checkTarget(root string, from *mdFile, raw string, byPath map[string]*mdFile) string {
	pathPart, fragment, _ := strings.Cut(raw, "#")
	if i := strings.IndexByte(pathPart, '?'); i >= 0 {
		pathPart = pathPart[:i]
	}
	if u, err := url.PathUnescape(pathPart); err == nil {
		pathPart = u
	}
	if f, err := url.PathUnescape(fragment); err == nil {
		fragment = f
	}
	target := from
	if pathPart != "" {
		var abs string
		if strings.HasPrefix(pathPart, "/") {
			abs = filepath.Join(root, filepath.FromSlash(pathPart))
		} else {
			abs = filepath.Join(root, filepath.FromSlash(path0(from.Path)), filepath.FromSlash(pathPart))
		}
		if _, err := os.Stat(abs); err != nil {
			return "target does not exist"
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "" // outside the repository: not checked
		}
		target = byPath[filepath.ToSlash(rel)]
	}
	if fragment == "" || target == nil {
		return ""
	}
	if !target.Anchors[strings.ToLower(fragment)] && !target.Anchors[fragment] {
		return "no such anchor in " + target.Path
	}
	return ""
}

func path0(p string) string {
	d := filepath.ToSlash(filepath.Dir(filepath.FromSlash(p)))
	if d == "." {
		return ""
	}
	return d
}
