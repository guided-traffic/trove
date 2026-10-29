package errs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKindStatus(t *testing.T) {
	cases := []struct {
		kind   Kind
		fields []FieldError
		want   int
	}{
		{Internal, nil, 500},
		{Invalid, nil, 400},
		{Invalid, []FieldError{{Pointer: "/a", Code: "x"}}, 422},
		{Unauthenticated, nil, 401},
		{Forbidden, nil, 403},
		{NotFound, nil, 404},
		{MethodNotAllowed, nil, 405},
		{Conflict, nil, 409},
		{PreconditionFailed, nil, 412},
		{TooLarge, nil, 413},
		{Unsupported, nil, 415},
		{RateLimited, nil, 429},
		{InsufficientStorage, nil, 507},
		{Unavailable, nil, 503},
		{UpstreamFailed, nil, 502},
		{UpstreamTimeout, nil, 504},
		{Misdirected, nil, 421},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("R-ARCH-023 %s maps to %d", c.kind, c.want), func(t *testing.T) {
			e := E(c.kind, "x", "d", WithFields(c.fields...))
			if got := e.Status(); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
		})
	}
}

func TestWrapAndAs(t *testing.T) {
	t.Run("R-ARCH-023 Wrap keeps the cause reachable through errors.Is", func(t *testing.T) {
		cause := errors.New("pq: connection refused")
		e := Wrap(cause, "meta.Begin")
		if e.Kind != Internal || e.Code != CodeInternal || e.Op != "meta.Begin" {
			t.Fatalf("unexpected wrap result %+v", e)
		}
		if !errors.Is(e, cause) {
			t.Fatal("cause not reachable")
		}
	})
	t.Run("R-ARCH-023 Wrap leaves a classified error alone", func(t *testing.T) {
		orig := E(NotFound, CodeRepositoryNotFound, "no such repository")
		got := Wrap(fmt.Errorf("handler: %w", orig), "api.Get")
		if got != orig || got.Op != "api.Get" || got.Kind != NotFound {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("R-ARCH-023 Wrap of nil is nil", func(t *testing.T) {
		if Wrap(nil, "x") != nil {
			t.Fatal("want nil")
		}
	})
	t.Run("R-ARCH-023 KindOf reads through wrapping", func(t *testing.T) {
		err := fmt.Errorf("outer: %w", E(Conflict, CodeConflict, ""))
		if KindOf(err) != Conflict {
			t.Fatal("want Conflict")
		}
		if KindOf(errors.New("plain")) != Internal {
			t.Fatal("want Internal")
		}
	})
}

func TestWriteProblem(t *testing.T) {
	render := func(err error) (*httptest.ResponseRecorder, Problem, string) {
		rec := httptest.NewRecorder()
		var logBuf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/nope", nil)
		Write(rec, req, err, "req-1", log)
		var p Problem
		if rec.Body.Len() > 0 {
			if jerr := json.Unmarshal(rec.Body.Bytes(), &p); jerr != nil {
				t.Fatalf("body is not JSON: %v", jerr)
			}
		}
		return rec, p, logBuf.String()
	}

	t.Run("R-ARCH-023 not found renders problem+json with code and request id", func(t *testing.T) {
		rec, p, _ := render(E(NotFound, CodeRepositoryNotFound, "repository \"nope\" does not exist"))
		if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if p.Code != "repository_not_found" || p.RequestID != "req-1" || p.Status != 404 {
			t.Fatalf("unexpected problem %+v", p)
		}
		if p.Type != "/api/v1/problems/repository_not_found" || p.Instance != "/api/v1/repositories/nope" {
			t.Fatalf("type %q instance %q", p.Type, p.Instance)
		}
	})
	t.Run("R-ARCH-023 every problem carries X-Content-Type-Options nosniff", func(t *testing.T) {
		for _, err := range []error{E(NotFound, CodeNotFound, "x"), errors.New("plain"), E(Unavailable, CodeUnavailable, "x")} {
			if rec, _, _ := render(err); rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%v: X-Content-Type-Options = %q", err, rec.Header().Get("X-Content-Type-Options"))
			}
		}
	})
	t.Run("R-ARCH-023 a plain error becomes internal and hides its cause", func(t *testing.T) {
		rec, p, logs := render(errors.New("pq: password authentication failed for user trove"))
		if rec.Code != 500 || p.Code != "internal" {
			t.Fatalf("status %d code %q", rec.Code, p.Code)
		}
		if strings.Contains(rec.Body.String(), "password") {
			t.Fatalf("cause leaked into body: %s", rec.Body.String())
		}
		if !strings.Contains(logs, "password authentication failed") || !strings.Contains(logs, `"level":"ERROR"`) {
			t.Fatalf("cause missing from error log: %s", logs)
		}
	})
	t.Run("R-ARCH-023 internal kind ignores a detail set by the caller", func(t *testing.T) {
		_, p, _ := render(E(Internal, CodeInternal, "SELECT * FROM secrets"))
		if strings.Contains(p.Detail, "SELECT") {
			t.Fatalf("detail leaked: %q", p.Detail)
		}
	})
	t.Run("R-ARCH-023 database_unavailable is 503 with Retry-After and no SQL", func(t *testing.T) {
		rec, p, _ := render(E(Unavailable, CodeDatabaseUnavailable, "the database is unavailable",
			WithRetryAfter(1500*time.Millisecond), WithCause(errors.New("dial tcp 10.0.0.1:5432: refused"))))
		if rec.Code != 503 || rec.Header().Get("Retry-After") != "2" {
			t.Fatalf("status %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
		}
		if p.Code != "database_unavailable" || strings.Contains(rec.Body.String(), "dial tcp") {
			t.Fatalf("body %s", rec.Body.String())
		}
	})
	t.Run("R-ARCH-023 unauthenticated carries WWW-Authenticate", func(t *testing.T) {
		rec, _, _ := render(E(Unauthenticated, CodeUnauthenticated, "", WithChallenge(`Bearer realm="trove"`)))
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != `Bearer realm="trove"` {
			t.Fatalf("status %d header %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	})
	t.Run("R-ARCH-023 method not allowed carries Allow", func(t *testing.T) {
		rec, _, _ := render(E(MethodNotAllowed, CodeMethodNotAllowed, "", WithAllow("GET", "HEAD")))
		if rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("Allow %q", rec.Header().Get("Allow"))
		}
	})
	t.Run("R-ARCH-023 validation errors are listed with 422", func(t *testing.T) {
		rec, p, _ := render(E(Invalid, CodeValidation, "invalid body",
			WithFields(FieldError{Pointer: "/key", Code: "pattern", Detail: "must match ^[a-z]+$"})))
		if rec.Code != 422 || len(p.Errors) != 1 || p.Errors[0].Pointer != "/key" {
			t.Fatalf("status %d problem %+v", rec.Code, p)
		}
	})
	t.Run("R-ARCH-023 4xx is logged at debug, 5xx at error", func(t *testing.T) {
		_, _, l4 := render(E(NotFound, CodeNotFound, ""))
		_, _, l5 := render(E(UpstreamFailed, CodeUpstreamFailed, ""))
		if !strings.Contains(l4, `"level":"DEBUG"`) || !strings.Contains(l5, `"level":"ERROR"`) {
			t.Fatalf("levels wrong: %s / %s", l4, l5)
		}
	})
}

func TestRegistry(t *testing.T) {
	t.Run("R-ARCH-023 every code is unique snake_case", func(t *testing.T) {
		seen := map[string]bool{}
		for _, c := range Codes() {
			if seen[c.Code] {
				t.Errorf("duplicate code %q", c.Code)
			}
			seen[c.Code] = true
			if c.Code == "" || c.Code != strings.ToLower(c.Code) || strings.ContainsAny(c.Code, " -") {
				t.Errorf("code %q is not snake_case", c.Code)
			}
			if c.Title == "" || c.Detail == "" {
				t.Errorf("code %q lacks title or detail", c.Code)
			}
		}
	})
	t.Run("R-ARCH-023 every kind has a generic code", func(t *testing.T) {
		kinds := map[Kind]bool{}
		for _, c := range Codes() {
			kinds[c.Kind] = true
		}
		for k := Internal; k <= Misdirected; k++ {
			if !kinds[k] {
				t.Errorf("kind %s has no registered code", k)
			}
		}
	})
}

func TestProblemWireNames(t *testing.T) {
	t.Run("R-API-009 the request ID member is requestId", func(t *testing.T) {
		rec := httptest.NewRecorder()
		Write(rec, httptest.NewRequest("GET", "/x", nil), E(NotFound, CodeNotFound, "gone"), "abc", nil)
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m["requestId"] != "abc" || m["request_id"] != nil {
			t.Fatalf("%v", m)
		}
	})
	t.Run("R-API-009 the generic codes follow the specification", func(t *testing.T) {
		for _, c := range []string{"malformed_request", "server_busy", "database_timeout", "database_unavailable"} {
			if _, ok := Lookup(c); !ok {
				t.Errorf("code %q is not in the registry", c)
			}
		}
		for _, c := range []string{"invalid_request", "too_many_requests_in_flight"} {
			if _, ok := Lookup(c); ok {
				t.Errorf("code %q must be gone", c)
			}
		}
	})
}
