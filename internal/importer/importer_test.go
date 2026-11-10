package importer

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// factory is an outbound factory that admits the loopback addresses of the test servers.
func factory() *outbound.Factory {
	return outbound.New(config.Outbound{
		TLS:           config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"},
		AddressPolicy: config.OutboundAddressPolicy{AllowCIDRs: []string{"127.0.0.0/8"}},
	})
}

func noSleep(rec *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		if rec != nil {
			*rec = append(*rec, d)
		}
		return nil
	}
}

func TestR_IMP_008_TheLimiterSpacesRequestsAndHalvesTheRateWhenTheSourceSaysSo(t *testing.T) {
	l := NewLimiter(10)
	if got := l.Interval(); got != 100*time.Millisecond {
		t.Fatalf("interval %v, want 100ms", got)
	}
	l.Slow()
	if got := l.Interval(); got != 200*time.Millisecond {
		t.Fatalf("after Slow: %v, want 200ms", got)
	}
	for range 20 {
		l.Slow()
	}
	if got := l.Interval(); got != 10*time.Second {
		t.Fatalf("the slowest rate is one request in ten seconds, got %v", got)
	}
	var none *Limiter
	none.Slow()
	if err := none.Wait(context.Background()); err != nil {
		t.Fatalf("a nil limiter limits nothing: %v", err)
	}
	if got := NewLimiter(0).Interval(); got != 0 {
		t.Fatalf("rate 0 means no limit, got %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := NewLimiter(0.001)
	_ = slow.Wait(context.Background()) // the first request goes at once
	if err := slow.Wait(ctx); err == nil {
		t.Fatal("a cancelled wait returned no error")
	}
}

func TestR_IMP_008_ARetryWaitsAsLongAsTheSourceAsksAndNoLongerThanTheCap(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	var waits []time.Duration
	lim := NewLimiter(5)
	c := &SourceClient{
		HTTP: factory().Client(outbound.PurposeRemote), Origin: mustURL(t, srv.URL+"/"), Limiter: lim,
		Retry: Retry{Attempts: 5, MaxWait: 10 * time.Second, Sleep: noSleep(&waits)},
	}
	status, _, err := c.GetStatus(context.Background(), srv.URL+"/x", "", 1024)
	if err != nil || status != 200 {
		t.Fatalf("%d %v", status, err)
	}
	if len(waits) != 2 || waits[0] != 10*time.Second || waits[1] != 10*time.Second {
		t.Fatalf("waits %v, want the cap twice (Retry-After was an hour)", waits)
	}
	if got := lim.Interval(); got != 800*time.Millisecond {
		t.Fatalf("two rate-limit answers halve the rate twice: interval %v, want 800ms", got)
	}
	// An answer that is no rate limit and no server failure is not retried.
	calls.Store(-100)
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	c.Origin = mustURL(t, notFound.URL+"/")
	waits = nil
	if status, _, err := c.GetStatus(context.Background(), notFound.URL+"/x", "", 1024); err != nil || status != 404 || len(waits) != 0 {
		t.Fatalf("404: %d %v waits %v", status, err, waits)
	}
}

func TestR_IMP_008_TheAttemptsAreBounded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := &SourceClient{HTTP: factory().Client(outbound.PurposeRemote), Origin: mustURL(t, srv.URL+"/"), Retry: Retry{Attempts: 3, Sleep: noSleep(nil)}}
	if _, _, err := c.GetStatus(context.Background(), srv.URL+"/x", "", 1024); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err %v, want the last status", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("%d requests, want 3", calls.Load())
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestR_IMP_002_AnAddressIsCheckedWithTheRuleOfAnUpstreamURL(t *testing.T) {
	for _, c := range []struct{ name, from, to, want string }{
		{"scheme", "ftp://x.example/", "https://t.example/", "source address"},
		{"user information", "https://u:p@x.example/", "https://t.example/", "user information"},
		{"fragment", "https://x.example/#f", "https://t.example/", "fragment"},
		{"relative", "x.example", "https://t.example/", "source address"},
		{"target scheme", "https://x.example/", "file:///tmp", "target address"},
		{"query", "https://x.example/?a=b", "https://t.example/", "query"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := Build(factory(), Params{Format: "npm", From: c.from, To: c.to, Token: "t"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to mention %q", err, c.want)
			}
		})
	}
	if _, _, _, err := Build(factory(), Params{Format: "npm", From: "https://x.example/", To: "https://t.example/"}); err == nil {
		t.Error("an import without a token for the target was built")
	}
	if _, _, _, err := Build(factory(), Params{Format: "npm", From: "https://x.example/", To: "https://t.example/", Token: "t", SourceAuth: AuthBasic}); err == nil {
		t.Error("basic authentication without a password was built")
	}
	if _, _, _, err := Build(factory(), Params{Format: "galaxy", From: "https://x.example/", To: "https://t.example/", Token: "t"}); err == nil || !strings.Contains(err.Error(), "npm and nuget") {
		t.Errorf("an unknown format: %v", err)
	}
}

func TestR_IMP_002_TheSourceIsReachedOnlyWhereTheEgressPolicyAdmits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Error("the policy let a request through") }))
	defer srv.Close()
	denied := outbound.New(config.Outbound{
		TLS:           config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"},
		AddressPolicy: config.OutboundAddressPolicy{DenyCIDRs: []string{"127.0.0.0/8"}},
	})
	_, src, _, err := Build(denied, Params{Format: "npm", From: srv.URL + "/", To: "https://t.example/", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	src.Retry = Retry{Attempts: 4, Sleep: noSleep(nil)}
	_, _, err = src.GetStatus(context.Background(), srv.URL+"/x", "", 1024)
	if !errors.Is(err, outbound.ErrEgressDenied) {
		t.Fatalf("err %v, want an egress denial", err)
	}
}

func TestR_IMP_002_TheCredentialGoesToTheSourcesOriginAndToNoOtherHost(t *testing.T) {
	var originAuth, otherAuth atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("{}"))
	}))
	defer origin.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("file"))
	}))
	defer other.Close()
	_, src, _, err := Build(factory(), Params{
		Format: "npm", From: origin.URL + "/", To: "https://t.example/", Token: "tok",
		SourceAuth: AuthBearer, SourcePassword: "s3cret", ExtraHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.GetStatus(context.Background(), origin.URL+"/doc", "", 1024); err != nil {
		t.Fatal(err)
	}
	if got := originAuth.Load(); got != "Bearer s3cret" {
		t.Errorf("the origin got Authorization %q", got)
	}
	f, err := src.Download(context.Background(), other.URL+"/file", t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	f.Remove()
	if got := otherAuth.Load(); got != "" {
		t.Errorf("an extra host got Authorization %q; it must be asked without the credential", got)
	}
	// An address outside the origin and the extra hosts is not asked at all.
	if _, err := src.Download(context.Background(), "http://elsewhere.example/file", t.TempDir(), 1024); !errors.Is(err, ErrForeignHost) {
		t.Errorf("err %v, want ErrForeignHost", err)
	}
	if src.Allowed("http://user:pw@127.0.0.1/x") {
		t.Error("an address with user information was admitted")
	}
}

func TestR_IMP_004_TheTargetGetsTheTokenAndNoOtherHostDoes(t *testing.T) {
	var got atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-NuGet-ApiKey"))
		w.WriteHeader(200)
	}))
	defer target.Close()
	_, _, dst, err := Build(factory(), Params{Format: "nuget", From: "https://x.example/v3/index.json", To: target.URL + "/api/nuget/p/r/v3/index.json", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Do(context.Background(), Request{Method: http.MethodGet, URL: target.URL + "/x"}); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "Bearer tok|tok" {
		t.Errorf("the target saw %q", got.Load())
	}
	if _, err := dst.Do(context.Background(), Request{Method: http.MethodGet, URL: "http://elsewhere.example/x"}); err == nil {
		t.Error("a request to another origin than the target was sent")
	}
}

func TestR_IMP_005_VersionsAreImportedInTheOrderOfTheirPublication(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2024, 1, 1, h, 0, 0, 0, time.UTC) }
	vs := []Version{
		{Version: "2.0.0", Published: at(5)},
		{Version: "1.0.0", Published: at(1)},
		{Version: "1.1.0", Published: at(3)},
		{Version: "1.10.0"},
		{Version: "1.2.0"},
		{Version: "1.0.0-rc.1"},
	}
	sortVersions(vs)
	var got []string
	for _, v := range vs {
		got = append(got, v.Version)
	}
	// Versions without a time sort by number among themselves and keep their place against the dated ones by number.
	if idx(got, "1.0.0") > idx(got, "1.1.0") || idx(got, "1.1.0") > idx(got, "2.0.0") {
		t.Errorf("order %v: dated versions are not in the order of their times", got)
	}
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.2.0", "1.10.0", -1},
		{"1.0", "1.0.0", 0},
		{"1.0.0+b", "1.0.0", 0},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-1", "1.0.0-a", -1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func idx(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestR_IMP_007_TheStateFileKeepsWhatIsDoneSurvivesACutLineAndBelongsToOneImport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	j, err := OpenJournal(path, "npm", "https://src.example/", "https://trove.example/api/npm/p/r/")
	if err != nil {
		t.Fatal(err)
	}
	done := PackageResult{Name: "a", Versions: []VersionResult{{Version: "1.0.0", Outcome: OutcomeImported}}}
	if err := j.Record(done); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	// A killed process leaves a cut line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"kind":"package","package":{"name":"b","vers`)
	_ = f.Close()

	j, err = OpenJournal(path, "npm", "https://src.example/", "https://trove.example/api/npm/p/r/")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := j.Done("a"); !ok {
		t.Error("a complete package was lost")
	}
	if _, ok := j.Done("b"); ok {
		t.Error("a cut line counted as a complete package")
	}
	if err := j.Record(PackageResult{Name: "c"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j, err = OpenJournal(path, "npm", "https://src.example/", "https://trove.example/api/npm/p/r/")
	if err != nil {
		t.Fatalf("a record after a cut line made the file unreadable: %v", err)
	}
	if _, ok := j.Done("c"); !ok {
		t.Error("the record after the cut line was lost")
	}
	_ = j.Close()

	for _, other := range [][3]string{{"nuget", "https://src.example/", "https://trove.example/api/npm/p/r/"}, {"npm", "https://other.example/", "https://trove.example/api/npm/p/r/"}, {"npm", "https://src.example/", "https://trove.example/api/npm/p/other/"}} {
		if _, err := OpenJournal(path, other[0], other[1], other[2]); err == nil || !strings.Contains(err.Error(), "another import") {
			t.Errorf("a state file of another import was opened: %v", err)
		}
	}
	junk := filepath.Join(t.TempDir(), "junk")
	_ = os.WriteFile(junk, []byte("not json\n"), 0o600)
	if _, err := OpenJournal(junk, "npm", "a", "b"); err == nil {
		t.Error("a file that is no state file was opened")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v, want 0600", st.Mode().Perm())
	}
	var nilJ *Journal
	if _, ok := nilJ.Done("a"); ok || nilJ.Record(done) != nil || nilJ.Close() != nil {
		t.Error("a nil journal must hold nothing and accept everything")
	}
}

// fake is a Format over canned plans, to test the runner.
type fake struct {
	mu        sync.Mutex
	plans     map[string]*Plan
	held      map[string]map[string]Held
	refuse    map[string]*Refusal // by name
	published []string
	finished  []string
}

func (*fake) ID() string                                     { return "fake" }
func (*fake) ValidName(n string) bool                        { return !strings.Contains(n, "!") }
func (*fake) Preflight(context.Context, *TargetClient) error { return nil }
func (*fake) Search(context.Context, *SourceClient, string) ([]string, error) {
	return []string{"found"}, nil
}

func (f *fake) Plan(_ context.Context, _ *SourceClient, n string) (*Plan, error) {
	if p, ok := f.plans[n]; ok {
		return p, nil
	}
	return &Plan{Name: n}, nil
}

func (f *fake) Held(_ context.Context, _ *TargetClient, n string) (map[string]Held, map[string]string, error) {
	return f.held[n], nil, nil
}

func (f *fake) Publish(_ context.Context, _ *TargetClient, p *Plan, v Version, _ *File, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.refuse[p.Name]; r != nil {
		return r
	}
	f.published = append(f.published, p.Name+"@"+v.Version)
	return nil
}

func (f *fake) Finish(_ context.Context, _ *TargetClient, p *Plan, _ map[string]Held, _ map[string]string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, p.Name)
	return nil, nil
}

// content serves the files of the runner tests: /ok, /big (2 KiB).
func content(t *testing.T) (*httptest.Server, map[string]Digest) {
	t.Helper()
	files := map[string][]byte{"/ok": []byte("the file"), "/other": []byte("another file"), "/big": make([]byte, 2048)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	d := map[string]Digest{}
	for p, b := range files {
		s := sha256.Sum256(b)
		d[p] = Digest{Alg: "sha256", Sum: s[:]}
	}
	return srv, d
}

func runner(t *testing.T, f Format, names ...string) (Options, *httptest.Server, map[string]Digest) {
	t.Helper()
	srv, digests := content(t)
	o := Options{
		Format: f, Names: names, Dir: t.TempDir(), Concurrency: 1,
		Source: &SourceClient{HTTP: factory().Client(outbound.PurposeRemote), Origin: mustURL(t, srv.URL+"/"), Retry: Retry{Attempts: 1}},
		Target: &TargetClient{HTTP: factory().Client(outbound.PurposeRemote), Base: mustURL(t, srv.URL+"/"), Retry: Retry{Attempts: 1}},
	}
	return o, srv, digests
}

func plan(name string, srv *httptest.Server, d map[string]Digest, files ...string) *Plan {
	p := &Plan{Name: name}
	for i, f := range files {
		p.Versions = append(p.Versions, Version{Version: fmt.Sprintf("1.%d.0", i), URL: srv.URL + f, Want: d[f], Listed: true})
	}
	return p
}

func TestR_IMP_006_EveryPackageIsReportedAndAFailureDoesNotStopTheRest(t *testing.T) {
	f := &fake{plans: map[string]*Plan{}, held: map[string]map[string]Held{}, refuse: map[string]*Refusal{"denied": {Status: 403, Code: "forbidden"}}}
	o, srv, d := runner(t, f, "bad!name", "denied", "empty", "good", "mismatch", "nodigest", "foreign", "big", "taken", "same")
	f.plans["denied"] = plan("denied", srv, d, "/ok")
	f.plans["good"] = plan("good", srv, d, "/ok", "/other")
	f.plans["mismatch"] = plan("mismatch", srv, d, "/ok")
	f.plans["mismatch"].Versions[0].Want = d["/other"] // the source announced the digest of another file
	f.plans["nodigest"] = plan("nodigest", srv, d, "/ok")
	f.plans["nodigest"].Versions[0].Want = Digest{}
	f.plans["foreign"] = plan("foreign", srv, d, "/ok")
	f.plans["foreign"].Versions[0].URL = "http://elsewhere.example/ok"
	f.plans["big"] = plan("big", srv, d, "/big")
	f.plans["taken"] = plan("taken", srv, d, "/ok")
	f.plans["same"] = plan("same", srv, d, "/ok")
	o.MaxBytes = 1024
	sum256 := d["/ok"].Sum
	f.held["taken"] = map[string]Held{"1.0.0": {Digests: map[string][]byte{"sha256": d["/other"].Sum}, Listed: true}}
	f.held["same"] = map[string]Held{"1.0.0": {Digests: map[string][]byte{"sha256": sum256}, Listed: true}}

	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ outcome, code string }{
		"denied": {OutcomeFailed, CodeForbidden}, "mismatch": {OutcomeFailed, CodeDigestMismatch}, "nodigest": {OutcomeFailed, CodeNoDigest},
		"foreign": {OutcomeFailed, CodeForeignHost}, "big": {OutcomeFailed, CodeTooLarge}, "taken": {OutcomeConflict, "version_exists"},
		"same": {OutcomeSkipped, ""}, "good": {OutcomeImported, ""},
	}
	for name, w := range want {
		p := pkgOf(t, rep, name)
		if p.Versions[0].Outcome != w.outcome || p.Versions[0].Code != w.code {
			t.Errorf("%s: %+v, want %s %s", name, p.Versions[0], w.outcome, w.code)
		}
	}
	if p := pkgOf(t, rep, "bad!name"); p.Code != CodeInvalidName {
		t.Errorf("bad name: %+v", p)
	}
	if p := pkgOf(t, rep, "empty"); p.Code != CodeSourceError {
		t.Errorf("a package the source does not hold: %+v", p)
	}
	if got := strings.Join(f.published, ","); got != "good@1.0.0,good@1.1.0" {
		t.Errorf("published %q: only the good package may reach the target", got)
	}
	if rep.OK() {
		t.Error("a report with failures is not OK")
	}
	totals, problems := rep.Totals()
	if totals[OutcomeImported] != 2 || totals[OutcomeConflict] != 1 || problems != 8 {
		t.Errorf("totals %v, problems %d", totals, problems)
	}
	// Finish ran for the packages that have a version in the target, not for those whose versions all failed.
	if got := strings.Join(f.finished, ","); got != "good,same" && got != "same,good" {
		t.Errorf("finished %q", got)
	}
}

func TestR_IMP_006_ATokenThatIsNotAcceptedAndAFullStorageStopTheRun(t *testing.T) {
	for _, c := range []struct {
		status int
		want   string
	}{{401, "401"}, {507, "507"}} {
		f := &fake{plans: map[string]*Plan{}, held: map[string]map[string]Held{}, refuse: map[string]*Refusal{"a": {Status: c.status, Code: "x"}}}
		o, srv, d := runner(t, f, "a", "b", "c")
		for _, n := range []string{"a", "b", "c"} {
			f.plans[n] = plan(n, srv, d, "/ok")
		}
		rep, err := Run(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%d: err %v, want the run stopped", c.status, err)
		}
		if rep.Aborted == "" {
			t.Errorf("%d: the report does not say why it stopped", c.status)
		}
		if len(f.published) != 0 {
			t.Errorf("%d: published %v after the stop", c.status, f.published)
		}
	}
}

func TestR_IMP_001_ADryRunReadsBothSidesAndWritesNothing(t *testing.T) {
	var gets atomic.Int32
	f := &fake{plans: map[string]*Plan{}, held: map[string]map[string]Held{}}
	o, srv, d := runner(t, f, "a")
	f.plans["a"] = plan("a", srv, d, "/ok", "/other")
	f.held["a"] = map[string]Held{"1.0.0": {Digests: map[string][]byte{"sha256": d["/ok"].Sum}}}
	o.DryRun = true
	o.Source.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		gets.Add(1)
		return nil, errors.New("a dry run downloaded")
	})
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	p := pkgOf(t, rep, "a")
	if p.Versions[0].Outcome != OutcomeSkipped || p.Versions[1].Outcome != OutcomeWouldAdd {
		t.Errorf("outcomes %+v", p.Versions)
	}
	if gets.Load() != 0 || len(f.published) != 0 || len(f.finished) != 0 {
		t.Errorf("a dry run touched something: %d downloads, published %v, finished %v", gets.Load(), f.published, f.finished)
	}
	if !rep.OK() {
		t.Error("a dry run that found only importable versions is OK")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestR_IMP_003_TheSearchAddsToTheGivenNamesWithoutRepeats(t *testing.T) {
	f := &fake{plans: map[string]*Plan{}, held: map[string]map[string]Held{}}
	o, _, _ := runner(t, f, "found", "zed")
	q := ""
	o.Search = &q
	names, err := discover(context.Background(), o)
	if err != nil || strings.Join(names, ",") != "found,zed" {
		t.Fatalf("%v %v", names, err)
	}
	o.Names, o.Search = nil, nil
	if _, err := discover(context.Background(), o); err == nil {
		t.Error("an import of nothing was started")
	}
}

func TestR_IMP_009_AFormatIsFoundByItsNameAndTheDigestPreferenceIsTheStrongestAnnounced(t *testing.T) {
	for _, id := range []string{"npm", "nuget"} {
		if f, err := FormatByID(id); err != nil || f.ID() != id {
			t.Errorf("%s: %v", id, err)
		}
	}
	sum512 := sha512.Sum512([]byte("x"))
	sum256 := sha256.Sum256([]byte("x"))
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum256[:]) + " sha512-" + base64.StdEncoding.EncodeToString(sum512[:])
	if d := strongest(integrity, hex.EncodeToString(make([]byte, 20))); d.Alg != "sha512" || hex.EncodeToString(d.Sum) != hex.EncodeToString(sum512[:]) {
		t.Errorf("strongest of sha256 and sha512: %+v", d)
	}
	if d := strongest("", hex.EncodeToString(make([]byte, 20))); d.Alg != "sha1" {
		t.Errorf("only a shasum: %+v", d)
	}
	if d := strongest("sha512-notbase64!!", "zz"); d.Alg != "" {
		t.Errorf("an unreadable digest was taken: %+v", d)
	}
	if d := strongest("sha512-"+base64.StdEncoding.EncodeToString([]byte("short")), ""); d.Alg != "" {
		t.Errorf("a digest of the wrong length was taken: %+v", d)
	}
}

func TestR_IMP_005_TheNPMPlanReadsOrderTagsDigestsAndWhatIsNotKept(t *testing.T) {
	sum := sha512.Sum512([]byte("x"))
	integ := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	doc := map[string]any{
		"name": "pkg", "dist-tags": map[string]string{"latest": "1.1.0", "next": "2.0.0-rc.1"},
		"time": map[string]string{"created": "2024-01-01T00:00:00Z", "1.0.0": "2024-01-02T00:00:00Z", "1.1.0": "2024-01-04T00:00:00Z", "2.0.0-rc.1": "2024-01-03T00:00:00Z"},
		"versions": map[string]any{
			"1.0.0":      map[string]any{"name": "pkg", "version": "1.0.0", "deprecated": "use 1.1", "dist": map[string]any{"integrity": integ, "tarball": "https://r.example/pkg-1.0.0.tgz"}},
			"1.1.0":      map[string]any{"name": "pkg", "version": "1.1.0", "dist": map[string]any{"shasum": hex.EncodeToString(make([]byte, 20)), "tarball": "https://r.example/pkg-1.1.0.tgz"}},
			"2.0.0-rc.1": map[string]any{"name": "pkg", "version": "2.0.0-rc.1", "dist": map[string]any{"integrity": integ, "tarball": "x", "attestations": map[string]any{"url": "u"}}},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pkg":
			_ = json.NewEncoder(w).Encode(doc)
		case "/liar":
			d := map[string]any{"name": "another", "versions": map[string]any{}}
			_ = json.NewEncoder(w).Encode(d)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := &SourceClient{HTTP: factory().Client(outbound.PurposeRemote), Origin: mustURL(t, srv.URL+"/"), Retry: Retry{Attempts: 1}}
	p, err := NPM{}.Plan(context.Background(), src, "pkg")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, v := range p.Versions {
		order = append(order, v.Version)
	}
	if strings.Join(order, ",") != "1.0.0,2.0.0-rc.1,1.1.0" {
		t.Errorf("order %v, want the order of the source's publication times", order)
	}
	if p.Tags["latest"] != "1.1.0" || p.Tags["next"] != "2.0.0-rc.1" {
		t.Errorf("tags %v", p.Tags)
	}
	if p.Versions[0].Want.Alg != "sha512" || p.Versions[2].Want.Alg != "sha1" {
		t.Errorf("digests %+v %+v", p.Versions[0].Want, p.Versions[2].Want)
	}
	notKept := strings.Join(p.NotKept, "|")
	if !strings.Contains(notKept, "deprecation of 1.0.0") || !strings.Contains(notKept, "attestations of 2.0.0-rc.1") {
		t.Errorf("not kept: %q", notKept)
	}
	if tagFor(p, "2.0.0-rc.1") != "next" || tagFor(p, "1.0.0") != "latest" {
		t.Errorf("tagFor: %s %s", tagFor(p, "2.0.0-rc.1"), tagFor(p, "1.0.0"))
	}
	if _, err := (NPM{}).Plan(context.Background(), src, "liar"); err == nil {
		t.Error("a document that names another package was accepted")
	}
	if p, err := (NPM{}).Plan(context.Background(), src, "absent"); err != nil || len(p.Versions) != 0 {
		t.Errorf("an unknown package: %v %+v", err, p)
	}
	for name, ok := range map[string]bool{"a": true, "@s/n": true, "a/b": false, "../x": false, "@s": false, "a b": false, "": false, strings.Repeat("a", 215): false} {
		if (NPM{}).ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
}

func TestR_IMP_004_ThePublishBodyStreamsTheTarballBase64AndCarriesTheVerifiedDigests(t *testing.T) {
	tgz := tarball(t, "pkg", "1.0.0")
	var body []byte
	var length int64
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAll(r)
		length, path = r.ContentLength, r.URL.EscapedPath()
		w.WriteHeader(201)
	}))
	defer srv.Close()
	dst := &TargetClient{HTTP: factory().Client(outbound.PurposeRemote), Base: mustURL(t, srv.URL+"/api/npm/p/r/"), Retry: Retry{Attempts: 1}}
	file := spool(t, tgz)
	defer file.Remove()
	p := &Plan{Name: "@s/pkg"}
	// The tarball says pkg, the plan says @s/pkg: the importer refuses before sending anything.
	err := NPM{}.Publish(context.Background(), dst, p, Version{Version: "1.0.0"}, file, "latest")
	var ie *ItemError
	if !errors.As(err, &ie) || ie.Code != CodeIdentityMismatch || body != nil {
		t.Fatalf("a tarball of another name: %v (sent %d bytes)", err, len(body))
	}
	p.Name = "pkg"
	if err := (NPM{}).Publish(context.Background(), dst, p, Version{Version: "1.0.0", Meta: []byte(`{"readme":"hello"}`)}, file, "beta"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/npm/p/r/pkg" || length != int64(len(body)) {
		t.Errorf("path %q, Content-Length %d for a body of %d", path, length, len(body))
	}
	var doc struct {
		Tags     map[string]string         `json:"dist-tags"`
		Versions map[string]map[string]any `json:"versions"`
		Att      map[string]map[string]any `json:"_attachments"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the body is no JSON: %v", err)
	}
	if doc.Tags["beta"] != "1.0.0" || len(doc.Tags) != 1 {
		t.Errorf("tags %v: exactly one tag, as npm publish sends", doc.Tags)
	}
	dist, _ := doc.Versions["1.0.0"]["dist"].(map[string]any)
	s512 := sha512.Sum512(tgz)
	if dist["integrity"] != "sha512-"+base64.StdEncoding.EncodeToString(s512[:]) {
		t.Errorf("dist.integrity %v is not the digest of the verified file", dist["integrity"])
	}
	if doc.Versions["1.0.0"]["readme"] != "hello" {
		t.Errorf("the source's readme was not carried: %v", doc.Versions["1.0.0"]["readme"])
	}
	att := doc.Att["pkg-1.0.0.tgz"]
	got, _ := base64.StdEncoding.DecodeString(att["data"].(string))
	if string(got) != string(tgz) || int(att["length"].(float64)) != len(tgz) {
		t.Errorf("the attachment is not the file: %d bytes of %d", len(got), len(tgz))
	}
}

func TestR_IMP_004_AnArchiveWithoutAPackageJSONIsRefusedBeforeItIsSent(t *testing.T) {
	file := spool(t, []byte("not an archive"))
	defer file.Remove()
	_, err := tarballManifest(file)
	var ie *ItemError
	if !errors.As(err, &ie) {
		t.Fatalf("err %v", err)
	}
}

func TestR_IMP_004_TheStoredDigestsOfANuGetPackageAreReadFromTheHeadersOfAHead(t *testing.T) {
	sum := sha512.Sum512([]byte("x"))
	h := "sha-512=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":, sha-256=:AAAA:, junk"
	got := parseReprDigest(h)
	if hex.EncodeToString(got["sha512"]) != hex.EncodeToString(sum[:]) {
		t.Errorf("sha512 %x", got["sha512"])
	}
	if _, ok := got["sha256"]; ok {
		t.Error("a digest of the wrong length was taken")
	}
}
