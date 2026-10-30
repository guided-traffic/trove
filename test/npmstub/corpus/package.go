// Package corpus is the synthetic package set of the counting test upstream (test/npmstub): a
// deterministic registry of several hundred packages with a dependency graph, scopes, prereleases
// and dist-tags, whose documents and tarballs are byte-for-byte reproducible. Nothing in it reaches
// the network, and no package has an install script.
package corpus

import (
	"slices"
	"strings"
	"time"
)

// Dist is the integrity part of a version: what a client verifies a tarball against.
type Dist struct {
	Integrity    string // "sha512-" and the base64 digest
	Shasum       string // hex SHA-1, for clients that predate integrity
	FileCount    int
	UnpackedSize int
}

// Version is one published version of a package.
type Version struct {
	Version      string
	Dependencies map[string]string
	Time         time.Time
	Deprecated   string
	Dist         Dist
}

// Package is a package and everything the registry says about it.
type Package struct {
	Name        string
	Description string
	Created     time.Time
	Modified    time.Time
	Versions    []Version // ascending by SemVer
	DistTags    map[string]string
}

// Clone returns a copy that can be changed without touching the original; the dependency maps of
// the versions are shared, because nothing changes them.
func (p *Package) Clone() *Package {
	c := *p
	c.Versions = slices.Clone(p.Versions)
	c.DistTags = make(map[string]string, len(p.DistTags))
	for k, v := range p.DistTags {
		c.DistTags[k] = v
	}
	return &c
}

// Find returns the index of a version, or -1.
func (p *Package) Find(version string) int {
	for i := range p.Versions {
		if p.Versions[i].Version == version {
			return i
		}
	}
	return -1
}

// Insert adds a version at its place in the SemVer order and returns false if the number exists.
func (p *Package) Insert(v Version) bool {
	if p.Find(v.Version) >= 0 {
		return false
	}
	at, _ := slices.BinarySearchFunc(p.Versions, v.Version, func(e Version, target string) int {
		return CompareVersions(e.Version, target)
	})
	p.Versions = slices.Insert(p.Versions, at, v)
	if v.Time.After(p.Modified) {
		p.Modified = v.Time
	}
	return true
}

// Remove deletes a version and the dist-tags that pointed to it, and reports whether it existed.
func (p *Package) Remove(version string) bool {
	i := p.Find(version)
	if i < 0 {
		return false
	}
	p.Versions = slices.Delete(p.Versions, i, i+1)
	for tag, v := range p.DistTags {
		if v == version {
			delete(p.DistTags, tag)
		}
	}
	return true
}

// Retag points `latest` at the highest stable version (or the highest version when there is no
// stable one) when it has no tag, and `next` at a prerelease above latest.
func (p *Package) Retag() {
	if _, ok := p.DistTags["latest"]; !ok {
		best := ""
		for _, v := range p.Versions {
			if IsPrerelease(v.Version) {
				continue
			}
			if best == "" || CompareVersions(v.Version, best) > 0 {
				best = v.Version
			}
		}
		if best == "" && len(p.Versions) > 0 {
			best = p.Versions[len(p.Versions)-1].Version
		}
		if best != "" {
			p.DistTags["latest"] = best
		}
	}
}

// Unscoped returns the part of the name after the scope.
func Unscoped(name string) string {
	if strings.HasPrefix(name, "@") {
		if _, rest, ok := strings.Cut(name, "/"); ok {
			return rest
		}
	}
	return name
}

// TarballFile is the file name a registry gives a version's tarball: "<unscoped>-<version>.tgz".
func TarballFile(name, version string) string {
	return Unscoped(name) + "-" + version + ".tgz"
}

// VersionOfFile extracts the version from a tarball file name of the package, and reports whether
// the file name has that form.
func VersionOfFile(name, file string) (string, bool) {
	prefix := Unscoped(name) + "-"
	v, ok := strings.CutPrefix(file, prefix)
	if !ok {
		return "", false
	}
	v, ok = strings.CutSuffix(v, ".tgz")
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
