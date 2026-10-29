package main

import (
	"fmt"
	"strconv"
	"strings"
)

const detailsThreshold = 15 // tables with more rows than this go into <details>

// num formats an integer with thousands separators.
func num(v int) string {
	s := strconv.Itoa(v)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func pct(a, b int) string {
	switch {
	case b == 0:
		return "–"
	case a == 0:
		return "0%"
	case a == b:
		return "100%"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}

func cell(text string) string { return strings.ReplaceAll(text, "|", `\|`) }

func table(header []string, rows [][]string, align []string) []string {
	if align == nil {
		align = make([]string, len(header))
		for i := range align {
			align[i] = "---"
		}
	}
	out := []string{"| " + strings.Join(header, " | ") + " |", "|" + strings.Join(align, "|") + "|"}
	for _, r := range rows {
		out = append(out, "| "+strings.Join(r, " | ")+" |")
	}
	return out
}

func maybeDetails(summary string, lines []string, rows int) []string {
	if rows <= detailsThreshold {
		return lines
	}
	out := []string{"<details>", "<summary>" + summary + "</summary>", ""}
	out = append(out, lines...)
	return append(out, "", "</details>")
}

// wrap breaks text at spaces into lines of at most width characters; a word
// longer than the width stays whole.
func wrap(text string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = w
		case len([]rune(cur))+1+len([]rune(w)) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func list(ss []string, sep string) string { return strings.Join(ss, sep) }

// renderChecklist renders docs/coverage/checklist.md.
func renderChecklist(m *model) string {
	feats := m.Features
	specN := m.countSpecified(feats)
	openN := m.countOpen(feats)
	only := m.troveOnlyReqs()
	anyImpl := m.implementedReqs() > 0

	out := []string{
		"# Coverage checklist",
		"",
		"<!-- Generated file: do not edit by hand. -->",
		"",
		"Generated from the headings of the [feature catalog](../features/README.md) (`### <ID> · <Title>`",
		"and the `Priority:` line below it) and of the [specification](../spec/README.md)",
		"(`### R-… · …` and the `Priority: … · Covers: …` line below it). Do not edit it by hand: change",
		"the catalog or the spec, then regenerate this file whenever an ID, a title, a priority or a",
		"`Covers:` line changes. The rules and the headline numbers are in",
		"[README.md](README.md#how-the-numbers-are-produced).",
		"",
	}
	if anyImpl {
		out = append(out,
			fmt.Sprintf("**Status.** %s of %s spec requirements are implemented: a passing test names them and no",
				num(m.implementedReqs()), num(len(m.Reqs))),
			"failing test does (the list is [implemented.txt](implemented.txt)). *implemented*: every requirement",
			"that lists the feature in its `Covers:` line is implemented. *specified*: at least one spec",
			"requirement lists the feature in its `Covers:` line and not all of them are implemented. *open*: no",
			"requirement does. For entries with priority *Out of scope*, open is the intended end state.",
		)
	} else {
		out = append(out,
			"**Status.** Nothing is implemented. *specified*: at least one spec requirement lists the feature",
			"in its `Covers:` line. *open*: no requirement does. For entries with priority *Out of scope*,",
			"open is the intended end state.",
		)
	}
	totals := fmt.Sprintf("**Totals.** %s catalog features on %d pages: %s specified, %s open. ",
		num(len(feats)), len(m.Pages), num(specN), num(openN))
	if anyImpl {
		totals = fmt.Sprintf("**Totals.** %s catalog features on %d pages: %s specified (%s of them implemented), %s open. ",
			num(len(feats)), len(m.Pages), num(specN), num(m.countImplemented(feats)), num(openN))
	}
	totals += fmt.Sprintf("%s spec requirements: %s cover catalog features, %d cover none (`%s`, "+
		"[listed at the end](#requirements-that-cover-no-catalog-feature)).",
		num(len(m.Reqs)), num(len(m.Reqs)-len(only)), len(only), troveOnly)
	out = append(out, "")
	out = append(out, wrap(totals, 100)...)
	out = append(out, "", "## Pages", "")

	var rows [][]string
	for _, p := range m.Pages {
		mvp := "–"
		if n := m.countAll(p.Features, prioMVP); n > 0 {
			mvp = fmt.Sprintf("%d of %d", m.countSpecified(p.Features, prioMVP), n)
		}
		row := []string{
			fmt.Sprintf("[%s](#%s)", cell(p.Title), m.anchor(p)),
			"`" + list(m.prefixes(p), "`, `") + "`",
			num(len(p.Features)),
			mvp,
			num(m.countSpecified(p.Features)),
			num(m.countOpen(p.Features)),
		}
		if anyImpl {
			row = append(row, num(m.countImplemented(p.Features)))
		}
		rows = append(rows, row)
	}
	hdr := []string{"Catalog page", "Prefix", "Features", "MVP specified", "Specified", "Open"}
	align := []string{"---", "---", "---:", "---:", "---:", "---:"}
	if anyImpl {
		hdr = append(hdr, "Implemented")
		align = append(align, "---:")
	}
	out = append(out, maybeDetails(fmt.Sprintf("%d catalog pages", len(m.Pages)), table(hdr, rows, align), len(rows))...)

	for _, p := range m.Pages {
		out = append(out, "", "## "+m.sectionHeading(p), "")
		var parts []string
		for _, pr := range priorities() {
			if n := m.countAll(p.Features, pr); n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d of %d", pr, m.countSpecified(p.Features, pr), n))
			}
		}
		out = append(out,
			fmt.Sprintf("Catalog page: [%s](../features/%s) · specified by priority: %s", p.Rel, p.Rel, list(parts, " · ")), "")
		var frows [][]string
		for _, f := range p.Features {
			var links []string
			for _, r := range m.coveredBy[f.ID] {
				links = append(links, requirementLink(m, r))
			}
			covered := list(links, ", ")
			if covered == "" {
				covered = "–"
			}
			frows = append(frows, []string{f.ID, cell(f.Title), f.Priority, m.status(f), covered})
		}
		summary := fmt.Sprintf("%d features: %d specified, %d open", len(frows), m.countSpecified(p.Features), m.countOpen(p.Features))
		if anyImpl {
			summary += fmt.Sprintf(", %d implemented", m.countImplemented(p.Features))
		}
		out = append(out, maybeDetails(summary,
			table([]string{"ID", "Feature", "Priority", "Status", "Covered by"}, frows, nil), len(frows))...)
	}

	out = append(out, "", "## Requirements that cover no catalog feature", "",
		fmt.Sprintf("Requirements whose `Covers:` line says `%s`: trove's own design work that no", troveOnly),
		"catalog feature measures. They count toward the requirement total, not toward any share.", "")
	var orows [][]string
	for _, r := range only {
		id := r.ID
		if m.implemented[r.ID] {
			id += " (implemented)"
		}
		orows = append(orows, []string{id, cell(r.Title), r.Priority, fmt.Sprintf("[%s](../spec/%s)", r.Page, r.Page)})
	}
	out = append(out, maybeDetails(fmt.Sprintf("%d requirements", len(orows)),
		table([]string{"Requirement", "Title", "Priority", "Spec page"}, orows, nil), len(orows))...)
	out = append(out, "")
	return strings.Join(out, "\n")
}

func requirementLink(m *model, r requirement) string {
	s := fmt.Sprintf("[%s](../spec/%s)", r.ID, r.Page)
	if m.implemented[r.ID] {
		s += " (implemented)"
	}
	return s
}
