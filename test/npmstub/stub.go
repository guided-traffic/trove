// Package npmstub is the counting test upstream for trove's npm repositories: a stub npm registry
// that serves the synthetic package set of package corpus (and a few recorded documents), counts
// every request per path, and fails on command. It answers over TLS only; there is no plain-HTTP
// listener to enable.
//
// Use it in a Go test through Start, or as a container (cmd/npmstub, Containerfile). A test reads
// what the upstream saw through Counts, and drives the failure cases of R-NPM-024 to R-NPM-026
// and R-NPM-033 through SetMode, Publish and Drop, or over the control API (/-/stub/) when the
// stub runs in a container.
package npmstub

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// Config configures a Server. The zero value serves the default corpus, derives the tarball
// addresses from the Host header and has no control API.
type Config struct {
	// Corpus is the package set; nil means corpus.Default().
	Corpus *corpus.Corpus
	// Recorded is the recorded set; nil means the embedded one (testdata/recorded). Use an empty
	// filesystem for none.
	Recorded fs.FS
	// PublicURL is the base the dist.tarball addresses of documents point to, for example
	// "https://localhost:8080/api/npm/npm-all". Empty: "https://" and the request's Host.
	PublicURL string
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token.
	// Empty: the API does not exist (the in-process Go methods are always available).
	ControlToken string
	// Now is the clock for publish times; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Server is the stub registry. It is safe for concurrent use.
type Server struct {
	cfg      Config
	recorded map[string]*recorded
	recFS    fs.FS

	mu      sync.Mutex
	counts  map[countKey]*Entry
	mode    Mode
	overlay map[string]*corpus.Package // packages changed by Publish, as clones
	dropped map[string]map[string]bool
	touched map[string]time.Time // when a package last lost a version
	cache   map[cacheKey]*rendered
}

// New creates a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Corpus == nil {
		cfg.Corpus = corpus.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	recFS := cfg.Recorded
	if recFS == nil {
		var err error
		if recFS, err = embeddedRecorded(); err != nil {
			return nil, err
		}
	}
	rec, err := loadRecorded(recFS)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, recorded: rec, recFS: recFS}
	s.resetLocked()
	return s, nil
}

// resetLocked clears every piece of state; the caller holds s.mu or owns s exclusively.
func (s *Server) resetLocked() {
	s.counts = map[countKey]*Entry{}
	s.mode = Mode{}
	s.overlay = map[string]*corpus.Package{}
	s.dropped = map[string]map[string]bool{}
	s.touched = map[string]time.Time{}
	s.cache = map[cacheKey]*rendered{}
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
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		if a.Conditional != b.Conditional {
			return !a.Conditional
		}
		return a.Auth < b.Auth
	})
	return out
}

// Reset zeroes the request counters; modes and published or dropped versions stay.
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
	s.cache = map[cacheKey]*rendered{}
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

// Publish adds a version at runtime, creating the package when the stub does not know it: the way
// a test offers a higher version of a name a local repository claims. The version gets the
// tarball of corpus.Tarball for these dependencies. It becomes `latest` when it is a release above
// the current one; tags are set in addition.
func (s *Server) Publish(name, version string, deps map[string]string, tags ...string) error {
	if _, ok := s.recorded[name]; ok {
		return fmt.Errorf("package %s is recorded and cannot change", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.overlay[name]
	if p == nil {
		if base, ok := s.cfg.Corpus.Get(name); ok {
			p = base.Clone()
		} else {
			now := s.cfg.Now().UTC()
			p = &corpus.Package{
				Name: name, Description: "Package " + name + " published to the test upstream",
				Created: now, Modified: now, DistTags: map[string]string{},
			}
		}
	}
	at := s.cfg.Now().UTC()
	if !p.Insert(corpus.NewVersion(name, version, deps, at)) {
		return fmt.Errorf("%s@%s: %w", name, version, ErrExists)
	}
	p.Modified = at
	if latest, ok := p.DistTags["latest"]; !corpus.IsPrerelease(version) && (!ok || corpus.CompareVersions(version, latest) > 0) {
		p.DistTags["latest"] = version
	}
	for _, tag := range tags {
		p.DistTags[tag] = version
	}
	p.Retag()
	s.overlay[name] = p
	delete(s.dropped[name], version)
	s.cache = map[cacheKey]*rendered{}
	return nil
}

// Drop removes a version from the package's document, as an upstream does that withdraws a
// release. The tarball keeps being served unless Mode.DropTarballs is set. Restore undoes it.
func (s *Server) Drop(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.baseLocked(name)
	if !ok || p.Find(version) < 0 {
		return fmt.Errorf("%s@%s: no such version", name, version)
	}
	if s.dropped[name] == nil {
		s.dropped[name] = map[string]bool{}
	}
	s.dropped[name][version] = true
	s.touched[name] = s.cfg.Now().UTC()
	s.cache = map[cacheKey]*rendered{}
	return nil
}

// Restore lists a dropped version in the document again.
func (s *Server) Restore(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dropped[name][version] {
		return fmt.Errorf("%s@%s: not dropped", name, version)
	}
	delete(s.dropped[name], version)
	s.touched[name] = s.cfg.Now().UTC()
	s.cache = map[cacheKey]*rendered{}
	return nil
}

// baseLocked is the package before drops and modes: the published clone or the corpus entry.
func (s *Server) baseLocked(name string) (*corpus.Package, bool) {
	if p := s.overlay[name]; p != nil {
		return p, true
	}
	return s.cfg.Corpus.Get(name)
}

// viewLocked is the package as a document shows it now: dropped versions removed and, for the
// names in Mode.BadIntegrity, an integrity the tarballs do not have.
func (s *Server) viewLocked(name string) (*corpus.Package, bool) {
	base, ok := s.baseLocked(name)
	if !ok {
		return nil, false
	}
	drops := s.dropped[name]
	bad := s.mode.has(s.mode.BadIntegrity, name)
	if len(drops) == 0 && !bad {
		return base, true
	}
	p := base.Clone()
	for v := range drops {
		p.Remove(v)
	}
	if t, ok := s.touched[name]; ok && t.After(p.Modified) {
		p.Modified = t
	}
	if bad {
		for i := range p.Versions {
			p.Versions[i].Dist.Integrity = badIntegrity
		}
	}
	p.Retag()
	return p, true
}

// badIntegrity is a well-formed sha512 integrity that no tarball has.
var badIntegrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
