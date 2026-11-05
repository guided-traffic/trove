package npmstub

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests of the throttling modes carry R-REPO-059 (the modes of the counting stub) and R-REPO-054 (the answers the
// mirror treats as rate limits).

func (f *fixture) setThrottle(t Throttle) {
	f.t.Helper()
	if err := f.stub.SetMode(Mode{Throttle: &t}); err != nil {
		f.t.Fatal(err)
	}
}

func TestThrottleRetryAfterInSecondsOnTheNthRequest(t *testing.T) {
	// R-REPO-054 acceptance: the eleventh tarball request answers 429 with Retry-After: 7, and only it.
	f := newFixture(t, Config{})
	_, name := names(t)
	if err := f.stub.Publish(name, "9.9.9", nil); err != nil {
		t.Fatal(err)
	}
	file := "/" + name + "/-/" + name + "-9.9.9.tgz"
	f.setThrottle(Throttle{Kind: Throttle429, Class: KindTarball, From: 11, RetryAfterSeconds: 7})
	for i := 1; i <= 13; i++ {
		r := f.get(file)
		switch {
		case i == 11:
			if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "7" {
				t.Fatalf("request %d: %d, Retry-After %q", i, r.status, r.header.Get("Retry-After"))
			}
		case r.status != 200:
			t.Fatalf("request %d: %d", i, r.status)
		}
	}
	// A document request is not of the class and is never throttled; the counter counts the throttled answers.
	if st := f.status(docPath(name)); st != 200 || f.stub.Throttled() != 1 {
		t.Fatalf("document %d, throttled %d", st, f.stub.Throttled())
	}
}

func TestThrottleRetryAfterAsDate(t *testing.T) {
	// R-REPO-054 acceptance: Retry-After as an HTTP date 5 s ahead.
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Kind: Throttle429Date, RetryAfterSeconds: 5})
	r := f.get("/-/ping")
	at, err := http.ParseTime(r.header.Get("Retry-After"))
	if r.status != http.StatusTooManyRequests || err != nil {
		t.Fatalf("%d, Retry-After %q: %v", r.status, r.header.Get("Retry-After"), err)
	}
	if d := time.Until(at); d < 3*time.Second || d > 6*time.Second {
		t.Fatalf("the date is %s ahead", d)
	}
}

func TestThrottle503WithRetryAfter(t *testing.T) {
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Kind: Throttle503, RetryAfterSeconds: 30, Count: 2})
	for i := 0; i < 2; i++ {
		if r := f.get("/-/ping"); r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "30" {
			t.Fatalf("request %d: %d, %q", i, r.status, r.header.Get("Retry-After"))
		}
	}
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("after the count: %d", st)
	}
}

func TestThrottle503SlowDownWithoutAHint(t *testing.T) {
	// R-REPO-054 acceptance: 503 with the S3 error body and no Retry-After, three times in a row.
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Kind: Throttle503SlowDown, Count: 3})
	for i := 0; i < 3; i++ {
		r := f.get("/-/ping")
		if r.status != http.StatusServiceUnavailable || string(r.body) != SlowDownBody || r.header.Get("Retry-After") != "" {
			t.Fatalf("request %d: %d %q, Retry-After %q", i, r.status, r.body, r.header.Get("Retry-After"))
		}
	}
	if string(SlowDownBody) != "<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>" {
		t.Fatal("the body is not the S3 error of R-REPO-059")
	}
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("the fourth request: %d", st)
	}
}

func TestThrottleQuotaHeaders(t *testing.T) {
	// R-REPO-054 acceptance: 403 and 429 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset, no Retry-After.
	for kind, status := range map[string]int{Throttle403Quota: http.StatusForbidden, Throttle429Quota: http.StatusTooManyRequests} {
		f := newFixture(t, Config{})
		f.setThrottle(Throttle{Kind: kind, RetryAfterSeconds: 4})
		before := time.Now().Unix()
		r := f.get("/-/ping")
		reset, err := strconv.ParseInt(r.header.Get("X-RateLimit-Reset"), 10, 64)
		if r.status != status || r.header.Get("X-RateLimit-Remaining") != "0" || err != nil || r.header.Get("Retry-After") != "" {
			t.Fatalf("%s: %d, headers %v", kind, r.status, r.header)
		}
		if reset < before+3 || reset > before+6 {
			t.Fatalf("%s: reset %d, now %d", kind, reset, before)
		}
	}
}

func TestThrottleForAPeriod(t *testing.T) {
	// R-REPO-059: throttled for a period from the first throttled request, then healthy again.
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Kind: Throttle429, RetryAfterSeconds: 1, PeriodMS: 300, From: 2})
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("the first request: %d", st)
	}
	for i := 0; i < 3; i++ {
		if st := f.status("/-/ping"); st != http.StatusTooManyRequests {
			t.Fatalf("inside the period, request %d: %d", i, st)
		}
	}
	time.Sleep(350 * time.Millisecond)
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("after the period: %d", st)
	}
	// Setting a mode starts the counters afresh.
	f.setThrottle(Throttle{Kind: Throttle429, RetryAfterSeconds: 1, From: 2})
	if f.status("/-/ping") != 200 || f.status("/-/ping") != http.StatusTooManyRequests {
		t.Fatal("the counters were not reset")
	}
}

func TestThrottleDelaysEveryResponse(t *testing.T) {
	// R-REPO-059: a delay per response, throttled or not, of the class only.
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Class: KindReplication, DelayMS: 150})
	start := time.Now()
	f.repl("/")
	f.repl("/")
	if d := time.Since(start); d < 280*time.Millisecond {
		t.Fatalf("two replication responses took %s", d)
	}
	start = time.Now()
	f.get("/-/ping")
	if d := time.Since(start); d > 140*time.Millisecond {
		t.Fatalf("a registry request was held %s", d)
	}
	if f.stub.Throttled() != 0 {
		t.Fatalf("a delay throttled %d requests", f.stub.Throttled())
	}
}

func TestThrottleOfTheReplicationHostOnly(t *testing.T) {
	// R-REPO-059: the replication requests are a class of their own.
	f := newFixture(t, Config{})
	f.setThrottle(Throttle{Kind: Throttle429, Class: KindReplication, RetryAfterSeconds: 2, Count: 5})
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("registry host: %d", st)
	}
	r := f.repl("/")
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "2" {
		t.Fatalf("replication host: %d", r.status)
	}
	// A throttled request is counted and logged like any other.
	if n := f.stub.Counts().Requests(http.MethodGet, "/"); n != 1 || len(f.stub.Replications()) != 1 {
		t.Fatalf("%d counted, %d logged", n, len(f.stub.Replications()))
	}
}

func TestThrottleIsSetOverTheControlAPI(t *testing.T) {
	// R-REPO-059: the control API switches it, and refuses what it does not implement.
	f := newFixture(t, Config{ControlToken: "tok"})
	auth := map[string]string{"Authorization": "Bearer tok"}
	set := func(body string) int {
		return f.do(http.MethodPost, "/-/stub/mode", auth, strings.NewReader(body)).status
	}
	if st := set(`{"throttle":{"kind":"429","retry_after_seconds":9,"count":1}}`); st != 200 {
		t.Fatalf("set: %d", st)
	}
	if r := f.get("/-/ping", "Authorization", ""); r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "9" {
		t.Fatalf("throttled: %d", r.status)
	}
	for _, bad := range []string{
		`{"throttle":{"kind":"418"}}`, `{"throttle":{"kind":"429","class":"nope"}}`, `{"throttle":{"kind":"429","count":-1}}`,
		`{"throttle":{"kind":"429","period_ms":99999999}}`, `{"throttle":{"unknown":1}}`,
	} {
		if st := set(bad); st != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, st)
		}
	}
	if st := set(`{}`); st != 200 {
		t.Fatalf("clear: %d", st)
	}
	if st := f.status("/-/ping"); st != 200 {
		t.Fatalf("after clearing: %d", st)
	}
}
