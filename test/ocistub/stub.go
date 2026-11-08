// Package ocistub is the counting test upstream for trove's OCI remote repositories and mirror: a stub OCI distribution registry
// that serves a deterministic, generated image set, counts every request, and fails on command. Everything is generated in
// code (layers, configurations, manifests, indexes, attestation manifests, referrers), so nothing is recorded or downloaded
// and two starts produce the same digests. It answers over TLS only; there is no plain-HTTP listener.
//
// Three hosts, as the large public registries have them: the registry (the /v2/ API), the token realm (/token) and the blob CDN
// (/cdn/, where a blob GET on the registry redirects to). The hosts are configured (Config.PublicURL, RealmURL, CDNURL) or
// derived from the request (see Server.bases). The registry challenges with WWW-Authenticate: Bearer realm=...,service=...,scope=...
// and the realm issues tokens to anonymous clients or to clients with the right Basic credentials (Mode.Auth).
//
// Use it in a Go test through Start, or as a container (cmd/ocistub, Containerfile). A test reads what the upstream saw through
// Counts and drives the failure cases through SetMode, SetThrottle, MoveTag, SetTag, DeleteTag and RevokeTokens, or over the
// control API (/-/stub/) when the stub runs in a container.
package ocistub

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults of Config.
const (
	DefaultService  = "ocistub"
	DefaultUsername = "hub-user"
	DefaultPassword = "hub-pass"
	// DefaultPageSize is the page size of the tags list and the catalog: every page is capped to it whatever n a client asks,
	// so that the 2,500 tags of BulkRepository come in four pages.
	DefaultPageSize = 700
	// DefaultTokenTTL is the lifetime of a token in seconds.
	DefaultTokenTTL = 300
)

// Config configures a Server. The zero value serves the default corpus, derives the three hosts from the Host header and has no
// control API.
type Config struct {
	// Corpus is the image set; nil means Default(). The server works on a copy.
	Corpus *Corpus
	// PublicURL, RealmURL and CDNURL are the bases of the registry, the token realm and the blob CDN in the challenges and
	// the redirects, for example "https://ocistub.ns.svc". Empty: derived from the request.
	PublicURL, RealmURL, CDNURL string
	// Service is the service name in the challenge and the token request; empty means DefaultService.
	Service string
	// Username and Password are the credentials of AuthBasic and of a client that offers any; empty: DefaultUsername and
	// DefaultPassword.
	Username, Password string
	// PageSize caps a page of the tags list and the catalog; 0 means DefaultPageSize.
	PageSize int
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token. Empty: the API does not exist
	// (the in-process Go methods are always available).
	ControlToken string
	// Now is the clock for tokens, throttles and quotas; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Server is the stub server. It is safe for concurrent use.
type Server struct {
	cfg Config

	mu     sync.Mutex
	counts map[countKey]*Entry
	mode   Mode
	c      *Corpus
	// epoch is bumped by RevokeTokens; a token of an earlier epoch is refused. seq numbers the tokens issued.
	epoch, seq int

	// throttle is the rate-limit switch of throttle.go and quota the pull quota of quota.go, each with a lock of its own.
	throttle throttleState
	quota    quotaState
	// fl counts the requests open at once (inflight.go); it has a lock of its own, so a request is counted while it waits.
	fl inflightState
}

// New creates a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Corpus == nil {
		cfg.Corpus = sharedDefault()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	cfg.Service = orDefault(cfg.Service, DefaultService)
	cfg.Username = orDefault(cfg.Username, DefaultUsername)
	cfg.Password = orDefault(cfg.Password, DefaultPassword)
	cfg.PageSize = positiveOr(cfg.PageSize, DefaultPageSize)
	for _, u := range []string{cfg.PublicURL, cfg.RealmURL, cfg.CDNURL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return nil, fmt.Errorf("%q is no absolute http or https URL", u)
		}
	}
	s := &Server{cfg: cfg}
	s.resetLocked()
	return s, nil
}

var (
	sharedOnce   sync.Once
	sharedCorpus *Corpus
)

// sharedDefault is one build of Default() for every server that does not bring a corpus: a server works on a copy, so none can
// change it, and building the 2,500-tag set once keeps the start of a stub cheap.
func sharedDefault() *Corpus {
	sharedOnce.Do(func() { sharedCorpus = Default() })
	return sharedCorpus
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func positiveOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// resetLocked clears every piece of state; the caller holds s.mu or owns s exclusively.
func (s *Server) resetLocked() {
	s.counts = map[countKey]*Entry{}
	s.mode = Mode{}
	s.c = s.cfg.Corpus.clone()
	s.epoch, s.seq = 0, 0
	s.clearThrottle()
	s.quota.reset()
}

// bases are the addresses of the three hosts.
type bases struct{ registry, realm, cdn string }

// The names of the three hosts when the stub runs on a loopback address and the Config fixes none. They fit the host grammar of a
// remote repository's extraHosts, and nothing resolves them: no resolver is asked for a name under "localhost" unless the system
// answers it, which Go does not do by itself. A client reaches the listener through Running.DialOverride (trove's
// outbound.WithDialOverride) or Running.DialContext (a Go test's transport), which map the three host:port pairs to the listener.
const (
	LoopbackRegistryHost = "registry.localhost"
	LoopbackRealmHost    = "auth.localhost"
	LoopbackCDNHost      = "cdn.localhost"
)

func isLoopbackName(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}

// derive decides the addresses for a request to hostport. A fixed address of the Config wins. Otherwise a request that reaches the
// stub under a loopback name gets the registry registry.localhost, the realm auth.localhost and the CDN cdn.localhost on the same
// port: three names for one listener, which is how a Go test runs three hosts without a resolver of its own. Any other Host is used
// for all three, so a deployment that wants separate hosts sets the URLs.
func (s *Server) derive(hostport string) bases {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	withPort := func(h string) string {
		if port == "" {
			return "https://" + h
		}
		return "https://" + net.JoinHostPort(h, port)
	}
	b := bases{registry: "https://" + hostport, realm: "https://" + hostport, cdn: "https://" + hostport}
	if isLoopbackName(host) {
		b = bases{registry: withPort(LoopbackRegistryHost), realm: withPort(LoopbackRealmHost), cdn: withPort(LoopbackCDNHost)}
	}
	if u := strings.TrimSuffix(s.cfg.PublicURL, "/"); u != "" {
		b.registry = u
	}
	if u := strings.TrimSuffix(s.cfg.RealmURL, "/"); u != "" {
		b.realm = u
	}
	if u := strings.TrimSuffix(s.cfg.CDNURL, "/"); u != "" {
		b.cdn = u
	}
	return b
}

// Counts returns a snapshot of the request counters.
func (s *Server) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Counts{Entries: make([]Entry, 0, len(s.counts))}
	for _, e := range s.counts {
		c := *e
		c.Statuses = make(map[string]int, len(e.Statuses))
		for k, v := range e.Statuses {
			c.Statuses[k] = v
		}
		out.Entries = append(out.Entries, c)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Query != b.Query:
			return a.Query < b.Query
		case a.Host != b.Host:
			return a.Host < b.Host
		case strings.Join(a.Accept, ",") != strings.Join(b.Accept, ","):
			return strings.Join(a.Accept, ",") < strings.Join(b.Accept, ",")
		case a.Method != b.Method:
			return a.Method < b.Method
		case a.Conditional != b.Conditional:
			return !a.Conditional
		}
		return a.Auth < b.Auth
	})
	return out
}

// Reset zeroes the request counters; modes, tokens and changes to the corpus stay.
func (s *Server) Reset() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = map[countKey]*Entry{}
}

// ResetAll returns the stub to its initial state: counters zero, healthy, the corpus as built, no throttle or quota used, the
// tokens of earlier epochs valid again.
func (s *Server) ResetAll() {
	s.fl.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

// SetMode replaces the switches and starts the pull quota's count afresh.
func (s *Server) SetMode(m Mode) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m.clone()
	s.quota.reset()
	return nil
}

// Mode returns the current switches.
func (s *Server) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode.clone()
}

// RevokeTokens makes every token issued so far invalid: the registry answers 401 to it and a client has to run the handshake again.
func (s *Server) RevokeTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
}

// Errors of the changes to the corpus.
var (
	// ErrUnknown reports a change to a repository, tag or digest the stub does not have.
	ErrUnknown = errors.New("no such repository, tag or digest")
	// ErrNoRecipe reports a tag that cannot be regenerated (it names a manifest the stub did not generate as an image).
	ErrNoRecipe = errors.New("the tag's manifest has no image recipe")
)

// MoveTag points a tag at a newly generated revision of its image: the same platforms and media types with other layers, so
// other digests. The earlier manifest stays in the repository and is still served by its digest, as in a registry. It returns the
// new digest.
func (s *Server) MoveTag(name, tag string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	if r == nil || r.tags[tag] == "" {
		return "", fmt.Errorf("%s:%s: %w", name, tag, ErrUnknown)
	}
	rc, ok := r.recipes[r.tags[tag]]
	if !ok {
		return "", fmt.Errorf("%s:%s: %w", name, tag, ErrNoRecipe)
	}
	// The revision is seeded by the tag: two tags that share an image move to different images, and a tag moved twice to a new one
	// each time.
	rc.Seed, _, _ = strings.Cut(rc.Seed, "+")
	rc.Seed += "+" + tag
	rc.Rev++
	o := s.c.build(r, name, rc)
	r.tags[tag] = o.digest
	return o.digest, nil
}

// SetTag points a tag, new or existing, at a manifest the repository holds.
func (s *Server) SetTag(name, tag, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	if r == nil || s.c.manifests[digest] == nil || !r.members[digest] {
		return fmt.Errorf("%s@%s: %w", name, digest, ErrUnknown)
	}
	r.tags[tag] = digest
	return nil
}

// DeleteTag removes a tag; the manifest stays and is served by its digest.
func (s *Server) DeleteTag(name, tag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	if r == nil || r.tags[tag] == "" {
		return fmt.Errorf("%s:%s: %w", name, tag, ErrUnknown)
	}
	delete(r.tags, tag)
	return nil
}

// Repositories lists the repositories, sorted.
func (s *Server) Repositories() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Names()
}

// Tags returns the tags of a repository with their digests.
func (s *Server) Tags(name string) (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	if r == nil {
		return nil, false
	}
	out := make(map[string]string, len(r.tags))
	for k, v := range r.tags {
		out[k] = v
	}
	return out, true
}

// Digest returns the manifest digest of a tag as the stub serves it now.
func (s *Server) Digest(name, tag string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Digest(name, tag)
}

// BlobInfo describes a blob a manifest names.
type BlobInfo struct {
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ImageInfo describes a manifest as the stub serves it: its digest, what it names (configuration and layers, or the manifests of
// an index with their platforms) and the subject it refers to.
type ImageInfo struct {
	Name           string      `json:"name"`
	Reference      string      `json:"reference,omitempty"`
	Digest         string      `json:"digest"`
	MediaType      string      `json:"media_type"`
	Size           int64       `json:"size"`
	ArtifactType   string      `json:"artifact_type,omitempty"`
	Platform       string      `json:"platform,omitempty"`
	AttestationFor string      `json:"attestation_for,omitempty"`
	Subject        string      `json:"subject,omitempty"`
	Config         *BlobInfo   `json:"config,omitempty"`
	Layers         []BlobInfo  `json:"layers,omitempty"`
	Manifests      []ImageInfo `json:"manifests,omitempty"`
}

// Image describes the manifest a tag or digest names. Reading it is not a request to the registry and is not counted.
func (s *Server) Image(name, reference string) (ImageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	if r == nil {
		return ImageInfo{}, fmt.Errorf("%s: %w", name, ErrUnknown)
	}
	digest := reference
	if d, ok := r.tags[reference]; ok {
		digest = d
	}
	if !r.members[digest] || s.c.manifests[digest] == nil {
		return ImageInfo{}, fmt.Errorf("%s:%s: %w", name, reference, ErrUnknown)
	}
	info := s.c.describe(name, digest)
	info.Reference = reference
	return info, nil
}
