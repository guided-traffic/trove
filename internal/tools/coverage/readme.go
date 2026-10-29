package main

import (
	"fmt"
	"sort"
	"strings"
)

// audience is a group of catalog pages whose MVP and P1 entries are counted
// together. Page-level on purpose: the catalog carries no per-feature
// audience tags.
type audience struct {
	Name, Who string
	Pages     []string
}

func audiences() []audience {
	shared := []string{
		"formats/README.md", "repositories.md", "remote-repositories.md", "artifacts.md",
		"search-and-query.md", "builds-and-ci.md", "identity-and-access.md",
		"authentication-providers.md", "access-tokens.md", "oidc-ci-integration.md", "ui-inventory.md",
	}
	return []audience{
		{
			"npm-only team", "publishes and installs npm packages from workstations and GitLab CI",
			append([]string{"formats/npm.md"}, shared...),
		},
		{
			".NET team", "publishes and restores NuGet packages from workstations and GitLab CI",
			append([]string{"formats/nuget.md"}, shared...),
		},
		{"platform/ops team", "runs trove on Kubernetes and administers it", []string{
			"ha-and-deployment.md", "operations-and-monitoring.md", "storage-and-blobs.md",
			"retention-and-cleanup.md", "repositories.md", "remote-repositories.md",
			"identity-and-access.md", "authentication-providers.md", "access-tokens.md",
			"oidc-ci-integration.md", "api-admin-operations.md", "events-and-extensibility.md",
			"ui-inventory.md",
		}},
	}
}

func audiencePriorities() []string { return []string{prioMVP, prioP1} }

func (m *model) pageByRel() map[string]*page {
	out := map[string]*page{}
	for _, p := range m.Pages {
		out[p.Rel] = p
	}
	return out
}

func (m *model) checkAudiences() []string {
	var errs []string
	by := m.pageByRel()
	for _, a := range audiences() {
		for _, rel := range a.Pages {
			if _, ok := by[rel]; !ok {
				errs = append(errs, fmt.Sprintf("audience %q names catalog page %q, which has no entries", a.Name, rel))
			}
		}
	}
	return errs
}

func (m *model) onlyPriority(prio string) []feature {
	var out []feature
	for _, f := range m.Features {
		if f.Priority == prio {
			out = append(out, f)
		}
	}
	return out
}

func blockHeadline(m *model) []string {
	feats := m.Features
	mvp, p1 := m.onlyPriority(prioMVP), m.onlyPriority(prioP1)
	var scope []feature
	for _, f := range feats {
		if contains(inScopePriorities(), f.Priority) {
			scope = append(scope, f)
		}
	}
	only := m.troveOnlyReqs()
	rp := map[string]int{}
	pages := map[string]bool{}
	for _, r := range m.Reqs {
		rp[r.Priority]++
		pages[r.Page] = true
	}
	var rpText []string
	for _, p := range priorities() {
		if rp[p] > 0 {
			rpText = append(rpText, fmt.Sprintf("%d %s", rp[p], p))
		}
	}
	nImpl := m.countImplemented(feats)
	kind := "measured from passing tests that name requirements (Q-ROAD-4)"
	rows := [][]string{
		{"**Implemented**", fmt.Sprintf("**%s** (%d of %s catalog features)", pct(nImpl, len(feats)), nImpl, num(len(feats))), kind},
		{
			"**MVP features covered by spec requirements**",
			fmt.Sprintf("**%s** (%d of %d)", pct(m.countSpecified(mvp), len(mvp)), m.countSpecified(mvp), len(mvp)),
			"measured from `Covers:` lines",
		},
		{
			"P1 features covered by spec requirements",
			fmt.Sprintf("%s (%d of %d)", pct(m.countSpecified(p1), len(p1)), m.countSpecified(p1), len(p1)),
			"measured from `Covers:` lines",
		},
		{
			"In-scope features covered (MVP, P1, P2, Later)",
			fmt.Sprintf("%s (%s of %s)", pct(m.countSpecified(scope), len(scope)), num(m.countSpecified(scope)), num(len(scope))),
			"measured from `Covers:` lines",
		},
		{"Catalog features", fmt.Sprintf("%s on %d pages", num(len(feats)), len(m.Pages)), "measured from catalog headings"},
		{
			"Spec requirements", fmt.Sprintf("%s on %d pages (%s)", num(len(m.Reqs)), len(pages), list(rpText, ", ")),
			"measured from spec headings",
		},
		{
			"Requirements covering no catalog feature", fmt.Sprintf("%d (`%s`)", len(only), troveOnly),
			"measured from `Covers:` lines",
		},
	}
	return table([]string{"Number", "Value", "Kind"}, rows, nil)
}

func (m *model) pulledByMVP(feats []feature) int {
	n := 0
	for _, f := range feats {
		for _, r := range m.coveredBy[f.ID] {
			if r.Priority == prioMVP {
				n++
				break
			}
		}
	}
	return n
}

func blockPriority(m *model) []string {
	var rows [][]string
	row := func(label string, fs []feature) []string {
		s := m.countSpecified(fs)
		return []string{
			label, num(len(fs)), num(s), num(len(fs) - s), pct(s, len(fs)),
			num(m.pulledByMVP(fs)), num(m.countImplemented(fs)),
		}
	}
	for _, pr := range priorities() {
		rows = append(rows, row("**"+pr+"**", m.onlyPriority(pr)))
	}
	rows = append(rows, row("**Total**", m.Features))
	return table(
		[]string{"Priority", "Features", "Specified", "Open", "Specified share", "Covered by an MVP requirement", "Implemented"},
		rows, []string{"---", "---:", "---:", "---:", "---:", "---:", "---:"})
}

func pageRows(m *model, pages []*page) [][]string {
	var rows [][]string
	for _, p := range pages {
		row := []string{fmt.Sprintf("[%s](checklist.md#%s)", cell(p.Title), m.anchor(p)), num(len(p.Features))}
		for _, pr := range priorities() {
			row = append(row, num(m.countAll(p.Features, pr)))
		}
		row = append(row, num(m.countSpecified(p.Features)), num(m.countOpen(p.Features)))
		rows = append(rows, row)
	}
	return rows
}

func blockPages(m *model) []string {
	hdr := []string{"Catalog page", "Features", "MVP", "P1", "P2", "Later", "Out of scope", "Specified", "Open"}
	align := append([]string{"---"}, strings.Split(strings.Repeat("---:,", 8), ",")[:8]...)
	var general, formats []*page
	for _, p := range m.Pages {
		if strings.HasPrefix(p.Rel, "formats/") && p.Rel != "formats/README.md" {
			formats = append(formats, p)
		} else {
			general = append(general, p)
		}
	}
	out := table(hdr, pageRows(m, general), align)
	var fmtFeats []feature
	for _, p := range formats {
		fmtFeats = append(fmtFeats, p.Features...)
	}
	out = append(out, "",
		fmt.Sprintf("Per-format pages (%d pages, %s features, %s specified):", len(formats), num(len(fmtFeats)), num(m.countSpecified(fmtFeats))),
		"")
	return append(out, maybeDetails(fmt.Sprintf("%d format pages", len(formats)),
		table(hdr, pageRows(m, formats), align), len(formats))...)
}

func blockAudiences(m *model) []string {
	by := m.pageByRel()
	var rows, defs [][]string
	counted := map[string]bool{}
	for _, a := range audiences() {
		var feats []feature
		for _, rel := range a.Pages {
			feats = append(feats, by[rel].Features...)
			counted[rel] = true
		}
		mvp := m.count(feats, []string{prioMVP}, nil)
		both := m.count(feats, audiencePriorities(), nil)
		ms, bs := m.countSpecified(feats, prioMVP), m.countSpecified(feats, audiencePriorities()...)
		type open struct {
			n     int
			title string
		}
		var opens []open
		for _, rel := range a.Pages {
			opens = append(opens, open{m.countOpen(by[rel].Features, audiencePriorities()...), by[rel].Title})
		}
		sort.SliceStable(opens, func(i, j int) bool {
			if opens[i].n != opens[j].n {
				return opens[i].n > opens[j].n
			}
			return opens[i].title < opens[j].title
		})
		var top []string
		for _, o := range opens {
			if len(top) < 3 && o.n > 0 {
				top = append(top, fmt.Sprintf("%s %d", o.title, o.n))
			}
		}
		topText := list(top, "; ")
		if topText == "" {
			topText = "none"
		}
		rows = append(rows, []string{
			"**" + a.Name + "**",
			fmt.Sprintf("%d of %d specified, %d open (%s)", ms, mvp, mvp-ms, pct(ms, mvp)),
			fmt.Sprintf("%d of %d specified, %d open (%s)", bs, both, both-bs, pct(bs, both)),
			num(m.countImplemented(feats, audiencePriorities()...)),
			topText,
		})
		var links []string
		for _, rel := range a.Pages {
			links = append(links, fmt.Sprintf("[%s](../features/%s)", rel, rel))
		}
		defs = append(defs, []string{"**" + a.Name + "**", a.Who, list(links, ", ")})
	}
	out := table([]string{"Audience", "MVP entries", "MVP and P1 entries", "Implemented", "Most open entries (MVP and P1)"},
		rows, []string{"---", "---", "---", "---:", "---"})
	out = append(out, "", "Pages counted per audience:", "")
	out = append(out, table([]string{"Audience", "Who", "Catalog pages counted"}, defs, nil)...)
	var uncounted []*page
	for _, p := range m.Pages {
		if !counted[p.Rel] {
			uncounted = append(uncounted, p)
		}
	}
	unEntries, without := 0, 0
	var unLinks []string
	for _, p := range uncounted {
		n := m.countAll(p.Features, audiencePriorities()...)
		unEntries += n
		if n == 0 {
			without++
			continue
		}
		unLinks = append(unLinks, fmt.Sprintf("[%s](checklist.md#%s)", p.Title, m.anchor(p)))
	}
	out = append(out, "", fmt.Sprintf("Pages counted for no audience (%d, %d MVP and P1 entries): %s; plus %d pages without MVP or P1 entries.",
		len(uncounted), unEntries, list(unLinks, ", "), without))
	return out
}

type block struct {
	Name string
	Fn   func(*model) []string
}

func blocks() []block {
	return []block{{"headline", blockHeadline}, {"priority", blockPriority}, {"pages", blockPages}, {"audiences", blockAudiences}}
}

// renderReadme replaces the marked blocks of docs/coverage/README.md. Text
// outside the markers is never touched.
func renderReadme(m *model, text string) (string, error) {
	for _, b := range blocks() {
		begin := fmt.Sprintf("<!-- coverage-gen:begin %s -->", b.Name)
		end := fmt.Sprintf("<!-- coverage-gen:end %s -->", b.Name)
		if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 || strings.Index(text, begin) > strings.Index(text, end) {
			return "", fmt.Errorf("docs/coverage/README.md: marker pair for block %q missing or malformed", b.Name)
		}
		head, rest, _ := strings.Cut(text, begin)
		_, tail, _ := strings.Cut(rest, end)
		text = head + begin + "\n" + strings.Join(b.Fn(m), "\n") + "\n" + end + tail
	}
	return text, nil
}
