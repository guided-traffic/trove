package ci

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// The discovery and key fetch of R-CI-003. Every request goes through a client of the outbound factory
// (R-ARCH-022): TLS against the system roots and the configured bundles plus the issuer's own
// ca_bundle_ref, at most three redirects, https only, the egress address policy on every hop (so a
// jwks_uri that names an internal address, taken from a discovery document or from a redirect, gets no
// connection), a timeout and a size limit. A fetch runs outside any database transaction and holds no
// connection while it waits (R-ARCH-019). The URLs come only from the issuer record and its discovery
// document; no token header names one (RFC 8725 §3.10).

// maxRedirects is the redirect limit of every discovery and key request (R-CI-003).
const maxRedirects = 3

// The failure classes of a fetch: what last_error, the key status and the audit event name, with the HTTP
// status and the host, never a byte of a response body (R-CI-003).
const (
	classEgressDenied       = "egress_denied"
	classTimeout            = "timeout"
	classConnection         = "connection_failed"
	classTLS                = "tls_verification_failed"
	classTrustStore         = "trust_store_unavailable"
	classTooManyRedirects   = "too_many_redirects"
	classInsecureRedirect   = "insecure_redirect"
	classHTTPStatus         = "http_status"
	classResponseTooLarge   = "response_too_large"
	classInvalidResponse    = "invalid_response"
	classIssuerMismatch     = "issuer_mismatch"
	classAlgorithmsNotOffer = "algorithms_not_offered"
	classNoUsableKeys       = "no_usable_keys"
	classInvalidURL         = "invalid_url"
	classCanceled           = "canceled"
)

// FetchError is a failed fetch: the class, the HTTP status when the issuer answered, and the host of the
// URL. It carries nothing of a response body. Code is the management code of the 422 that a create with
// validation answers for it.
type FetchError struct {
	Class  string
	Status int
	Host   string
}

// Error renders the one line that last_error holds: the class, the HTTP status when there is one and the
// host.
func (e *FetchError) Error() string {
	var b strings.Builder
	b.WriteString(e.Class)
	if e.Status != 0 {
		b.WriteString(" status=")
		b.WriteString(strconv.Itoa(e.Status))
	}
	if e.Host != "" {
		b.WriteString(" host=")
		b.WriteString(e.Host)
	}
	return b.String()
}

// code is the management code of the 422 of a validated create.
func (e *FetchError) code() Reason {
	switch e.Class {
	case classIssuerMismatch:
		return reason.IssuerMismatch
	case classAlgorithmsNotOffer:
		return reason.AlgorithmsNotOffered
	case classNoUsableKeys:
		return reason.NoUsableKeys
	}
	return reason.DiscoveryUnreachable
}

// fetched is the result of one successful fetch.
type fetched struct {
	JWKSURI string
	// Stored is the reduced key set (keys_jwks.go) as it is stored.
	Stored []byte
	Set    keySet
	ETag   string
	// MaxAge is the Cache-Control max-age of the key response, 0 when there is none.
	MaxAge    time.Duration
	FetchedAt time.Time
}

// fetcher fetches discovery documents and key sets. It keeps one client per trust setup (the clients own
// connection pools).
type fetcher struct {
	factory *outbound.Factory
	outCfg  config.Outbound
	cfg     config.CISigningKeys
	now     func() time.Time

	mu       sync.Mutex
	clients  map[clientKey]*http.Client
	envMu    sync.Mutex
	envFiles map[string]string
	envDir   string
}

type clientKey struct {
	purpose string
	bundle  string
	proxy   bool
}

func newFetcher(f *outbound.Factory, out config.Outbound, cfg config.CISigningKeys, now func() time.Time) *fetcher {
	return &fetcher{factory: f, outCfg: out, cfg: cfg, now: now, clients: make(map[clientKey]*http.Client), envFiles: map[string]string{}}
}

// bundleEnvPrefix is the prefix of the only environment variables a ca_bundle_ref may name, so that trove's
// own variables (secrets, database credentials) can never be routed into a trust store (Q-CI-31).
const bundleEnvPrefix = "TROVE_CIC_"

var bundleEnvName = regexp.MustCompile(`^TROVE_CIC_[A-Z0-9_]{1,200}$`)

// bundleFile returns the file that holds the PEM bundle of a ca_bundle_ref (R-CI-001, Q-CI-31). A file
// reference is the file itself, which the trust store re-reads when it changes. An environment reference is
// written once to a private file of the process (mode 0600 in a 0700 directory), because the trust store
// reads files; the variable is fixed for the life of the process, so a change needs a restart. The
// reference was checked at write time by [validBundleRef].
func (f *fetcher) bundleFile(ref string) (string, error) {
	if p, ok := strings.CutPrefix(ref, "file:"); ok {
		return p, nil
	}
	name := strings.TrimPrefix(ref, "env:")
	val, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(val) == "" {
		return "", errors.New("the variable is not set")
	}
	f.envMu.Lock()
	defer f.envMu.Unlock()
	if p, ok := f.envFiles[name]; ok {
		return p, nil
	}
	if f.envDir == "" {
		dir, err := os.MkdirTemp("", "trove-ci-ca-")
		if err != nil {
			return "", err
		}
		f.envDir = dir
	}
	p := filepath.Join(f.envDir, name+".pem")
	if err := os.WriteFile(p, []byte(val), 0o600); err != nil {
		return "", err
	}
	f.envFiles[name] = p
	return p, nil
}

// client returns the client of a purpose for an issuer's trust and proxy setting.
func (f *fetcher) client(purpose string, is Issuer) (*http.Client, error) {
	if f.factory == nil {
		return nil, &FetchError{Class: classTrustStore}
	}
	ref := ""
	if is.CABundleRef != nil {
		ref = *is.CABundleRef
	}
	key := clientKey{purpose: purpose, bundle: ref, proxy: is.UseOutboundProxy}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[key]; ok {
		return c, nil
	}
	opts := []outbound.ClientOption{
		outbound.WithTimeout(f.cfg.FetchTimeout),
		outbound.WithMaxRedirects(maxRedirects),
		outbound.WithRedirectCheck(httpsOnly),
	}
	if !is.UseOutboundProxy {
		opts = append(opts, outbound.WithDirect())
	}
	if ref != "" {
		// The system roots and the configured bundles, plus the issuer's own: a private CA adds to the
		// trust of the installation and does not replace it.
		path, err := f.bundleFile(ref)
		if err != nil {
			return nil, &FetchError{Class: classTrustStore}
		}
		files := append(append([]string{}, f.outCfg.TLS.CAFiles...), path)
		store := tlsfiles.New(tlsfiles.Spec{CAFiles: files, SystemRoots: f.outCfg.TLS.UseSystemRoots},
			tlsfiles.Labels{Kind: tlsfiles.KindOutbound})
		if err := store.Loaded(); err != nil {
			return nil, &FetchError{Class: classTrustStore}
		}
		opts = append(opts, outbound.WithRootCAs(store))
	}
	c := f.factory.Client(purpose, opts...)
	f.clients[key] = c
	return c, nil
}

// httpsOnly refuses a redirect to anything but an https URL (R-CI-003).
func httpsOnly(req *http.Request, _ []*http.Request) error {
	if req.URL.Scheme != "https" {
		return outbound.ErrInsecureRedirect
	}
	return nil
}

// fetch runs the discovery and the key request of an issuer and returns the reduced, parsed key set. The
// errors are *FetchError.
func (f *fetcher) fetch(ctx context.Context, is Issuer) (*fetched, error) {
	discURL := derefStr(is.DiscoveryURL)
	if discURL == "" {
		discURL = strings.TrimSuffix(is.Issuer, "/") + "/.well-known/openid-configuration"
	}
	body, _, err := f.get(ctx, outbound.PurposeOIDCDiscovery, is, discURL)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	host := hostOf(discURL)
	if json.Unmarshal(body, &doc) != nil {
		return nil, &FetchError{Class: classInvalidResponse, Host: host}
	}
	if doc.Issuer != is.Issuer {
		return nil, &FetchError{Class: classIssuerMismatch, Host: host}
	}
	if !intersects(doc.Algs, is.AllowedAlgorithms) {
		return nil, &FetchError{Class: classAlgorithmsNotOffer, Host: host}
	}
	jwksURI := derefStr(is.JWKSURI)
	if jwksURI == "" {
		jwksURI = doc.JWKSURI
	}
	if !isHTTPS(jwksURI) {
		return nil, &FetchError{Class: classInvalidURL, Host: host}
	}
	body, h, err := f.get(ctx, outbound.PurposeJWKS, is, jwksURI)
	if err != nil {
		return nil, err
	}
	stored, err := reduceJWKS(body)
	if err != nil {
		return nil, &FetchError{Class: classInvalidResponse, Host: hostOf(jwksURI)}
	}
	set, err := parseStored(stored)
	if err != nil || len(set.Usable) == 0 {
		return nil, &FetchError{Class: classNoUsableKeys, Host: hostOf(jwksURI)}
	}
	return &fetched{
		JWKSURI: jwksURI, Stored: stored, Set: set, ETag: h.Get("ETag"), MaxAge: maxAge(h.Get("Cache-Control")),
		FetchedAt: f.now(),
	}, nil
}

// get is one GET with the limits of the key settings. It returns the body and the response headers.
func (f *fetcher) get(ctx context.Context, purpose string, is Issuer, rawURL string) ([]byte, http.Header, error) {
	host := hostOf(rawURL)
	if !isHTTPS(rawURL) {
		return nil, nil, &FetchError{Class: classInvalidURL, Host: host}
	}
	c, err := f.client(purpose, is)
	if err != nil {
		var fe *FetchError
		if errors.As(err, &fe) {
			fe.Host = host
		}
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, nil, &FetchError{Class: classInvalidURL, Host: host}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, classify(ctx, err, host)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, &FetchError{Class: classHTTPStatus, Status: resp.StatusCode, Host: host}
	}
	limit := int64(f.cfg.MaxResponseBytes)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, classify(ctx, err, host)
	}
	if int64(len(body)) > limit {
		return nil, nil, &FetchError{Class: classResponseTooLarge, Host: host}
	}
	return body, resp.Header, nil
}

// classify names the failure of a request: the class and the host. The text of err (which can carry an
// address and a rule) does not leave this function.
func classify(ctx context.Context, err error, host string) *FetchError {
	fe := &FetchError{Host: host}
	var unknownAuthority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostErr x509.HostnameError
	var netErr net.Error
	switch {
	case errors.Is(err, outbound.ErrEgressDenied):
		fe.Class = classEgressDenied
	case errors.Is(err, outbound.ErrTooManyRedirects):
		fe.Class = classTooManyRedirects
	case errors.Is(err, outbound.ErrInsecureRedirect):
		fe.Class = classInsecureRedirect
	case errors.Is(ctx.Err(), context.Canceled):
		fe.Class = classCanceled
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		fe.Class = classTimeout
	case errors.As(err, &unknownAuthority) || errors.As(err, &invalid) || errors.As(err, &hostErr) || strings.Contains(err.Error(), "tls:"):
		fe.Class = classTLS
	default:
		fe.Class = classConnection
	}
	return fe
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// maxAge reads the max-age directive of a Cache-Control header; 0 when there is none or it is not a
// number. no-store and no-cache count as no max-age.
func maxAge(h string) time.Duration {
	for _, d := range strings.Split(h, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if v, ok := strings.CutPrefix(d, "max-age="); ok {
			n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
			if err != nil || n < 0 {
				return 0
			}
			if n > int64(24*365*time.Hour/time.Second) {
				n = int64(24 * 365 * time.Hour / time.Second)
			}
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

// nextRefresh is the delay until the next scheduled fetch of a stored set: the response's max-age clamped
// to [min_refresh_interval, max_refresh_interval], the default interval without one (R-CI-003).
func nextRefresh(cfg config.CISigningKeys, age time.Duration) time.Duration {
	d := age
	if d <= 0 {
		d = cfg.DefaultRefreshInterval
	}
	return min(max(d, cfg.MinRefreshInterval), cfg.MaxRefreshInterval)
}

// validBundleRef checks the syntax of a ca_bundle_ref (Q-CI-31): "file:" and an absolute path to a PEM
// bundle, or "env:" and the name of a variable that holds the PEM text, named TROVE_CIC_<NAME>.
func validBundleRef(ref string) error {
	if p, ok := strings.CutPrefix(ref, "file:"); ok {
		if strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\x00\n\r") {
			return nil
		}
	} else if n, ok := strings.CutPrefix(ref, "env:"); ok && bundleEnvName.MatchString(n) {
		return nil
	}
	return errors.New("a ca_bundle_ref is file:<absolute path> of a PEM bundle or env:" + bundleEnvPrefix + "<NAME>, a variable that holds the PEM text")
}
