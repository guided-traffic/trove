package ocistub

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The token realm and the check of the registry. A client that gets 401 with
//
//	WWW-Authenticate: Bearer realm="<realm>/token",service="<service>",scope="repository:<name>:pull"
//
// asks the realm (GET /token?service=...&scope=...) for a token, with Basic credentials or without, and retries with
// Authorization: Bearer <token>. A token is signed, carries the scopes the realm granted, an expiry and the revocation epoch it
// was issued in (Server.RevokeTokens). The realm grants only the pull action, whatever a client asks for.

// access is a scope the realm granted.
type access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

type claims struct {
	Subject string   `json:"sub,omitempty"`
	Access  []access `json:"access"`
	Issued  int64    `json:"iat"`
	Expires int64    `json:"exp"`
	Epoch   int      `json:"epoch"`
	Seq     int      `json:"seq"`
}

var tokenKey = []byte("ocistub token signing key (a test double)")

func sign(payload []byte) string {
	mac := hmac.New(sha256.New, tokenKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
}

var errToken = errors.New("invalid token")

func verify(token string) (claims, error) {
	var c claims
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok {
		return c, errToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return c, errToken
	}
	mac := hmac.New(sha256.New, tokenKey)
	mac.Write(payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) != 1 {
		return c, errToken
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, errToken
	}
	return c, nil
}

// parseScopes reads the scope parameters of a token request: "type:name:action,action", several per parameter separated by
// spaces.
func parseScopes(values []string) []access {
	var out []access
	for _, v := range values {
		for _, one := range strings.Fields(v) {
			parts := strings.SplitN(one, ":", 3)
			if len(parts) != 3 {
				continue
			}
			out = append(out, access{Type: parts[0], Name: parts[1], Actions: strings.Split(parts[2], ",")})
		}
	}
	return out
}

// serveToken is the realm.
func (s *Server) serveToken(w http.ResponseWriter, r *http.Request, m Mode) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		return
	}
	if m.RefuseToken != 0 {
		writeError(w, m.RefuseToken, "UNAUTHORIZED", "the realm refuses to issue a token")
		return
	}
	q := r.URL.Query()
	if q.Get("service") != s.cfg.Service {
		writeError(w, http.StatusBadRequest, "UNAUTHORIZED", "invalid service "+q.Get("service"))
		return
	}
	user, pass, hasCreds := r.BasicAuth()
	if hasCreds && (subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) != 1) {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+s.cfg.Service+`"`)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "incorrect username or password")
		return
	}
	if !hasCreds && m.auth() == AuthBasic {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+s.cfg.Service+`"`)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	granted := []access{}
	for _, a := range parseScopes(q["scope"]) {
		switch {
		case a.Type == "repository" && slices.Contains(m.PrivateRepos, a.Name) && !hasCreds:
			// private to an anonymous client: a token without the scope, which is what Docker Hub issues
		case a.Type == "repository":
			granted = append(granted, access{Type: a.Type, Name: a.Name, Actions: intersect(a.Actions, "pull")})
		case a.Type == "registry" && a.Name == "catalog":
			granted = append(granted, access{Type: a.Type, Name: a.Name, Actions: []string{"*"}})
		}
	}
	ttl := positiveOr(m.TokenTTLSeconds, DefaultTokenTTL)
	now := s.cfg.Now()
	s.mu.Lock()
	s.seq++
	c := claims{Access: granted, Issued: now.Unix(), Expires: now.Add(time.Duration(ttl) * time.Second).Unix(), Epoch: s.epoch, Seq: s.seq}
	s.mu.Unlock()
	if hasCreds {
		c.Subject = user
	}
	payload, err := json.Marshal(c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	token := sign(payload)
	body := map[string]any{"token": token, "access_token": token, "issued_at": now.UTC().Format(time.RFC3339)}
	if !m.TokenOmitExpiry {
		body["expires_in"] = ttl
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

// intersect keeps the requested actions that are in the allowed ones; "*" counts as a request for all of them.
func intersect(requested []string, allowed ...string) []string {
	out := []string{}
	for _, a := range allowed {
		if slices.Contains(requested, a) || slices.Contains(requested, "*") {
			out = append(out, a)
		}
	}
	return out
}

// need is the scope a route requires: type, name and action; the type is empty for the base path.
func need(rt route) (typ, name, action string) {
	switch rt.kind {
	case KindManifest, KindBlob, KindTags, KindReferrers:
		return "repository", rt.name, "pull"
	case KindCatalog:
		return "registry", "catalog", "*"
	}
	return "", "", ""
}

// authorize checks the credentials of a registry request and answers the challenge when they do not do. It reports whether the
// request goes on.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) bool {
	if m.auth() == AuthNone {
		return true
	}
	typ, name, action := need(rt)
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	reason := ""
	if hasBearer {
		reason = "invalid_token"
		switch {
		case m.StaticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(m.StaticToken)) == 1:
			return true
		default:
			if c, err := verify(token); err == nil && s.live(c) {
				reason = "insufficient_scope"
				if typ == "" || covers(c, typ, name, action) {
					return true
				}
			}
		}
	}
	challenge := `Bearer realm="` + b.realm + tokenPath + `",service="` + s.cfg.Service + `"`
	if typ != "" {
		challenge += `,scope="` + typ + ":" + name + ":" + action + `"`
	}
	if reason != "" {
		challenge += `,error="` + reason + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if typ == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return false
	}
	detail := []map[string]string{{"Type": typ, "Class": "", "Name": name, "Action": action}}
	writeErrorDetail(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", detail)
	return false
}

// live reports whether a token is unexpired and of the current epoch.
func (s *Server) live(c claims) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.Epoch == s.epoch && c.Expires > s.cfg.Now().Unix()
}

func covers(c claims, typ, name, action string) bool {
	for _, a := range c.Access {
		if a.Type == typ && a.Name == name && (slices.Contains(a.Actions, action) || slices.Contains(a.Actions, "*")) {
			return true
		}
	}
	return false
}
