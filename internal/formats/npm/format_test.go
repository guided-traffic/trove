package npm

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/observability"
)

func TestR_REPO_003_TheCatalogFactsOfNPM(t *testing.T) {
	d := New().Descriptor()
	if d.ID != "npm" || d.Layout != "npm" || d.ProtocolBasePath != "/api/npm/{key}/" || d.PatternSubject != formats.SubjectName {
		t.Fatalf("%+v", d.Facts)
	}
	if d.DefaultUpstreamURL != "https://registry.npmjs.org/" || d.ProbePath != "-/ping" ||
		d.DefaultMetadataTTLSeconds == nil || *d.DefaultMetadataTTLSeconds != 7200 ||
		d.DefaultNotFoundTTLSeconds == nil || *d.DefaultNotFoundTTLSeconds != 1800 {
		t.Fatalf("upstream facts %+v", d.Facts)
	}
	if !d.PackageNames || !d.PinsAndClaims || !d.PriorityMembers || d.Caps {
		t.Fatalf("%+v", d.Facts)
	}
	t.Run("R-AUTHZ-018 a local repository holds package ownership enforced by default", func(t *testing.T) {
		if d.OwnershipDefault != catalog.OwnershipEnforce {
			t.Fatalf("OwnershipDefault = %q", d.OwnershipDefault)
		}
	})
	t.Run("Q-ARCH-193 the data port is the Store interface and the property prefix is npm.", func(t *testing.T) {
		if d.DataPort != reflect.TypeFor[Store]() || d.PropertyPrefix != "npm." {
			t.Fatalf("%v %q", d.DataPort, d.PropertyPrefix)
		}
	})
	t.Run("R-NPM-001 the format lists the local class until its remote and virtual adapters exist", func(t *testing.T) {
		f := New()
		if !d.Supports(formats.Local) || d.Supports(formats.Remote) != (f.Remote() != nil) || d.Supports(formats.Virtual) != (f.Virtual() != nil) {
			t.Fatalf("classes %v remote %v virtual %v", d.Classes, f.Remote(), f.Virtual())
		}
	})
	t.Run("R-ARCH-010 the format is valid and is registered by formats/all", func(t *testing.T) {
		if err := formats.Validate(New()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestR_NPM_005_AccessLogFields: the request headers npm-command and npm-session reach
// the access record of a request that the handler answers and of one the router
// refuses, and are used for nothing else.
func TestR_NPM_005_AccessLogFields(t *testing.T) {
	f := New()
	var logs bytes.Buffer
	m := observability.NewMetrics("web")
	mw := observability.RequestLog(slog.New(slog.NewJSONHandler(&logs, nil)), m)
	run := func(h http.Handler, hdr ...string) map[string]any {
		logs.Reset()
		r := httptest.NewRequest("GET", "/api/npm/npm-local/-/ping", nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		mw(h).ServeHTTP(httptest.NewRecorder(), r)
		var rec map[string]any
		if err := json.Unmarshal(logs.Bytes(), &rec); err != nil {
			t.Fatalf("%v: %q", err, logs.String())
		}
		return rec
	}
	t.Run("a refusal rendered by WriteError", func(t *testing.T) {
		rec := run(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.WriteError(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "no"))
		}), "npm-command", "publish", "npm-session", "abc123")
		if rec["npm-command"] != "publish" || rec["npm-session"] != "abc123" {
			t.Fatalf("%v", rec)
		}
	})
	t.Run("a handler of a route", func(t *testing.T) {
		rq := request(t, f, &fakeHost{}, "GET", "", "", "npm-command", "view", "npm-session", "s\x01ess")
		rec := run(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rq.Request = r
			formats.ServeRoute(f, w, rq, false)
		}), "npm-command", "view", "npm-session", "s\x01ess")
		if rec["npm-command"] != "view" || rec["npm-session"] != "sess" {
			t.Fatalf("%v", rec)
		}
	})
	t.Run("a request without the headers has null fields", func(t *testing.T) {
		rec := run(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.WriteError(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "no"))
		}))
		if v, present := rec["npm-command"]; present && v != nil {
			t.Fatalf("%v", rec)
		}
	})
	t.Run("a value from the client is bounded", func(t *testing.T) {
		long := strings.Repeat("x", 5000)
		rec := run(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.WriteError(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "no"))
		}), "npm-session", long)
		if s, _ := rec["npm-session"].(string); len(s) > observability.MaxExtraField+len(observability.TruncatedMarker) {
			t.Fatalf("%d bytes", len(s))
		}
	})
}

// TestR_NPM_044_MetricsOfTheProtocol: every request is counted with its repository
// class, operation and status, and its duration observed, on the registry the replica
// bound; a replica that did not bind counts nothing and still serves.
func TestR_NPM_044_MetricsOfTheProtocol(t *testing.T) {
	f := New()
	m := observability.NewMetrics("web")
	host := &fakeHost{}
	f.Bind(host, defaultConfig(t), m)
	rq := request(t, f, host, "GET", "-/v1/login", "")
	serve(t, f, rq) // 404
	rq = request(t, f, host, "POST", "-/v1/login", "")
	serve(t, f, rq)                                 // 501
	serve(t, f, request(t, f, host, "GET", "", "")) // 200
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	var durations uint64
	for _, fam := range fams {
		for _, mm := range fam.GetMetric() {
			switch fam.GetName() {
			case "trove_npm_requests_total":
				key := ""
				for _, l := range mm.GetLabel() {
					key += l.GetName() + "=" + l.GetValue() + ","
				}
				got[key] = mm.GetCounter().GetValue()
			case "trove_npm_request_duration_seconds":
				durations += mm.GetHistogram().GetSampleCount()
			}
		}
	}
	for _, want := range []string{
		"class=local,operation=deferred.get,status=404,", "class=local,operation=deferred.post,status=501,", "class=local,operation=root,status=200,",
	} {
		if got[want] != 1 {
			t.Errorf("%s = %v; have %v", want, got[want], got)
		}
	}
	if durations != 3 {
		t.Errorf("%d durations", durations)
	}
	if errs := m.Check(); len(errs) != 0 {
		t.Errorf("metric naming rules: %v", errs)
	}
	t.Run("a Host nobody bound runs on the defaults", func(t *testing.T) {
		other := &fakeHost{}
		if w := serve(t, f, request(t, f, other, "GET", "", "")); w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
		if r := f.replicaOf(other); r.met != nil || r.cfg.Publish.MaxTarballSize == 0 || r.cfg.Publish.ManifestConsistency != "reject" {
			t.Fatalf("%+v", r)
		}
	})
}
