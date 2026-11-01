// Package nugetstub is the counting test upstream for trove's NuGet repositories: a stub NuGet v3
// server that serves a deterministic, synthetic package set, counts every request per path and host,
// and fails on command. Packages are generated in code (valid .nupkg archives with a .nuspec), so
// nothing is recorded or downloaded. It answers over TLS only; there is no plain-HTTP listener.
//
// The service index spreads its resources over two hosts, as nuget.org does: the index and the
// registrations live on the API host, the flat container (version lists, .nuspec, .nupkg) and the
// search on the content host. The hosts are configured (Config.PublicURL, Config.ContentURL) or
// derived from the request (see Server.bases). The registrations come in a plain and a gzip hive,
// and a package with 200 versions has registration pages that are not inlined.
//
// Use it in a Go test through Start, or as a container (cmd/nugetstub, Containerfile). A test reads
// what the upstream saw through Counts and drives the failure cases through SetMode, Publish, Drop
// and SetListed, or over the control API (/-/stub/) when the stub runs in a container.
package nugetstub

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config configures a Server. The zero value serves the default corpus, derives the hosts of its
// documents from the Host header and has no control API.
type Config struct {
	// Corpus is the package set; nil means Default().
	Corpus *Corpus
	// PublicURL is the base of the API host in the service index and the registrations, for
	// example "https://nugetstub.ns.svc". Empty: derived from the request.
	PublicURL string
	// ContentURL is the base of the content host (flat container and search). Empty: the
	// PublicURL, or the derived name (see Server.bases).
	ContentURL string
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token.
	// Empty: the API does not exist (the in-process Go methods are always available).
	ControlToken string
	// Now is the clock for publish times; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Server is the stub server. It is safe for concurrent use.
type Server struct {
	cfg Config

	mu      sync.Mutex
	counts  map[countKey]*Entry
	mode    Mode
	overlay map[string]*Package // packages changed at runtime, by lowercase ID, as clones
	dropped map[string]map[string]bool
	touched map[string]time.Time // when a package last changed
	built   map[string]*builtPackage
}

type builtPackage struct {
	nupkg []byte
	hash  string
}

// New creates a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Corpus == nil {
		cfg.Corpus = Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	for _, u := range []string{cfg.PublicURL, cfg.ContentURL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return nil, fmt.Errorf("%q is no absolute http or https URL", u)
		}
	}
	s := &Server{cfg: cfg, built: map[string]*builtPackage{}}
	s.resetLocked()
	return s, nil
}

// resetLocked clears every piece of state; the caller holds s.mu or owns s exclusively.
func (s *Server) resetLocked() {
	s.counts = map[countKey]*Entry{}
	s.mode = Mode{}
	s.overlay = map[string]*Package{}
	s.dropped = map[string]map[string]bool{}
	s.touched = map[string]time.Time{}
}

// Counts returns a snapshot of the request counters.
func (s *Server) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Counts{Entries: make([]Entry, 0, len(s.counts))}
	for _, e := range s.counts {
		out.Entries = append(out.Entries, *e)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Host != b.Host:
			return a.Host < b.Host
		case a.Method != b.Method:
			return a.Method < b.Method
		case a.Conditional != b.Conditional:
			return !a.Conditional
		}
		return a.Auth < b.Auth
	})
	return out
}

// Reset zeroes the request counters; modes and changes to packages stay.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = map[countKey]*Entry{}
}

// ResetAll returns the stub to its initial state: counters zero, healthy, no change to the corpus.
func (s *Server) ResetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

// SetMode replaces the failure switches.
func (s *Server) SetMode(m Mode) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
	return nil
}

// Mode returns the current failure switches.
func (s *Server) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// ErrExists reports a Publish of a version that exists.
var ErrExists = errors.New("version exists")

// ErrUnknown reports a change to a package or version the stub does not have.
var ErrUnknown = errors.New("no such package or version")

// Publish adds a version at runtime, creating the package when the stub does not know it: the way
// a test offers a version that was not there when a client cached the version list. deps maps the
// dependency ID to its range. The version must be in normalized form (major.minor.patch with an
// optional lowercase prerelease label).
func (s *Server) Publish(id, version string, deps map[string]string) error {
	if id == "" || strings.ContainsAny(id, "/\\ ") {
		return fmt.Errorf("package ID %q: invalid", id)
	}
	if !validVersion(version) {
		return fmt.Errorf("version %q: not a normalized version", version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.writableLocked(id)
	if p == nil {
		p = &Package{ID: id, Description: "Package " + id + " published to the test upstream"}
	}
	now := s.cfg.Now().UTC()
	v := Version{Version: version, Published: now, Listed: true}
	for depID, rng := range deps {
		v.Dependencies = append(v.Dependencies, Dependency{ID: depID, Range: rng})
	}
	sort.Slice(v.Dependencies, func(i, j int) bool { return v.Dependencies[i].ID < v.Dependencies[j].ID })
	if !p.insert(v) {
		return fmt.Errorf("%s %s: %w", id, version, ErrExists)
	}
	s.overlay[p.Lower()] = p
	delete(s.dropped[p.Lower()], version)
	s.touched[p.Lower()] = now
	return nil
}

// Drop removes a version from the version list and the registrations, as an upstream does that
// withdraws a release. The .nupkg keeps being served. Restore undoes it.
func (s *Server) Drop(id, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	base, ok := s.baseLocked(id)
	if !ok || base.find(version) < 0 {
		return fmt.Errorf("%s %s: %w", id, version, ErrUnknown)
	}
	k := base.Lower()
	if s.dropped[k] == nil {
		s.dropped[k] = map[string]bool{}
	}
	s.dropped[k][version] = true
	s.touched[k] = s.cfg.Now().UTC()
	return nil
}

// Restore lists a dropped version again.
func (s *Server) Restore(id, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.ToLower(id)
	if !s.dropped[k][version] {
		return fmt.Errorf("%s %s: not dropped: %w", id, version, ErrUnknown)
	}
	delete(s.dropped[k], version)
	s.touched[k] = s.cfg.Now().UTC()
	return nil
}

// SetListed unlists (false) or relists (true) a version: the registrations say so in their
// `listed` field and the search leaves it out; the flat container is unaffected.
func (s *Server) SetListed(id, version string, listed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.writableLocked(id)
	if p == nil || p.find(version) < 0 {
		return fmt.Errorf("%s %s: %w", id, version, ErrUnknown)
	}
	p.Versions[p.find(version)].Listed = listed
	s.overlay[p.Lower()] = p
	s.touched[p.Lower()] = s.cfg.Now().UTC()
	return nil
}

// writableLocked returns the package as a clone owned by the overlay, or nil for an unknown ID.
func (s *Server) writableLocked(id string) *Package {
	k := strings.ToLower(id)
	if p := s.overlay[k]; p != nil {
		return p
	}
	if base, ok := s.cfg.Corpus.Get(k); ok {
		p := base.clone()
		s.overlay[k] = p
		return p
	}
	return nil
}

// baseLocked is the package before drops and modes: the overlay entry or the corpus entry.
func (s *Server) baseLocked(id string) (*Package, bool) {
	k := strings.ToLower(id)
	if p := s.overlay[k]; p != nil {
		return p, true
	}
	return s.cfg.Corpus.Get(k)
}

// view is the package as the documents show it now: dropped versions removed, and the time of its
// last change.
func (s *Server) view(id string) (*Package, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	base, ok := s.baseLocked(id)
	if !ok {
		return nil, time.Time{}, false
	}
	k := base.Lower()
	p := base.clone()
	p.Versions = p.Versions[:0:0]
	var modified time.Time
	for _, v := range base.Versions {
		if s.dropped[k][v.Version] {
			continue
		}
		p.Versions = append(p.Versions, v)
		if v.Published.After(modified) {
			modified = v.Published
		}
	}
	if t := s.touched[k]; t.After(modified) {
		modified = t
	}
	return p, modified, len(p.Versions) > 0
}

// build returns the archive of a version and its hash; the bytes of one version never change.
func (s *Server) build(p *Package, v Version) *builtPackage {
	key := p.Lower() + "@" + v.Version
	s.mu.Lock()
	b := s.built[key]
	s.mu.Unlock()
	if b != nil {
		return b
	}
	nupkg := Nupkg(p, v)
	b = &builtPackage{nupkg: nupkg, hash: PackageHash(nupkg)}
	s.mu.Lock()
	s.built[key] = b
	s.mu.Unlock()
	return b
}
