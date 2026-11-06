package bastionmark

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

const mainURL = "https://trove.example.com"

// mk is a format with one route of every kind the marking tells apart and its own error shape.
type mk struct {
	formats.Base
	handler formats.HandlerFunc
}

func (mk) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "mk", Classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual}, Layout: "mk",
			ProtocolBasePath: "/api/mk/{key}/", PatternSubject: formats.SubjectPath,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func (m mk) Routes() []formats.Route {
	r := func(name, method, tmpl string, kind formats.RouteKind, act formats.Action, ex string) formats.Route {
		return formats.Route{Name: name, Method: method, Template: tmpl, Examples: []string{ex}, Action: act, Kind: kind, Handler: m.handler}
	}
	return []formats.Route{
		r("mk.file.get", http.MethodGet, "/f/{file}", formats.Download, formats.ActionRead, "f/x.bin"),
		r("mk.doc.get", http.MethodGet, "/d/{package}", formats.Metadata, formats.ActionRead, "d/left-pad"),
		r("mk.computed.get", http.MethodGet, "/c/{package}", formats.Computed, formats.ActionRead, "c/left-pad"),
		r("mk.search.get", http.MethodGet, "/s/{package}", formats.Volatile, formats.ActionRead, "s/left-pad"),
		r("mk.audit.post", http.MethodPost, "/audit/{package}", formats.Volatile, formats.ActionRead, "audit/left-pad"),
		r("mk.file.put", http.MethodPut, "/f/{file}", formats.Upload, formats.ActionDeploy, "f/x.bin"),
		r("mk.tag.put", http.MethodPut, "/t/{package}", formats.Admin, formats.ActionDeploy, "t/left-pad"),
		r("mk.rev.delete", http.MethodDelete, "/d/{package}", formats.Delete, formats.ActionDelete, "d/left-pad"),
		r("mk.admin.read.put", http.MethodPut, "/a/{package}", formats.Admin, formats.ActionRead, "a/left-pad"),
	}
}

func (m mk) Match(method, path string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(m.Routes(), method, path)
}

func (mk) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{Kind: formats.Content} }

func (mk) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

func (mk) WriteError(w http.ResponseWriter, _ *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "internal", ""
	if e, ok := errs.As(err); ok {
		status, code, detail = e.Status(), e.Code, e.Detail
	}
	w.Header().Set("X-Mk-Error", code)
	w.Header().Set("X-Mk-Detail", detail)
	w.WriteHeader(status)
}

type fakeDecider struct {
	authz.Decider
	outcome authz.Outcome
}

func (*fakeDecider) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	return &authz.Index{}, nil
}

func (d *fakeDecider) DecideOn(context.Context, *authz.Index, authz.Subject, authz.Coordinate) authz.Decision {
	return authz.Decision{Outcome: d.outcome}
}

type fakeRepos struct {
	repository.Reader
	byKey map[string]repository.Repository
}

func (r *fakeRepos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	if repo, ok := r.byKey[key]; ok {
		return repo, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no")
}

type fakeHost struct{ formats.Host }

func (fakeHost) Begin(*formats.Request) func() { return func() {} }

// shapes answers the shape a test sets, and records what it was asked.
type shapes struct {
	mu    sync.Mutex
	read  iface.Read
	err   error
	asked []string
	subs  []authz.Subject
}

func (s *shapes) Shapes(_ context.Context, _ *authz.Index, sub authz.Subject, keys []string) (map[string]bastionmain.Shape, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, keys...)
	s.subs = append(s.subs, sub)
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]bastionmain.Shape{}
	for _, k := range keys {
		out[k] = bastionmain.Shape{Read: s.read}
	}
	return out, nil
}

// Request headers of the test chain: they stand in for the recognition and the authentication, which are other
// stages.
const (
	hRecognize = "X-Test-Recognize" // "forward", "probe" or "route"
	hCred      = "X-Test-Cred"      // "token", "token-no-expiry", "anon", "session", "bad"
)

var expiry = time.Unix(1_900_000_000, 0).UTC()

type rig struct {
	t       *testing.T
	shapes  *shapes
	decider *fakeDecider
	reached []string
	chain   http.Handler
	metrics *bastionmain.Metrics
	// effects of the handler of the mk format, set by a test
	status  int
	notes   func(ctx context.Context)
	respond func(w http.ResponseWriter)
}

func newRig(t *testing.T, fs ...formats.Format) *rig {
	t.Helper()
	r := &rig{t: t, shapes: &shapes{read: iface.ReadFull}, decider: &fakeDecider{}, status: http.StatusOK}
	m := mk{handler: func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		r.reached = append(r.reached, rq.Route.Name)
		if r.notes != nil {
			r.notes(ctx)
		}
		if r.respond != nil {
			r.respond(w)
		}
		w.WriteHeader(r.status)
		return nil
	}}
	if len(fs) == 0 {
		fs = []formats.Format{m}
	}
	d, err := dispatch.New(dispatch.Options{
		Host: fakeHost{}, Decider: r.decider, Formats: func() []formats.Format { return fs },
		Repos: &fakeRepos{byKey: map[string]repository.Repository{
			"mk-local": {Name: "1", Key: "mk-local", Class: repository.Local, Format: "mk"},
			"npm-all":  {Name: "2", Key: "npm-all", Class: repository.Virtual, Format: "npm"},
			"nuget-v3": {Name: "3", Key: "nuget-v3", Class: repository.Local, Format: "nuget"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	d.Register(router)
	r.metrics, err = bastionmain.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	marker := New(Options{Shapes: r.shapes, MainURL: mainURL + "/", Metrics: r.metrics})
	recognize := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch req.Header.Get(hRecognize) {
			case "":
			case "forward":
				req = req.WithContext(bastionmain.WithRecognition(req.Context(), bastionmain.Recognition{Bastion: "edge-1", Interface: iface.InterfaceVersion}))
			case "probe":
				req = req.WithContext(bastionmain.WithRecognition(req.Context(), bastionmain.Recognition{Bastion: "edge-1", Interface: iface.InterfaceVersion, Probe: true}))
			case "route":
				req = req.WithContext(bastionmain.WithRecognition(req.Context(), bastionmain.Recognition{Bastion: "edge-1", Interface: iface.InterfaceVersion, Route: true}))
			}
			next.ServeHTTP(w, req)
		})
	}
	r.chain = httpserver.Chain{
		Router: router,
		Authenticate: func(req *http.Request) (httpserver.Principal, error) {
			switch req.Header.Get(hCred) {
			case "token":
				exp := expiry
				return httpserver.Principal{
					Kind: httpserver.KindUser, ID: "p-1", Name: "alice",
					Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "t-1", ExpiresAt: &exp},
				}, nil
			case "token-no-expiry":
				return httpserver.Principal{
					Kind: httpserver.KindServiceAccount, ID: "p-2", Name: "bot",
					Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "t-2"},
				}, nil
			case "session":
				return httpserver.Principal{
					Kind: httpserver.KindUser, ID: "p-1", Name: "alice",
					Credential: httpserver.Credential{Method: httpserver.MethodSession, SessionID: "s-1"},
				}, nil
			case "bad":
				return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The token is not valid.")
			}
			return httpserver.AnonymousPrincipal(), nil
		},
		Bastion: func(next http.Handler) http.Handler { return recognize(marker.Stage()(next)) },
	}.Handler()
	return r
}

func (r *rig) do(method, path string, hdr ...string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.doBody(method, path, nil, hdr...)
}

func (r *rig) doBody(method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, body)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	r.chain.ServeHTTP(rec, req)
	return rec
}

func markingOf(t *testing.T, rec *httptest.ResponseRecorder) iface.Marking {
	t.Helper()
	m, ok, err := iface.ParseMarking(rec.Header())
	if err != nil || !ok {
		t.Fatalf("no readable marking (ok %v, err %v): %v", ok, err, rec.Header())
	}
	return m
}

func hasMarking(h http.Header) []string {
	var out []string
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), iface.HeaderPrefix) {
			out = append(out, k)
		}
	}
	return out
}

// TestR_BAS_013_ClassOfTheResponse: the class follows from the route kind, the status and the shape alone.
func TestR_BAS_013_ClassOfTheResponse(t *testing.T) {
	const forward = "forward"
	cases := []struct {
		name   string
		path   string
		method string
		status int
		read   iface.Read
		notes  func(context.Context)
		want   iface.Class
	}{
		{"a download served is an artifact for a full reader", "/api/mk/mk-local/f/x.bin", "GET", 200, iface.ReadFull, nil, iface.ClassArtifact},
		{"a download served is an artifact for a partial reader", "/api/mk/mk-local/f/x.bin", "GET", 200, iface.ReadPartial, nil, iface.ClassArtifact},
		{"a download validated is an artifact", "/api/mk/mk-local/f/x.bin", "GET", 304, iface.ReadFull, nil, iface.ClassArtifact},
		{"a HEAD of a download is an artifact", "/api/mk/mk-local/f/x.bin", "HEAD", 200, iface.ReadFull, nil, iface.ClassArtifact},
		{"a range of a download is pass", "/api/mk/mk-local/f/x.bin", "GET", 206, iface.ReadFull, nil, iface.ClassPass},
		{"a redirect is pass", "/api/mk/mk-local/f/x.bin", "GET", 302, iface.ReadFull, nil, iface.ClassPass},
		{"a document for a full reader is shared", "/api/mk/mk-local/d/left-pad", "GET", 200, iface.ReadFull, nil, iface.ClassShared},
		{"a document validated for a full reader is shared", "/api/mk/mk-local/d/left-pad", "GET", 304, iface.ReadFull, nil, iface.ClassShared},
		{"a document for a partial reader is private", "/api/mk/mk-local/d/left-pad", "GET", 200, iface.ReadPartial, nil, iface.ClassPrivate},
		{"a computed route follows the document rule", "/api/mk/mk-local/c/left-pad", "GET", 200, iface.ReadFull, nil, iface.ClassShared},
		{"a volatile route is pass", "/api/mk/mk-local/s/left-pad", "GET", 200, iface.ReadFull, nil, iface.ClassPass},
		{"a server error is pass", "/api/mk/mk-local/d/left-pad", "GET", 502, iface.ReadFull, nil, iface.ClassPass},
		{"a document without a shape is pass", "/api/mk/mk-local/d/left-pad", "GET", 200, "", nil, iface.ClassPass},
		{
			"a 404 of a missing item for a full reader is absent", "/api/mk/mk-local/d/left-pad", "GET", 404, iface.ReadFull,
			func(ctx context.Context) { bastionmain.NoteMissing(ctx) }, iface.ClassAbsent,
		},
		{
			"a 404 of a missing download for a full reader is absent", "/api/mk/mk-local/f/x.bin", "GET", 404, iface.ReadFull,
			func(ctx context.Context) { bastionmain.NoteMissing(ctx) }, iface.ClassAbsent,
		},
		{"a 404 that no lookup found missing is pass", "/api/mk/mk-local/d/left-pad", "GET", 404, iface.ReadFull, nil, iface.ClassPass},
		{
			"a 404 of a missing item for a partial reader is pass", "/api/mk/mk-local/d/left-pad", "GET", 404, iface.ReadPartial,
			func(ctx context.Context) { bastionmain.NoteMissing(ctx) }, iface.ClassPass,
		},
		{
			"a 404 after a refusal or a serve-time check is pass", "/api/mk/mk-local/d/left-pad", "GET", 404, iface.ReadFull,
			func(ctx context.Context) { bastionmain.NoteMissing(ctx); bastionmain.NoteUnknown(ctx) }, iface.ClassPass,
		},
		{
			"a 404 of a volatile route is pass", "/api/mk/mk-local/s/left-pad", "GET", 404, iface.ReadFull,
			func(ctx context.Context) { bastionmain.NoteMissing(ctx) }, iface.ClassPass,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.shapes.read, r.status, r.notes = tc.read, tc.status, tc.notes
			rec := r.do(tc.method, tc.path, hRecognize, forward, hCred, "token")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			mark := markingOf(t, rec)
			if mark.Class != tc.want {
				t.Fatalf("class %s, want %s (%v)", mark.Class, tc.want, rec.Header())
			}
			if mark.Repository != "mk-local" || mark.Read != tc.read || mark.Interface != iface.InterfaceVersion {
				t.Fatalf("marking %+v", mark)
			}
		})
	}
	t.Run("a shape that cannot be computed leaves the response pass without a Read header", func(t *testing.T) {
		r := newRig(t)
		r.shapes.err = errors.New("stale")
		rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, forward, hCred, "token")
		if mark := markingOf(t, rec); mark.Class != iface.ClassPass || rec.Header().Get(iface.HeaderRead) != "" {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("the shape is asked for the subject and the repository of the decision, once", func(t *testing.T) {
		r := newRig(t)
		r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, forward, hCred, "token")
		if len(r.shapes.asked) != 1 || r.shapes.asked[0] != "mk-local" {
			t.Fatalf("asked %v", r.shapes.asked)
		}
	})
}

// TestR_BAS_013_Refusals: a refusal, a hidden item and a serve-time refusal are never absent.
func TestR_BAS_013_Refusals(t *testing.T) {
	t.Run("a refusal of the decision point is pass, with the repository and no shape", func(t *testing.T) {
		r := newRig(t)
		r.decider.outcome = authz.DenyHidden
		rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward", hCred, "token")
		mark := markingOf(t, rec)
		if rec.Code != 404 || mark.Class != iface.ClassPass || mark.Repository != "mk-local" || rec.Header().Get(iface.HeaderRead) != "" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("a hidden item and a missing one have the same 404 and only the second can be absent", func(t *testing.T) {
		r := newRig(t)
		r.status = 404
		hidden := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward", hCred, "token")
		r.notes = bastionmain.NoteMissing
		missing := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward", hCred, "token")
		if hidden.Code != missing.Code || hidden.Body.String() != missing.Body.String() {
			t.Fatal("the bodies differ")
		}
		if markingOf(t, hidden).Class != iface.ClassPass || markingOf(t, missing).Class != iface.ClassAbsent {
			t.Fatalf("%v / %v", hidden.Header(), missing.Header())
		}
	})
	t.Run("no route is the format's 404, pass, with no repository shape", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("GET", "/api/mk/mk-local/nothing/here", hRecognize, "forward", hCred, "token")
		if rec.Code != 404 || rec.Header().Get("X-Mk-Error") != "not_found" || markingOf(t, rec).Class != iface.ClassPass {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
}

// TestR_BAS_013_SubjectHeader: who the response says the caller is.
func TestR_BAS_013_SubjectHeader(t *testing.T) {
	cases := []struct {
		name string
		cred string
		want string
		code int
	}{
		{"a token with an expiry", "token", "token=t-1; principal=p-1; expires=1900000000", 200},
		{"a token that does not expire", "token-no-expiry", "token=t-2; principal=p-2", 200},
		{"no credential", "anon", "anonymous", 200},
		{"a session cookie is no credential for a Bastion", "session", "invalid", 200},
		{"a rejected credential", "bad", "invalid", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward", hCred, tc.cred)
			if rec.Code != tc.code || rec.Header().Get(iface.HeaderSubject) != tc.want {
				t.Fatalf("%d, subject %q, want %d %q", rec.Code, rec.Header().Get(iface.HeaderSubject), tc.code, tc.want)
			}
			if tc.code == 401 {
				if mark := markingOf(t, rec); mark.Class != iface.ClassPass || rec.Header().Get(iface.HeaderRepository) != "" {
					t.Fatalf("%v", rec.Header())
				}
			}
		})
	}
	t.Run("an anonymous caller refused is invalid on the 401", func(t *testing.T) {
		r := newRig(t)
		r.status = 401
		rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward")
		if rec.Header().Get(iface.HeaderSubject) != "invalid" {
			t.Fatalf("%v", rec.Header())
		}
	})
}

// TestR_BAS_013_HeadersOnlyOnBastionRequests: no response to a request without a valid Bastion token carries a
// Trove-Bastion- header, over the route table of the formats.
func TestR_BAS_013_HeadersOnlyOnBastionRequests(t *testing.T) {
	fs := []formats.Format{npm.New(), nuget.New()}
	cases := 0
	// The decision refuses every request, so no handler runs and every route answers in its format's shape.
	for _, outcome := range []authz.Outcome{authz.DenyHidden, authz.DenyVisible, authz.DenyUnauthenticated} {
		r := newRig(t, fs...)
		r.decider.outcome = outcome
		for _, f := range fs {
			id := string(f.Descriptor().ID)
			key := map[string]string{"npm": "npm-all", "nuget": "nuget-v3"}[id]
			for _, rt := range f.Routes() {
				for _, ex := range rt.Examples {
					// Every request carries the headers a Bastion would send and a token that no stage recognized.
					rec := r.doBody(rt.Method, "/api/"+id+"/"+key+"/"+ex, strings.NewReader(""),
						iface.HeaderAuthorization, "Bearer trv_bst_nothing", iface.HeaderInterface, "1", iface.HeaderProbe, "1", hCred, "token")
					if got := hasMarking(rec.Header()); len(got) > 0 {
						t.Fatalf("%s %s %s (decision %d): %d carries %v", rt.Method, id, ex, outcome, rec.Code, got)
					}
					cases++
				}
			}
		}
	}
	if cases == 0 {
		t.Fatal("no route was exercised")
	}
	t.Run("a header a handler or a stage below set is stripped from the response of an ordinary caller", func(t *testing.T) {
		r := newRig(t)
		r.respond = func(w http.ResponseWriter) {
			w.Header().Set(iface.HeaderClass, "artifact")
			w.Header().Set(iface.HeaderRead, "full")
			w.Header().Set("trove-bastion-subject", "anonymous")
		}
		rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hCred, "token")
		if got := hasMarking(rec.Header()); len(got) > 0 || rec.Code != 200 {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("the strip holds when the handler writes the body without a status line, and when it flushes", func(t *testing.T) {
		for _, flush := range []bool{false, true} {
			h := httpserver.Chain{Router: httpserver.NewClientRouter(), Bastion: New(Options{}).Stage()}
			_ = h
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(iface.HeaderClass, "artifact")
				if flush {
					w.(http.Flusher).Flush()
				} else {
					_, _ = w.Write([]byte("x"))
				}
			})
			rec := httptest.NewRecorder()
			New(Options{}).Stage()(inner).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			if len(hasMarking(rec.Result().Header)) > 0 {
				t.Fatalf("flush %v: %v", flush, rec.Result().Header)
			}
		}
	})
}

// TestR_BAS_013_InterfaceRoutes: an interface route has no addressed repository and no caller but the Bastion.
func TestR_BAS_013_InterfaceRoutes(t *testing.T) {
	r := newRig(t)
	rec := r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "route", hCred, "token")
	if got := hasMarking(rec.Header()); len(got) != 2 || rec.Header().Get(iface.HeaderClass) != "pass" || rec.Header().Get(iface.HeaderInterface) != "1" {
		t.Fatalf("%v", rec.Header())
	}
}

// TestR_BAS_013_ForwardedRequestsAreCountedByClass: trove_bastion_forwarded_requests_total.
func TestR_BAS_013_ForwardedRequestsAreCountedByClass(t *testing.T) {
	r := newRig(t)
	r.do("GET", "/api/mk/mk-local/f/x.bin", hRecognize, "forward", hCred, "token")
	r.do("GET", "/api/mk/mk-local/f/x.bin", hRecognize, "forward", hCred, "token")
	r.do("GET", "/api/mk/mk-local/d/left-pad", hRecognize, "forward", hCred, "token")
	r.do("PUT", "/api/mk/mk-local/f/x.bin", hRecognize, "probe")
	r.do("GET", "/api/mk/mk-local/f/x.bin", hCred, "token") // an ordinary caller is not counted
	for class, want := range map[string]float64{"artifact": 2, "shared": 1, "probe": 1} {
		if got := counter(t, r.metrics, class); got != want {
			t.Errorf("%s: %v, want %v", class, got, want)
		}
	}
}

// TestR_BAS_014_ProbeIsRefusedInTheShapeOfTheFormat: the write probe is answered after routing and before any
// handler or body read.
func TestR_BAS_014_ProbeIsRefusedInTheShapeOfTheFormat(t *testing.T) {
	t.Run("every write route of the fake format is refused with bastion_read_only and the URL of the repository", func(t *testing.T) {
		r := newRig(t)
		for _, tc := range []struct{ method, path, target string }{
			{"PUT", "/api/mk/mk-local/f/x.bin", mainURL + "/api/mk/mk-local/"},
			{"PUT", "/api/mk/mk-local/t/left-pad", mainURL + "/api/mk/mk-local/"},
			{"DELETE", "/api/mk/mk-local/d/left-pad", mainURL + "/api/mk/mk-local/"},
			{"PUT", "/api/mk/mk-local/a/left-pad", mainURL + "/api/mk/mk-local/"}, // an Admin route, whatever its action
		} {
			rec := r.do(tc.method, tc.path, hRecognize, "probe")
			want := "This is a read-only Bastion of " + mainURL + "; publish to " + tc.target
			if rec.Code != 403 || rec.Header().Get("X-Mk-Error") != iface.CodeReadOnly || rec.Header().Get("X-Mk-Detail") != want {
				t.Errorf("%s %s: %d %v", tc.method, tc.path, rec.Code, rec.Header())
			}
			if markingOf(t, rec).Class != iface.ClassPass {
				t.Errorf("%s: %v", tc.path, rec.Header())
			}
		}
		if len(r.reached) != 0 {
			t.Fatalf("a handler ran: %v", r.reached)
		}
	})
	t.Run("a read route that takes a body answers 204 with the route header and the handler does not run", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("POST", "/api/mk/mk-local/audit/left-pad", hRecognize, "probe")
		if rec.Code != 204 || rec.Header().Get(iface.HeaderRoute) != iface.RouteReadWithBody || len(r.reached) != 0 {
			t.Fatalf("%d %v reached %v", rec.Code, rec.Header(), r.reached)
		}
		if markingOf(t, rec).Class != iface.ClassPass {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("no route is the format's own 404", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("PUT", "/api/mk/mk-local/no/such/route", hRecognize, "probe")
		if rec.Code != 404 || rec.Header().Get("X-Mk-Error") != "not_found" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("the probe is answered before the decision point is asked", func(t *testing.T) {
		r := newRig(t)
		r.decider.outcome = authz.DenyVisible
		if rec := r.do("PUT", "/api/mk/mk-local/f/x.bin", hRecognize, "probe"); rec.Header().Get("X-Mk-Error") != iface.CodeReadOnly {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("a write that reaches the main installation without the probe header is refused the same way", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("PUT", "/api/mk/mk-local/f/x.bin", hRecognize, "forward", hCred, "token")
		if rec.Code != 403 || rec.Header().Get("X-Mk-Error") != iface.CodeReadOnly || len(r.reached) != 0 {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("the real request of a read route that takes a body is served", func(t *testing.T) {
		r := newRig(t)
		r.status = 200
		rec := r.do("POST", "/api/mk/mk-local/audit/left-pad", hRecognize, "forward", hCred, "token")
		if rec.Code != 200 || len(r.reached) != 1 || rec.Header().Get(iface.HeaderRoute) != "" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if markingOf(t, rec).Class != iface.ClassPass {
			t.Fatalf("%v", rec.Header())
		}
	})
	t.Run("an ordinary caller's write is not touched", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("PUT", "/api/mk/mk-local/f/x.bin", hCred, "token")
		if rec.Code != 200 || len(r.reached) != 1 {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("the body of the request is not read", func(t *testing.T) {
		r := newRig(t)
		rec := r.doBody("PUT", "/api/mk/mk-local/f/x.bin", failingReader{t}, hRecognize, "probe")
		if rec.Code != 403 {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("without a main URL the detail still says where to publish", func(t *testing.T) {
		m := New(Options{})
		err := m.readOnly(bastionmain.RouteMatch{Format: "npm", Repo: "npm-all"})
		if e, ok := errs.As(err); !ok || e.Code != iface.CodeReadOnly || e.Status() != 403 || !strings.Contains(e.Detail, "main installation") {
			t.Fatalf("%v", err)
		}
	})
}

type failingReader struct{ t *testing.T }

func (f failingReader) Read([]byte) (int, error) {
	f.t.Error("the body was read")
	return 0, io.EOF
}

// TestR_BAS_014_RealFormats: the refusal is in the error shape of npm and of NuGet, and every write route of
// both is refused as a probe.
func TestR_BAS_014_RealFormats(t *testing.T) {
	fs := []formats.Format{npm.New(), nuget.New()}
	keys := map[string]string{"npm": "npm-all", "nuget": "nuget-v3"}
	t.Run("npm publish prints the error in npm's shape with the URL to publish to", func(t *testing.T) {
		r := newRig(t, fs...)
		rec := r.do("PUT", "/api/npm/npm-all/lodash", hRecognize, "probe")
		body := rec.Body.String()
		if rec.Code != 403 || rec.Header().Get("Content-Type") != "application/json" ||
			!strings.Contains(body, `"code":"bastion_read_only"`) || !strings.Contains(body, "publish to "+mainURL+"/api/npm/npm-all/") {
			t.Fatalf("%d %v\n%s", rec.Code, rec.Header(), body)
		}
	})
	t.Run("npm audit is a read route that takes a body", func(t *testing.T) {
		r := newRig(t, fs...)
		for _, p := range []string{"/-/npm/v1/security/advisories/bulk", "/-/npm/v1/security/audits", "/-/npm/v1/security/audits/quick"} {
			rec := r.do("POST", "/api/npm/npm-all"+p, hRecognize, "probe")
			if rec.Code != 204 || rec.Header().Get(iface.HeaderRoute) != iface.RouteReadWithBody {
				t.Errorf("%s: %d %v", p, rec.Code, rec.Header())
			}
		}
	})
	t.Run("dotnet nuget push is refused without a credential", func(t *testing.T) {
		r := newRig(t, fs...)
		rec := r.do("PUT", "/api/nuget/nuget-v3/v3/package", hRecognize, "probe")
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "bastion_read_only") {
			t.Fatalf("%d %v\n%s", rec.Code, rec.Header(), rec.Body.String())
		}
	})
	t.Run("every route that writes, of both formats, is refused as a probe", func(t *testing.T) {
		r := newRig(t, fs...)
		refused, bodies := 0, 0
		for _, f := range fs {
			id := string(f.Descriptor().ID)
			for _, rt := range f.Routes() {
				writes := rt.Kind == formats.Upload || rt.Kind == formats.Delete || rt.Kind == formats.Admin || rt.Action != formats.ActionRead
				if rt.Method == http.MethodGet || rt.Method == http.MethodHead {
					continue
				}
				for _, ex := range rt.Examples {
					rec := r.do(rt.Method, "/api/"+id+"/"+keys[id]+"/"+ex, hRecognize, "probe")
					switch {
					case writes && rec.Code == 403 && strings.Contains(rec.Body.String(), "bastion_read_only"):
						refused++
					case !writes && rec.Code == 204 && rec.Header().Get(iface.HeaderRoute) == iface.RouteReadWithBody:
						bodies++
					default:
						t.Errorf("%s %s %s: %d %v", rt.Method, id, ex, rec.Code, rec.Header())
					}
				}
			}
		}
		if refused < 10 || bodies < 3 {
			t.Fatalf("refused %d, with body %d: the route tables look empty", refused, bodies)
		}
	})
}

func counter(t *testing.T, m *bastionmain.Metrics, class string) float64 {
	t.Helper()
	var out dto.Metric
	if err := m.Forwarded.WithLabelValues("edge-1", class).Write(&out); err != nil {
		t.Fatal(err)
	}
	return out.GetCounter().GetValue()
}
