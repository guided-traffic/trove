package npm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// provider is the advisory provider of a test: it records what it was sent and answers
// with a fixed body, or with what answer returns.
type provider struct {
	sent      []formats.Exchange
	deadlines []time.Duration
	answer    func(x formats.Exchange) (*http.Response, error)
}

const providerBody = `{"lodash":[{"id":1,"url":"https://advisories.example.org/1","title":"t","severity":"high","vulnerable_versions":"<4.17.21"}]}`

func (p *provider) exchange(ctx context.Context, _ *formats.Request, x formats.Exchange) (*http.Response, error) {
	p.sent = append(p.sent, x)
	if dl, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, time.Until(dl))
	}
	if p.answer != nil {
		return p.answer(x)
	}
	return &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(providerBody)), ContentLength: int64(len(providerBody)),
	}, nil
}

// names are the names a forwarded body asked about.
func (p *provider) names(t *testing.T, i int) []string {
	t.Helper()
	var body map[string][]string
	if err := json.Unmarshal(p.sent[i].Body, &body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for n := range body {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// auditHost is a remote repository with the npm settings of config, the twin and a provider.
func auditHost(t *testing.T, class formats.Class, config string) (*formattest.Host, *npmtest.Store, *provider) {
	t.Helper()
	p := &provider{}
	twin := npmtest.New()
	h := formattest.NewHost(New(), formattest.HostOptions{
		Class: class, Data: twin, Remote: &formattest.MemRemote{Exchanges: p.exchange},
	})
	h.Repo().Config = []byte(config)
	return h, twin, p
}

// fetched records that the remote holds a stored document of the names.
func fetched(t *testing.T, twin *npmtest.Store, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := twin.ApplyFill(context.Background(), nil, rows.RemoteFill{RepoID: 1, Name: n}); err != nil {
			t.Fatal(err)
		}
	}
}

// audit posts an audit body on the default endpoint.
func audit(t *testing.T, h *formattest.Host, path, body string, hdr ...string) *formattest.Response {
	t.Helper()
	rq, err := h.NewRequest(context.Background(), http.MethodPost, path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rq.Endpoint = formats.EndpointFacts{Name: "default", CacheFill: true}
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	return h.ServeRequest(rq, false)
}

func gzipped(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func errorCode(t *testing.T, r *formattest.Response) string {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(r.Body.Bytes(), &b); err != nil || b.Error == "" || b.Message == "" {
		t.Fatalf("%d is no npm error body: %s", r.Code, r.Body)
	}
	return b.Code
}

func TestR_NPM_038_ARemoteForwardsOnlyProxiedNamesThatNothingMarksInternal(t *testing.T) {
	h, twin, p := auditHost(t, formats.Remote, `{}`)
	fetched(t, twin, "lodash", "left-pad", "@acme/ui", "@acme/tools", "pinned-name")
	twin.SetAuditFacts(npmtest.AuditFacts{
		Local: map[string]bool{"@acme/ui": true}, Claimed: map[string]bool{"@acme/tools": true}, Pinned: map[string]bool{"pinned-name": true},
	})
	body := `{"lodash":["4.17.20","4.17.21"],"left-pad":["1.3.0"],"@acme/ui":["1.0.0"],"@acme/tools":["2.0.0"],` +
		`"pinned-name":["1.0.0"],"never-fetched":["1.0.0"],"..":["1.0.0"]}`
	r := audit(t, h, auditBulkPath, body)
	if r.Code != 200 || r.Body.String() != providerBody {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	// A pin rule belongs to a virtual repository: the request addressed the remote, so none applies.
	if got := p.names(t, 0); strings.Join(got, ",") != "left-pad,lodash,pinned-name" {
		t.Fatalf("the provider was sent %v, want left-pad, lodash and pinned-name", got)
	}
	var sent map[string][]string
	_ = json.Unmarshal(p.sent[0].Body, &sent)
	if strings.Join(sent["lodash"], ",") != "4.17.20,4.17.21" {
		t.Errorf("the versions of lodash were forwarded as %v", sent["lodash"])
	}
	want := "trove audited 3 of 7 packages; 4 internal or not yet proxied names were not sent to the advisory provider"
	if got := r.Header().Get("npm-notice"); got != want {
		t.Errorf("npm-notice %q, want %q", got, want)
	}
	if r.Header().Get("Content-Type") != "application/json; charset=utf-8" || r.Header().Get("Cache-Control") != "no-store" ||
		r.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", r.Header())
	}
	if twin.AuditQueries != 1 {
		t.Errorf("%d queries, want one for the whole body", twin.AuditQueries)
	}
	x := p.sent[0]
	if x.Method != http.MethodPost || x.Path != auditBulkPath || x.Base != nil || x.Header.Get("Content-Type") != "application/json" {
		t.Errorf("exchange %+v", x)
	}

	t.Run("a gzip body is the same request", func(t *testing.T) {
		r := audit(t, h, auditBulkPath, gzipped(t, body), "Content-Encoding", "gzip")
		if r.Code != 200 || strings.Join(p.names(t, 1), ",") != "left-pad,lodash,pinned-name" {
			t.Fatalf("%d %s, sent %v", r.Code, r.Body, p.names(t, 1))
		}
	})

	t.Run("a body of withheld names only is {} and asks no provider", func(t *testing.T) {
		before := len(p.sent)
		r := audit(t, h, auditBulkPath, `{"@acme/ui":["1.0.0"],"never-fetched":["1.0.0"]}`)
		if r.Code != 200 || r.Body.String() != "{}" || len(p.sent) != before {
			t.Fatalf("%d %s, provider asked %d times", r.Code, r.Body, len(p.sent)-before)
		}
		if got := r.Header().Get("npm-notice"); got != "trove audited 0 of 2 packages; 2 internal or not yet proxied names were not sent to the advisory provider" {
			t.Errorf("npm-notice %q", got)
		}
	})

	t.Run("an empty body is {} without a notice", func(t *testing.T) {
		before := len(p.sent)
		r := audit(t, h, auditBulkPath, `{}`)
		if r.Code != 200 || r.Body.String() != "{}" || r.Header().Get("npm-notice") != "" || len(p.sent) != before {
			t.Fatalf("%d %s %v", r.Code, r.Body, r.Header())
		}
	})
}

func TestR_NPM_038_ReasonsAreCountedOnceInTheirOrder(t *testing.T) {
	for _, c := range []struct {
		a        rows.AuditName
		admitted bool
		want     string
	}{
		{rows.AuditName{Proxied: true}, true, ""},
		{rows.AuditName{Proxied: true}, false, withheldNotProxied},
		{rows.AuditName{}, true, withheldNotProxied},
		{rows.AuditName{Proxied: true, Local: true, Claimed: true, Pinned: true}, true, withheldLocal},
		{rows.AuditName{Claimed: true, Pinned: true}, true, withheldClaimed},
		{rows.AuditName{Pinned: true}, false, withheldPinned},
	} {
		if got := withheldReason(c.a, c.admitted); got != c.want {
			t.Errorf("%+v admitted %v: %q, want %q", c.a, c.admitted, got, c.want)
		}
	}
}

func TestR_NPM_038_TheBodyIsBounded(t *testing.T) {
	h, twin, p := auditHost(t, formats.Remote, `{"npm":{"audit":{"maxBodySize":"1KiB"}}}`)
	fetched(t, twin, "lodash")
	for _, c := range []struct {
		name, body string
		hdr        []string
		status     int
		code       string
	}{
		{"a body above the limit as sent", `{"lodash":["` + strings.Repeat("1", 1100) + `"]}`, nil, 413, errs.CodeNPMTooLarge},
		{"a small gzip body that inflates beyond ten times the limit", gzipped(t, `{"lodash":["`+strings.Repeat("0", 11<<10)+`"]}`), []string{"Content-Encoding", "gzip"}, 413, errs.CodeNPMTooLarge},
		{"a declared length above the limit, before a byte is read", `{}`, []string{"Content-Length", "2048"}, 413, errs.CodeNPMTooLarge},
		{"a content coding other than gzip", `{}`, []string{"Content-Encoding", "br"}, 415, errs.CodeUnsupported},
		{"gzip that is not", `{}`, []string{"Content-Encoding", "gzip"}, 400, errs.CodeInvalid},
		{"no object", `["lodash"]`, nil, 400, errs.CodeInvalid},
		{"versions that are no list", `{"lodash":"4.17.21"}`, nil, 400, errs.CodeInvalid},
		{"null", `null`, nil, 400, errs.CodeInvalid},
		{"data after the object", `{"lodash":[]} {}`, nil, 400, errs.CodeInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			rq, err := h.NewRequest(context.Background(), http.MethodPost, auditBulkPath, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			rq.Endpoint = formats.EndpointFacts{Name: "default", CacheFill: true}
			for i := 0; i+1 < len(c.hdr); i += 2 {
				rq.Header.Set(c.hdr[i], c.hdr[i+1])
			}
			if rq.Header.Get("Content-Length") != "" {
				rq.ContentLength = 2048
			}
			r := h.ServeRequest(rq, false)
			if r.Code != c.status || errorCode(t, r) != c.code {
				t.Fatalf("%d %s, want %d %s", r.Code, r.Body, c.status, c.code)
			}
		})
	}
	if len(p.sent) != 0 {
		t.Errorf("a refused body reached the provider %d times", len(p.sent))
	}

	t.Run("more names than one audit takes", func(t *testing.T) {
		h, _, _ := auditHost(t, formats.Remote, `{"npm":{"audit":{"maxBodySize":"10MiB"}}}`)
		var b strings.Builder
		b.WriteString("{")
		for i := range auditMaxNames + 1 {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"p` + strconv.Itoa(i) + `":[]`)
		}
		b.WriteString("}")
		if r := audit(t, h, auditBulkPath, b.String()); r.Code != 413 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
}

func TestR_NPM_038_WhereTheAuditIsNotAvailable(t *testing.T) {
	text := "security audit is not available on repository main; it is disabled or no upstream provides advisories"
	cases := []struct {
		name   string
		class  formats.Class
		config string
		facts  formats.EndpointFacts
		view   bool
	}{
		{"a local repository", formats.Local, `{}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false},
		{"a remote in mode disabled", formats.Remote, `{"npm":{"audit":{"mode":"disabled"}}}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false},
		{"a virtual repository without a forwarding member", formats.Virtual, `{}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false},
		{"an endpoint that does not fill caches", formats.Remote, `{}`, formats.EndpointFacts{Name: "partners"}, false},
		{"an endpoint with exposure rules", formats.Remote, `{}`, formats.EndpointFacts{Name: "customers", Rules: true, CacheFill: true}, false},
		{"the cache view", formats.Remote, `{}`, formats.EndpointFacts{Name: "default", CacheFill: true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, twin, p := auditHost(t, c.class, c.config)
			fetched(t, twin, "lodash")
			for _, path := range []string{auditBulkPath, "-/npm/v1/security/audits/quick", "-/npm/v1/security/audits"} {
				rq, err := h.NewRequest(context.Background(), http.MethodPost, path, strings.NewReader(`{"lodash":["4.17.21"]}`))
				if err != nil {
					t.Fatal(err)
				}
				rq.Endpoint, rq.CacheView = c.facts, c.view
				r := h.ServeRequest(rq, false)
				var b errorBody
				_ = json.Unmarshal(r.Body.Bytes(), &b)
				if r.Code != 501 || b.Code != errs.CodeNPMAuditUnavailable || b.Error != text {
					t.Errorf("%s: %d %s", path, r.Code, r.Body)
				}
			}
			if len(p.sent) != 0 {
				t.Errorf("the provider was asked")
			}
		})
	}

	t.Run("npm 6's endpoints of a forwarding repository say where audits are answered", func(t *testing.T) {
		h, _, _ := auditHost(t, formats.Remote, `{}`)
		r := audit(t, h, "-/npm/v1/security/audits/quick", `{}`)
		if r.Code != 501 || errorCode(t, r) != errs.CodeNPMAuditUnavailable || !strings.Contains(r.Body.String(), "bulk advisory endpoint") {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})

	t.Run("an offline remote", func(t *testing.T) {
		h, twin, p := auditHost(t, formats.Remote, `{}`)
		fetched(t, twin, "lodash")
		p.answer = func(formats.Exchange) (*http.Response, error) { return nil, formats.ErrOffline }
		r := audit(t, h, auditBulkPath, `{"lodash":["4.17.21"]}`)
		if r.Code != 501 || errorCode(t, r) != errs.CodeNPMAuditUnavailable {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
}

func TestR_NPM_038_AProviderFailureIs502(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(formats.Exchange) (*http.Response, error)
		code   string
	}{
		{"no answer in time", func(formats.Exchange) (*http.Response, error) {
			return nil, errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "The upstream of main did not answer in time.")
		}, errs.CodeUpstreamTimeout},
		{"refused by the egress policy", func(formats.Exchange) (*http.Response, error) {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeEgressDenied, "The address of x is refused by the egress address policy.")
		}, errs.CodeEgressDenied},
		{"an error without a kind", func(formats.Exchange) (*http.Response, error) { return nil, errors.New("boom") }, errs.CodeUpstreamUnreachable},
		{"503", status(503), errs.CodeUpstreamError},            //nolint:bodyclose // the handler under test closes the answer
		{"401", status(401), errs.CodeUpstreamAuthFailed},       //nolint:bodyclose // the handler under test closes the answer
		{"404", status(404), errs.CodeUpstreamUnexpectedStatus}, //nolint:bodyclose // the handler under test closes the answer
	} {
		t.Run(c.name, func(t *testing.T) {
			h, twin, p := auditHost(t, formats.Remote, `{}`)
			fetched(t, twin, "lodash")
			p.answer = c.answer
			r := audit(t, h, auditBulkPath, `{"lodash":["4.17.21"]}`)
			if r.Code != 502 || errorCode(t, r) != c.code {
				t.Fatalf("%d %s, want 502 %s", r.Code, r.Body, c.code)
			}
			if r.Header().Get("npm-notice") != "" {
				t.Errorf("a failure carries the notice")
			}
		})
	}
}

func status(code int) func(formats.Exchange) (*http.Response, error) {
	return func(formats.Exchange) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"x"}`))}, nil
	}
}

func TestR_NPM_038_TheProviderAndTheDeadline(t *testing.T) {
	h, twin, p := auditHost(t, formats.Remote, `{"npm":{"audit":{"providerURL":"https://advisories.example.org/npm/","timeout":"10s"}}}`)
	fetched(t, twin, "lodash")
	cfg := &config.Config{}
	cfg.Server.ShutdownTimeout = 5 * time.Second
	f := h.Format().(*Format)
	f.Bind(h, cfg, nil)
	if r := audit(t, h, auditBulkPath, `{"lodash":["4.17.21"]}`); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if got := p.sent[0].Base; got == nil || got.String() != "https://advisories.example.org/npm/" {
		t.Errorf("base %v", got)
	}
	// min(npm.audit.timeout 10 s, server.shutdown_timeout 5 s - 1 s) = 4 s.
	if d := p.deadlines[0]; d > 4*time.Second || d < 3*time.Second {
		t.Errorf("the forward has %s left, want at most 4 s", d)
	}
	if got := (&replica{shutdown: 40 * time.Second}).auditBound(30 * time.Second); got != 30*time.Second {
		t.Errorf("bound %s, want the timeout", got)
	}
}

func TestR_NPM_038_SettingsSchemaAndDefaults(t *testing.T) {
	r := catalog.NewRegistry()
	r.Register(formats.CatalogEntry(New()))
	e, _ := r.Lookup(string(ID))
	for _, ok := range []string{
		`{"audit":{}}`,
		`{"audit":{"mode":"disabled"}}`,
		`{"audit":{"mode":"forward","providerURL":null,"timeout":"30s","maxBodySize":"10MiB"}}`,
		`{"audit":{"providerURL":"https://advisories.example.org/","timeout":"300s","maxBodySize":"64KiB"}}`,
	} {
		if vs := e.ValidateSettings(catalog.Remote, []byte(ok)); len(vs) != 0 {
			t.Errorf("%s: %+v", ok, vs)
		}
	}
	for _, bad := range []string{
		`{"audit":{"mode":"always"}}`,
		`{"audit":{"providerURL":"http://advisories.example.org/"}}`,
		`{"audit":{"providerURL":"https://user:secret@advisories.example.org/"}}`,
		`{"audit":{"providerURL":"https://token@advisories.example.org/npm/"}}`,
		`{"audit":{"providerURL":"https://advisories.example.org/?token=x"}}`,
		`{"audit":{"timeout":"0s"}}`,
		`{"audit":{"timeout":"301s"}}`,
		`{"audit":{"timeout":"1m"}}`,
		`{"audit":{"maxBodySize":"11MiB"}}`,
		`{"audit":{"maxBodySize":"1048576"}}`,
		`{"audit":{"unknown":true}}`,
	} {
		if vs := e.ValidateSettings(catalog.Remote, []byte(bad)); len(vs) == 0 {
			t.Errorf("%s is accepted", bad)
		}
	}
	if vs := e.ValidateSettings(catalog.Local, []byte(`{"audit":{}}`)); len(vs) != 1 || vs[0].Code != catalog.SettingNotApplicable {
		t.Errorf("audit on a local repository: %+v", vs)
	}

	d := settingsOf(nil).Audit
	if !d.Forward || d.Provider != nil || d.Timeout != 30*time.Second || d.MaxBodySize != 1<<20 {
		t.Errorf("defaults %+v", d)
	}
	s := settingsOf([]byte(`{"npm":{"audit":{"mode":"disabled","providerURL":"https://a.example.org/x","timeout":"5s","maxBodySize":"512KiB"}}}`)).Audit
	if s.Forward || s.Provider.String() != "https://a.example.org/x" || s.Timeout != 5*time.Second || s.MaxBodySize != 512<<10 {
		t.Errorf("read %+v", s)
	}
	// A value the schema refuses can only come from a row written around the service: the default.
	w := settingsOf([]byte(`{"npm":{"audit":{"providerURL":"http://a.example.org/","timeout":"0s","maxBodySize":"20MiB"}}}`)).Audit
	if w.Provider != nil || w.Timeout != 30*time.Second || w.MaxBodySize != 1<<20 {
		t.Errorf("out of range %+v", w)
	}
	// User info would carry a credential in a document every reader of the configuration sees
	// (H-303): the provider is the upstream then, which gets the sealed credentials only.
	for _, u := range []string{"https://user:secret@a.example.org/", "https://token@a.example.org/", "https://a.example.org/?token=x"} {
		if p := settingsOf([]byte(`{"npm":{"audit":{"providerURL":"` + u + `"}}}`)).Audit.Provider; p != nil {
			t.Errorf("%s is used as the provider", u)
		}
	}
}
