package proxytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy"
)

// Tokens is a token service's state: the tokens it issued and which of them its registry still accepts. It knows no registry
// protocol; the generic handshake of [Adapter] reads {"token": …, "expires_in": …}.
type Tokens struct {
	// TTL is the expires_in the service reports, in seconds; 0 reports none.
	TTL int
	// Pad is the number of bytes of padding the answer carries, to exceed a bound.
	Pad int
	// Delay is the wait before an answer.
	Delay time.Duration
	// Status replaces the answer's status (0 is 200).
	Status int

	mu     sync.Mutex
	issued int
	valid  map[string]bool
}

// NewTokens returns a service that reports a lifetime of ttl seconds.
func NewTokens(ttl int) *Tokens { return &Tokens{TTL: ttl, valid: map[string]bool{}} }

// Issued is the number of tokens the service has issued.
func (t *Tokens) Issued() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.issued
}

// Accept reports whether the registry accepts the token.
func (t *Tokens) Accept(token string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.valid[token]
}

// Revoke makes the registry refuse a token it issued.
func (t *Tokens) Revoke(token string) {
	t.mu.Lock()
	delete(t.valid, token)
	t.mu.Unlock()
}

// issue creates the next token.
func (t *Tokens) issue() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issued++
	tok := "tok-" + strconv.Itoa(t.issued)
	t.valid[tok] = true
	return tok
}

// TokenService makes path an endpoint that issues a token per request. The scope and service the client asked for are in the
// request the upstream records (Seen has the host and the Authorization header, the query is in RawQuery of the request the
// route's handler sees; a test that needs it sets its own Handler).
func (u *Upstream) TokenService(path string, t *Tokens) {
	u.Set(path, Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		if t.Delay > 0 {
			select {
			case <-time.After(t.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if t.Status != 0 && t.Status != http.StatusOK {
			http.Error(w, "refused", t.Status)
			return
		}
		doc := map[string]any{"token": t.issue()}
		if t.TTL > 0 {
			doc["expires_in"] = t.TTL
		}
		if t.Pad > 0 {
			doc["padding"] = strings.Repeat("x", t.Pad)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}})
}

// Challenge makes path answer 401 with `WWW-Authenticate: Bearer realm="<realm>",service="test"` unless the request carries
// `Authorization: Bearer <token>` for a token accept takes; then it answers as the route r does (status, body, type, ETag and
// headers; no conditional handling). A HEAD request gets the headers of r and no body.
func (u *Upstream) Challenge(path, realm string, accept func(token string) bool, r Route) {
	u.Set(path, Route{Handler: func(w http.ResponseWriter, req *http.Request) {
		token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !ok || !accept(token) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="test"`, realm))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h := w.Header()
		if r.Type != "" {
			h.Set("Content-Type", r.Type)
		}
		if r.ETag != "" {
			h.Set("ETag", r.ETag)
		}
		for k, v := range r.Header {
			h.Set(k, v)
		}
		status := r.Status
		if status == 0 {
			status = http.StatusOK
		}
		h.Set("Content-Length", strconv.Itoa(len(r.Body)))
		w.WriteHeader(status)
		if req.Method != http.MethodHead {
			_, _ = w.Write(r.Body)
		}
	}})
}

// bearerRealm reads the realm of a Bearer challenge.
func bearerRealm(h http.Header) (string, bool) {
	for _, v := range h.Values("Www-Authenticate") {
		rest, ok := strings.CutPrefix(v, "Bearer ")
		if !ok {
			continue
		}
		for _, part := range strings.Split(rest, ",") {
			if k, val, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k == "realm" {
				return strings.Trim(val, `"`), true
			}
		}
	}
	return "", false
}

// The handshake of the generic adapter ([Adapter.Auth]): the realm is read from the challenge, as a registry client reads it, so
// the upstream chooses the host the engine is asked to send a request to.

var (
	_ proxy.Authenticator = (*Adapter)(nil)
	_ proxy.Revalidator   = (*Adapter)(nil)
)

// TokenRequest is a GET of the challenge's realm with the service and a scope named after the request: the token is kept per
// scope, so two paths share a token when they name the same scope (see [Adapter.ScopeOf]).
func (a *Adapter) TokenRequest(ctx context.Context, _ proxy.UpstreamBase, orig *http.Request, path string, status int, challenge http.Header) (*http.Request, string, bool, error) {
	a.tokenRequests.Add(1)
	if !a.Auth || status != http.StatusUnauthorized {
		return nil, "", false, nil
	}
	realm, ok := bearerRealm(challenge)
	if !ok {
		return nil, "", false, nil
	}
	scope := "pull:" + orig.URL.Path
	if a.ScopeOf != nil {
		scope = a.ScopeOf(orig, path)
	}
	u, err := url.Parse(realm)
	if err != nil {
		return nil, "", false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "the realm is not a URL", errs.WithCause(err))
	}
	q := u.Query()
	q.Set("service", "test")
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	return req, scope, true, err
}

// ReadToken reads {"token": …, "expires_in": …}; a status that is not 200 is an error.
func (a *Adapter) ReadToken(_ context.Context, status int, body []byte) (string, time.Duration, error) {
	if status != http.StatusOK {
		return "", 0, errors.New("the token service refused")
	}
	var doc struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", 0, err
	}
	return "Bearer " + doc.Token, time.Duration(doc.ExpiresIn) * time.Second, nil
}

// TokenRequests is the number of times the engine asked for a token request.
func (a *Adapter) TokenRequests() int { return int(a.tokenRequests.Load()) }

// DigestHeader is the header of the generic revalidation: the hexadecimal SHA-256 of the document the upstream holds.
const DigestHeader = "X-Test-Digest"

// RevalidationRequest is a HEAD derived from the engine's GET (the same URL and headers) when [Adapter.HeadRevalidation] is set; else the engine's conditional
// GET.
func (a *Adapter) RevalidationRequest(ctx context.Context, _ proxy.UpstreamBase, _, _ string, _ proxy.Document, get *http.Request) (*http.Request, bool, error) {
	if !a.HeadRevalidation {
		return nil, false, nil
	}
	a.revalidations.Add(1)
	a.mu.Lock()
	a.revalidationGets = append(a.revalidationGets, get.URL.String()+" "+get.Header.Get("Accept"))
	a.mu.Unlock()
	req := get.Clone(ctx)
	req.Method = http.MethodHead
	return req, true, nil
}

// Unchanged compares [DigestHeader] with the stored document's digest; an answer without the header is changed.
func (a *Adapter) Unchanged(_ string, stored proxy.Document, resp *http.Response) bool {
	return resp.Header.Get(DigestHeader) == fmt.Sprintf("%x", stored.Digest)
}

// Revalidations is the number of revalidation requests the adapter built.
func (a *Adapter) Revalidations() int { return int(a.revalidations.Load()) }

// RevalidationGets are the URL and Accept header of the GET the engine handed to each revalidation request.
func (a *Adapter) RevalidationGets() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.revalidationGets...)
}
