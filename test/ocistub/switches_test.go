// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A client with a short timeout, for the switches that make the stub not answer.
func (f *fixture) impatient(d time.Duration) *http.Client {
	c := *f.client
	c.Timeout = d
	return &c
}

func TestDownUnavailable(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Down: DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/v2/", "/v2/library/alpine/manifests/3"} {
		if r := f.get(f.reg+p, nil); r.status != 503 || r.errorCode(t) != "UNAVAILABLE" {
			t.Errorf("%s while down = %d", p, r.status)
		}
	}
	if r := f.get(f.realm+"/token?service="+DefaultService, nil); r.status != 503 {
		t.Errorf("realm while down = %d", r.status)
	}
	if err := f.stub.SetMode(Mode{}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.reg+"/v2/", nil); r.status != 401 {
		t.Errorf("after clearing the mode = %d", r.status)
	}
}

func TestDownReset(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Down: DownReset}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.reg+"/v2/", nil)
	if resp, err := f.client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("a cut connection answered %d", resp.StatusCode)
	}
	// The cut request is counted with status 0.
	if n := f.stub.Counts().WithStatus(0); n < 1 {
		t.Errorf("requests cut = %d, want at least 1", n)
	}
}

func TestDownHang(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Down: DownHang}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.reg+"/v2/", nil)
	start := time.Now()
	resp, err := f.impatient(300 * time.Millisecond).Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("a held request answered %d", resp.StatusCode)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("the client gave up after %v, before its timeout", time.Since(start))
	}
}

func TestFailStatusPerClass(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, BlobsDirect: true, FailStatus: map[string]int{ClassBlob: 502, ClassTags: 500}}); err != nil {
		t.Fatal(err)
	}
	layer := firstLayer(t, f.stub, "library/alpine", "3.19")
	if r := f.get(f.reg+"/v2/library/alpine/blobs/"+layer, nil); r.status != 502 {
		t.Errorf("blob = %d, want 502", r.status)
	}
	if r := f.get(f.reg+"/v2/library/alpine/tags/list", nil); r.status != 500 {
		t.Errorf("tags = %d, want 500", r.status)
	}
	if r := f.manifestOf("library/alpine", "3.19"); r.status != 200 {
		t.Errorf("manifest of a class that does not fail = %d", r.status)
	}
	if err := f.stub.SetMode(Mode{Auth: AuthNone, FailStatus: map[string]int{ClassAll: 504}}); err != nil {
		t.Fatal(err)
	}
	if r := f.manifestOf("library/alpine", "3.19"); r.status != 504 {
		t.Errorf("all classes failing: manifest = %d", r.status)
	}
	// The CDN host fails on its own class.
	if err := f.stub.SetMode(Mode{Auth: AuthNone, FailStatus: map[string]int{ClassCDN: 503}}); err != nil {
		t.Fatal(err)
	}
	redirect := f.get(f.reg+"/v2/library/alpine/blobs/"+layer, nil).header.Get("Location")
	if r := f.get(redirect, nil); r.status != 503 {
		t.Errorf("CDN = %d, want 503", r.status)
	}
}

func TestDelayPerClass(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, DelayMS: map[string]int{ClassManifest: 2000}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.manifestOf("library/alpine", "3")
	held := time.Since(start)
	if held < 1900*time.Millisecond {
		t.Errorf("manifest answered after %v, want at least 2 s", held)
	}
	start = time.Now()
	f.get(f.reg+"/v2/library/alpine/tags/list", nil)
	if d := time.Since(start); d > held-time.Second {
		t.Errorf("tags answered after %v, a class with no delay, against %v for the delayed class", d, held)
	}
	if err := f.stub.SetMode(Mode{Auth: AuthNone, DelayMS: map[string]int{ClassAll: 300, ClassTags: 200}}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	f.get(f.reg+"/v2/library/alpine/tags/list", nil)
	if d := time.Since(start); d < 480*time.Millisecond {
		t.Errorf("tags answered after %v, want the class and the all delay (500 ms)", d)
	}
}

func TestNotFoundRepository(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, NotFound: []string{"acme/a"}}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.reg+"/v2/acme/a/tags/list", nil); r.status != 404 || r.errorCode(t) != "NAME_UNKNOWN" {
		t.Errorf("tags of a repository the stub 'does not have' = %d", r.status)
	}
	if r := f.get(f.reg+"/v2/acme/b/tags/list", nil); r.status != 200 {
		t.Errorf("another repository = %d", r.status)
	}
}

func TestSchema1Tag(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, Schema1: []string{"library/busybox:1.35"}}); err != nil {
		t.Fatal(err)
	}
	r := f.manifestOf("library/busybox", "1.35")
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	r.json(t, &doc)
	if r.status != 200 || r.header.Get("Content-Type") != MediaDockerSchema1 || doc.SchemaVersion != 1 || sha(r.body) != r.header.Get("Docker-Content-Digest") {
		t.Fatalf("schema 1 tag = %d %s, schemaVersion %d", r.status, r.header.Get("Content-Type"), doc.SchemaVersion)
	}
	if o := f.manifestOf("library/busybox", "latest"); o.header.Get("Content-Type") != MediaDockerList {
		t.Errorf("another tag = %s", o.header.Get("Content-Type"))
	}
	// By digest the real manifest is still there.
	d, _ := f.stub.Digest("library/busybox", "1.35")
	if o := f.manifestOf("library/busybox", d); o.header.Get("Content-Type") != MediaDockerManifest {
		t.Errorf("by digest = %s", o.header.Get("Content-Type"))
	}
}

// Bytes that are not the bytes of the digest header (R-OCI-022).
func TestWrongDigestManifest(t *testing.T) {
	f := newFixture(t, Config{})
	d, _ := f.stub.Digest("acme/a", "1.0")
	for _, entry := range []string{"acme/a", "acme/a:1.0", "acme/a@" + d} {
		if err := f.stub.SetMode(Mode{Auth: AuthNone, WrongDigest: []string{entry}}); err != nil {
			t.Fatal(err)
		}
		ref := "1.0"
		if strings.Contains(entry, "@") {
			ref = d
		}
		r := f.manifestOf("acme/a", ref)
		if r.status != 200 || r.header.Get("Docker-Content-Digest") != d || sha(r.body) == d {
			t.Errorf("entry %q: %d, header %s, body digest %s", entry, r.status, r.header.Get("Docker-Content-Digest"), sha(r.body))
		}
		if ok := f.manifestOf("acme/a", "1.1"); entry != "acme/a" && sha(ok.body) != ok.header.Get("Docker-Content-Digest") {
			t.Errorf("entry %q breaks another tag", entry)
		}
	}
}

func TestWrongBlob(t *testing.T) {
	f := newFixture(t, Config{})
	layer := firstLayer(t, f.stub, "acme/a", "1.0")
	if err := f.stub.SetMode(Mode{Auth: AuthNone, BlobsDirect: true, WrongBlob: []string{"acme/a@" + layer}}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.reg+"/v2/acme/a/blobs/"+layer, nil); r.status != 200 || sha(r.body) == layer {
		t.Errorf("wrong blob: %d, digest of the bytes %s", r.status, sha(r.body))
	}
	if r := f.get(f.reg+"/v2/other/c/blobs/"+layer, nil); sha(r.body) != layer {
		t.Error("the same blob under another repository is affected")
	}
	// Through the CDN as well.
	if err := f.stub.SetMode(Mode{Auth: AuthNone, WrongBlob: []string{"acme/a"}}); err != nil {
		t.Fatal(err)
	}
	redirect := f.get(f.reg+"/v2/acme/a/blobs/"+layer, nil).header.Get("Location")
	if r := f.get(redirect, nil); sha(r.body) == layer {
		t.Error("the CDN serves the right bytes of a wrong blob")
	}
}

// Docker Hub counts manifest GET requests only, and answers over the limit with 429, the quota headers and no Retry-After (R-OCI-025).
func TestPullQuota(t *testing.T) {
	clk := newClock()
	f := newFixture(t, Config{Now: clk.now})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, PullQuota: &PullQuota{Limit: 3}}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"2;w=21600", "1;w=21600", "0;w=21600"} {
		r := f.manifestOf("library/alpine", "3")
		if r.status != 200 || r.header.Get("RateLimit-Remaining") != want || r.header.Get("RateLimit-Limit") != "3;w=21600" {
			t.Fatalf("pull %d: %d, limit %q remaining %q, want remaining %s", i+1, r.status, r.header.Get("RateLimit-Limit"), r.header.Get("RateLimit-Remaining"), want)
		}
	}
	// HEAD, tags lists and blobs do not count and show the remainder.
	head := f.do(http.MethodHead, f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll})
	if head.status != 200 || head.header.Get("RateLimit-Remaining") != "0;w=21600" {
		t.Errorf("HEAD over the limit = %d %q", head.status, head.header.Get("RateLimit-Remaining"))
	}
	if r := f.get(f.reg+"/v2/library/alpine/tags/list", nil); r.status != 200 {
		t.Errorf("tags over the limit = %d", r.status)
	}
	r := f.manifestOf("library/alpine", "3")
	if r.status != 429 || r.errorCode(t) != "TOOMANYREQUESTS" || r.header.Get("RateLimit-Remaining") != "0;w=21600" || r.header.Get("Retry-After") != "" {
		t.Fatalf("over the limit: %d %s remaining %q Retry-After %q", r.status, r.body, r.header.Get("RateLimit-Remaining"), r.header.Get("Retry-After"))
	}
	if q := f.stub.Quota(); q.Used != 3 || q.Remaining != 0 || q.Limit != 3 {
		t.Errorf("quota state = %+v", q)
	}
	// The window ends and the quota starts again.
	clk.advance(21600 * time.Second)
	if r := f.manifestOf("library/alpine", "3"); r.status != 200 || r.header.Get("RateLimit-Remaining") != "2;w=21600" {
		t.Errorf("after the window = %d %q", r.status, r.header.Get("RateLimit-Remaining"))
	}
	// Setting the mode counts afresh.
	if err := f.stub.SetMode(Mode{Auth: AuthNone, PullQuota: &PullQuota{Limit: 1, WindowSeconds: 60}}); err != nil {
		t.Fatal(err)
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 200 || r.header.Get("RateLimit-Limit") != "1;w=60" {
		t.Errorf("new quota = %d %q", r.status, r.header.Get("RateLimit-Limit"))
	}
}

func TestPullQuotaDefaultsToHubNumbers(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, PullQuota: &PullQuota{}}); err != nil {
		t.Fatal(err)
	}
	r := f.manifestOf("library/alpine", "3")
	if r.header.Get("RateLimit-Limit") != "100;w=21600" || r.header.Get("RateLimit-Remaining") != "99;w=21600" {
		t.Errorf("headers = %q %q", r.header.Get("RateLimit-Limit"), r.header.Get("RateLimit-Remaining"))
	}
}

func TestThrottleModes(t *testing.T) {
	clk := newClock()
	f := newFixture(t, Config{Now: clk.now})
	f.open()
	get := func() reply { return f.get(f.reg+"/v2/library/alpine/tags/list", nil) }
	set := func(th Throttle) {
		t.Helper()
		if err := f.stub.SetThrottle(th); err != nil {
			t.Fatal(err)
		}
	}

	set(Throttle{Mode: ThrottleTooManyRequests, RetryAfterSeconds: 30})
	if r := get(); r.status != 429 || r.header.Get("Retry-After") != "30" || r.errorCode(t) != "TOOMANYREQUESTS" {
		t.Errorf("429: %d Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
	set(Throttle{Mode: ThrottleTooManyRequestsDate, RetryAfterSeconds: 90})
	r := get()
	when, err := http.ParseTime(r.header.Get("Retry-After"))
	if r.status != 429 || err != nil || !when.Equal(clk.now().Add(90*time.Second)) {
		t.Errorf("429-date: %d Retry-After %q (%v)", r.status, r.header.Get("Retry-After"), err)
	}
	set(Throttle{Mode: ThrottleUnavailable, RetryAfterSeconds: 5})
	if r := get(); r.status != 503 || r.header.Get("Retry-After") != "5" {
		t.Errorf("503: %d Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
	set(Throttle{Mode: ThrottleSlowDown})
	if r := get(); r.status != 503 || r.header.Get("Retry-After") != "" || string(r.body) != SlowDownBody || r.header.Get("Content-Type") != "application/xml" {
		t.Errorf("503-slowdown: %d %q", r.status, r.body)
	}
	set(Throttle{Mode: ThrottleQuota403, RetryAfterSeconds: 60})
	if r := get(); r.status != 403 || r.header.Get("X-RateLimit-Remaining") != "0" || r.header.Get("X-RateLimit-Reset") != strconv.FormatInt(clk.now().Add(time.Minute).Unix(), 10) {
		t.Errorf("403-quota: %d %q %q", r.status, r.header.Get("X-RateLimit-Remaining"), r.header.Get("X-RateLimit-Reset"))
	}
	set(Throttle{Mode: ThrottleQuota429})
	if r := get(); r.status != 429 || r.header.Get("X-RateLimit-Remaining") != "0" || r.header.Get("X-RateLimit-Reset") != "" || r.header.Get("Retry-After") != "" {
		t.Errorf("429-quota: %d headers %v", r.status, r.header)
	}
	set(Throttle{Mode: ThrottleHub})
	if r := get(); r.status != 429 || r.header.Get("RateLimit-Limit") != "100;w=21600" || r.header.Get("RateLimit-Remaining") != "0;w=21600" || r.header.Get("Retry-After") != "" {
		t.Errorf("429-hub: %d headers %v", r.status, r.header)
	}
	set(Throttle{})
	if r := get(); r.status != 200 {
		t.Errorf("cleared throttle = %d", r.status)
	}
}

func TestThrottleNthAndPeriod(t *testing.T) {
	clk := newClock()
	f := newFixture(t, Config{Now: clk.now})
	f.open()
	statuses := func(n int) []int {
		var out []int
		for range n {
			out = append(out, f.get(f.reg+"/v2/library/alpine/tags/list", nil).status)
		}
		return out
	}
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleTooManyRequests, Nth: 3, Count: 2}); err != nil {
		t.Fatal(err)
	}
	got := statuses(6)
	want := []int{200, 200, 429, 429, 200, 200}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nth 3 count 2: %v, want %v", got, want)
		}
	}
	if st := f.stub.ThrottleState(); st.Seen != 6 || st.Throttled != 2 {
		t.Errorf("state = %+v", st)
	}
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleTooManyRequests, Nth: 2, Count: -1}); err != nil {
		t.Fatal(err)
	}
	if got := statuses(4); got[0] != 200 || got[1] != 429 || got[3] != 429 {
		t.Errorf("nth 2 and on: %v", got)
	}
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleTooManyRequests, ForMS: 5000}); err != nil {
		t.Fatal(err)
	}
	if got := statuses(1); got[0] != 429 {
		t.Errorf("inside the period: %v", got)
	}
	clk.advance(6 * time.Second)
	if got := statuses(1); got[0] != 200 {
		t.Errorf("after the period: %v", got)
	}
}

// Docker Hub counts manifest GET requests: a throttle narrowed to the manifest class leaves every other request alone and uncounted.
func TestThrottleClass(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleHub, Class: ClassManifest, Nth: 2}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if r := f.get(f.reg+"/v2/library/alpine/tags/list", nil); r.status != 200 {
			t.Fatalf("tags throttled = %d", r.status)
		}
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 200 {
		t.Errorf("first manifest = %d", r.status)
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 429 {
		t.Errorf("second manifest = %d, want 429", r.status)
	}
	if st := f.stub.ThrottleState(); st.Seen != 2 || st.Throttled != 1 {
		t.Errorf("state = %+v, want only the manifest requests counted", st)
	}
}

func TestThrottleDelay(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	if err := f.stub.SetThrottle(Throttle{DelayMS: 200}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if r := f.get(f.reg+"/v2/", nil); r.status != 200 {
		t.Fatalf("delayed request = %d", r.status)
	}
	if d := time.Since(start); d < 190*time.Millisecond {
		t.Errorf("answered after %v", d)
	}
}

func TestModeAndThrottleValidation(t *testing.T) {
	s, _ := New(Config{})
	for name, m := range map[string]Mode{
		"down":          {Down: "boom"},
		"auth":          {Auth: "kerberos"},
		"delay class":   {DelayMS: map[string]int{"nope": 1}},
		"delay range":   {DelayMS: map[string]int{ClassAll: -1}},
		"fail class":    {FailStatus: map[string]int{"nope": 500}},
		"fail status":   {FailStatus: map[string]int{ClassBlob: 404}},
		"refuse status": {RefuseToken: 200},
		"ttl":           {TokenTTLSeconds: -1},
		"quota":         {PullQuota: &PullQuota{Limit: -1}},
	} {
		if err := s.SetMode(m); err == nil {
			t.Errorf("mode %s: accepted", name)
		}
	}
	for name, th := range map[string]Throttle{
		"mode":        {Mode: "teapot"},
		"class":       {Mode: ThrottleHub, Class: "nope"},
		"class all":   {Mode: ThrottleHub, Class: ClassAll},
		"retry":       {Mode: ThrottleHub, RetryAfterSeconds: MaxThrottleSeconds + 1},
		"nth and for": {Mode: ThrottleHub, Nth: 1, ForMS: 1},
		"nth":         {Mode: ThrottleHub, Nth: -1},
		"delay":       {DelayMS: int(MaxHang/time.Millisecond) + 1},
	} {
		if err := s.SetThrottle(th); err == nil {
			t.Errorf("throttle %s: accepted", name)
		}
	}
	if (Mode{}).Validate() != nil {
		t.Error("the zero mode is invalid")
	}
}

// Requests are counted while they are held, so a test can see how many are open at once.
func TestInflight(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, DelayMS: map[string]int{ClassTags: 4000}}); err != nil {
		t.Fatal(err)
	}
	f.stub.Reset()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.get(f.reg+"/v2/library/alpine/tags/list", nil)
		}()
	}
	deadline := time.Now().Add(30 * time.Second)
	for f.stub.Inflight().Open < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.stub.Inflight(); got.Open != 3 || got.Max != 3 {
		t.Errorf("inflight while held = %+v", got)
	}
	wg.Wait()
	if got := f.stub.Inflight(); got.Open != 0 || got.Max != 3 || got.Requests != 3 {
		t.Errorf("inflight after = %+v", got)
	}
}

func TestStartNeedsTLS(t *testing.T) {
	s, _ := New(Config{})
	if _, err := s.Start("127.0.0.1:0", nil); err == nil {
		t.Error("started without a certificate")
	}
	if _, err := New(Config{PublicURL: "registry.test"}); err == nil {
		t.Error("a URL without a scheme was accepted")
	}
	f := newFixture(t, Config{})
	plain := &http.Client{Timeout: 5 * time.Second}
	if resp, err := plain.Get("http://" + f.run.Addr + "/v2/"); err == nil {
		defer resp.Body.Close()
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		if resp.StatusCode == 200 {
			t.Errorf("the stub answered plain HTTP with %d", resp.StatusCode)
		}
	}
}

func TestReloadingKeyPairServesRotatedCertificate(t *testing.T) {
	// The stub's certificate is read again from its files, so a rotation needs no restart.
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	write := func(ss *SelfSigned) {
		t.Helper()
		keyDER, err := x509.MarshalPKCS8PrivateKey(ss.Cert.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ss.Cert.Certificate[0]}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewSelfSigned(LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	write(first)
	get, err := ReloadingKeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	stub, _ := New(Config{})
	run, err := stub.Start("127.0.0.1:0", &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close() //nolint:errcheck // test cleanup

	served := func() []byte {
		t.Helper()
		conn, err := tls.Dial("tcp", run.Addr, &tls.Config{RootCAs: first.Pool, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // the test reads the presented certificate
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close() //nolint:errcheck // test client
		return conn.ConnectionState().PeerCertificates[0].Raw
	}
	if !bytes.Equal(served(), first.Cert.Certificate[0]) {
		t.Fatal("the first certificate is not served")
	}
	second, err := NewSelfSigned(LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	write(second)
	future := time.Now().Add(time.Minute)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, future, future); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(served(), second.Cert.Certificate[0]) {
		t.Fatal("the rotated certificate is not served")
	}
	if err := os.WriteFile(certFile, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Minute)
	_ = os.Chtimes(certFile, later, later)
	if !bytes.Equal(served(), second.Cert.Certificate[0]) {
		t.Fatal("a broken rotation must keep serving the last good certificate")
	}
}
