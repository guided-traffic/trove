package npmstub

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAdvisoryEndpoint(t *testing.T) {
	f := newFixture(t, Config{ControlToken: "tok"})
	const bulk = "/-/npm/v1/security/advisories/bulk"
	body := `{"left-pad":["1.3.0"],"@cobalt/dusky-gauge":["2.0.0"],"azure-orbit":["1.0.0"]}`

	t.Run("R-NPM-038 the recorded advisories of the names asked about, in the public registry's shape", func(t *testing.T) {
		r := f.do(http.MethodPost, bulk, map[string]string{"Content-Type": "application/json", "Authorization": "Bearer x"}, strings.NewReader(body))
		if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
			t.Fatalf("%d %s", r.status, r.body)
		}
		var got map[string][]struct {
			ID                 int    `json:"id"`
			Severity           string `json:"severity"`
			VulnerableVersions string `json:"vulnerable_versions"`
		}
		if err := json.Unmarshal(r.body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["left-pad"][0].ID != 1900001 || got["@cobalt/dusky-gauge"][0].VulnerableVersions != ">=1.0.0 <3.0.0" {
			t.Fatalf("%s", r.body)
		}
	})

	t.Run("R-NPM-038 a gzip body is read too, and every request is in the log", func(t *testing.T) {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte(`{"lodash":["4.17.20"]}`))
		_ = zw.Close()
		r := f.do(http.MethodPost, bulk, map[string]string{"Content-Encoding": "gzip"}, &b)
		if r.status != 200 || !strings.Contains(string(r.body), "1900002") {
			t.Fatalf("%d %s", r.status, r.body)
		}
		got := f.stub.Audits()
		if len(got) != 2 || got[0].Auth != "bearer" || got[1].Encoding != "gzip" || !Asked(got, "azure-orbit") || Asked(got, "react") {
			t.Fatalf("%+v", got)
		}
		var api struct {
			Requests []AuditRequest `json:"requests"`
		}
		if err := json.Unmarshal(f.do(http.MethodGet, "/-/stub/audits", map[string]string{"Authorization": "Bearer tok"}, nil).body, &api); err != nil || len(api.Requests) != 2 {
			t.Fatalf("control API: %v %+v", err, api)
		}
		if n := f.stub.Counts().count(KindAudit, "", ""); n != 2 {
			t.Errorf("counted %d audit requests", n)
		}
	})

	t.Run("R-NPM-038 the switches apply: delay, down, authorization; a body that is no object is 400", func(t *testing.T) {
		if err := f.stub.SetMode(Mode{DelayMS: map[string]int{KindAudit: 150}}); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if r := f.do(http.MethodPost, bulk, nil, strings.NewReader(`{}`)); r.status != 200 || string(r.body) != "{}" || time.Since(start) < 150*time.Millisecond {
			t.Fatalf("%d %s after %s", r.status, r.body, time.Since(start))
		}
		_ = f.stub.SetMode(Mode{Down: DownUnavailable})
		if r := f.do(http.MethodPost, bulk, nil, strings.NewReader(`{}`)); r.status != 503 {
			t.Fatalf("down: %d", r.status)
		}
		_ = f.stub.SetMode(Mode{RequireAuthorization: "Bearer secret"})
		if r := f.do(http.MethodPost, bulk, nil, strings.NewReader(`{}`)); r.status != 401 {
			t.Fatalf("no credential: %d", r.status)
		}
		_ = f.stub.SetMode(Mode{})
		if r := f.do(http.MethodPost, bulk, nil, strings.NewReader(`[]`)); r.status != 400 {
			t.Fatalf("no object: %d", r.status)
		}
		if r := f.do(http.MethodGet, bulk, nil, nil); r.status != 405 {
			t.Fatalf("GET: %d", r.status)
		}
		f.stub.Reset()
		if len(f.stub.Audits()) != 0 {
			t.Fatal("Reset left the log")
		}
	})

	t.Run("R-NPM-038 the recorded advisories carry what pnpm accepts: a numeric id, a known severity, a range", func(t *testing.T) {
		for name, list := range Advisories() {
			for _, raw := range list {
				var a struct {
					ID                 *float64 `json:"id"`
					Severity           string   `json:"severity"`
					VulnerableVersions string   `json:"vulnerable_versions"`
				}
				if err := json.Unmarshal(raw, &a); err != nil || a.ID == nil || a.VulnerableVersions == "" ||
					!strings.Contains(" info low moderate high critical ", " "+a.Severity+" ") {
					t.Errorf("%s: %s", name, raw)
				}
			}
		}
	})
}
