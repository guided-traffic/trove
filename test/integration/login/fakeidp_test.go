//go:build integration

package login

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// idpUser is an account of the fake provider.
type idpUser struct {
	sub, username, email, name string
	groups                     any // []string, absent when nil; any other value for wrong_type
	overage                    bool
}

// fakeIDP is an in-process OpenID Provider over TLS: discovery, a JWK Set with
// key rotation, an authorization endpoint that approves at once, the token
// endpoint with PKCE and client authentication, userinfo and end_session. The
// switches make it misbehave the way R-AUTH-006's acceptance criteria name.
type fakeIDP struct {
	t      *testing.T
	srv    *httptest.Server
	issuer string
	secret string

	mu        sync.Mutex
	keys      []*rsa.PrivateKey // keys[0] signs; all are published
	kids      []string
	user      idpUser
	codes     map[string]*grant
	sessions  map[string]bool // IdP sessions by subject; ended by end_session
	endCalls  []url.Values
	tokenHits int

	// Switches.
	wrongNonce      bool
	unpublishedKey  bool
	algNone         bool
	extraAudience   string
	tokenDelay      time.Duration
	issuerInDoc     string
	advertiseIss    bool
	omitUserinfoSub bool
}

type grant struct {
	clientID, redirectURI, challenge, nonce string
	user                                    idpUser
}

func newFakeIDP(t *testing.T, ca *tlstest.CA, secret string) *fakeIDP {
	t.Helper()
	f := &fakeIDP{t: t, secret: secret, codes: map[string]*grant{}, sessions: map[string]bool{}}
	f.rotate()
	f.user = idpUser{sub: "sub-alice", username: "alice", email: "alice@example.com", name: "Alice Example", groups: []string{"trove-team-web", "unrelated"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", f.userinfo)
	mux.HandleFunc("GET /end_session", f.endSession)
	f.srv = httptest.NewUnstartedServer(mux)
	leaf := ca.Issue(t, tlstest.Options{})
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf.TLS}, MinVersion: tls.VersionTLS12}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

// rotate adds a new signing key in front; the old ones stay published.
func (f *fakeIDP) rotate() {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.keys = append([]*rsa.PrivateKey{k}, f.keys...)
	f.kids = append([]string{"k" + randHex(4)}, f.kids...)
	f.mu.Unlock()
}

func (f *fakeIDP) set(fn func(*fakeIDP)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeIDP) discovery(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	iss := f.issuer
	if f.issuerInDoc != "" {
		iss = f.issuerInDoc
	}
	adv := f.advertiseIss
	f.mu.Unlock()
	writeJSON(w, map[string]any{
		"issuer": iss, "authorization_endpoint": f.issuer + "/authorize", "token_endpoint": f.issuer + "/token",
		"userinfo_endpoint": f.issuer + "/userinfo", "jwks_uri": f.issuer + "/jwks", "end_session_endpoint": f.issuer + "/end_session",
		"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": adv,
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIDP) jwks(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []map[string]any
	for i, k := range f.keys {
		keys = append(keys, map[string]any{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kids[i],
			"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes()),
		})
	}
	writeJSON(w, map[string]any{"keys": keys})
}

// authorize approves at once, like a provider with a live session.
func (f *fakeIDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := randHex(16)
	f.mu.Lock()
	f.codes[code] = &grant{clientID: q.Get("client_id"), redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), user: f.user}
	f.sessions[f.user.sub] = true
	f.mu.Unlock()
	target, _ := url.Parse(q.Get("redirect_uri"))
	v := url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {f.issuer}}
	target.RawQuery = v.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (f *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokenHits++
	delay := f.tokenDelay
	f.mu.Unlock()
	time.Sleep(delay)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user, pass, ok := r.BasicAuth()
	clientID, _ := url.QueryUnescape(user)
	secret, _ := url.QueryUnescape(pass)
	f.mu.Lock()
	want := f.secret
	g := f.codes[r.Form.Get("code")]
	delete(f.codes, r.Form.Get("code"))
	f.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(want)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if g == nil || g.clientID != clientID || g.redirectURI != r.Form.Get("redirect_uri") || b64(sum[:]) != g.challenge {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	writeJSON(w, map[string]any{"access_token": "at-" + g.user.sub, "token_type": "Bearer", "expires_in": 300, "id_token": f.idToken(g)})
}

func (f *fakeIDP) idToken(g *grant) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	c := map[string]any{
		"iss": f.issuer, "aud": g.clientID, "sub": g.user.sub, "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "nonce": g.nonce, "preferred_username": g.user.username, "email": g.user.email,
		"email_verified": true, "name": g.user.name, "sid": "idp-sid-" + g.user.sub,
	}
	if g.user.groups != nil {
		c["groups"] = g.user.groups
	}
	if g.user.overage {
		c["_claim_names"] = map[string]any{"groups": "src1"}
	}
	if f.wrongNonce {
		c["nonce"] = "not-the-flow-nonce"
	}
	if f.extraAudience != "" {
		c["aud"] = []string{g.clientID, f.extraAudience}
		c["azp"] = g.clientID
	}
	key, kid := f.keys[0], f.kids[0]
	if f.unpublishedKey {
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		key = k
	}
	alg := "RS256"
	if f.algNone {
		alg = "none"
	}
	hb, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
	pb, _ := json.Marshal(c)
	input := b64(hb) + "." + b64(pb)
	if f.algNone {
		return input + "."
	}
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Error(err)
	}
	return input + "." + b64(sig)
}

func (f *fakeIDP) userinfo(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "at-")
	f.mu.Lock()
	omit := f.omitUserinfoSub
	f.mu.Unlock()
	if omit {
		sub = "someone-else"
	}
	writeJSON(w, map[string]any{"sub": sub})
}

// endSession is RP-Initiated Logout 1.0 §2: it ends the provider's session of
// the hinted user and redirects to the registered post-logout URI.
func (f *fakeIDP) endSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.endCalls = append(f.endCalls, q)
	f.mu.Unlock()
	http.Redirect(w, r, q.Get("post_logout_redirect_uri"), http.StatusFound)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
