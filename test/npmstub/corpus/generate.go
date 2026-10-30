package corpus

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// Config selects a corpus. The same Config always generates the same corpus.
type Config struct {
	Seed     uint64
	Packages int
}

// DefaultConfig is the corpus the lockfile in perf/npm is resolved from: change it and
// `make npm-reference` has to be run again.
func DefaultConfig() Config { return Config{Seed: 20261030, Packages: 700} }

// Corpus is a set of packages, in the order they were generated; a package depends only on
// packages before it, so the dependency graph is acyclic.
type Corpus struct {
	Packages []*Package
	byName   map[string]*Package
}

var (
	defaultOnce   sync.Once
	defaultCorpus *Corpus
)

// Default returns the corpus of DefaultConfig, generated once per process. Callers must not
// change it; Package.Clone gives a copy to change.
func Default() *Corpus {
	defaultOnce.Do(func() { defaultCorpus = New(DefaultConfig()) })
	return defaultCorpus
}

// Get returns a package by name.
func (c *Corpus) Get(name string) (*Package, bool) {
	p, ok := c.byName[name]
	return p, ok
}

// Names returns the package names in sorted order.
func (c *Corpus) Names() []string {
	names := make([]string, 0, len(c.Packages))
	for _, p := range c.Packages {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

var adjectives = []string{
	"amber", "azure", "brisk", "calm", "crisp", "dusky", "eager", "faint", "gentle", "hazel",
	"ivory", "jolly", "keen", "lucid", "mellow", "nimble", "opal", "plain", "quiet", "rapid",
	"sable", "tidy", "umber", "vivid", "warm", "xenial", "young", "zesty", "bold", "clear",
	"deep", "early", "fair", "grand", "hushed", "inner", "just", "kind", "light", "mild",
}

var nouns = []string{
	"anchor", "beacon", "cable", "dial", "ember", "fjord", "gauge", "harbor", "index", "joint",
	"kernel", "lantern", "mesa", "nozzle", "orbit", "pylon", "quill", "ridge", "signal", "trellis",
	"upland", "valve", "wharf", "yarn", "zenith", "bridge", "canal", "delta", "engine", "forge",
	"grove", "hinge", "inlet", "jetty", "knoll", "ledger", "mortar", "notch", "outpost", "relay",
}

var scopes = []string{"lattice", "quartz", "cobalt", "tundra", "meridian", "harvest", "pillar", "sundial"}

const (
	baseTier     = 45  // packages below this index are leaves or nearly so, and the hubs others depend on
	midTier      = 300 // up to here packages have a few dependencies, above it many
	heavyVersion = 1000
)

// heavy packages carry a thousand versions each (the sized documents of the performance tests).
var heavy = map[int]bool{7: true, 23: true}

// New generates a corpus.
func New(cfg Config) *Corpus {
	names := packageNames(cfg)
	c := &Corpus{byName: make(map[string]*Package, cfg.Packages)}
	epoch := time.Date(2019, 1, 7, 9, 30, 0, 0, time.UTC)
	for i, name := range names {
		p := generate(cfg.Seed, i, name, epoch, c)
		c.Packages = append(c.Packages, p)
		c.byName[name] = p
	}
	return c
}

// packageNames picks cfg.Packages distinct names from the word lists in a seeded order; about a
// quarter are scoped.
func packageNames(cfg Config) []string {
	r := newRNG(cfg.Seed)
	pairs := len(adjectives) * len(nouns)
	order := make([]int, pairs)
	for i := range order {
		order[i] = i
	}
	for i := pairs - 1; i > 0; i-- {
		j := r.intn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	if cfg.Packages > pairs {
		panic(fmt.Sprintf("corpus: %d packages requested, the word lists give %d names", cfg.Packages, pairs))
	}
	names := make([]string, 0, cfg.Packages)
	for _, k := range order[:cfg.Packages] {
		base := adjectives[k/len(nouns)] + "-" + nouns[k%len(nouns)]
		if r.chance(25) {
			base = "@" + scopes[r.intn(len(scopes))] + "/" + base
		}
		names = append(names, base)
	}
	return names
}

func generate(seed uint64, i int, name string, epoch time.Time, c *Corpus) *Package {
	r := newRNG(mix(seed, i))
	isHeavy := heavy[i]

	count := 3 + r.intn(8)
	switch {
	case isHeavy:
		count = heavyVersion
	case i >= midTier:
		count = 1 + r.intn(6)
	case i >= baseTier:
		count = 2 + r.intn(7)
	}
	versions := versionNumbers(r, count, isHeavy)

	deps := pickDependencies(r, i, c)
	kinds := make([]int, len(deps))
	for k := range kinds {
		kinds[k] = r.intn(10)
	}

	p := &Package{
		Name:        name,
		Description: "Synthetic package " + name + " of the trove test corpus",
		Created:     epoch.Add(time.Duration(i) * 30 * time.Hour),
		DistTags:    map[string]string{},
	}
	at := p.Created
	last := len(versions) - 1
	for j, ver := range versions {
		step := 24*time.Hour + time.Duration(r.intn(20*24*60))*time.Minute
		if isHeavy {
			step = 2*time.Hour + time.Duration(r.intn(600))*time.Minute
		}
		at = at.Add(step)
		v := NewVersion(name, ver, dependenciesAt(deps, kinds, j, len(versions), c), at)
		if j < last && j >= 2 && !IsPrerelease(ver) && r.chance(2) {
			v.Deprecated = "This version has a known defect; use a newer release."
		}
		p.Versions = append(p.Versions, v)
	}
	p.Modified = at
	tagPackage(r, p)
	return p
}

// versionNumbers returns count ascending versions: patch, minor and major steps, with prereleases
// ahead of some majors, and a pending prerelease after the last release of some packages.
func versionNumbers(r *rng, count int, many bool) []string {
	major, minor, patch := 1, 0, 0
	if r.chance(20) {
		major, minor = 0, 1
	}
	out := []string{fmt.Sprintf("%d.%d.%d", major, minor, patch)}
	patchBelow, minorBelow := 68, 92
	if many {
		patchBelow, minorBelow = 90, 99
	}
	for len(out) < count {
		roll := r.intn(100)
		switch {
		case roll < patchBelow:
			patch++
		case roll < minorBelow:
			minor, patch = minor+1, 0
		default:
			major, minor, patch = major+1, 0, 0
			if !many && r.chance(45) {
				out = append(out, fmt.Sprintf("%d.0.0-beta.1", major))
				if r.chance(50) {
					out = append(out, fmt.Sprintf("%d.0.0-beta.2", major))
				}
				if r.chance(50) {
					out = append(out, fmt.Sprintf("%d.0.0-rc.1", major))
				}
			}
		}
		out = append(out, fmt.Sprintf("%d.%d.%d", major, minor, patch))
	}
	if !many && r.chance(15) {
		// A prerelease of the next major above the last release, which the `next` tag points to.
		out = append(out, fmt.Sprintf("%d.0.0-beta.1", major+1))
	}
	return out
}

// pickDependencies chooses the names package i depends on: leaves have none, the middle few, the
// top many; half of the choices are hubs among the first packages.
func pickDependencies(r *rng, i int, c *Corpus) []string {
	if i == 0 {
		return nil
	}
	var want int
	switch {
	case i < baseTier:
		want = max(0, r.intn(3)-1) // none for two thirds of the leaves, else one
	case i < midTier:
		want = r.intn(5)
	default:
		want = 2 + r.intn(8)
	}
	picked := map[int]bool{}
	for k := 0; k < want; k++ {
		var t int
		if r.chance(50) {
			limit := min(i, baseTier)
			t = min(r.intn(limit), r.intn(limit))
		} else {
			t = r.intn(i)
		}
		picked[t] = true
	}
	names := make([]string, 0, len(picked))
	for t := range picked {
		names = append(names, c.Packages[t].Name)
	}
	slices.Sort(names)
	return names
}

// dependenciesAt returns the dependencies of version j of n: early versions have fewer, the last
// has all, and each range names a release of the dependency that exists.
func dependenciesAt(deps []string, kinds []int, j, n int, c *Corpus) map[string]string {
	if len(deps) == 0 {
		return nil
	}
	use := 1 + (len(deps)-1)*(j+1)/n
	out := make(map[string]string, use)
	for k := 0; k < use; k++ {
		out[deps[k]] = rangeOver(c.byName[deps[k]], kinds[k], j, n)
	}
	return out
}

// rangeOver builds a range that a release of dep satisfies: later versions of the dependent ask for
// later releases of the dependency.
func rangeOver(dep *Package, kind, j, n int) string {
	var stable []string
	for _, v := range dep.Versions {
		if !IsPrerelease(v.Version) {
			stable = append(stable, v.Version)
		}
	}
	sv := stable[(len(stable)-1)*(j+1)/n]
	pv, _ := parseVersion(sv)
	switch kind {
	case 0, 1, 2, 3, 4, 5:
		return "^" + sv
	case 6, 7:
		return "~" + sv
	case 8:
		return fmt.Sprintf(">=%d.%d.0 <%d.0.0", pv.major, pv.minor, pv.major+1)
	default:
		return sv
	}
}

// tagPackage sets latest (the highest release), next (a prerelease above it) and, for some packages
// with several majors, legacy (the last release of the major before latest's).
func tagPackage(r *rng, p *Package) {
	p.Retag()
	latest := p.DistTags["latest"]
	last := p.Versions[len(p.Versions)-1].Version
	if CompareVersions(last, latest) > 0 {
		p.DistTags["next"] = last
	}
	lp, _ := parseVersion(latest)
	if lp.major > 1 && r.chance(30) {
		best := ""
		for _, v := range p.Versions {
			vp, _ := parseVersion(v.Version)
			if vp.major == lp.major-1 && !IsPrerelease(v.Version) {
				best = v.Version
			}
		}
		if best != "" {
			p.DistTags["legacy"] = best
		}
	}
}

// ReferenceMinNames is the least number of packages the reference project's tree has: the exit
// criterion E2.2 asks for 500, and the margin covers a version choice that drops a few.
const ReferenceMinNames = 540

// Reference is the project the performance lockfile is resolved for.
type Reference struct {
	Name         string
	Version      string
	Dependencies map[string]string
}

// Reference chooses root dependencies whose transitive closure, by package name, has at least
// minNames packages: the lockfile of the project then has at least that many entries. The choice is
// greedy by the number of packages a root adds, ties broken by name, so it is deterministic.
func (c *Corpus) Reference(minNames int) Reference {
	closures := make(map[string]map[string]bool, len(c.Packages))
	for _, p := range c.Packages {
		cl := map[string]bool{p.Name: true}
		latest := p.Versions[p.Find(p.DistTags["latest"])].Dependencies
		for dep := range latest {
			for n := range closures[dep] {
				cl[n] = true
			}
		}
		closures[p.Name] = cl
	}
	covered := map[string]bool{}
	roots := map[string]string{}
	for len(covered) < minNames {
		bestName, bestGain := "", 0
		for _, name := range c.Names() {
			if _, taken := roots[name]; taken {
				continue
			}
			gain := 0
			for n := range closures[name] {
				if !covered[n] {
					gain++
				}
			}
			if gain > bestGain {
				bestName, bestGain = name, gain
			}
		}
		if bestName == "" {
			panic(fmt.Sprintf("corpus: the packages cannot reach a closure of %d names", minNames))
		}
		for n := range closures[bestName] {
			covered[n] = true
		}
		p := c.byName[bestName]
		roots[bestName] = "^" + p.DistTags["latest"]
	}
	return Reference{Name: "trove-npm-reference", Version: "1.0.0", Dependencies: roots}
}

// PackageJSON renders the project's package.json: private, with the root dependencies and nothing
// that runs at install time.
func (ref Reference) PackageJSON() []byte {
	out, err := json.MarshalIndent(struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Private      bool              `json:"private"`
		Description  string            `json:"description"`
		License      string            `json:"license"`
		Dependencies map[string]string `json:"dependencies"`
	}{ref.Name, ref.Version, true, "Reference project of the trove performance and e2e tests: resolved only from the synthetic corpus.", "MIT", ref.Dependencies}, "", "  ")
	if err != nil {
		panic(err) // strings and a map of strings
	}
	return append(out, '\n')
}

// String lists the corpus size, for logs.
func (c *Corpus) String() string {
	versions := 0
	for _, p := range c.Packages {
		versions += len(p.Versions)
	}
	return fmt.Sprintf("%d packages, %d versions", len(c.Packages), versions)
}
