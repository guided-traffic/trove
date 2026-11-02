package nugetstub

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dependency is one dependency of a package version: an ID and a NuGet version range.
type Dependency struct {
	ID    string `json:"id"`
	Range string `json:"range"`
}

// Version is one version of a package.
type Version struct {
	// Version is the normalized form (major.minor.patch with an optional prerelease label).
	Version      string
	Dependencies []Dependency
	Published    time.Time
	Listed       bool
	// Deprecation and Vulnerabilities are what the registration says of the version, in the
	// JSON-LD form nuget.org writes them: each with an @id in the upstream's catalog.
	Deprecation     *Deprecation
	Vulnerabilities []Vulnerability
}

// Deprecation is the deprecation of a version.
type Deprecation struct {
	Reasons []string
	Message string
	// AlternateID and AlternateRange name the package to use instead; empty for none.
	AlternateID, AlternateRange string
}

// Vulnerability is an advisory against a version: a web page for people and a severity from 0
// (low) to 3 (critical), as nuget.org writes it.
type Vulnerability struct {
	AdvisoryURL string
	Severity    int
}

// Advisory is the advisory the corpus records against NewtonsoftID 13.0.1, and the alternate
// package its deprecation names. LocalAdvisory is an advisory address that is no web link, which
// a remote drops (Q-NUGET-85); LegacyID's version carries both.
const (
	Advisory          = "https://github.com/advisories/GHSA-5crp-9r3c-p9vr"
	DeprecatedVersion = "13.0.1"
	LocalAdvisory     = "file://advisories.fabrikam.test/share/GHSA-0000-0000-0000"
)

// Package is one package ID with its versions in ascending order.
type Package struct {
	ID          string
	Description string
	Versions    []Version
}

// Lower is the lowercase ID, the form every URL of the protocol uses.
func (p *Package) Lower() string { return strings.ToLower(p.ID) }

// clone copies a package deeply enough to change its version list.
func (p *Package) clone() *Package {
	c := *p
	c.Versions = append([]Version(nil), p.Versions...)
	return &c
}

func (p *Package) find(version string) int {
	for i, v := range p.Versions {
		if v.Version == version {
			return i
		}
	}
	return -1
}

func (p *Package) insert(v Version) bool {
	if p.find(v.Version) >= 0 {
		return false
	}
	p.Versions = append(p.Versions, v)
	sort.SliceStable(p.Versions, func(i, j int) bool { return compareVersions(p.Versions[i].Version, p.Versions[j].Version) < 0 })
	return true
}

// Corpus is the package set the stub serves, keyed by lowercase ID.
type Corpus struct {
	packages map[string]*Package
	order    []string // lowercase IDs in insertion order
}

// Get returns a package by its ID in any case.
func (c *Corpus) Get(id string) (*Package, bool) {
	p, ok := c.packages[strings.ToLower(id)]
	return p, ok
}

// IDs lists the lowercase IDs in the order of the set.
func (c *Corpus) IDs() []string { return append([]string(nil), c.order...) }

// Len is the number of packages.
func (c *Corpus) Len() int { return len(c.order) }

func (c *Corpus) add(p *Package) {
	k := p.Lower()
	if _, ok := c.packages[k]; !ok {
		c.order = append(c.order, k)
	}
	c.packages[k] = p
}

// Names of the packages with a fixed meaning in tests and in the spec's criteria.
const (
	// ManyVersionsID has ManyVersions versions, so its registration index lists pages that are not inlined.
	ManyVersionsID = "Contoso.Versions"
	ManyVersions   = 200
	// NewtonsoftID has the versions 13.0.1 and 13.0.3.
	NewtonsoftID = "Newtonsoft.Json"
	// UtilID has the versions 2.0.0 and 2.1.0, a name a local repository claims in tests.
	UtilID = "Contoso.Util"
	// OtherID is the package a wrong-content mode serves in place of the requested one.
	OtherID = "Other.Pkg"
	// LegacyID has one version, deprecated with an alternate package and with two advisories, one
	// of them a file:// address, so its search result carries what a forwarded search cleans.
	LegacyID = "Fabrikam.Legacy"
)

var (
	areas    = []string{"Core", "Data", "Net", "Text", "Json", "Log", "Cache", "Auth", "Queue", "Mail"}
	suffixes = []string{"Abstractions", "Extensions", "Client", "Server", "Options", "Hosting", "Primitives", "Buffers", "Streams", "Codecs", "Pipelines", "Scheduling", "Metrics", "Tracing", "Retry", "Policy", "Mapping", "Validation"}

	epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Default returns the deterministic package set: 180 packages in a dependency graph (each depends
// on two or three packages that come before it in the order, so the graph has no cycle and every
// version of a package has the same dependencies), the ManyVersionsID package and four named
// packages. Every call builds the same set.
func Default() *Corpus {
	c := &Corpus{packages: map[string]*Package{}}
	var ids []string
	for _, a := range areas {
		for _, s := range suffixes {
			ids = append(ids, "Contoso."+a+"."+s)
		}
	}
	for i, id := range ids {
		p := &Package{ID: id, Description: "Synthetic package " + id + " of the test upstream"}
		var deps []Dependency
		if i > 0 {
			seen := map[int]bool{}
			for k := 0; k < 2+i%2; k++ {
				j := i - 1 - (i*7+k*13+i/3)%min(i, 9) // close to i, so chains are deep
				if seen[j] {
					continue
				}
				seen[j] = true
				deps = append(deps, Dependency{ID: ids[j], Range: "1.0.0"})
			}
			sort.Slice(deps, func(a, b int) bool { return deps[a].ID < deps[b].ID })
		}
		labels := []string{"1.0.0"}
		if i%2 == 0 {
			labels = append(labels, "1.1.0")
		}
		if i%3 == 0 {
			labels = append(labels, "2.0.0")
		}
		if i%5 == 0 {
			labels = append(labels, "2.0.0-beta.1")
		}
		for k, v := range labels {
			p.insert(Version{Version: v, Dependencies: deps, Published: epoch.Add(time.Duration(i*24+k) * time.Hour), Listed: true})
		}
		c.add(p)
	}

	many := &Package{ID: ManyVersionsID, Description: "Synthetic package with 200 versions"}
	for i := 0; i < ManyVersions; i++ {
		many.insert(Version{Version: fmt.Sprintf("1.%d.0", i), Published: epoch.Add(time.Duration(i) * time.Hour), Listed: true})
	}
	c.add(many)

	simple := func(id, desc string, versions ...string) {
		p := &Package{ID: id, Description: desc}
		for k, v := range versions {
			p.insert(Version{Version: v, Published: epoch.Add(time.Duration(k) * time.Hour), Listed: true})
		}
		c.add(p)
	}
	simple(NewtonsoftID, "Synthetic stand-in for a popular JSON package", DeprecatedVersion, "13.0.3")
	simple(UtilID, "Synthetic package for claim and pin tests", "2.0.0", "2.1.0")
	simple(OtherID, "Synthetic package served in place of another", "1.0.0")
	// The older version carries a deprecation and an advisory, as versions of popular packages on
	// nuget.org do, so the members a remote passes through are in every run (R-NUGET-015).
	nj := c.packages[strings.ToLower(NewtonsoftID)]
	nj.Versions[0].Deprecation = &Deprecation{
		Reasons: []string{"Legacy", "CriticalBugs"}, Message: "Use 13.0.3 or later.", AlternateID: NewtonsoftID, AlternateRange: "[13.0.3, )",
	}
	nj.Versions[0].Vulnerabilities = []Vulnerability{{AdvisoryURL: Advisory, Severity: 2}}
	// The latest version of LegacyID is deprecated, so a search result shows it (as nuget.org's
	// show the deprecation and the advisories of the version they name).
	simple(LegacyID, "Synthetic package whose only version is deprecated", "1.0.0")
	legacy := c.packages[strings.ToLower(LegacyID)]
	legacy.Versions[0].Deprecation = &Deprecation{
		Reasons: []string{"Legacy"}, Message: "Use Newtonsoft.Json.", AlternateID: NewtonsoftID, AlternateRange: "[13.0.3, )",
	}
	legacy.Versions[0].Vulnerabilities = []Vulnerability{{AdvisoryURL: Advisory, Severity: 3}, {AdvisoryURL: LocalAdvisory, Severity: 1}}
	return c
}

// Closure returns the lowercase IDs reachable from the roots through dependencies, roots
// included, ascending.
func (c *Corpus) Closure(roots ...string) []string {
	seen := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		k := strings.ToLower(id)
		p, ok := c.packages[k]
		if seen[k] || !ok {
			return
		}
		seen[k] = true
		for _, d := range p.Versions[0].Dependencies {
			walk(d.ID)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReferenceMinPackages is the size of the restore graph the reference solution reaches
// (criterion E3.2 requires at least 100; the margin keeps it above 100 if the set changes).
const ReferenceMinPackages = 120

// ReferenceRoots picks the root packages of the reference solution: generated packages from the
// end of the order at a fixed stride, until there are at least referenceMinRoots of them and their
// closure has at least min IDs. The choice is deterministic. The result is sorted.
func (c *Corpus) ReferenceRoots(min int) []string {
	var generated []string
	for _, k := range c.order {
		if strings.HasPrefix(k, "contoso.") && k != strings.ToLower(ManyVersionsID) && k != strings.ToLower(UtilID) {
			generated = append(generated, k)
		}
	}
	var roots []string
	have := map[string]bool{}
	for i := len(generated) - 1; i >= 0 && (len(roots) < referenceMinRoots || len(have) < min); i -= referenceStride {
		roots = append(roots, c.packages[generated[i]].ID)
		for _, id := range c.Closure(generated[i]) {
			have[id] = true
		}
	}
	sort.Strings(roots)
	return roots
}

// referenceStride and referenceMinRoots shape the reference solution's root list: roots are taken
// from the end of the generated order at a fixed stride, so the project has several direct
// references that share transitive dependencies, as a real project does.
const (
	referenceStride   = 17
	referenceMinRoots = 8
)

// LatestStable is the highest version of a package without a prerelease label.
func (p *Package) LatestStable() string {
	for i := len(p.Versions) - 1; i >= 0; i-- {
		if !isPrerelease(p.Versions[i].Version) {
			return p.Versions[i].Version
		}
	}
	return p.Versions[len(p.Versions)-1].Version
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9a-z-]+(\.[0-9a-z-]+)*)?$`)

// validVersion reports whether v is a normalized three-part version with an optional lowercase
// prerelease label, the only form the stub offers.
func validVersion(v string) bool { return versionPattern.MatchString(v) }

func isPrerelease(v string) bool { return strings.Contains(v, "-") }

// compareVersions orders normalized versions as SemVer does: numerically by part, a release above
// its prereleases, prerelease labels identifier by identifier.
func compareVersions(a, b string) int {
	an, apre, _ := strings.Cut(a, "-")
	bn, bpre, _ := strings.Cut(b, "-")
	ap, bp := strings.Split(an, "."), strings.Split(bn, ".")
	for i := 0; i < 3 && i < len(ap) && i < len(bp); i++ {
		x, _ := strconv.Atoi(ap[i])
		y, _ := strconv.Atoi(bp[i])
		if x != y {
			return cmpInt(x, y)
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	as, bs := strings.Split(apre, "."), strings.Split(bpre, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, xerr := strconv.Atoi(as[i])
		y, yerr := strconv.Atoi(bs[i])
		switch {
		case xerr == nil && yerr == nil && x != y:
			return cmpInt(x, y)
		case xerr == nil && yerr != nil:
			return -1
		case xerr != nil && yerr == nil:
			return 1
		case xerr != nil && yerr != nil && as[i] != bs[i]:
			return strings.Compare(as[i], bs[i])
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
