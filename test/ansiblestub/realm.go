package ansiblestub

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The realm: a single-sign-on token endpoint on its own host name, and the switch that makes the Galaxy host accept only the
// access tokens it issued, as the hub behind a single sign-on does (R-ANS-019). The endpoint takes the refresh-token grant
// (grant_type, client_id, refresh_token, form encoded) and answers the access token with its lifetime; a refresh token that is not
// in Realm.RefreshTokens is answered 400 invalid_grant. The mechanism is the documented one; the service itself is not reachable
// from the tests.

// DefaultRealmPath is the path of the token endpoint on the realm host.
const DefaultRealmPath = "/auth/realms/test/protocol/openid-connect/token"

// DefaultAccessTTLSeconds is the lifetime of an access token when Realm.AccessTTLSeconds is 0.
const DefaultAccessTTLSeconds = 300

// Realm is the configuration of the realm. The zero value requires nothing: the Galaxy host accepts any request.
type Realm struct {
	// Require makes the Galaxy host answer 401 to a request that has no valid access token: "Authorization: Bearer <token>" with a
	// token the realm issued, not expired and not revoked.
	Require bool `json:"require,omitempty"`
	// RefreshTokens are the offline tokens the realm accepts.
	RefreshTokens []string `json:"refresh_tokens,omitempty"`
	// ClientID, when set, is the client identifier the grant must carry.
	ClientID string `json:"client_id,omitempty"`
	// AccessTTLSeconds is the lifetime of the access tokens; 0 means DefaultAccessTTLSeconds.
	AccessTTLSeconds int `json:"access_ttl_seconds,omitempty"`
}

// Validate rejects a realm the stub does not implement.
func (r Realm) Validate() error {
	if r.AccessTTLSeconds < 0 || r.AccessTTLSeconds > MaxThrottleSeconds {
		return fmt.Errorf("access_ttl_seconds %d out of range", r.AccessTTLSeconds)
	}
	for _, t := range r.RefreshTokens {
		if t == "" {
			return fmt.Errorf("refresh_tokens: an empty token")
		}
	}
	return nil
}

// RealmState is the realm with what it saw: the exchanges it answered and refused, the access tokens it issued, and the requests
// the Galaxy host accepted and refused for want of a valid token.
type RealmState struct {
	Realm
	// Exchanges is the number of grants answered with an access token, Failed the number refused.
	Exchanges int `json:"exchanges"`
	Failed    int `json:"failed"`
	// ClientIDs lists the client identifiers the grants carried, in order of the first appearance.
	ClientIDs []string `json:"client_ids,omitempty"`
	// Issued lists the access tokens in the order they were issued.
	Issued []string `json:"issued,omitempty"`
	// Accepted and Rejected count the requests of the Galaxy host decided by the requirement while it is on.
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	// RejectedTokens lists the bearer tokens that were refused (an expired or revoked one, one nobody issued), without repeats.
	RejectedTokens []string `json:"rejected_tokens,omitempty"`
}

type issuedToken struct {
	token   string
	expires time.Time
	revoked bool
}

// realmState is the realm's mutable state; the Server's mutex guards it.
type realmState struct {
	cfg       Realm
	exchanges int
	failed    int
	clientIDs []string
	tokens    []*issuedToken
	accepted  int
	rejected  int
	rejTokens []string
}

// SetRealm replaces the realm configuration and forgets what the realm issued and counted.
func (s *Server) SetRealm(r Realm) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.RefreshTokens = slices.Clone(r.RefreshTokens)
	s.realm = realmState{cfg: r}
	return nil
}

// RealmState returns the realm with its counters.
func (s *Server) RealmState() RealmState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.realm
	out := RealmState{
		Realm: st.cfg, Exchanges: st.exchanges, Failed: st.failed, ClientIDs: slices.Clone(st.clientIDs),
		Accepted: st.accepted, Rejected: st.rejected, RejectedTokens: slices.Clone(st.rejTokens),
	}
	out.RefreshTokens = slices.Clone(st.cfg.RefreshTokens)
	for _, t := range st.tokens {
		out.Issued = append(out.Issued, t.token)
	}
	return out
}

// ExpireAccessTokens revokes every access token issued so far: the next request that carries one is answered 401, and the client
// has to exchange its refresh token again. It returns the number of tokens that were still valid.
func (s *Server) ExpireAccessTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	n := 0
	for _, t := range s.realm.tokens {
		if !t.revoked && now.Before(t.expires) {
			n++
		}
		t.revoked = true
	}
	return n
}

// checkBearer decides a request of the Galaxy host under the realm requirement and reports whether it may go on.
func (s *Server) checkBearer(authorization string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.realm.cfg.Require {
		return true
	}
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if ok {
		now := s.cfg.Now()
		for _, t := range s.realm.tokens {
			if t.token == token && !t.revoked && now.Before(t.expires) {
				s.realm.accepted++
				return true
			}
		}
	}
	s.realm.rejected++
	if ok && !slices.Contains(s.realm.rejTokens, token) {
		s.realm.rejTokens = append(s.realm.rejTokens, token)
	}
	return false
}

// serveRealm answers the token endpoint.
func (s *Server) serveRealm(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != s.cfg.RealmPath {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeText(w, http.StatusMethodNotAllowed, "the token endpoint takes POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		s.realmError(w, http.StatusBadRequest, "invalid_request", "the form cannot be read")
		return
	}
	form := r.PostForm
	clientID := form.Get("client_id")
	if form.Get("grant_type") != "refresh_token" {
		s.realmError(w, http.StatusBadRequest, "unsupported_grant_type", "only the refresh_token grant is supported")
		return
	}

	s.mu.Lock()
	cfg := s.realm.cfg
	if clientID != "" && !slices.Contains(s.realm.clientIDs, clientID) {
		s.realm.clientIDs = append(s.realm.clientIDs, clientID)
	}
	s.mu.Unlock()

	if cfg.ClientID != "" && clientID != cfg.ClientID {
		s.realmError(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}
	if !slices.Contains(cfg.RefreshTokens, form.Get("refresh_token")) {
		s.realmError(w, http.StatusBadRequest, "invalid_grant", "Offline user session not found or expired")
		return
	}

	ttl := cfg.AccessTTLSeconds
	if ttl == 0 {
		ttl = DefaultAccessTTLSeconds
	}
	s.mu.Lock()
	s.tokenSeq++
	tok := &issuedToken{token: fmt.Sprintf("access-%d", s.tokenSeq), expires: s.cfg.Now().Add(time.Duration(ttl) * time.Second)}
	s.realm.tokens = append(s.realm.tokens, tok)
	s.realm.exchanges++
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok.token, "expires_in": ttl, "refresh_expires_in": 0, "token_type": "Bearer", "scope": "openid",
	})
}

func (s *Server) realmError(w http.ResponseWriter, status int, code, description string) {
	s.mu.Lock()
	s.realm.failed++
	s.mu.Unlock()
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}
