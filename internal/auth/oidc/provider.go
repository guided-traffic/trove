package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// callTimeout bounds every call to a provider: discovery, JWKS, token endpoint,
// userinfo (R-AUTH-004).
const callTimeout = 10 * time.Second

// jwksRefetchEvery is the throttle of a JWKS refetch for an unknown kid, per
// provider and replica (R-AUTH-004).
const jwksRefetchEvery = 60 * time.Second

// maxBackoff caps the retry interval of a failing discovery (R-AUTH-004).
const maxBackoff = 5 * time.Minute

// maxDocument bounds a discovery document, a JWK Set, a token response and a
// userinfo response.
const maxDocument = 1 << 20

// discovery is the part of an OpenID Provider Metadata document trove uses
// (OpenID Connect Discovery 1.0 §3, RFC 9207, RP-Initiated Logout §2.1).
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	IssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

// Status is a provider's state for the settings view of R-AUTH-040 and the
// login options of R-AUTH-009. It holds no secret.
type Status struct {
	ID          string
	DisplayName string
	Issuer      string
	Available   bool
	LastError   string
	LastErrorAt time.Time
}

// HTTPClients returns the outbound client of a purpose; the composition root
// passes the clients of internal/outbound (R-ARCH-022), so every provider call
// goes through the egress policy and the trust store.
type HTTPClients func(purpose string) *http.Client

// The purposes of the provider calls (the kind label of the outbound metrics):
// discovery, token endpoint and userinfo share the login purpose.
const (
	outboundPurposeLogin = outbound.PurposeOIDCDiscovery
	outboundPurposeJWKS  = outbound.PurposeJWKS
)

// Provider is one configured login provider on this replica: the defaulted
// configuration, the discovery document and the JWK Set of its last successful
// fetch, and the client secret. Each replica keeps its own copy (R-AUTH-042).
type Provider struct {
	ID          string
	cfg         config.AuthOIDCProvider
	redirectURI string
	log         *slog.Logger
	http        *http.Client // discovery, token endpoint, userinfo
	jwksHTTP    *http.Client
	secret      *secretSource

	mu        sync.RWMutex
	doc       *discovery
	keys      []*jwk
	lastErr   error
	lastErrAt time.Time
	refetchAt time.Time // last JWKS refetch for an unknown kid
	refreshed chan struct{}
}

func newProvider(id string, raw config.AuthOIDCProvider, baseURL string, clients HTTPClients, getenv func(string) string, log *slog.Logger) (*Provider, error) {
	cfg := raw.Defaulted()
	p := &Provider{
		ID: id, cfg: cfg, redirectURI: strings.TrimSuffix(baseURL, "/") + "/auth/oidc/" + id + "/callback",
		log: log.With("provider", id), refreshed: make(chan struct{}, 1),
	}
	p.http = clients(outboundPurposeLogin)
	p.jwksHTTP = clients(outboundPurposeJWKS)
	switch {
	case cfg.ClientAuthMethod == config.OIDCClientAuthNone:
	case cfg.ClientSecretFile != "":
		p.secret = &secretSource{path: cfg.ClientSecretFile}
		if _, err := p.secret.get(); err != nil {
			return nil, fmt.Errorf("auth.oidc.providers.%s.client_secret_file: %w", id, err)
		}
	default:
		v := getenv(cfg.ClientSecretEnv)
		if v == "" {
			return nil, fmt.Errorf("auth.oidc.providers.%s.client_secret_env: the variable %s is empty or unset", id, cfg.ClientSecretEnv)
		}
		p.secret = &secretSource{value: v}
	}
	return p, nil
}

// RedirectURI is the provider's own redirect URI, derived from the base URL and
// never from the request (R-AUTH-003).
func (p *Provider) RedirectURI() string { return p.redirectURI }

// Status returns the provider's discovery state.
func (p *Provider) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := Status{ID: p.ID, DisplayName: p.cfg.DisplayName, Issuer: p.cfg.Issuer, Available: p.doc != nil, LastErrorAt: p.lastErrAt}
	if p.lastErr != nil {
		st.LastError = p.lastErr.Error()
	}
	return st
}

func (p *Provider) document() *discovery {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.doc
}

// run keeps the discovery document fresh: a first fetch at once, then one every
// discovery_refresh; a failure retries with a backoff that doubles up to 5 min.
// A failing provider never blocks start-up or readiness (R-AUTH-004).
func (p *Provider) run(ctx context.Context) {
	backoff := 5 * time.Second
	for {
		err := p.refresh(ctx)
		wait := p.cfg.DiscoveryRefresh
		if err != nil {
			wait = backoff
			backoff = min(2*backoff, maxBackoff)
		} else {
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Refresh runs one discovery attempt now, outside the provider's schedule.
func (p *Provider) Refresh(ctx context.Context) error { return p.refresh(ctx) }

// refresh fetches and checks the discovery document and the JWK Set. On
// failure the provider keeps a document it had, unless the new one proves the
// configuration wrong (issuer mismatch, no S256): then it turns unusable.
func (p *Provider) refresh(ctx context.Context) error {
	doc, err := p.fetchDiscovery(ctx)
	if err == nil {
		var keys []*jwk
		if keys, err = p.fetchJWKS(ctx, doc.JWKSURI); err == nil {
			p.mu.Lock()
			p.doc, p.keys, p.lastErr = doc, keys, nil
			p.mu.Unlock()
			select {
			case p.refreshed <- struct{}{}:
			default:
			}
			return nil
		}
	}
	p.mu.Lock()
	p.lastErr, p.lastErrAt = err, time.Now()
	var cfgErr *configError
	if errors.As(err, &cfgErr) {
		p.doc, p.keys = nil, nil
	}
	p.mu.Unlock()
	p.log.Warn("OIDC discovery failed; retrying with backoff", "issuer", p.cfg.Issuer, "error", err)
	return err
}

// configError is a discovery result that proves the provider entry wrong.
type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func (p *Provider) fetchDiscovery(ctx context.Context) (*discovery, error) {
	u := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	body, err := p.get(ctx, p.http, u)
	if err != nil {
		return nil, err
	}
	var d discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("discovery document: %w", err)
	}
	// §4.3: the issuer in the document is identical to the configured one.
	if d.Issuer != p.cfg.Issuer {
		return nil, &configError{fmt.Sprintf("the discovery document names issuer %q, the configuration %q (OpenID Connect Discovery 1.0 §4.3)", d.Issuer, p.cfg.Issuer)}
	}
	if len(d.CodeChallengeMethods) > 0 && !slices.Contains(d.CodeChallengeMethods, "S256") {
		return nil, &configError{"the provider advertises code_challenge_methods_supported without S256; trove always uses PKCE with S256 (R-AUTH-005)"}
	}
	for name, v := range map[string]string{"authorization_endpoint": d.AuthorizationEndpoint, "token_endpoint": d.TokenEndpoint, "jwks_uri": d.JWKSURI} {
		if !acceptableURL(v) {
			return nil, &configError{fmt.Sprintf("discovery %s %q is missing or not https", name, v)}
		}
	}
	for name, v := range map[string]string{"userinfo_endpoint": d.UserinfoEndpoint, "end_session_endpoint": d.EndSessionEndpoint} {
		if v != "" && !acceptableURL(v) {
			return nil, &configError{fmt.Sprintf("discovery %s %q is not https", name, v)}
		}
	}
	return &d, nil
}

// acceptableURL is https, or http on a loopback host (tests), like the issuer.
func acceptableURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && config.LoopbackHost(u.Hostname()))
}

func (p *Provider) fetchJWKS(ctx context.Context, uri string) ([]*jwk, error) {
	body, err := p.get(ctx, p.jwksHTTP, uri)
	if err != nil {
		return nil, fmt.Errorf("JWKS: %w", err)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS: no usable signature key")
	}
	return keys, nil
}

// get is one bounded GET of a JSON document.
func (p *Provider) get(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	return body, nil
}

// key returns the verification key of a token. An unknown kid refetches the
// JWK Set at most once per jwksRefetchEvery, inside the callback that needs it
// (R-AUTH-004, R-AUTH-006 step 7).
func (p *Provider) key(ctx context.Context, kid, alg string) *jwk {
	p.mu.RLock()
	k := selectKey(p.keys, kid, alg)
	doc := p.doc
	p.mu.RUnlock()
	if k != nil || doc == nil {
		return k
	}
	p.mu.Lock()
	if time.Since(p.refetchAt) < jwksRefetchEvery {
		p.mu.Unlock()
		return nil
	}
	p.refetchAt = time.Now()
	p.mu.Unlock()
	keys, err := p.fetchJWKS(ctx, doc.JWKSURI)
	if err != nil {
		p.log.Warn("JWKS refetch for an unknown kid failed", "kid", kid, "error", err)
		return nil
	}
	p.mu.Lock()
	p.keys = keys
	p.mu.Unlock()
	return selectKey(keys, kid, alg)
}

// secretSource is the client secret: a file read again whenever its
// modification time changes, so a rotated Kubernetes Secret needs no restart
// (R-AUTH-004), or a value read once from the environment.
type secretSource struct {
	path  string
	value string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	cur   string
}

func (s *secretSource) get() (string, error) {
	if s.path == "" {
		return s.value, nil
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != "" && fi.ModTime().Equal(s.mtime) && fi.Size() == s.size {
		return s.cur, nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", errors.New("the client secret file is empty")
	}
	s.cur, s.mtime, s.size = v, fi.ModTime(), fi.Size()
	return v, nil
}
