package bastionmain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

const (
	tokValid   = "trv_bst_validvalidvalidvalidvalidvalid"
	tokRevoked = "trv_bst_revokedrevokedrevokedrevokedrevoked"
	tokStore   = "trv_bst_storefailurestorefailurestore"
	pat        = "trv_pat_0123456789012345678901234567890123"
)

// fakeTokens is the TokenAuthenticator of the tests. Like the token service, it asks the throttle of the request
// only for a token it does not hold (tokValid is held) and then refuses without a lookup.
type fakeTokens struct {
	calls int
	reqs  []AuthRequest
}

func (f *fakeTokens) Authenticate(ctx context.Context, raw string, req AuthRequest) (BastionToken, error) {
	f.calls++
	f.reqs = append(f.reqs, req)
	if raw != tokValid && req.Throttle != nil && req.Throttle.Exceeded(ctx) {
		return BastionToken{}, ErrThrottled
	}
	switch raw {
	case tokValid:
		return BastionToken{
			TokenID: "0192f1c4-0000-7000-8000-000000000001", Hint: "vali", BastionID: "0192f1c4-0000-7000-8000-0000000000b1",
			Name: "partners", ExpiresAt: time.Date(2027, 11, 2, 0, 0, 0, 0, time.UTC),
		}, nil
	case tokStore:
		return BastionToken{}, errors.New("connection refused")
	}
	return BastionToken{}, ErrTokenInvalid
}

// run sends r through the stage and returns the answer and what the handler behind it saw.
func run(t *testing.T, o RecognizeOptions, r *http.Request) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	if o.Problem == nil {
		o.Problem = func(w http.ResponseWriter, r *http.Request, err error) { errs.Write(w, r, err, "req-1", nil) }
	}
	var seen *http.Request
	h := Recognize(o)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, seen
}

func forwarded(path string, hdr ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

func version() string { return strconv.Itoa(iface.InterfaceVersion) }

func problem(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != errs.ProblemContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, errs.ProblemContentType)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	return m
}

// TestR_BAS_008_ARevokedBastionTokenAnswersAProblemOnAPackageRoute: the first acceptance criterion of R-BAS-008.
func TestR_BAS_008_ARevokedBastionTokenAnswersAProblemOnAPackageRoute(t *testing.T) {
	m, _ := NewMetrics(nil)
	w, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}, Metrics: m},
		forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+tokRevoked, iface.HeaderInterface, version()))
	if w.Code != http.StatusUnauthorized || seen != nil {
		t.Fatalf("status %d, handler reached: %v", w.Code, seen != nil)
	}
	if got := problem(t, w)["code"]; got != errs.CodeBastionTokenInvalid {
		t.Errorf("code = %v", got)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
}

// TestR_BAS_008_ATokenOfAnotherTypeLeavesTheRequestOrdinary: a valid token of another type in
// Trove-Bastion-Authorization is ignored; no Recognition, no interface header; so is every other Trove-Bastion-*
// header without a valid token.
func TestR_BAS_008_ATokenOfAnotherTypeLeavesTheRequestOrdinary(t *testing.T) {
	tokens := &fakeTokens{}
	for name, r := range map[string]*http.Request{
		"a personal access token":     forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+pat, iface.HeaderInterface, version(), iface.HeaderReplica, "pod-1"),
		"no Bastion token":            forwarded("/api/npm/web/npm-all/lodash", iface.HeaderInterface, version(), iface.HeaderProbe, "1", iface.HeaderReplica, "pod-1"),
		"another scheme":              forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, "Basic "+tokValid),
		"a bare token":                forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, tokValid),
		"an empty value":              forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, ""),
		"a PAT on an interface route": forwarded(iface.PathHandshake, "Authorization", "Bearer "+pat, iface.HeaderInterface, version()),
	} {
		t.Run(name, func(t *testing.T) {
			w, seen := run(t, RecognizeOptions{Tokens: tokens}, r)
			if w.Code != http.StatusOK || seen == nil {
				t.Fatalf("status %d, handler reached: %v", w.Code, seen != nil)
			}
			if _, ok := RecognitionFrom(seen.Context()); ok {
				t.Error("the request carries a Recognition")
			}
			if got := w.Header().Get(iface.HeaderInterface); got != "" {
				t.Errorf("an ordinary request's answer carries %s: %q", iface.HeaderInterface, got)
			}
		})
	}
	if tokens.calls != 0 {
		t.Errorf("the authenticator was asked %d times for credentials that are no Bastion token", tokens.calls)
	}
}

func TestR_BAS_008_AValidTokenRecognizesTheRequest(t *testing.T) {
	t.Run("a forwarded request keeps the caller's credential and loses the Bastion token", func(t *testing.T) {
		r := forwarded("/api/npm/web/npm-all/lodash",
			iface.HeaderAuthorization, "bearer "+tokValid, iface.HeaderInterface, version(), iface.HeaderReplica, "trove-bastion-7d9c-x2k4q",
			"Authorization", "Bearer caller-credential")
		w, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, r)
		if seen == nil {
			t.Fatalf("not served: %d %s", w.Code, w.Body)
		}
		rec, ok := RecognitionFrom(seen.Context())
		if !ok {
			t.Fatal("no Recognition")
		}
		want := Recognition{
			Bastion: "partners", BastionID: "0192f1c4-0000-7000-8000-0000000000b1", TokenID: "0192f1c4-0000-7000-8000-000000000001",
			TokenExpiresAt: time.Date(2027, 11, 2, 0, 0, 0, 0, time.UTC), Interface: iface.InterfaceVersion, Replica: "trove-bastion-7d9c-x2k4q",
		}
		if rec != want {
			t.Errorf("Recognition = %+v, want %+v", rec, want)
		}
		if got := seen.Header.Get("Authorization"); got != "Bearer caller-credential" {
			t.Errorf("the caller's Authorization = %q", got)
		}
		if got := seen.Header.Get(iface.HeaderAuthorization); got != "" {
			t.Errorf("the Bastion token reaches the stages behind: %q", got)
		}
		if got := r.Header.Get(iface.HeaderAuthorization); got == "" {
			t.Error("the stage changed the request it was given (the outer stages log it)")
		}
		if got := w.Header().Get(iface.HeaderInterface); got != version() {
			t.Errorf("%s = %q", iface.HeaderInterface, got)
		}
	})

	t.Run("an interface route takes the token out of Authorization", func(t *testing.T) {
		w, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, forwarded(iface.PathHandshake, "Authorization", "Bearer "+tokValid, iface.HeaderInterface, version()))
		if seen == nil {
			t.Fatalf("not served: %d %s", w.Code, w.Body)
		}
		rec, _ := RecognitionFrom(seen.Context())
		if !rec.Route || seen.Header.Get("Authorization") != "" {
			t.Errorf("route %v, Authorization %q", rec.Route, seen.Header.Get("Authorization"))
		}
	})

	t.Run("the probe flag needs a method that is not GET or HEAD", func(t *testing.T) {
		for method, want := range map[string]bool{http.MethodPut: true, http.MethodPost: true, http.MethodDelete: true, http.MethodGet: false, http.MethodHead: false} {
			r := httptest.NewRequest(method, "/api/npm/web/npm-all/lodash", nil)
			r.Header.Set(iface.HeaderAuthorization, "Bearer "+tokValid)
			r.Header.Set(iface.HeaderInterface, version())
			r.Header.Set(iface.HeaderProbe, iface.ProbeValue)
			_, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, r)
			rec, _ := RecognitionFrom(seen.Context())
			if rec.Probe != want {
				t.Errorf("%s: Probe = %v, want %v", method, rec.Probe, want)
			}
		}
	})

	t.Run("the replica name is clipped and holds no control characters", func(t *testing.T) {
		r := forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version())
		r.Header.Set(iface.HeaderReplica, "pod\x01"+strings.Repeat("é", 300))
		_, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, r)
		rec, _ := RecognitionFrom(seen.Context())
		if len(rec.Replica) > maxReplicaName || strings.ContainsRune(rec.Replica, 1) || !strings.HasPrefix(rec.Replica, "pod") {
			t.Errorf("replica = %q (%d bytes)", rec.Replica, len(rec.Replica))
		}
	})
}

// TestR_BAS_009_AMissingOrDifferentInterfaceVersionAnswersAMismatch: the version of a recognized request is
// checked before anything else; the problem names the main installation's version.
func TestR_BAS_009_AMissingOrDifferentInterfaceVersionAnswersAMismatch(t *testing.T) {
	m, _ := NewMetrics(nil)
	for name, r := range map[string]*http.Request{
		"missing on a package route": forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+tokValid),
		"another version":            forwarded("/api/npm/web/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, "2"),
		"not a number":               forwarded(iface.PathFeed, "Authorization", "Bearer "+tokValid, iface.HeaderInterface, "one"),
		"leading zero":               forwarded(iface.PathFeed, "Authorization", "Bearer "+tokValid, iface.HeaderInterface, "01"),
		"missing on a route":         forwarded(iface.PathHandshake, "Authorization", "Bearer "+tokValid),
	} {
		t.Run(name, func(t *testing.T) {
			w, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}, Metrics: m}, r)
			if w.Code != http.StatusBadRequest || seen != nil {
				t.Fatalf("status %d, handler reached: %v", w.Code, seen != nil)
			}
			p := problem(t, w)
			if p["code"] != errs.CodeBastionInterfaceMismatch || p[iface.ProblemMemberInterfaceVersion] != float64(iface.InterfaceVersion) {
				t.Errorf("problem = %v", p)
			}
			if got := w.Header().Get(iface.HeaderInterface); got != version() {
				t.Errorf("%s = %q: the main installation names its own version", iface.HeaderInterface, got)
			}
		})
	}

	t.Run("two version headers are a mismatch, not a pick", func(t *testing.T) {
		r := forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid)
		r.Header.Add(iface.HeaderInterface, version())
		r.Header.Add(iface.HeaderInterface, version())
		if w, _ := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, r); w.Code != http.StatusBadRequest {
			t.Errorf("status %d", w.Code)
		}
	})

	t.Run("an invalid token is refused before the version is looked at", func(t *testing.T) {
		w, _ := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokRevoked, iface.HeaderInterface, "9"))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status %d", w.Code)
		}
	})
}

func TestR_BAS_008_ABastionTokenTheStoreCannotCheckIsAnOutageNotAVerdict(t *testing.T) {
	w, seen := run(t, RecognizeOptions{Tokens: &fakeTokens{}}, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokStore, iface.HeaderInterface, version()))
	if w.Code != http.StatusServiceUnavailable || seen != nil {
		t.Fatalf("status %d", w.Code)
	}
	if problem(t, w)["code"] != errs.CodeDatabaseUnavailable {
		t.Errorf("body %s", w.Body)
	}
}

func TestR_BAS_008_WithoutAnAuthenticatorEveryBastionTokenIsInvalid(t *testing.T) {
	w, _ := run(t, RecognizeOptions{}, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version()))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d", w.Code)
	}
}

// TestR_BAS_008_InterfaceRequestsAreCountedByRegistrationOperationAndOutcome: trove_bastion_interface_requests_total.
func TestR_BAS_008_InterfaceRequestsAreCountedByRegistrationOperationAndOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusOK
	serve := func(r *http.Request) {
		h := Recognize(RecognizeOptions{Tokens: &fakeTokens{}, Metrics: m, Problem: func(w http.ResponseWriter, r *http.Request, err error) { errs.Write(w, r, err, "", nil) }})(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	good := func(path string) *http.Request {
		return forwarded(path, "Authorization", "Bearer "+tokValid, iface.HeaderInterface, version())
	}
	serve(good(iface.PathHandshake))
	serve(good(iface.PathHandshake))
	status = http.StatusUnprocessableEntity
	serve(good(iface.PathCheck))
	status = http.StatusServiceUnavailable
	serve(good(iface.PathFeed))
	status = http.StatusNotImplemented
	serve(good(iface.PathDownloads))
	serve(forwarded(iface.PathFeed, "Authorization", "Bearer "+tokRevoked, iface.HeaderInterface, version()))
	serve(forwarded(iface.PathHandshake, "Authorization", "Bearer "+tokValid))
	serve(good("/api/v1/bastion/unknown"))
	status = http.StatusOK
	serve(forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version()))

	for _, c := range []struct {
		bastion, op, outcome string
		want                 float64
	}{
		{"partners", iface.OperationHandshake, OutcomeOK, 2},
		{"partners", iface.OperationCheck, OutcomeRefused, 1},
		{"partners", iface.OperationFeed, OutcomeError, 1},
		{"partners", iface.OperationDownloads, OutcomeNotServed, 1},
		{UnknownBastion, iface.OperationFeed, OutcomeInvalid, 1},
		{"partners", iface.OperationHandshake, OutcomeMismatch, 1},
	} {
		var d dto.Metric
		if err := m.Interface.WithLabelValues(c.bastion, c.op, c.outcome).Write(&d); err != nil {
			t.Fatal(err)
		}
		if got := d.GetCounter().GetValue(); got != c.want {
			t.Errorf("%s/%s/%s = %v, want %v", c.bastion, c.op, c.outcome, got, c.want)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == MetricInterfaceRequests && len(f.GetMetric()) != 6 {
			t.Errorf("%d series: a forwarded request or an unknown path was counted as an interface request", len(f.GetMetric()))
		}
	}
}
