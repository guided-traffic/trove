package npmstub

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// ServedVersion is one version as the package's document lists it now: the answer of
// GET /-/stub/version. A client that lost the answer of a publish reads it to tell whether the
// publish took effect.
type ServedVersion struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
	Integrity    string            `json:"integrity"` // dist.integrity of the version's tarball
	Tags         []string          `json:"tags"`      // the dist-tags that point at the version, sorted
}

// Served returns a version as the document lists it now, and false when the document does not
// list it (an unknown name or version, a dropped version, a recorded package). Reading it is not
// a request to the registry and is not counted.
func (s *Server) Served(name, version string) (ServedVersion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.viewLocked(name)
	if !ok {
		return ServedVersion{}, false
	}
	i := p.Find(version)
	if i < 0 {
		return ServedVersion{}, false
	}
	v := p.Versions[i]
	out := ServedVersion{Name: p.Name, Version: v.Version, Dependencies: maps.Clone(v.Dependencies), Integrity: v.Dist.Integrity, Tags: []string{}}
	for tag, tv := range p.DistTags {
		if tv == version {
			out.Tags = append(out.Tags, tag)
		}
	}
	sort.Strings(out.Tags)
	return out, true
}

// Matches returns nil when the served version is what a publish of name, version, deps and tags
// makes: the same name and version, the same dependencies, the integrity of the tarball the stub
// builds for exactly these (corpus.Tarball), and every tag pointing at it (the stub may point
// `latest` at it as well). Otherwise the error names each difference.
func (v ServedVersion) Matches(name, version string, deps map[string]string, tags []string) error {
	var diffs []string
	if v.Name != name || v.Version != version {
		diffs = append(diffs, fmt.Sprintf("it is %s@%s, not %s@%s", v.Name, v.Version, name, version))
	}
	if !maps.Equal(v.Dependencies, deps) {
		diffs = append(diffs, fmt.Sprintf("dependencies %v, sent %v", v.Dependencies, deps))
	}
	if want := corpus.NewVersion(name, version, deps, time.Time{}).Dist.Integrity; v.Integrity != want {
		diffs = append(diffs, fmt.Sprintf("integrity %s, the tarball of the publish has %s", v.Integrity, want))
	}
	for _, tag := range tags {
		if !slices.Contains(v.Tags, tag) {
			diffs = append(diffs, fmt.Sprintf("dist-tag %s does not point at it (tags %v)", tag, v.Tags))
		}
	}
	if len(diffs) > 0 {
		return errors.New(strings.Join(diffs, "; "))
	}
	return nil
}
