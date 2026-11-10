package ansiblestub

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub/ansiblegen"
)

// The corpus: collections and roles that the stub serves. Archives are generated on first use and kept; a version document needs
// the digest of its archive, so the first request for a version builds it.

// versionDef is one collection version.
type versionDef struct {
	version         string
	requiresAnsible string
	deps            map[string]string
	signed          bool
	// seq orders the creation times: version n was created n days after the start of the corpus.
	seq int
}

// collectionDef is one collection with its versions in ascending semantic-version order. A value is never changed after the
// server holds it; AddVersion and DropVersion replace it.
type collectionDef struct {
	ns, name string
	versions []*versionDef
}

func (c *collectionDef) fullName() string { return c.ns + "." + c.name }

func (c *collectionDef) find(version string) *versionDef {
	for _, v := range c.versions {
		if v.version == version {
			return v
		}
	}
	return nil
}

// highest is the newest stable version, or the newest one when every version is a pre-release.
func (c *collectionDef) highest() *versionDef {
	var best *versionDef
	for _, v := range c.versions {
		if sv, err := parseSemver(v.version); err == nil && len(sv.pre) > 0 {
			continue
		}
		best = v // ascending order
	}
	if best == nil && len(c.versions) > 0 {
		best = c.versions[len(c.versions)-1]
	}
	return best
}

func (c *collectionDef) clone() *collectionDef {
	n := *c
	n.versions = append([]*versionDef(nil), c.versions...)
	return &n
}

// roleVersion is a tag of a role; Host is the name of the host its download_url points to (a Config host name field: "github" or
// "unlisted").
type roleVersion struct {
	name string
	site string
}

// roleDef is one role of the v1 API.
type roleDef struct {
	id                int
	owner, name       string
	user, repo        string
	branch, summaryTx string
	versions          []roleVersion
}

func (r *roleDef) find(tag string) (roleVersion, bool) {
	for _, v := range r.versions {
		if v.name == tag {
			return v, true
		}
	}
	return roleVersion{}, false
}

// corpusStart is the creation time of version 0.
var corpusStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (v *versionDef) created() time.Time { return corpusStart.AddDate(0, 0, v.seq) }

// defaultCorpus builds the collections and roles of the stub. fillers adds that many one-version collections "filler.c0000" and on,
// to make the index longer than a page.
func defaultCorpus(fillers int) ([]*collectionDef, []*roleDef) {
	mk := func(ns, name string, versions ...*versionDef) *collectionDef {
		for i, v := range versions {
			v.seq = i
			if v.requiresAnsible == "" {
				v.requiresAnsible = ">=2.14.0"
			}
		}
		c := &collectionDef{ns: ns, name: name, versions: versions}
		sort.SliceStable(c.versions, func(i, j int) bool { return compareVersions(c.versions[i].version, c.versions[j].version) < 0 })
		return c
	}
	ver := func(v string) *versionDef { return &versionDef{version: v} }

	many := make([]*versionDef, 0, 250)
	for i := range 250 {
		many = append(many, ver(fmt.Sprintf("1.%d.0", i)))
	}
	cols := []*collectionDef{
		mk("acme", "base", ver("1.0.0"), ver("1.2.0")),
		mk("acme", "tools", ver("1.0.0"), ver("1.1.0"),
			&versionDef{version: "2.0.0", requiresAnsible: ">=2.15.0", deps: map[string]string{"acme.base": ">=1.0.0"}},
			ver("2.1.0-rc.1")),
		mk("acme", "signed", &versionDef{version: "1.0.0", signed: true}),
		mk("other", "stuff", ver("1.0.0")),
		mk("many", "versions", many...),
	}
	for i := range fillers {
		cols = append(cols, mk("filler", fmt.Sprintf("c%04d", i), ver("1.0.0")))
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].fullName() < cols[j].fullName() })

	roleVersions := func(site string, names ...string) []roleVersion {
		out := make([]roleVersion, 0, len(names))
		for _, n := range names {
			out = append(out, roleVersion{name: n, site: site})
		}
		return out
	}
	manyTags := make([]string, 0, 60)
	for i := range 60 {
		manyTags = append(manyTags, fmt.Sprintf("1.%d.0", i))
	}
	roles := []*roleDef{
		{
			id: 16, owner: "geerlingguy", name: "pip", user: "geerlingguy", repo: "ansible-role-pip", branch: "master",
			summaryTx: "Pip (Python package manager) for Linux.", versions: roleVersions(siteGitHub, "1.0.0", "1.1.0", "2.0.0"),
		},
		// 0.9.0 points to a host that is no GitHub host: a remote must drop that version from its list.
		{
			id: 17, owner: "acme", name: "rolex", user: "acme", repo: "ansible-role-rolex", branch: "main",
			summaryTx: "A role with a version on an unlisted host.", versions: append(roleVersions(siteUnlisted, "0.9.0"), roleVersions(siteGitHub, "1.0.0")...),
		},
		{
			id: 18, owner: "many", name: "tags", user: "many", repo: "ansible-role-tags", branch: "main",
			summaryTx: "A role with 60 tags (two pages of versions).", versions: roleVersions(siteGitHub, manyTags...),
		},
	}
	return cols, roles
}

// archiveKey names a generated archive.
type archiveKey struct {
	ns, name, version string
	variant           bool
}

// archiveCache keeps the generated archives; a version's two archives (the original and the variant of the digest modes) are
// different files.
type archiveCache struct {
	mu    sync.Mutex
	items map[archiveKey]ansiblegen.Archive
	roles map[string][]byte
}

func (c *archiveCache) collection(def *collectionDef, v *versionDef, variant bool) (ansiblegen.Archive, error) {
	key := archiveKey{def.ns, def.name, v.version, variant}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.items[key]; ok {
		return a, nil
	}
	spec := ansiblegen.Spec{Namespace: def.ns, Name: def.name, Version: v.version, Dependencies: v.deps, RequiresAnsible: v.requiresAnsible}
	if variant {
		spec.Extra = map[string]string{"variant.txt": "the second archive of this version\n"}
	}
	a, err := ansiblegen.Collection(spec)
	if err != nil {
		return ansiblegen.Archive{}, err
	}
	if c.items == nil {
		c.items = map[archiveKey]ansiblegen.Archive{}
	}
	c.items[key] = a
	return a, nil
}

func (c *archiveCache) role(user, repo, tag string) ([]byte, error) {
	key := user + "/" + repo + "@" + tag
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.roles[key]; ok {
		return b, nil
	}
	b, err := ansiblegen.Role(ansiblegen.RoleSpec{User: user, Repo: repo, Tag: tag})
	if err != nil {
		return nil, err
	}
	if c.roles == nil {
		c.roles = map[string][]byte{}
	}
	c.roles[key] = b
	return b, nil
}
