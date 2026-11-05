package nugetstub

import (
	"errors"
	"fmt"
	"maps"
	"strings"
)

// ServedVersion is one version as the documents of the stub list it now: the answer of
// GET /-/stub/version. A client that lost the answer of a publish reads it to tell whether the
// publish took effect.
type ServedVersion struct {
	Name            string            `json:"name"` // the package ID as the stub has it
	Version         string            `json:"version"`
	Dependencies    map[string]string `json:"dependencies"` // dependency ID to range
	Listed          bool              `json:"listed"`
	Deprecated      bool              `json:"deprecated"`
	Vulnerabilities int               `json:"vulnerabilities"` // advisories the registration names for it
}

// Served returns a version as the documents list it now, and false when they do not list it (an
// unknown ID or version, a dropped version). Reading it is not a request to the feed and is not
// counted.
func (s *Server) Served(id, version string) (ServedVersion, bool) {
	p, _, ok := s.view(id)
	if !ok {
		return ServedVersion{}, false
	}
	i := p.find(version)
	if i < 0 {
		return ServedVersion{}, false
	}
	v := p.Versions[i]
	deps := make(map[string]string, len(v.Dependencies))
	for _, d := range v.Dependencies {
		deps[d.ID] = d.Range
	}
	return ServedVersion{
		Name: p.ID, Version: v.Version, Dependencies: deps, Listed: v.Listed,
		Deprecated: v.Deprecation != nil, Vulnerabilities: len(v.Vulnerabilities),
	}, true
}

// Matches returns nil when the served version is what a publish of id, version and deps makes:
// the same ID (in the same case) and version, the same dependencies, listed, without a
// deprecation or an advisory. Otherwise the error names each difference.
func (v ServedVersion) Matches(id, version string, deps map[string]string) error {
	var diffs []string
	if v.Name != id || v.Version != version {
		diffs = append(diffs, fmt.Sprintf("it is %s %s, not %s %s", v.Name, v.Version, id, version))
	}
	if !maps.Equal(v.Dependencies, deps) {
		diffs = append(diffs, fmt.Sprintf("dependencies %v, sent %v", v.Dependencies, deps))
	}
	if !v.Listed {
		diffs = append(diffs, "it is unlisted")
	}
	if v.Deprecated || v.Vulnerabilities > 0 {
		diffs = append(diffs, fmt.Sprintf("it is deprecated (%v) or names %d advisories, a publish makes neither", v.Deprecated, v.Vulnerabilities))
	}
	if len(diffs) > 0 {
		return errors.New(strings.Join(diffs, "; "))
	}
	return nil
}
