package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// The unit tests of the operations of the Bastion interface that need no database: the check (R-BAS-011) with a
// fake authentication stage and a fake shape resolver, and the validation of the downloads intake. The feed, the
// handshake and the contact are tested against PostgreSQL (test/integration/bastioniface).

// recorded is an audit.Recorder that counts the asynchronous events it is given.
type recorded struct {
	mu sync.Mutex
	n  int
}

func (r *recorded) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (r *recorded) Record(context.Context, audit.Event)                  { r.mu.Lock(); r.n++; r.mu.Unlock() }

func (r *recorded) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.n }

type fakeShapes struct {
	shapes map[string]bastionmain.Shape
	asked  []string
	err    error
}

func (f *fakeShapes) Shapes(_ context.Context, _ *authz.Index, _ authz.Subject, keys []qname.Repo) (map[qname.Repo]bastionmain.Shape, error) {
	out := map[qname.Repo]bastionmain.Shape{}
	for _, k := range keys {
		f.asked = append(f.asked, k.String())
		if sh, ok := f.shapes[k.String()]; ok {
			out[k] = sh
		}
	}
	return out, f.err
}

// beginAccess is an Access that can begin a request's index without a decision point.
type beginAccess struct{ allowAll }

func (beginAccess) Begin(ctx context.Context, _ string, _ ...qname.Repo) (*edge.Access, error) {
	return &edge.Access{Subject: edge.SubjectOf(ctx)}, nil
}

const (
	bastionTokenID     = "0192f1c4-0000-7000-8000-000000000001"
	callerTokenID      = "0192f1c4-0000-7000-8000-0000000000e1"
	checkedPrincipalID = "0192f1c4-0000-7000-8000-0000000000e2"
)

// checkSetup serves the interface routes behind a stand-in for the recognition stage.
type checkSetup struct {
	setup
	shapes *fakeShapes
	asked  *http.Request
}

func newCheckSetup(t *testing.T, authn func(*http.Request) (httpserver.Principal, error)) *checkSetup {
	t.Helper()
	cs := &checkSetup{shapes: &fakeShapes{shapes: map[string]bastionmain.Shape{
		"it/npm-all":     {Read: iface.ReadFull},
		"it/npm-partner": {Read: iface.ReadNone, Status: http.StatusNotFound},
		"it/npm-local":   {Read: iface.ReadPartial},
	}}}
	svc := BastionServices{
		Config: config.Bastions{Check: config.BastionsCheck{MaxRepositories: 3}, Downloads: config.BastionsDownloads{MaxBatch: 2, MaxAge: time.Hour}},
		Authenticate: func(r *http.Request) (httpserver.Principal, error) {
			cs.asked = r
			return authn(r)
		},
		Shapes: cs.shapes,
	}
	inner := newSetup(t, func(o *Options) {
		o.Access, o.Bastion, o.API.Config.MaxRequestBodyBytes = beginAccess{}, svc, config.MiB
	}, nil)
	cs.setup = inner
	h := cs.handler
	cs.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(bastionmain.WithRecognition(r.Context(), bastionmain.Recognition{Bastion: "partners", BastionID: "0192f1c4-0000-7000-8000-0000000000b1", TokenID: bastionTokenID, Route: true, Interface: iface.InterfaceVersion}))
		h.ServeHTTP(w, r)
	})
	return cs
}

func (cs *checkSetup) post(path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	cs.handler.ServeHTTP(w, r)
	return w
}

func checkedCaller(exp *time.Time) func(*http.Request) (httpserver.Principal, error) {
	return func(*http.Request) (httpserver.Principal, error) {
		return httpserver.Principal{
			Kind: httpserver.KindServiceAccount, ID: checkedPrincipalID, Name: "acme-full",
			Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: callerTokenID, ExpiresAt: exp},
		}, nil
	}
}

func decodeCheck(t *testing.T, w *httptest.ResponseRecorder) iface.CheckAnswer {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get(iface.HeaderInterface); got != strconv.Itoa(iface.InterfaceVersion) {
		t.Errorf("%s = %q", iface.HeaderInterface, got)
	}
	var ans iface.CheckAnswer
	if err := iface.DecodeStrict(w.Body, &ans); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return ans
}

// TestR_BAS_011_ACheckRunsTheCallersCredentialThroughTheAuthenticationStage: the subject of a valid token, the
// shapes with the status for none, an unknown key, and the request the stage got: the caller's credential as a
// bearer, the caller's address, a package path and a safe method.
func TestR_BAS_011_ACheckRunsTheCallersCredentialThroughTheAuthenticationStage(t *testing.T) {
	exp := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	cs := newCheckSetup(t, checkedCaller(&exp))
	cs.shapes.shapes["it/npm-missing"] = bastionmain.Shape{Read: iface.ReadNone, Status: http.StatusNotFound}
	w := cs.post(iface.PathCheck, `{"credential":"trv_sat_3kQ9","clientAddress":"203.0.113.7","repositories":["it/npm-all","it/npm-partner","it/npm-local","it/npm-missing"]}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("four repositories with max_repositories 3: %d %s", w.Code, w.Body)
	}
	w = cs.post(iface.PathCheck, `{"credential":"trv_sat_3kQ9","clientAddress":"203.0.113.7","repositories":["it/npm-all","it/npm-partner","it/npm-local"]}`)
	ans := decodeCheck(t, w)
	if ans.Subject.Kind != iface.SubjectToken || ans.Subject.TokenID != callerTokenID || ans.Subject.PrincipalID != checkedPrincipalID || ans.Subject.ExpiresAt == nil || !ans.Subject.ExpiresAt.Equal(exp) {
		t.Errorf("subject = %+v", ans.Subject)
	}
	want := map[string]iface.RepositoryRead{
		"it/npm-all":     {Read: iface.ReadFull},
		"it/npm-partner": {Read: iface.ReadNone, Status: 404},
		"it/npm-local":   {Read: iface.ReadPartial},
	}
	for k, v := range want {
		if ans.Repositories[k] != v {
			t.Errorf("%s = %+v, want %+v", k, ans.Repositories[k], v)
		}
	}
	r := cs.asked
	if r == nil {
		t.Fatal("the authentication stage was not asked")
	}
	if got := r.Header.Get("Authorization"); got != "Bearer trv_sat_3kQ9" {
		t.Errorf("Authorization = %q", got)
	}
	if got := httpserver.InfoFrom(r.Context()).Client; got != netip.MustParseAddr("203.0.113.7") {
		t.Errorf("the stage sees the client address %v: the throttle would count the Bastion's address", got)
	}
	if r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, "/api/v1") || !strings.HasPrefix(r.URL.Path, "/api/") {
		t.Errorf("the stage got %s %s", r.Method, r.URL.Path)
	}
	if len(r.Header) != 1 {
		t.Errorf("the stage got headers %v: the Bastion's own must not reach it", r.Header)
	}
}

func TestR_BAS_011_AnExpiryIsAbsentForATokenThatDoesNotExpire(t *testing.T) {
	cs := newCheckSetup(t, checkedCaller(nil))
	w := cs.post(iface.PathCheck, `{"credential":"trv_sat_x","clientAddress":"2001:db8::7","repositories":["it/npm-all"]}`)
	if ans := decodeCheck(t, w); ans.Subject.ExpiresAt != nil || strings.Contains(w.Body.String(), "expiresAt") {
		t.Errorf("subject = %+v body %s", ans.Subject, w.Body)
	}
}

// TestR_BAS_011_ASubjectIsInvalidAnonymousRefusedOrThrottled: the verdicts of the stage that are not a valid
// credential become the subject kinds; a subject that cannot be used carries no shapes.
func TestR_BAS_011_ASubjectIsInvalidAnonymousRefusedOrThrottled(t *testing.T) {
	invalid := errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The token is not valid.")
	for name, c := range map[string]struct {
		err    error
		body   string
		kind   iface.SubjectKind
		retry  int
		shapes bool
	}{
		"revoked":           {invalid, `{"credential":"trv_pat_x","clientAddress":"203.0.113.7","repositories":["it/npm-all"]}`, iface.SubjectInvalid, 0, false},
		"owner disabled":    {errs.E(errs.Unauthenticated, errs.CodePrincipalDisabled, "x"), `{"credential":"trv_pat_x","clientAddress":"203.0.113.7","repositories":["it/npm-all"]}`, iface.SubjectInvalid, 0, false},
		"refused anonymous": {errs.E(errs.Unauthenticated, errs.CodeCredentialsRequired, "x"), `{"credential":null,"clientAddress":"203.0.113.7","repositories":["it/npm-all"]}`, iface.SubjectAnonymousRefused, 0, false},
		"throttled": {
			errs.E(errs.RateLimited, errs.CodeRateLimited, "x", errs.WithRetryAfter(59500*time.Millisecond)),
			`{"credential":"trv_pat_x","clientAddress":"203.0.113.7","repositories":["it/npm-all"]}`, iface.SubjectThrottled, 60, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cs := newCheckSetup(t, func(*http.Request) (httpserver.Principal, error) { return httpserver.Principal{}, c.err })
			ans := decodeCheck(t, cs.post(iface.PathCheck, c.body))
			if ans.Subject.Kind != c.kind || len(ans.Repositories) != 0 || len(cs.shapes.asked) != 0 {
				t.Errorf("subject %+v, %d shapes, resolver asked %v", ans.Subject, len(ans.Repositories), cs.shapes.asked)
			}
			if c.kind == iface.SubjectThrottled && (ans.Subject.RetryAfterSeconds == nil || *ans.Subject.RetryAfterSeconds != c.retry) {
				t.Errorf("retryAfterSeconds = %v, want %d", ans.Subject.RetryAfterSeconds, c.retry)
			}
		})
	}

	t.Run("an anonymous caller is checked like any other and a none names 401", func(t *testing.T) {
		cs := newCheckSetup(t, func(*http.Request) (httpserver.Principal, error) { return httpserver.AnonymousPrincipal(), nil })
		cs.shapes.shapes["it/npm-secret"] = bastionmain.Shape{Read: iface.ReadNone}
		ans := decodeCheck(t, cs.post(iface.PathCheck, `{"credential":null,"clientAddress":"203.0.113.7","repositories":["it/npm-all","it/npm-secret"]}`))
		if ans.Subject.Kind != iface.SubjectAnonymous || ans.Repositories["it/npm-secret"] != (iface.RepositoryRead{Read: iface.ReadNone, Status: 401}) {
			t.Errorf("answer = %+v", ans)
		}
		if cs.asked.Header.Get("Authorization") != "" {
			t.Errorf("an anonymous check sent a credential: %q", cs.asked.Header.Get("Authorization"))
		}
	})

	t.Run("a failure of the stage's own stores is an error of the operation, not a verdict", func(t *testing.T) {
		cs := newCheckSetup(t, func(*http.Request) (httpserver.Principal, error) {
			return httpserver.Principal{}, errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "x")
		})
		if w := cs.post(iface.PathCheck, `{"credential":"trv_pat_x","clientAddress":"203.0.113.7","repositories":["it/npm-all"]}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d", w.Code)
		}
	})
}

func TestR_BAS_011_ANoneWithoutAStatusNamesWhatTheCallerWouldGet(t *testing.T) {
	if got := repositoryRead(bastionmain.Shape{Read: iface.ReadNone}, false); got != (iface.RepositoryRead{Read: iface.ReadNone, Status: 404}) {
		t.Errorf("named caller: %+v", got)
	}
	if got := repositoryRead(bastionmain.Shape{Read: iface.ReadNone, Status: 403}, false); got.Status != 403 {
		t.Errorf("403 kept: %+v", got)
	}
	if got := repositoryRead(bastionmain.Shape{Read: iface.ReadNone, Status: 500}, true); got.Status != 401 {
		t.Errorf("a status the wire does not carry: %+v", got)
	}
	if got := repositoryRead(bastionmain.Shape{Read: iface.ReadFull, Status: 404}, false); got != (iface.RepositoryRead{Read: iface.ReadFull}) {
		t.Errorf("a status with full: %+v", got)
	}
	if got := repositoryRead(bastionmain.Shape{}, false); got.Read != iface.ReadNone {
		t.Errorf("no shape is none: %+v", got)
	}
}

func TestR_BAS_011_ACheckBodyIsValidated(t *testing.T) {
	cs := newCheckSetup(t, checkedCaller(nil))
	for name, c := range map[string]struct {
		body   string
		status int
	}{
		"an address that is none":        {`{"credential":null,"clientAddress":"not-an-address","repositories":["it/npm-a"]}`, http.StatusUnprocessableEntity},
		"no repositories":                {`{"credential":null,"clientAddress":"203.0.113.7","repositories":[]}`, http.StatusUnprocessableEntity},
		"too many repositories":          {`{"credential":null,"clientAddress":"203.0.113.7","repositories":["it/npm-a","it/npm-b","it/npm-c","it/npm-d"]}`, http.StatusUnprocessableEntity},
		"a name without a project":       {`{"credential":null,"clientAddress":"203.0.113.7","repositories":["npm-all"]}`, http.StatusBadRequest},
		"an empty key":                   {`{"credential":null,"clientAddress":"203.0.113.7","repositories":[]}`, http.StatusUnprocessableEntity},
		"an unknown member":              {`{"credential":null,"clientAddress":"203.0.113.7","repositories":["it/npm-a"],"extra":1}`, http.StatusBadRequest},
		"trailing data":                  {`{"credential":null,"clientAddress":"203.0.113.7","repositories":["it/npm-a"]} {}`, http.StatusBadRequest},
		"a credential of the wrong type": {`{"credential":5,"clientAddress":"203.0.113.7","repositories":["it/npm-a"]}`, http.StatusBadRequest},
		"a credential longer than any":   {`{"credential":"` + strings.Repeat("x", 9000) + `","clientAddress":"203.0.113.7","repositories":["it/npm-a"]}`, http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			if w := cs.post(iface.PathCheck, c.body); w.Code != c.status {
				t.Errorf("status %d, want %d: %s", w.Code, c.status, w.Body)
			}
		})
	}
	t.Run("a body that is not JSON is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, iface.PathCheck, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		cs.handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status %d", w.Code)
		}
	})
	t.Run("without a resolver or a decision point the check is unavailable", func(t *testing.T) {
		s := newSetup(t, func(o *Options) {
			o.Access, o.Bastion, o.API.Config.MaxRequestBodyBytes = beginAccess{}, BastionServices{}, config.MiB
		}, nil)
		h := s.handler
		r := httptest.NewRequest(http.MethodPost, iface.PathCheck, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(bastionmain.WithRecognition(r.Context(), bastionmain.Recognition{Bastion: "p", Route: true, Interface: iface.InterfaceVersion}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d", w.Code)
		}
	})
}

// TestR_BAS_016_TheIntakeAnswers204AndRefusesAMalformedBody: the HTTP side of the intake; its rules are tested
// in internal/bastionmain.
func TestR_BAS_016_TheIntakeAnswers204AndRefusesAMalformedBody(t *testing.T) {
	cs := newCheckSetup(t, checkedCaller(nil))
	// The intake is not wired in this setup: the operation answers 503, never a panic.
	if w := cs.post(iface.PathDownloads, `{"events":[],"dropped":0}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired intake: %d", w.Code)
	}
	rec := &recorded{}
	svc := BastionServices{
		Config:    config.Bastions{Downloads: config.BastionsDownloads{MaxBatch: 2, MaxAge: time.Hour}},
		Downloads: bastionmain.NewIntake(bastionmain.IntakeOptions{Audit: rec, MaxBatch: 2, MaxAge: time.Hour, Record: true}),
	}
	s := newSetup(t, func(o *Options) {
		o.Access, o.Bastion, o.API.Config.MaxRequestBodyBytes = beginAccess{}, svc, config.MiB
	}, nil)
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, iface.PathDownloads, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(bastionmain.WithRecognition(r.Context(), bastionmain.Recognition{Bastion: "partners", BastionID: "0192f1c4-0000-7000-8000-0000000000b1", Route: true, Interface: iface.InterfaceVersion}))
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, r)
		return w
	}
	ev := `{"at":"` + time.Now().UTC().Format(time.RFC3339) + `","clientAddress":"203.0.113.7","repository":"npm-all","path":"/api/npm/npm-all/x/-/x-1.0.0.tgz","sha256":"` + strings.Repeat("ab", 32) + `","replica":"bastion-0"}`
	w := post(`{"events":[` + ev + `],"dropped":0}`)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get(iface.HeaderInterface) != strconv.Itoa(iface.InterfaceVersion) || rec.count() != 1 {
		t.Errorf("status %d body %q events %d", w.Code, w.Body, rec.count())
	}
	for name, body := range map[string]string{
		"an unknown member": `{"events":[],"dropped":0,"x":1}`,
		"a bad time":        `{"events":[{"at":"yesterday"}],"dropped":0}`,
	} {
		if w := post(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := post(`{"events":[` + ev + `,` + ev + `,` + ev + `],"dropped":0}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a batch above max_batch: %d", w.Code)
	}
	var p map[string]any
	_ = json.Unmarshal(post(`{"events":[],"dropped":-1}`).Body.Bytes(), &p)
	if p["code"] != errs.CodeInvalid {
		t.Errorf("a negative count: %v", p)
	}
}
