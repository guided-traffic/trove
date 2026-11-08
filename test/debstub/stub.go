// Package debstub is the counting test upstream for trove's APT remote repositories, mirror and virtual repositories: a stub Debian
// archive that serves a deterministic, generated package set, signs its releases with test keys, counts every request and fails on
// command. Everything is generated in code (installable .deb files, Packages and Sources indexes, Release documents, the by-hash
// tree, pdiffs, flat repositories, a large index), so nothing is recorded or downloaded. It answers over TLS only.
//
// What it serves, below its one host:
//
//	/debian/dists/{stable,testing}/ (also under the codenames ratel and badger), components main and contrib, architectures
//	amd64, arm64 and all, a Sources index for main, by-hash, and from the second generation on pdiffs
//	/debian/dists/bulk/       the large index (Config.BulkParagraphs paragraphs), built on first use
//	/debian/pool/             the package and source files
//	/flat/binary/             a flat repository with a directory ("deb <host>/flat binary/")
//	/flatroot/                a flat repository with the root as directory ("deb <host>/flatroot ./", requested as /./InRelease)
//
// The Release documents are signed by the keys of KeyNames with the OpenPGP module directly, not through trove's own signer, so
// that trove's verifier is tested against an independent implementation. Mode holds the switches of R-DEB-035; Throttle the
// rate-limit answers of R-REPO-059; NewGeneration publishes a new release of an archive.
//
// Use it in a Go test through Start, or as a container (cmd/debstub, Containerfile). A test reads what the upstream saw through
// Counts and drives the failure cases through SetMode, SetThrottle and NewGeneration, or over the control API (/-/stub/) when the
// stub runs in a container.
package debstub

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// DefaultBulkParagraphs is the size of the large index: the 70,000 paragraphs of the bounds tests.
const DefaultBulkParagraphs = 70000

// Config configures a Server. The zero value serves the default archive and has no control API.
type Config struct {
	// BulkParagraphs is the number of paragraphs of the large index; 0 means DefaultBulkParagraphs.
	BulkParagraphs int
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token. Empty: the API does not exist
	// (the in-process Go methods are always available).
	ControlToken string
	// Now is the clock for release dates and throttles; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Server is the stub server. It is safe for concurrent use.
type Server struct {
	cfg    Config
	keys   map[string]*testKey
	corpus *corpus

	mu       sync.Mutex
	counts   map[countKey]*Entry
	mode     Mode
	archives []*archive
	// static maps a URL path to the bytes of a package or source file; generations add packages.
	static map[string][]byte
	docs   map[string]*signedDocs

	throttle throttleState
	fl       inflightState
}

// New creates a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.BulkParagraphs <= 0 {
		cfg.BulkParagraphs = DefaultBulkParagraphs
	}
	keys, err := makeKeys()
	if err != nil {
		return nil, err
	}
	c, err := newCorpus()
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, keys: keys, corpus: c}
	if err := s.resetLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// dateFor is the Release Date of a new generation: a minute before the clock, and at least a second after the previous one.
func (s *Server) dateFor(prev time.Time) time.Time {
	d := s.cfg.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if !prev.IsZero() && !d.After(prev) {
		d = prev.Add(time.Second)
	}
	return d
}

// resetLocked returns every piece of state to its start; the caller holds s.mu or owns s exclusively.
func (s *Server) resetLocked() error {
	s.counts = map[countKey]*Entry{}
	s.mode = Mode{}
	s.docs = map[string]*signedDocs{}
	s.static = make(map[string][]byte, len(s.corpus.static)+4)
	for k, v := range s.corpus.static {
		s.static[k] = v
	}
	s.clearThrottle()
	archives, err := s.makeArchives()
	if err != nil {
		return err
	}
	s.archives = archives
	return nil
}

func newArchive(id, suite, codename string, dirs []string) *archive {
	return &archive{id: id, suite: suite, codename: codename, dirs: dirs, base: ArchiveBase + "/", byHash: map[string]*object{}}
}

var (
	allArches = []string{"all", "amd64", "arm64"}
	allComps  = []string{"main", "contrib"}
)

func (s *Server) makeArchives() ([]*archive, error) {
	var out []*archive
	for _, suite := range []struct{ id, codename string }{{SuiteStable, CodenameStable}, {SuiteTesting, CodenameTesting}} {
		a := newArchive(suite.id, suite.id, suite.codename, []string{ArchiveBase + "/dists/" + suite.id + "/", ArchiveBase + "/dists/" + suite.codename + "/"})
		a.components, a.arches = allComps, allArches
		for _, comp := range allComps {
			for _, arch := range allArches {
				ix := &index{rel: fmt.Sprintf("%s/binary-%s/", comp, arch), kind: "Packages"}
				for _, p := range s.corpus.binaries[suite.id][comp] {
					if p.spec.Architecture == arch {
						ix.pkgs = append(ix.pkgs, p)
					}
				}
				a.indexes = append(a.indexes, ix)
			}
		}
		a.indexes = append(a.indexes, &index{rel: "main/source/", kind: "Sources", srcs: s.corpus.sources[suite.id]})
		a.build(s.dateFor(time.Time{}))
		out = append(out, a)
	}

	bulk := newArchive(SuiteBulk, SuiteBulk, SuiteBulk, []string{ArchiveBase + "/dists/bulk/"})
	bulk.components, bulk.arches, bulk.bulk = []string{"main"}, []string{"amd64"}, s.cfg.BulkParagraphs
	bulk.indexes = []*index{{rel: "main/binary-amd64/", kind: "Packages", synth: s.cfg.BulkParagraphs}}
	out = append(out, bulk) // built on first use

	// A flat repository's Filename fields are relative to its base URL; the Release lies in the directory below it ("binary/"),
	// or in the base itself.
	for _, flat := range []struct{ id, base, prefix string }{{ArchiveFlat, FlatBase + "/", FlatDir + "/"}, {ArchiveFlatRoot, FlatRootBase + "/", ""}} {
		// APT expects the Suite or Codename of a flat repository to be the directory name it was asked for ("binary/"), and warns
		// "Conflicting distribution" otherwise; for the root ("./") it checks nothing.
		name := strings.TrimSuffix(flat.prefix, "/")
		if name == "" {
			name = flat.id
		}
		a := newArchive(flat.id, name, name, []string{flat.base + flat.prefix})
		a.flat, a.base, a.filePrefix = true, flat.base, flat.prefix
		p, err := buildPkg(pkgDef{name: "trove-flat", version: "1.0-1", desc: "a package of a flat repository"}, "all")
		if err != nil {
			return nil, err
		}
		p.path = flat.prefix + p.spec.Filename()
		s.static[a.base+p.path] = p.deb
		a.indexes = []*index{{rel: "", kind: "Packages", pkgs: []*pkg{p}}}
		a.build(s.dateFor(time.Time{}))
		out = append(out, a)
	}
	return out, nil
}

// archiveByID finds an archive by its id or its codename.
func (s *Server) archiveByID(id string) *archive {
	for _, a := range s.archives {
		if a.id == id || a.codename == id {
			return a
		}
	}
	return nil
}

// ErrUnknown reports an archive the stub does not have.
var ErrUnknown = errors.New("no such archive")

// NewGeneration publishes a new release of the archive (a suite id or codename, "flat" or "flatroot"): a package is added to its
// first Packages index, the Release gets a later Date and new checksums, the previous generation's files stay reachable by hash,
// and the changed Packages index gets a pdiff. It returns the new Release's Date.
func (s *Server) NewGeneration(id string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.archiveByID(id)
	if a == nil {
		return time.Time{}, fmt.Errorf("%s: %w", id, ErrUnknown)
	}
	if !a.built {
		a.build(s.dateFor(time.Time{}))
	}
	cur := a.current()
	var target *index
	for _, ix := range a.indexes {
		// A suite grows in main/binary-amd64, a flat repository in its one Packages file.
		if ix.kind == "Packages" && (a.flat || ix.rel == "main/binary-amd64/") {
			target = ix
			break
		}
	}
	arch := "amd64"
	if a.flat {
		arch = "all"
	}
	n := cur.n + 1
	p, err := buildPkg(pkgDef{name: fmt.Sprintf("trove-zz-g%03d", n), version: "1.0-1", comp: "main", desc: fmt.Sprintf("a package of generation %d", n)}, arch)
	if err != nil {
		return time.Time{}, err
	}
	if a.flat {
		p.path = a.filePrefix + p.spec.Filename()
	}
	s.static[a.base+p.path] = p.deb
	target.extra = append(target.extra, p)
	a.build(s.dateFor(cur.date))
	return a.current().date, nil
}

// Generation returns the number and the Date of an archive's current generation.
func (s *Server) Generation(id string) (int, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.archiveByID(id)
	if a == nil {
		return 0, time.Time{}, fmt.Errorf("%s: %w", id, ErrUnknown)
	}
	if !a.built {
		a.build(s.dateFor(time.Time{}))
	}
	return a.current().n, a.current().date, nil
}

// Reset zeroes the request counters; modes, generations and the throttle stay.
func (s *Server) Reset() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = map[countKey]*Entry{}
}

// ResetAll returns the stub to its initial state: counters zero, healthy, no throttle, every archive back at its first generation.
func (s *Server) ResetAll() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	// The archives are rebuilt from the generated corpus, which does not fail.
	_ = s.resetLocked()
}

// SetMode replaces the switches.
func (s *Server) SetMode(m Mode) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m.clone()
	return nil
}

// Mode returns the current switches.
func (s *Server) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode.clone()
}

// ArchiveInfo describes an archive as the stub serves it now.
type ArchiveInfo struct {
	ID         string   `json:"id"`
	Suite      string   `json:"suite"`
	Codename   string   `json:"codename"`
	Dirs       []string `json:"dirs"`
	Flat       bool     `json:"flat"`
	Generation int      `json:"generation"`
	Date       string   `json:"date"`
	// ReleaseSHA256 is the digest of the unsigned Release text under the mode in force.
	ReleaseSHA256 string `json:"release_sha256"`
}

// Archives describes the archives; the large index is built by asking, which can take a second.
func (s *Server) Archives() []ArchiveInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ArchiveInfo, 0, len(s.archives))
	for _, a := range s.archives {
		if !a.built {
			a.build(s.dateFor(time.Time{}))
		}
		g := a.current()
		out = append(out, ArchiveInfo{
			ID: a.id, Suite: a.suite, Codename: a.codename, Dirs: a.dirs, Flat: a.flat, Generation: g.n,
			Date: g.date.Format(releaseTimeFormat), ReleaseSHA256: sha256Hex(a.releaseText(s.mode, s.cfg.Now())),
		})
	}
	return out
}

// Release returns the unsigned Release text of an archive under the mode in force.
func (s *Server) Release(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.archiveByID(id)
	if a == nil {
		return nil, fmt.Errorf("%s: %w", id, ErrUnknown)
	}
	if !a.built {
		a.build(s.dateFor(time.Time{}))
	}
	return a.releaseText(s.mode, s.cfg.Now()), nil
}

// File describes a package or source file the stub serves.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Files lists the package and source files, sorted by path.
func (s *Server) Files() []File {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]File, 0, len(s.static))
	for p, b := range s.static {
		sum := debgen.Sum(b)
		out = append(out, File{Path: p, Size: sum.Size, SHA256: sum.SHA256})
	}
	sortFiles(out)
	return out
}
