package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestR_AUTH_010_Cookies(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookies(rec, NewSecret(), NewSecret())
	set := rec.Header().Values("Set-Cookie")
	t.Run("R-AUTH-010 the session cookie is __Host-, Secure, HttpOnly, SameSite=Lax, Path=/ and carries no Domain, Max-Age or Expires", func(t *testing.T) {
		s := set[0]
		if !strings.HasPrefix(s, CookieSession+"=") || !strings.Contains(s, "Path=/") || !strings.Contains(s, "Secure") ||
			!strings.Contains(s, "HttpOnly") || !strings.Contains(s, "SameSite=Lax") ||
			strings.Contains(s, "Domain") || strings.Contains(s, "Max-Age") || strings.Contains(s, "Expires") {
			t.Fatal(s)
		}
	})
	t.Run("R-AUTH-012 the CSRF cookie is __Host-, Secure, SameSite=Strict, readable by the UI", func(t *testing.T) {
		s := set[1]
		if !strings.HasPrefix(s, CookieCSRF+"=") || !strings.Contains(s, "SameSite=Strict") || strings.Contains(s, "HttpOnly") ||
			!strings.Contains(s, "Secure") || !strings.Contains(s, "Path=/") || strings.Contains(s, "Domain") {
			t.Fatal(s)
		}
	})
	t.Run("R-AUTH-013 clearing keeps Secure and Path=/ with Max-Age=0, or the browser keeps a __Host- cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ClearCookies(rec)
		for _, s := range rec.Header().Values("Set-Cookie") {
			if !strings.Contains(s, "Max-Age=0") || !strings.Contains(s, "Secure") || !strings.Contains(s, "Path=/") || strings.Contains(s, "Domain") {
				t.Fatal(s)
			}
		}
	})
	t.Run("R-AUTH-005 the binding cookie is __Host-, HttpOnly, Lax, Path=/ with Max-Age 600", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SetBindingCookie(rec, NewSecret(), 10*time.Minute)
		s := rec.Header().Get("Set-Cookie")
		if !strings.HasPrefix(s, CookieBinding+"=") || !strings.Contains(s, "Max-Age=600") || !strings.Contains(s, "HttpOnly") ||
			!strings.Contains(s, "SameSite=Lax") || !strings.Contains(s, "Path=/") || strings.Contains(s, "Domain") {
			t.Fatal(s)
		}
	})
	t.Run("R-AUTH-010 a secret is 43 characters of base64url; anything else is refused without a lookup", func(t *testing.T) {
		s := NewSecret()
		if len(s) != 43 || !WellFormed(s) || WellFormed(s+"x") || WellFormed(strings.Repeat("!", 43)) || WellFormed("") {
			t.Fatal(s)
		}
	})
}

// recorderSpy collects audit events.
type recorderSpy struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (r *recorderSpy) RecordTx(ctx context.Context, _ meta.Tx, ev audit.Event) error {
	r.Record(ctx, ev)
	return nil
}

func (r *recorderSpy) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
}

func (r *recorderSpy) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func bareStore(t *testing.T, rec audit.Recorder) *Store {
	t.Helper()
	m, err := newMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{o: Options{Audit: rec, Origin: func(*http.Request) string { return "https://trove.test" }}, m: m, csrfLast: map[string]time.Time{}}
}

func TestR_AUTH_012_CSRF(t *testing.T) {
	token := NewSecret()
	info := Info{ID: "s1", csrf: Hash(token)}
	req := func(method string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(method, "https://trove.test/api/v1/tokens", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		ok     bool
	}{
		{"a safe method passes without the header", http.MethodGet, nil, true},
		{"POST with the token and a same Origin passes", http.MethodPost, map[string]string{HeaderCSRF: token, "Origin": "https://trove.test"}, true},
		{"POST with the token, no Origin and Sec-Fetch-Site same-origin passes", http.MethodPost, map[string]string{HeaderCSRF: token, "Sec-Fetch-Site": "same-origin"}, true},
		{"POST with the default port in Origin passes", http.MethodPost, map[string]string{HeaderCSRF: token, "Origin": "https://TROVE.test:443"}, true},
		{"POST without the header is refused", http.MethodPost, map[string]string{"Origin": "https://trove.test"}, false},
		{"DELETE with a wrong token is refused", http.MethodDelete, map[string]string{HeaderCSRF: NewSecret(), "Origin": "https://trove.test"}, false},
		{"PUT with the token from another origin is refused", http.MethodPut, map[string]string{HeaderCSRF: token, "Origin": "https://evil.test"}, false},
		{"PATCH with the token and neither Origin nor Sec-Fetch-Site is refused", http.MethodPatch, map[string]string{HeaderCSRF: token}, false},
		{"POST with Origin null is refused", http.MethodPost, map[string]string{HeaderCSRF: token, "Origin": "null"}, false},
		{"POST with Sec-Fetch-Site cross-site is refused", http.MethodPost, map[string]string{HeaderCSRF: token, "Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, c := range cases {
		t.Run("R-AUTH-012 "+c.name, func(t *testing.T) {
			err := bareStore(t, &recorderSpy{}).CheckCSRF(req(c.method, c.hdr), info)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("R-AUTH-041 auth.csrf_failed is written at most once per session and minute on a replica", func(t *testing.T) {
		spy := &recorderSpy{}
		s := bareStore(t, spy)
		for range 5 {
			_ = s.CheckCSRF(req(http.MethodPost, nil), info)
		}
		_ = s.CheckCSRF(req(http.MethodPost, nil), Info{ID: "s2", csrf: Hash(token)})
		if n := spy.count(audit.EventAuthCSRFFailed); n != 2 {
			t.Fatalf("events = %d, want 2", n)
		}
	})
	t.Run("R-AUTH-012 the error maps to 403 csrf_failed and ErrInvalid to 401 session_expired", func(t *testing.T) {
		if !strings.Contains(Problem(ErrCSRF).Error(), "csrf_failed") || !strings.Contains(Problem(ErrInvalid).Error(), "session_expired") {
			t.Fatal("mapping")
		}
	})
}

func TestR_AUTH_023_SessionChanges(t *testing.T) {
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := registerCache(reg, time.Minute, 10)
	a, b := Hash(NewSecret()), Hash(NewSecret())
	fill := func() {
		c.Set(string(a), &entry{id: "a"})
		c.Set(string(b), &entry{id: "b"})
	}
	apply := func(v int, payload any) {
		raw, _ := json.Marshal(payload)
		reg.ApplyChange(ChangeArea, v, raw)
	}
	ended := func(h []byte) principal.Change {
		return principal.Change{Kind: principal.ChangeSession, Sessions: []principal.SessionRef{{ID: "a", Hash: hex.EncodeToString(h)}}}
	}
	t.Run("R-AUTH-023 a session change evicts exactly that session by the hash of its secret", func(t *testing.T) {
		fill()
		apply(principal.ChangeVersion, ended(a))
		if _, ok := c.Get(string(a)); ok {
			t.Fatal("a survived")
		}
		if _, ok := c.Get(string(b)); !ok {
			t.Fatal("b was evicted")
		}
	})
	t.Run("R-AUTH-023 Q-AUTH-22 token, membership, group and settings changes leave the session cache alone", func(t *testing.T) {
		for _, k := range []string{principal.ChangeToken, principal.ChangeMembership, principal.ChangeGroup, principal.ChangeSettings} {
			fill()
			apply(principal.ChangeVersion, principal.Change{Kind: k, IDs: []string{"x"}})
			if c.Len() != 2 {
				t.Fatalf("%s: len = %d", k, c.Len())
			}
		}
	})
	t.Run("R-AUTH-023 any other change of the area auth flushes, since entries hold the principal's status", func(t *testing.T) {
		for _, p := range []any{
			principal.Change{Kind: principal.ChangePrincipal, IDs: []string{"p"}},
			principal.Change{Kind: principal.ChangeAll},
			principal.Change{Kind: principal.ChangeSession},
			principal.Change{Kind: "unknown"},
			"garbage",
		} {
			fill()
			apply(principal.ChangeVersion, p)
			if c.Len() != 0 {
				t.Fatalf("%v: len = %d", p, c.Len())
			}
		}
		fill()
		apply(principal.ChangeVersion+1, ended(a))
		if c.Len() != 0 {
			t.Fatal("an unknown payload version must flush")
		}
	})
}

func TestR_AUTH_011_RecentAuthentication(t *testing.T) {
	t.Run("R-AUTH-011 recent authentication compares the replica's own clock", func(t *testing.T) {
		i := Info{authLocal: time.Now().Add(-90 * time.Minute)}
		if i.AuthenticatedWithin(time.Hour) || !i.AuthenticatedWithin(2*time.Hour) || (Info{}).AuthenticatedWithin(time.Hour) {
			t.Fatal("window")
		}
	})
	t.Run("R-AUTH-010 the kept last_seen_at only moves forward", func(t *testing.T) {
		now := time.Now()
		e := &entry{lastSeen: now}
		e.advance(now.Add(-time.Minute))
		if !e.seen().Equal(now) {
			t.Fatal("moved back")
		}
		e.advance(now.Add(time.Second))
		if !e.seen().Equal(now.Add(time.Second)) {
			t.Fatal("did not move forward")
		}
	})
}
