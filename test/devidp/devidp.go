// Package devidp is the sign-in provider of the UI development profile (deploy/ui-dev): an OpenID provider over TLS
// whose authorization endpoint lists the profile's users as buttons and signs in the one that is chosen, without a
// password. It serves the discovery document, the key set, the authorization code flow with PKCE (S256), the token
// endpoint with client_secret_basic, userinfo and end_session, which is what trove's login asks of a provider
// (docs/developer/login.md). trove creates each user at its first sign-in (just in time); what a user may do is
// granted in trove, by the seed of make ui-renders or by hand.
//
// It exists for local UI work and the renders of docs/developer/web-ui.md#renders-of-every-screen. Anyone who reaches
// it signs in as any of its users, so it listens on the loopback address only and is never part of an installation.
// It depends on the standard library only.
package devidp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is an account of the provider.
type User struct {
	Username string // preferred_username, the trove username
	Name     string // name, trove's display name
	Email    string
	Kind     string // what the profile's seed makes of the user, shown on the sign-in page
}

// Users are the three kinds of user of R-UI-067: the seed of make ui-renders makes sam a reader, priya the Owner of a
// project and ada an administrator. The usernames are what the seed and the renders refer to.
var Users = []User{
	{Username: "sam", Name: "Sam Taylor", Email: "sam@ui-dev.invalid", Kind: "Plain user: reads and installs"},
	{Username: "priya", Name: "Priya Raman", Email: "priya@ui-dev.invalid", Kind: "Power user: publishes and maintains a project"},
	{Username: "ada", Name: "Ada Brooks", Email: "ada@ui-dev.invalid", Kind: "Administrator"},
}

// Config configures a Provider.
type Config struct {
	Issuer       string // the issuer as the browser and trove reach it, https://localhost:19090 for example
	ClientID     string
	ClientSecret string
	// ClientBase is the origin of the client, trove's base URL (https://localhost:4200): a redirect URI and a post-logout
	// redirect URI must lie below it, as a provider allows only the URIs registered for a client.
	ClientBase string
	Users      []User        // Users when empty
	CodeTTL    time.Duration // how long a code may wait for its exchange; one minute when zero
	Now        func() time.Time
}

// Provider is the issuer. Its methods are safe for concurrent use.
type Provider struct {
	cfg   Config
	key   *rsa.PrivateKey
	kid   string
	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	clientID, redirectURI, challenge, nonce string
	user                                    User
	expires                                 time.Time
}

// New returns a Provider with a fresh RSA-2048 signing key. trove fetches the key set again when it meets an unknown
// key ID, so a restart of the provider needs no restart of trove.
func New(cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.ClientBase == "" {
		return nil, errors.New("devidp: the issuer, the client ID, the client secret and the client base are required")
	}
	cfg.ClientBase = strings.TrimSuffix(cfg.ClientBase, "/")
	if len(cfg.Users) == 0 {
		cfg.Users = Users
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Issuer = strings.TrimSuffix(cfg.Issuer, "/")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, key: key, kid: "devidp-" + randomHex(4), codes: map[string]grant{}}, nil
}

// Handler serves the provider's routes.
func (p *Provider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.chooser)
	mux.HandleFunc("POST /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	mux.HandleFunc("GET /end_session", p.endSession)
	return mux
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	iss := p.cfg.Issuer
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/authorize",
		"token_endpoint":                        iss + "/token",
		"userinfo_endpoint":                     iss + "/userinfo",
		"jwks_uri":                              iss + "/jwks",
		"end_session_endpoint":                  iss + "/end_session",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid,
		"n": b64(p.key.N.Bytes()), "e": b64(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

// The parameters of an authorization request that the chooser carries into its form.
var flowParams = []string{"client_id", "redirect_uri", "state", "nonce", "code_challenge", "code_challenge_method", "response_type", "scope"}

var chooserPage = template.Must(template.New("chooser").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Sign in to the UI development profile</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: system-ui, sans-serif; max-width: 32rem; margin: 3rem auto; padding: 0 1rem">
<h1 style="font-size: 1.5rem">Sign in as</h1>
<p>The sign-in provider of the UI development profile. It asks for no password: never use it outside this profile.</p>
<form method="post" action="/authorize">
{{range .Params}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}{{range .Users}}<p><button type="submit" name="username" value="{{.Username}}" style="font: inherit; padding: .5rem 1rem">{{.Name}} ({{.Username}})</button><br><small>{{.Kind}}</small></p>
{{end}}</form></body></html>`))

type param struct{ Name, Value string }

// chooser checks the authorization request and shows the users to choose from.
func (p *Provider) chooser(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := p.checkRequest(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params := make([]param, 0, len(flowParams))
	for _, name := range flowParams {
		params = append(params, param{name, q.Get(name)})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// No form-action: it would also refuse the redirect to the client's redirect URI that answers the form.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_ = chooserPage.Execute(w, map[string]any{"Params": params, "Users": p.cfg.Users})
}

// authorize signs the chosen user in: a code for the client, sent back to its redirect URI with the state and the issuer.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	if err := p.checkRequest(f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, ok := p.user(f.Get("username"))
	if !ok {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	code := randomHex(16)
	p.mu.Lock()
	p.codes[code] = grant{
		clientID: f.Get("client_id"), redirectURI: f.Get("redirect_uri"), challenge: f.Get("code_challenge"),
		nonce: f.Get("nonce"), user: user, expires: p.cfg.Now().Add(p.cfg.CodeTTL),
	}
	p.mu.Unlock()
	target, err := url.Parse(f.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	target.RawQuery = url.Values{"code": {code}, "state": {f.Get("state")}, "iss": {p.cfg.Issuer}}.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound) //nolint:gosec // checkRequest admits only targets below the client base
}

func (p *Provider) checkRequest(v url.Values) error {
	switch {
	case v.Get("client_id") != p.cfg.ClientID:
		return errors.New("unknown client_id")
	case v.Get("response_type") != "code":
		return errors.New("response_type must be code")
	case v.Get("code_challenge_method") != "S256" || v.Get("code_challenge") == "":
		return errors.New("PKCE with S256 is required")
	case !p.registered(v.Get("redirect_uri")):
		return errors.New("redirect_uri is not below the client base")
	}
	return nil
}

// registered reports whether a redirect target lies below the client's base URL.
func (p *Provider) registered(target string) bool {
	return strings.HasPrefix(target, p.cfg.ClientBase+"/")
}

func (p *Provider) user(username string) (User, bool) {
	for _, u := range p.cfg.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

// token exchanges a code once, after the client's secret and the PKCE verifier are checked.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	user, pass, ok := r.BasicAuth()
	clientID, _ := url.QueryUnescape(user)
	secret, _ := url.QueryUnescape(pass)
	if !ok || clientID != p.cfg.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(p.cfg.ClientSecret)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="devidp"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	g, found := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || p.cfg.Now().After(g.expires) || r.PostForm.Get("grant_type") != "authorization_code" ||
		g.clientID != clientID || g.redirectURI != r.PostForm.Get("redirect_uri") || b64(sum[:]) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	idToken, err := p.idToken(g)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "devidp-" + g.user.Username, "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func (p *Provider) idToken(g grant) (string, error) {
	now := p.cfg.Now()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": p.kid, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": p.cfg.Issuer, "aud": g.clientID, "sub": subject(g.user), "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce, "preferred_username": g.user.Username, "name": g.user.Name, "email": g.user.Email, "email_verified": true,
	})
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// subject is stable across restarts, so trove's link of a user to its subject survives them.
func subject(u User) string { return "devidp-" + u.Username }

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	username, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer devidp-")
	u, known := p.user(username)
	if !ok || !known {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sub": subject(u), "preferred_username": u.Username, "name": u.Name, "email": u.Email, "email_verified": true,
	})
}

// endSession is RP-initiated logout: the provider keeps no session, so it only sends the browser back.
func (p *Provider) endSession(w http.ResponseWriter, r *http.Request) {
	back := r.URL.Query().Get("post_logout_redirect_uri")
	if !p.registered(back) {
		http.Error(w, "post_logout_redirect_uri is not below the client base", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, back, http.StatusFound) //nolint:gosec // registered admits only targets below the client base
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
