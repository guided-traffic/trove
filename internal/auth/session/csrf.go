package session

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

// csrfEventEvery bounds auth.csrf_failed to one per session and minute on each
// replica (R-AUTH-041): a log-volume bound, not a count.
const csrfEventEvery = time.Minute

// maxCSRFDedup bounds the de-duplication map; reaching it starts it again.
const maxCSRFDedup = 10000

// CheckCSRF applies R-AUTH-012 to a request authenticated by the session
// cookie: a safe method passes; POST, PUT, PATCH and DELETE need the
// X-CSRF-Token header whose SHA-256 equals the session's, and an origin that is
// the base URL's. A failure is ErrCSRF (403 csrf_failed) and is audited.
func (s *Store) CheckCSRF(r *http.Request, si Info) error {
	return s.checkCSRF(r, si.ID, si.csrf)
}

func (s *Store) checkCSRF(r *http.Request, sessionID string, want []byte) error {
	if !unsafeMethod(r.Method) {
		return nil
	}
	check := ""
	switch {
	case !tokenMatches(r.Header.Get(HeaderCSRF), want):
		check = "token"
	case !SameOrigin(r, s.origin(r)):
		check = "origin"
	default:
		return nil
	}
	s.m.csrfFailed.Inc()
	s.auditCSRF(r, sessionID, check)
	return ErrCSRF
}

func (s *Store) origin(r *http.Request) string {
	if s.o.Origin == nil {
		return ""
	}
	return s.o.Origin(r)
}

func unsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// tokenMatches compares the SHA-256 of the presented token with the stored one
// in constant time.
func tokenMatches(token string, want []byte) bool {
	if token == "" || len(want) != 32 {
		return false
	}
	return subtle.ConstantTimeCompare(Hash(token), want) == 1
}

// SameOrigin is check 2 of R-AUTH-012: the Origin header equals the base URL's
// origin, or Origin is absent and Sec-Fetch-Site is same-origin. Both absent is
// a refusal, as is an empty expected origin. The local login applies the same
// check (R-AUTH-014).
func SameOrigin(r *http.Request, expected string) bool {
	exp := NormalizeOrigin(expected)
	if exp == "" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return NormalizeOrigin(o) == exp
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// NormalizeOrigin returns scheme://host[:port] in lower case without a default
// port, or "" for anything that is not an http or https origin ("null"
// included).
func NormalizeOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}

// auditCSRF writes auth.csrf_failed at most once per session and minute on this
// replica.
func (s *Store) auditCSRF(r *http.Request, sessionID, check string) {
	now := time.Now()
	s.cmu.Lock()
	if last, ok := s.csrfLast[sessionID]; ok && now.Sub(last) < csrfEventEvery {
		s.cmu.Unlock()
		return
	}
	if len(s.csrfLast) >= maxCSRFDedup {
		s.csrfLast = map[string]time.Time{}
	}
	s.csrfLast[sessionID] = now
	s.cmu.Unlock()
	details, _ := json.Marshal(map[string]string{"session_id": sessionID, "method": r.Method, "path": truncate(r.URL.Path, 512), "check": check})
	s.o.Audit.Record(r.Context(), audit.Event{
		Type: audit.EventAuthCSRFFailed, Actor: audit.Actor{Kind: "user"}, Outcome: audit.Failure, Reason: "csrf_failed",
		Source: s.source(r), Details: details,
	})
}

// SourceFunc fills the source of an event from a request; the composition root
// sets it to read the client address the chain resolved (R-ARCH-006).
type SourceFunc func(*http.Request) audit.Source

// source returns the event source of a request: the Source option, or the user
// agent alone.
func (s *Store) source(r *http.Request) audit.Source {
	if s.o.Source != nil {
		return s.o.Source(r)
	}
	return audit.Source{UserAgent: r.UserAgent()}
}
