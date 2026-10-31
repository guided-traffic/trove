package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// stage returns a middleware that records its name when a request enters it.
func stage(name string, rec *recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(name)
			next.ServeHTTP(w, r)
		})
	}
}

type recorder struct {
	mu    sync.Mutex
	names []string
}

func (r *recorder) add(n string) { r.mu.Lock(); r.names = append(r.names, n); r.mu.Unlock() }
func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) errs.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != errs.ProblemContentType {
		t.Fatalf("Content-Type = %q, want problem+json (body %s)", ct, rec.Body.String())
	}
	var p errs.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestRouter(pattern string, h http.HandlerFunc) *Router {
	r := NewClientRouter()
	r.HandleFunc(pattern, h)
	return r
}

func TestMiddlewareOrder(t *testing.T) {
	t.Run("R-ARCH-005 stages run in the fixed order, outermost first", func(t *testing.T) {
		var rec recorder
		router := newTestRouter("GET /x", func(w http.ResponseWriter, r *http.Request) {
			rec.add("handler")
			w.WriteHeader(204)
		})
		res, err := NewResolver(nil, "https://trove.example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		c := Chain{
			Router:      router,
			Trace:       stage("trace", &rec),
			AccessLog:   stage("log", &rec),
			MaxInFlight: 1,
			Resolver:    res,
			Annotate:    func(*http.Request, RequestInfo) { rec.add("addr") },
			Authenticate: func(*http.Request) (Principal, error) {
				rec.add("auth")
				return Principal{Anonymous: true}, nil
			},
			RateLimit: stage("ratelimit", &rec),
		}
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		want := []string{"trace", "log", "addr", "auth", "ratelimit", "handler"}
		if got := rec.get(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("order = %v, want %v", got, want)
		}
		if w.Code != 204 {
			t.Fatalf("status = %d", w.Code)
		}
	})

	t.Run("R-ARCH-005 the in-flight limit sits after the request log and before the client address", func(t *testing.T) {
		var rec recorder
		block, release := make(chan struct{}), make(chan struct{})
		router := newTestRouter("GET /slow", func(w http.ResponseWriter, r *http.Request) {
			close(block)
			<-release
		})
		c := Chain{
			Router: router, MaxInFlight: 1,
			AccessLog: stage("log", &rec),
			Annotate:  func(*http.Request, RequestInfo) { rec.add("addr") },
		}
		h := c.Handler()
		done := make(chan struct{})
		go func() {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/slow", nil))
			close(done)
		}()
		<-block
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/slow", nil))
		close(release)
		<-done
		// The refused request was logged (stage 3 ran) but never reached stage 5.
		got := rec.get()
		if strings.Join(got, ",") != "log,addr,log" {
			t.Fatalf("stages = %v, want [log addr log]", got)
		}
		if w.Code != 503 {
			t.Fatalf("status = %d", w.Code)
		}
	})
}

func TestMaxInFlight(t *testing.T) {
	t.Run("R-ARCH-005 requests beyond server.max_in_flight get 503 server_busy with Retry-After 1", func(t *testing.T) {
		started := make(chan struct{}, 2)
		release := make(chan struct{})
		router := newTestRouter("GET /slow", func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-release
		})
		h := Chain{Router: router, MaxInFlight: 2}.Handler()
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/slow", nil))
			}()
		}
		<-started
		<-started

		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/slow", nil))
		if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
			t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
		}
		if p := problemOf(t, w); p.Code != "server_busy" {
			t.Fatalf("code = %q", p.Code)
		}
		if w.Header().Get(HeaderRequestID) == "" {
			t.Fatal("the refusal carries no X-Request-Id")
		}

		close(release)
		wg.Wait()
		w = httptest.NewRecorder()
		router2 := newTestRouter("GET /fast", func(w http.ResponseWriter, r *http.Request) {})
		Chain{Router: router2, MaxInFlight: 2}.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/fast", nil))
		if w.Code != 200 {
			t.Fatalf("slot not released: %d", w.Code)
		}
	})
}

func TestRecovery(t *testing.T) {
	t.Run("R-ARCH-005 a panic becomes a 500 problem with code internal and a request ID", func(t *testing.T) {
		router := newTestRouter("GET /boom", func(http.ResponseWriter, *http.Request) { panic("secret internal detail") })
		w := httptest.NewRecorder()
		Chain{Router: router}.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
		if w.Code != 500 {
			t.Fatalf("status = %d", w.Code)
		}
		p := problemOf(t, w)
		if p.Code != "internal" || strings.Contains(w.Body.String(), "secret internal detail") {
			t.Fatalf("problem = %+v body %s", p, w.Body.String())
		}
		if id := w.Header().Get(HeaderRequestID); id == "" || p.RequestID != id {
			t.Fatalf("X-Request-Id %q, problem request_id %q", id, p.RequestID)
		}
	})

	t.Run("R-ARCH-005 a panic after the status line aborts the connection instead of writing a second status", func(t *testing.T) {
		router := newTestRouter("GET /late", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			panic("late")
		})
		defer func() {
			if r := recover(); !errors.Is(r.(error), http.ErrAbortHandler) {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", r)
			}
		}()
		Chain{Router: router}.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/late", nil))
	})
}

func TestIdentificationHeaders(t *testing.T) {
	t.Run("E0.10 X-Request-Id echoes the trace ID of an incoming traceparent", func(t *testing.T) {
		type traceKey struct{}
		// A stand-in for the observability middleware: it keeps the incoming trace ID.
		trace := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				parts := strings.Split(r.Header.Get("traceparent"), "-")
				ctx := r.Context()
				if len(parts) == 4 {
					ctx = context.WithValue(ctx, traceKey{}, parts[1])
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}
		traceID := func(ctx context.Context) string { s, _ := ctx.Value(traceKey{}).(string); return s }
		router := newTestRouter("GET /x", func(w http.ResponseWriter, r *http.Request) {
			if RequestID(r.Context()) != "4bf92f3577b34da6a3ce929d0e0e4736" {
				t.Errorf("RequestID(ctx) = %q", RequestID(r.Context()))
			}
		})
		h := Chain{Router: router, Trace: trace, TraceID: traceID}.Handler()
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get(HeaderRequestID); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Fatalf("X-Request-Id = %q", got)
		}
	})

	t.Run("R-OPS-002 every response carries an X-Request-Id, minted when no trace context exists", func(t *testing.T) {
		router := newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) {})
		h := Chain{Router: router}.Handler()
		seen := map[string]bool{}
		for _, path := range []string{"/x", "/nope", "/"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			id := w.Header().Get(HeaderRequestID)
			if len(id) != 32 {
				t.Fatalf("%s: X-Request-Id = %q, want 32 hex digits", path, id)
			}
			seen[id] = true
		}
		if len(seen) != 3 {
			t.Fatal("request IDs repeat")
		}
	})

	t.Run("R-OPS-002 X-Trove-Replica follows server.replica_header", func(t *testing.T) {
		router := newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) {})
		authed := func(r *http.Request) (Principal, error) {
			if r.Header.Get("Authorization") != "" {
				return Principal{Name: "alice"}, nil
			}
			return Principal{Anonymous: true}, nil
		}
		cases := []struct {
			mode        string
			auth, anon  string
			anonMissing string
		}{
			{ReplicaAuthenticated, "trove-web-1", "", "/nope"},
			{ReplicaAlways, "trove-web-1", "trove-web-1", "/nope"},
			{ReplicaNever, "", "", "/nope"},
		}
		for _, c := range cases {
			h := Chain{Router: router, Authenticate: authed, Replica: "trove-web-1", ReplicaHeader: c.mode}.Handler()
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set("Authorization", "Bearer x")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if got := w.Header().Get(HeaderReplica); got != c.auth {
				t.Errorf("%s authenticated: %q, want %q", c.mode, got, c.auth)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
			if got := w.Header().Get(HeaderReplica); got != c.anon {
				t.Errorf("%s anonymous: %q, want %q", c.mode, got, c.anon)
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", c.anonMissing, nil))
			if c.mode == ReplicaAlways && w.Header().Get(HeaderReplica) == "" {
				t.Errorf("always: a 404 must carry the header too")
			}
		}
	})

	t.Run("R-OPS-002 no response carries the product version in a header", func(t *testing.T) {
		router := newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) {})
		w := httptest.NewRecorder()
		Chain{Router: router}.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		for k, v := range w.Header() {
			if strings.EqualFold(k, "Server") || strings.EqualFold(k, "X-Powered-By") {
				t.Fatalf("header %s: %v", k, v)
			}
		}
	})
}

func TestAuthenticationStage(t *testing.T) {
	t.Run("R-ARCH-005 a credential that is present but invalid answers 401 before the handler", func(t *testing.T) {
		called := false
		router := newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) { called = true })
		h := Chain{Router: router, Authenticate: func(*http.Request) (Principal, error) {
			return Principal{}, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "The credential is not valid.", errs.WithChallenge("Bearer"))
		}}.Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Code != 401 || called || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("status %d called %v challenge %q", w.Code, called, w.Header().Get("WWW-Authenticate"))
		}
	})

	t.Run("R-ARCH-005 the handler sees the principal and the resolved request info", func(t *testing.T) {
		res, _ := NewResolver(nil, "https://trove.example.com", nil)
		router := newTestRouter("GET /x", func(w http.ResponseWriter, r *http.Request) {
			if p := PrincipalFrom(r.Context()); p.Name != "alice" {
				t.Errorf("principal = %+v", p)
			}
			if i := InfoFrom(r.Context()); i.BaseURL != "https://trove.example.com" || i.Client.String() != "192.0.2.1" {
				t.Errorf("info = %+v", i)
			}
		})
		h := Chain{Router: router, Resolver: res, Authenticate: func(*http.Request) (Principal, error) {
			return Principal{Name: "alice"}, nil
		}}.Handler()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil)) // httptest peer is 192.0.2.1
	})

	t.Run("R-ARCH-034 Q-HA-66 the request info carries the time the chain received the request, before authentication", func(t *testing.T) {
		var authAt, received time.Time
		router := newTestRouter("GET /x", func(_ http.ResponseWriter, r *http.Request) { received = InfoFrom(r.Context()).Received })
		h := Chain{Router: router, Authenticate: func(*http.Request) (Principal, error) {
			authAt = time.Now()
			time.Sleep(20 * time.Millisecond) // a token lookup that waits for the database
			return Principal{Name: "alice"}, nil
		}}.Handler()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
		if received.IsZero() || received.After(authAt) {
			t.Fatalf("Received = %v, authentication started %v: want a time before the authentication", received, authAt)
		}
	})
}

func TestDrainingConnectionClose(t *testing.T) {
	t.Run("R-ARCH-024 responses completed while draining carry Connection: close", func(t *testing.T) {
		router := newTestRouter("GET /x", func(http.ResponseWriter, *http.Request) {})
		draining := false
		h := Chain{Router: router, Draining: func() bool { return draining }}.Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Header().Get("Connection") != "" {
			t.Fatal("Connection set while not draining")
		}
		draining = true
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		if w.Header().Get("Connection") != "close" {
			t.Fatalf("Connection = %q", w.Header().Get("Connection"))
		}
	})
}

func TestRouterFallback(t *testing.T) {
	r := NewClientRouter()
	r.HandleFunc("GET /api/v1/things", func(http.ResponseWriter, *http.Request) {})
	r.HandleFunc("POST /api/v1/things", func(http.ResponseWriter, *http.Request) {})
	h := Chain{Router: r}.Handler()

	t.Run("R-ARCH-005 anything else answers a 404 problem", func(t *testing.T) {
		for _, path := range []string{"/nope", "/-/whoami", "/-/npm/v1/attestations/left-pad@1.3.0", "/api/v0/repositories", "/api/v3/repositories"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 404 {
				t.Fatalf("%s: status %d", path, w.Code)
			}
			if p := problemOf(t, w); p.Code != "not_found" || p.Instance != path {
				t.Fatalf("%s: problem %+v", path, p)
			}
		}
	})

	t.Run("R-ARCH-005 GET /{$} redirects to /ui/ with 302", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 302 || w.Header().Get("Location") != "/ui/" {
			t.Fatalf("status %d Location %q", w.Code, w.Header().Get("Location"))
		}
	})

	t.Run("R-API-004 a wrong method answers 405 with an Allow header", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/things", nil))
		if w.Code != 405 {
			t.Fatalf("status = %d", w.Code)
		}
		allow := w.Header().Get("Allow")
		for _, m := range []string{"GET", "HEAD", "POST"} {
			if !strings.Contains(allow, m) {
				t.Fatalf("Allow = %q lacks %s", allow, m)
			}
		}
		if problemOf(t, w).Code != "method_not_allowed" {
			t.Fatal("wrong code")
		}
	})

	t.Run("R-ARCH-005 a conflicting registration panics at start-up", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		rt := NewClientRouter()
		rt.HandleFunc("GET /a", func(http.ResponseWriter, *http.Request) {})
		rt.HandleFunc("GET /a", func(http.ResponseWriter, *http.Request) {})
	})

	t.Run("R-ARCH-005 the route label is the pattern, resolved before the inner stages copy the request", func(t *testing.T) {
		var got string
		logStage := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Pattern
				next.ServeHTTP(w, r)
			})
		}
		Chain{Router: r, AccessLog: logStage}.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/things", nil))
		if got != "POST /api/v1/things" {
			t.Fatalf("pattern seen by the log stage = %q", got)
		}
	})
}

func TestOps(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	t.Run("R-ARCH-005 the ops listener serves only /healthz, /readyz and /metrics", func(t *testing.T) {
		h := OpsHandler(OpsOptions{Live: ok, Ready: ok, Metrics: ok})
		for path, want := range map[string]int{"/healthz": 200, "/readyz": 200, "/metrics": 200, "/api/v1/system/ping": 404, "/ui/": 404, "/": 404} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != want {
				t.Errorf("%s: %d, want %d", path, w.Code, want)
			}
		}
	})

	t.Run("R-ARCH-005 the client listener has no probe or metrics path", func(t *testing.T) {
		h := Chain{Router: NewClientRouter()}.Handler()
		for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 404 {
				t.Errorf("%s: %d, want 404", path, w.Code)
			}
		}
	})
}

func TestNoSniff(t *testing.T) {
	t.Run("R-ARCH-005 every response carries X-Content-Type-Options: nosniff, handler, problem, 404 and panic alike", func(t *testing.T) {
		router := newTestRouter("GET /ok", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{}")) })
		router.Handle("GET /boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }))
		for _, p := range []string{"/ok", "/boom", "/missing"} {
			w := httptest.NewRecorder()
			Chain{Router: router}.Handler().ServeHTTP(w, httptest.NewRequest("GET", p, nil))
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s: X-Content-Type-Options = %q", p, got)
			}
		}
	})
}
