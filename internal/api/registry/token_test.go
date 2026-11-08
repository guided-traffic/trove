package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

var (
	local  = qname.Repo{Project: "web", Key: "docker-local"}
	hub    = qname.Repo{Project: "web", Key: "docker-hub"}
	t0     = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	nameRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
)

// decider allows "<principal id or anonymous> <repo> <package> <action>" and answers nothing else.
type decider struct {
	authz.Decider
	allow map[string]bool
}

func (d decider) Begin(context.Context, authz.Subject, ...qname.Repo) (*authz.Index, error) {
	return authz.BuildIndex(&authz.Snapshot{}, nil, nil), nil
}

func (d decider) PathsAllowedOn(_ context.Context, _ *authz.Index, sub authz.Subject, base authz.Coordinate, a authz.Action, paths []string, classify authz.Classifier) ([]bool, error) {
	who := sub.ID
	if sub.Anonymous {
		who = "anonymous"
	}
	out := make([]bool, len(paths))
	for i, p := range paths {
		pkg, _ := classify(p)
		out[i] = d.allow[who+" "+base.Repository.String()+" "+pkg+" "+string(a)]
	}
	return out, nil
}

type repos map[qname.Repo]repository.Repository

func (r repos) ByName(_ context.Context, n qname.Repo) (repository.Repository, error) {
	if x, ok := r[n]; ok {
		return x, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no")
}

func (r repos) ByID(context.Context, int64) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no")
}

func (r repos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

func (r repos) ListProject(context.Context, string) ([]repository.Repository, error) { return nil, nil }

// recorder keeps the events it is given.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *recorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (c *recorder) Record(_ context.Context, ev audit.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

// writeError renders the status, the challenge and the code, as the format's writer would.
func writeError(w http.ResponseWriter, _ *http.Request, err error) {
	_, e := errs.ProblemOf(err, "", "")
	if e.Challenge != "" {
		w.Header().Set("WWW-Authenticate", e.Challenge)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status())
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": e.Code}}})
}

type fixture struct {
	signer  *registrytoken.Signer
	pat     string
	expires time.Time
	rec     *recorder
	reg     *prometheus.Registry
	h       *Token
	allow   map[string]bool
	bound   qname.Repo
}

const aliceID = "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10"

func newFixture(t *testing.T, anonymous bool) *fixture {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k1.key")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{p}, ActiveKeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{rec: &recorder{}, reg: prometheus.NewRegistry(), expires: t0.Add(24 * time.Hour), allow: map[string]bool{}}
	if f.signer, err = registrytoken.NewSigner(ring, 300*time.Second); err != nil {
		t.Fatal(err)
	}
	if f.pat, err = token.Generate(token.TypePAT); err != nil {
		t.Fatal(err)
	}
	metrics, err := registrytoken.NewMetrics(f.reg)
	if err != nil {
		t.Fatal(err)
	}
	// authenticate stands for the endpoint's stage (auth.Stage.AuthenticateTokenRequest): the PAT is alice's, no credential
	// is anonymous when allowed, anything else is the stage's 401.
	authenticate := func(r *http.Request, c auth.Presented) (httpserver.Principal, error) {
		switch {
		case c.None() && anonymous:
			return httpserver.AnonymousPrincipal(), nil
		case c.None():
			return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeCredentialsRequired, "x", errs.WithChallenge(`Basic realm="trove"`))
		case c.SecretSHA256() == token.Hash(f.pat):
			exp := f.expires
			return httpserver.Principal{Kind: httpserver.KindUser, ID: aliceID, Name: "alice", Credential: httpserver.Credential{
				Method: httpserver.MethodBasic, TokenID: "t1", TokenType: httpserver.TokenPAT, ExpiresAt: &exp,
			}}, nil
		}
		return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "x", errs.WithChallenge(`Basic realm="trove"`))
	}
	f.h, err = NewToken(TokenOptions{
		Authenticate: authenticate, Signer: f.signer, Decider: decider{allow: f.allow}, Format: "oci", ValidName: nameRE.MatchString, Documents: NewDocuments(oci.New()),
		Repos: repos{
			local: {Format: "oci", Lifecycle: repository.Active}, hub: {Format: "oci", Lifecycle: repository.Active},
			{Project: "web", Key: "npm-local"}: {Format: "npm", Lifecycle: repository.Active},
		},
		Metrics: metrics, Audit: f.rec, WriteError: writeError,
		Bound:    func(*http.Request) (qname.Repo, bool, error) { return f.bound, !f.bound.IsZero(), nil },
		Endpoint: func(*http.Request) string { return "default" },
		Now:      func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) get(query string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v2/_token?"+query, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) post(form url.Values, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v2/_token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func basic(secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:"+secret))
}

// answer decodes a 200 and verifies its token.
func (f *fixture) answer(t *testing.T, w *httptest.ResponseRecorder) (tokenAnswer, registrytoken.Claims) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var a tokenAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	c, err := f.signer.Verify(a.Token, "default", t0)
	if err != nil {
		t.Fatalf("the issued token does not verify: %v", err)
	}
	return a, c
}

func (f *fixture) count(outcome string) float64 {
	fams, _ := f.reg.Gather()
	for _, fam := range fams {
		for _, m := range fam.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return -1
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTokenService(t *testing.T) {
	f := newFixture(t, false)
	f.allow[aliceID+" web/docker-local app read"] = true
	f.allow[aliceID+" web/docker-local app deploy"] = true
	scope := "service=example.com&scope=" + url.QueryEscape("repository:web/docker-local/app:pull,push")

	t.Run("GET with the trove token as Basic: 200, token and access_token equal, the lifetime, the grant of read and deploy", func(t *testing.T) {
		w := f.get(scope, "Authorization", basic(f.pat))
		a, c := f.answer(t, w)
		if a.Token != a.AccessToken || a.ExpiresIn != 300 || a.IssuedAt != "2026-11-08T10:00:00Z" {
			t.Fatalf("answer %+v", a)
		}
		if !c.Grant.Allows(local, "app", registrytoken.Pull|registrytoken.Push) || c.Binding.TokenID != "t1" ||
			c.Binding.SecretSHA256 != token.Hash(f.pat) || c.PrincipalID != aliceID || c.Endpoint != "default" {
			t.Fatalf("claims %+v", c)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get(APIVersionHeader) != APIVersion {
			t.Fatalf("headers %v", w.Header())
		}
		if strings.Contains(w.Body.String(), "refresh_token") {
			t.Fatal("a refresh token was issued")
		}
	})
	t.Run("a caller with read only is granted pull only; an empty grant is no error", func(t *testing.T) {
		delete(f.allow, aliceID+" web/docker-local app deploy")
		defer func() { f.allow[aliceID+" web/docker-local app deploy"] = true }()
		_, c := f.answer(t, f.get(scope, "Authorization", basic(f.pat)))
		if !c.Grant.Allows(local, "app", registrytoken.Pull) || c.Grant.Allows(local, "app", registrytoken.Push) {
			t.Fatalf("grant %+v", c.Grant)
		}
		_, c = f.answer(t, f.get("scope="+url.QueryEscape("repository:web/npm-local/app:pull"), "Authorization", basic(f.pat)))
		if len(c.Grant.Entries) != 0 {
			t.Fatalf("a repository of another format granted %+v", c.Grant)
		}
	})
	t.Run("the lifetime never reaches beyond the trove token's expiry", func(t *testing.T) {
		f.expires = t0.Add(90 * time.Second)
		defer func() { f.expires = t0.Add(24 * time.Hour) }()
		a, c := f.answer(t, f.get(scope, "Authorization", basic(f.pat)))
		if a.ExpiresIn != 90 || !c.ExpiresAt.Equal(t0.Add(90*time.Second)) {
			t.Fatalf("expires_in %d, %v", a.ExpiresIn, c.ExpiresAt)
		}
	})
	t.Run("a trove token that expires within this second gets no token: 401, counted and recorded", func(t *testing.T) {
		f.expires = t0.Add(500 * time.Millisecond)
		defer func() { f.expires = t0.Add(24 * time.Hour) }()
		denied, events := f.count(registrytoken.OutcomeDenied), len(f.rec.events)
		w := f.get(scope, "Authorization", basic(f.pat))
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d %v", w.Code, w.Header())
		}
		if f.count(registrytoken.OutcomeDenied) != denied+1 || len(f.rec.events) != events+1 {
			t.Fatal("the refusal was not counted or not recorded")
		}
	})
	t.Run("the catalog is granted with registry:catalog:*", func(t *testing.T) {
		_, c := f.answer(t, f.get("scope="+url.QueryEscape("registry:catalog:*"), "Authorization", basic(f.pat)))
		if !c.Grant.Catalog {
			t.Fatalf("grant %+v", c.Grant)
		}
	})
	t.Run("the cache view is granted under its base repository", func(t *testing.T) {
		f.allow[aliceID+" web/docker-hub library/alpine read"] = true
		_, c := f.answer(t, f.get("scope="+url.QueryEscape("repository:web/docker-hub~cache/library/alpine:pull"), "Authorization", basic(f.pat)))
		if !c.Grant.Allows(hub, "library/alpine", registrytoken.Pull) {
			t.Fatalf("grant %+v", c.Grant)
		}
	})
	// R-OCI-029: named in the test name by WP3 once bound hosts are mounted.
	t.Run("on a bound host the scope names the image of the bound repository", func(t *testing.T) {
		f.bound = local
		defer func() { f.bound = qname.Repo{} }()
		_, c := f.answer(t, f.get("scope="+url.QueryEscape("repository:app:pull"), "Authorization", basic(f.pat)))
		if !c.Grant.Allows(local, "app", registrytoken.Pull) {
			t.Fatalf("grant %+v", c.Grant)
		}
	})
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTokenServiceForm(t *testing.T) {
	f := newFixture(t, false)
	f.allow[aliceID+" web/docker-local app read"] = true
	t.Run("POST with the OAuth 2.0 password grant answers the same way, with containerd's content type too", func(t *testing.T) {
		form := url.Values{
			"grant_type": {"password"}, "username": {"x"}, "password": {f.pat}, "service": {"example.com"},
			"scope": {"repository:web/docker-local/app:pull"}, "client_id": {"containerd-client"},
		}
		for _, ct := range []string{"application/x-www-form-urlencoded", "application/x-www-form-urlencoded; charset=utf-8"} {
			a, c := f.answer(t, f.post(form, "Content-Type", ct))
			if a.AccessToken == "" || !c.Grant.Allows(local, "app", registrytoken.Pull) || c.Grant.Allows(local, "app", registrytoken.Push) {
				t.Fatalf("%s: answer %+v claims %+v", ct, a, c)
			}
		}
	})
	t.Run("the refresh grant is 400 unsupported_grant_type in both error shapes", func(t *testing.T) {
		w := f.post(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "service": {"example.com"}})
		var body struct {
			Errors []struct{ Code string } `json:"errors"`
			Error  string                  `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 400 || body.Error != "unsupported_grant_type" || len(body.Errors) != 1 || body.Errors[0].Code != "UNSUPPORTED" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("a malformed request is 400 invalid_request", func(t *testing.T) {
		var many []string
		for i := 0; i < 17; i++ {
			many = append(many, "scope="+url.QueryEscape("repository:web/docker-local/app:pull"))
		}
		for name, w := range map[string]*httptest.ResponseRecorder{
			"17 scopes":           f.get(strings.Join(many, "&"), "Authorization", basic(f.pat)),
			"no grant type":       f.post(url.Values{"password": {f.pat}}),
			"no password":         f.post(url.Values{"grant_type": {"password"}, "username": {"x"}}),
			"header and form":     f.post(url.Values{"grant_type": {"password"}, "password": {f.pat}}, "Authorization", basic(f.pat)),
			"not a form":          f.post(url.Values{}, "Content-Type", "application/json"),
			"a form beyond 16KiB": f.post(url.Values{"grant_type": {"password"}, "password": {strings.Repeat("a", maxFormBytes)}}),
		} {
			if w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
				t.Errorf("%s: %d %s", name, w.Code, w.Body)
			}
		}
	})
	t.Run("a grant that does not fit in 4 KiB is 400 invalid_request, never cut", func(t *testing.T) {
		var q []string
		long := strings.Repeat("a", 230)
		for i := 0; i < registrytoken.MaxEntries; i++ {
			name := long + string(rune('a'+i))
			f.allow[aliceID+" web/docker-local "+name+" read"] = true
			q = append(q, "scope="+url.QueryEscape("repository:web/docker-local/"+name+":pull"))
		}
		w := f.get(strings.Join(q, "&"), "Authorization", basic(f.pat))
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("another method is 405 with Allow", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v2/_token", nil))
		if w.Code != 405 {
			t.Fatalf("status %d", w.Code)
		}
	})
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTokenServiceChecksTheService(t *testing.T) {
	f := newFixture(t, false)
	r := httptest.NewRequest(http.MethodGet, "/v2/_token?service=other.example.com", nil)
	r.Header.Set("Authorization", basic(f.pat))
	r = r.WithContext(httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.example.com"}))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "trove.example.com") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodGet, "/v2/_token?service=trove.example.com", nil)
	r.Header.Set("Authorization", basic(f.pat))
	r = r.WithContext(httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.example.com"}))
	w = httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("the endpoint's own service: %d %s", w.Code, w.Body)
	}
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestAnonymousTokenRequests(t *testing.T) {
	t.Run("R-AUTH-037 with anonymous requests allowed: an anonymous token with the anonymous principal's grant", func(t *testing.T) {
		f := newFixture(t, true)
		f.allow["anonymous web/docker-hub library/alpine read"] = true
		w := f.get("scope=" + url.QueryEscape("repository:web/docker-hub/library/alpine:pull") + "&scope=" + url.QueryEscape("repository:web/docker-local/app:push,pull"))
		_, c := f.answer(t, w)
		if !c.Anonymous || c.PrincipalID != "" || !c.Grant.Allows(hub, "library/alpine", registrytoken.Pull) || len(c.Grant.Entries) != 1 {
			t.Fatalf("claims %+v", c)
		}
		if f.count(registrytoken.OutcomeAnonymous) != 1 || f.count(registrytoken.OutcomeIssued) != 0 {
			t.Fatalf("metrics anonymous %v issued %v", f.count(registrytoken.OutcomeAnonymous), f.count(registrytoken.OutcomeIssued))
		}
	})
	t.Run("R-AUTH-037 without: 401 with Basic realm=\"trove\", counted as denied and recorded", func(t *testing.T) {
		f := newFixture(t, false)
		w := f.get("scope=" + url.QueryEscape("repository:web/docker-hub/library/alpine:pull"))
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d %v", w.Code, w.Header())
		}
		if f.count(registrytoken.OutcomeDenied) != 1 || len(f.rec.events) != 1 {
			t.Fatalf("denied %v events %d", f.count(registrytoken.OutcomeDenied), len(f.rec.events))
		}
	})
}

// R-OCI-034: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTokenServiceEventAndMetrics(t *testing.T) {
	f := newFixture(t, false)
	bogus, _ := token.Generate(token.TypePAT)
	w := f.get("service=example.com", "Authorization", basic(bogus))
	if w.Code != 401 {
		t.Fatalf("status %d", w.Code)
	}
	w = f.post(url.Values{"grant_type": {"password"}, "password": {bogus}})
	if w.Code != 401 {
		t.Fatalf("status %d", w.Code)
	}
	f.answer(t, f.get("", "Authorization", basic(f.pat)))
	if len(f.rec.events) != 2 {
		t.Fatalf("%d events: none per issued token, one per refused request", len(f.rec.events))
	}
	for i, want := range []string{"basic", "form"} {
		ev := f.rec.events[i]
		var d map[string]string
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != audit.EventRegistryTokenDenied || ev.Outcome != audit.Failure || d["reason"] != "invalid_token" ||
			d["credential"] != want || ev.Endpoint != "default" || ev.Reason != "invalid_token" {
			t.Fatalf("event %d: %+v %s", i, ev, ev.Details)
		}
		if strings.Contains(string(ev.Details), bogus) || strings.Contains(string(ev.Details), "trv_") {
			t.Fatal("the event carries the credential")
		}
	}
	if f.count(registrytoken.OutcomeDenied) != 2 || f.count(registrytoken.OutcomeIssued) != 1 {
		t.Fatalf("denied %v issued %v", f.count(registrytoken.OutcomeDenied), f.count(registrytoken.OutcomeIssued))
	}
}

func TestTheTokenRequestNeverPrintsItsCredential(t *testing.T) {
	pat, _ := token.Generate(token.TypePAT)
	r := httptest.NewRequest(http.MethodGet, "/v2/_token", nil)
	r.SetBasicAuth("ci", pat)
	for _, tr := range []tokenRequest{
		{credential: auth.PresentedFrom(r), presented: "basic"},
		{credential: auth.PresentedForm("ci", pat), presented: "form"},
	} {
		for _, out := range []string{fmt.Sprintf("%v", tr), fmt.Sprintf("%+v", tr), fmt.Sprintf("%#v", tr), fmt.Sprint(tr.credential)} {
			if strings.Contains(out, "trv_") || strings.Contains(out, pat[8:20]) {
				t.Fatalf("printed: %s", out)
			}
		}
	}
}

func TestActionOf(t *testing.T) {
	for a, want := range map[authz.Action]registrytoken.Actions{
		authz.Read: registrytoken.Pull, authz.Deploy: registrytoken.Push, authz.Overwrite: registrytoken.Push, authz.Delete: registrytoken.Delete,
	} {
		if got, ok := ActionOf(a); !ok || got != want {
			t.Errorf("ActionOf(%s) = %v", a, got)
		}
	}
	for _, a := range []authz.Action{authz.Manage, authz.Annotate, ""} {
		if _, ok := ActionOf(a); ok {
			t.Errorf("%q maps to a token action", a)
		}
	}
}
