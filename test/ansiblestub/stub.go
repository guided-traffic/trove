// Package ansiblestub is the counting test upstream for trove's Ansible Galaxy remote repositories, mirror and virtual repositories
// and for the Galaxy import: a stub Galaxy that serves a deterministic, generated set of collections and roles, counts every
// request and fails on command. Everything is generated in code (collection archives with their MANIFEST.json and FILES.json, role
// archives, the documents), so nothing is recorded or downloaded. It answers over TLS only.
//
// It is five hosts on one listener, told apart by the Host header:
//
//	the Galaxy (every name that is none of the next four)
//	  /api/                         the API root: {"available_versions":{"v3":"v3/","v1":"v1/"}}
//	  /api/v3/plugin/ansible/content/published/collections/index/           the collection index (limit at most 100, offset)
//	  .../index/<ns>/<name>/                                                  the collection document
//	  .../index/<ns>/<name>/versions/?limit=&offset=                          the version list, newest first, with links
//	  .../index/<ns>/<name>/versions/<version>/                               the version document
//	  .../artifacts/<ns>-<name>-<version>.tar.gz                              the archive
//	  /api/v3/collections/...       the same documents at the short path (a 302 to the long one in Mode.Redirect)
//	  /api/pulp/content/published/<file>?validate_token=...                   the signed address of the redirect
//	  /api/v1/roles/?owner__username=&name=, /api/v1/roles/<id>/, /api/v1/roles/<id>/versions/?page_size=
//	                                the role API, paged by next_link
//	the GitHub stand-in (Config.GitHubHost)     /<user>/<repo>/archive/<tag>.tar.gz, a 302 to the codeload host
//	the codeload host (Config.CodeloadHost)     /<user>/<repo>/tar.gz/<tag>
//	the unlisted host (Config.UnlistedHost)     /<user>/<repo>/archive/<tag>.tar.gz served directly; a role version points there
//	the realm (Config.RealmHost)                the token endpoint of a single sign-on (Config.RealmPath), see realm.go
//
// The corpus has the collections acme.base, acme.tools (with a dependency and a pre-release), acme.signed (a version with opaque
// signatures), other.stuff and many.versions (250 versions, three list pages), optionally Config.Fillers more, and the roles
// geerlingguy.pip, acme.rolex (a version on the unlisted host) and many.tags (60 tags, two pages of versions).
//
// Mode holds the switches (gzip, redirect, reject_token, digest_changed, wrong_digest, slow, down), Throttle the rate-limit answers
// of R-REPO-059 (ratelimit), Realm the bearer requirement of the hub (R-ANS-019); AddVersion and DropVersion change what the
// collections list.
//
// Use it in a Go test through Start, or as a container (cmd/ansiblestub, Containerfile). A test reads what the upstream saw through
// Counts and drives the failure cases through the Set methods, or over the control API (/-/stub/) when the stub runs in a
// container.
package ansiblestub

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub/ansiblegen"
)

// Config configures a Server. The zero value serves the default corpus and has no control API.
type Config struct {
	// Fillers is the number of one-version collections "filler.c0000" and on that the index lists in addition (the public
	// Galaxy lists thousands); 0 means none.
	Fillers int
	// GitHubHost, CodeloadHost, UnlistedHost and RealmHost are the host names (without port) of the four other hosts; empty means
	// the Default constants. The certificate of the listener has to carry them, and a remote repository has to be allowed to
	// reach the first two (remote.extraHosts) and may not reach the unlisted one.
	GitHubHost   string
	CodeloadHost string
	UnlistedHost string
	RealmHost    string
	// RealmPath is the path of the token endpoint; empty means DefaultRealmPath.
	RealmPath string
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token. Empty: the API does not exist
	// (the in-process Go methods are always available).
	ControlToken string
	// Now is the clock for token lifetimes and throttles; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Server is the stub Galaxy. It is safe for concurrent use.
type Server struct {
	cfg      Config
	archives archiveCache
	roles    []*roleDef

	mu     sync.Mutex
	counts map[countKey]*Entry
	mode   Mode
	cols   []*collectionDef
	realm  realmState
	// tokenSeq numbers the access tokens; it is never reset, so that a token of an earlier realm is never valid again.
	tokenSeq int

	throttle throttleState
	fl       inflightState
}

// ErrUnknown reports a collection or version the stub does not have.
var ErrUnknown = errors.New("no such collection or version")

// New creates a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	for _, d := range []struct {
		field *string
		def   string
	}{{&cfg.GitHubHost, DefaultGitHubHost}, {&cfg.CodeloadHost, DefaultCodeloadHost}, {&cfg.UnlistedHost, DefaultUnlistedHost}, {&cfg.RealmHost, DefaultRealmHost}} {
		*d.field = strings.ToLower(orDefault(*d.field, d.def))
	}
	if cfg.RealmPath == "" {
		cfg.RealmPath = DefaultRealmPath
	}
	if !strings.HasPrefix(cfg.RealmPath, "/") {
		return nil, fmt.Errorf("ansiblestub: realm path %q must start with a slash", cfg.RealmPath)
	}
	seen := map[string]bool{}
	for _, h := range []string{cfg.GitHubHost, cfg.CodeloadHost, cfg.UnlistedHost, cfg.RealmHost} {
		if seen[h] {
			return nil, fmt.Errorf("ansiblestub: host name %q is used twice", h)
		}
		seen[h] = true
	}
	if cfg.Fillers < 0 {
		return nil, errors.New("ansiblestub: fillers must not be negative")
	}
	s := &Server{cfg: cfg}
	s.resetAllLocked()
	return s, nil
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// resetAllLocked returns every piece of state to its start; the caller holds s.mu or owns s exclusively.
func (s *Server) resetAllLocked() {
	s.counts = map[countKey]*Entry{}
	s.mode = Mode{}
	s.realm = realmState{}
	s.cols, s.roles = defaultCorpus(s.cfg.Fillers)
	s.clearThrottle()
}

// Reset zeroes the request counters; modes, the throttle, the realm and the corpus stay.
func (s *Server) Reset() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = map[countKey]*Entry{}
}

// ResetAll returns the stub to its initial state: counters zero, healthy, no throttle, no realm requirement and the corpus as it
// was generated (versions added are gone, versions dropped are back).
func (s *Server) ResetAll() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetAllLocked()
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

// findCollection returns the collection definition, or nil. The definition is never changed after the call.
func (s *Server) findCollection(ns, name string) *collectionDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cols {
		if c.ns == ns && c.name == name {
			return c
		}
	}
	return nil
}

// collections returns the collection definitions in name order.
func (s *Server) collections() []*collectionDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*collectionDef(nil), s.cols...)
}

// AddVersion makes the upstream publish a version: of a collection that exists, or a new collection. The version must be a
// semantic version the collection does not have yet.
func (s *Server) AddVersion(fullName, version string) error {
	ns, name, ok := strings.Cut(fullName, ".")
	if !ok || !validFullName(fullName) {
		return fmt.Errorf("collection %q: want <namespace>.<name>", fullName)
	}
	if _, err := parseSemver(version); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, c := range s.cols {
		if c.ns == ns && c.name == name {
			idx = i
		}
	}
	var c *collectionDef
	if idx >= 0 {
		c = s.cols[idx].clone()
		if c.find(version) != nil {
			return fmt.Errorf("%s %s: the version exists", fullName, version)
		}
	} else {
		c = &collectionDef{ns: ns, name: name}
	}
	seq := 0
	for _, v := range c.versions {
		seq = max(seq, v.seq+1)
	}
	c.versions = append(c.versions, &versionDef{version: version, requiresAnsible: ">=2.14.0", seq: seq})
	sort.SliceStable(c.versions, func(i, j int) bool { return compareVersions(c.versions[i].version, c.versions[j].version) < 0 })
	if idx >= 0 {
		s.cols[idx] = c
	} else {
		s.cols = append(s.cols, c)
		sort.Slice(s.cols, func(i, j int) bool { return s.cols[i].fullName() < s.cols[j].fullName() })
	}
	return nil
}

// DropVersion makes the upstream remove a version: its list, its document and its archive answer 404 from now on. A collection
// without versions disappears from the index.
func (s *Server) DropVersion(fullName, version string) error {
	ns, name, _ := strings.Cut(fullName, ".")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.cols {
		if c.ns != ns || c.name != name {
			continue
		}
		if c.find(version) == nil {
			break
		}
		n := c.clone()
		n.versions = n.versions[:0]
		for _, v := range c.versions {
			if v.version != version {
				n.versions = append(n.versions, v)
			}
		}
		if len(n.versions) == 0 {
			s.cols = append(s.cols[:i:i], s.cols[i+1:]...)
		} else {
			s.cols[i] = n
		}
		return nil
	}
	return fmt.Errorf("%s %s: %w", fullName, version, ErrUnknown)
}

// Version describes a collection version as the stub serves it under the original archive (before the digest modes).
type Version struct {
	Version  string `json:"version"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Signed   bool   `json:"signed,omitempty"`
}

// CollectionInfo describes a collection with its versions, newest first.
type CollectionInfo struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Versions  []Version `json:"versions"`
}

// RoleInfo describes a role.
type RoleInfo struct {
	ID       int      `json:"id"`
	Owner    string   `json:"owner"`
	Name     string   `json:"name"`
	GitHub   string   `json:"github"`
	Versions []string `json:"versions"`
	// UnlistedVersions are the tags whose download_url is on the unlisted host.
	UnlistedVersions []string `json:"unlisted_versions,omitempty"`
}

// CorpusInfo is what the stub serves, with the digests of the original archives. Building it generates every archive, which takes
// a moment for a large Config.Fillers.
type CorpusInfo struct {
	Collections []CollectionInfo `json:"collections"`
	Roles       []RoleInfo       `json:"roles"`
}

// Corpus describes the collections and roles. A collection of Config.Fillers is left out.
func (s *Server) Corpus() (CorpusInfo, error) {
	var out CorpusInfo
	for _, c := range s.collections() {
		if c.ns == "filler" {
			continue
		}
		info := CollectionInfo{Namespace: c.ns, Name: c.name}
		for i := len(c.versions) - 1; i >= 0; i-- {
			v := c.versions[i]
			a, err := s.archives.collection(c, v, false)
			if err != nil {
				return CorpusInfo{}, err
			}
			info.Versions = append(info.Versions, Version{Version: v.version, Filename: a.Filename, SHA256: a.SHA256, Size: a.Size, Signed: v.signed})
		}
		out.Collections = append(out.Collections, info)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.roles {
		info := RoleInfo{ID: r.id, Owner: r.owner, Name: r.name, GitHub: r.user + "/" + r.repo}
		for _, v := range r.versions {
			info.Versions = append(info.Versions, v.name)
			if v.site == siteUnlisted {
				info.UnlistedVersions = append(info.UnlistedVersions, v.name)
			}
		}
		out.Roles = append(out.Roles, info)
	}
	return out, nil
}

// served resolves what the stub serves for an archive of a version under the mode: the bytes, and what the documents announce.
func (s *Server) served(c *collectionDef, v *versionDef, m Mode) (file ansiblegen.Archive, ann announced, err error) {
	orig, err := s.archives.collection(c, v, false)
	if err != nil {
		return ansiblegen.Archive{}, announced{}, err
	}
	ann = announced{filename: orig.Filename, sha256: orig.SHA256, size: orig.Size}
	wrong, changed := listed(m.WrongDigest, c.fullName()), listed(m.DigestChanged, c.fullName())
	if !wrong && !changed {
		return orig, ann, nil
	}
	alt, err := s.archives.collection(c, v, true)
	if err != nil {
		return ansiblegen.Archive{}, announced{}, err
	}
	if !wrong {
		ann = announced{filename: alt.Filename, sha256: alt.SHA256, size: alt.Size}
	}
	return alt, ann, nil
}
