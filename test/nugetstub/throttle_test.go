package nugetstub

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The throttling modes of the stub (R-REPO-059) in the answers that the mirror's throttling (R-REPO-054) recognizes.

func (f *fixture) throttle(t Throttle) {
	f.t.Helper()
	if err := f.stub.SetThrottle(t); err != nil {
		f.t.Fatal(err)
	}
}

func TestR_REPO_059_ThrottleModesAnswerWithTheirHeadersAndBodies(t *testing.T) {
	now := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mode   string
		status int
		check  func(t *testing.T, r reply)
	}{
		{"429 with Retry-After in seconds", ThrottleTooManyRequests, 429, func(t *testing.T, r reply) {
			if got := r.header.Get("Retry-After"); got != "7" {
				t.Errorf("Retry-After %q", got)
			}
		}},
		{"429 with Retry-After as a date", ThrottleTooManyRequestsDate, 429, func(t *testing.T, r reply) {
			if got, want := r.header.Get("Retry-After"), now.Add(7*time.Second).Format(http.TimeFormat); got != want {
				t.Errorf("Retry-After %q, want %q", got, want)
			}
		}},
		{"503 with Retry-After", ThrottleUnavailable, 503, func(t *testing.T, r reply) {
			if got := r.header.Get("Retry-After"); got != "7" {
				t.Errorf("Retry-After %q", got)
			}
		}},
		{"503 SlowDown without a hint", ThrottleSlowDown, 503, func(t *testing.T, r reply) {
			if string(r.body) != SlowDownBody || r.header.Get("Retry-After") != "" {
				t.Errorf("body %q, Retry-After %q", r.body, r.header.Get("Retry-After"))
			}
			if !strings.Contains(string(r.body), "<Code>SlowDown</Code>") {
				t.Errorf("body %q", r.body)
			}
		}},
		{"403 with an exhausted quota", ThrottleQuota403, 403, func(t *testing.T, r reply) {
			quota(t, r, now.Add(7*time.Second))
		}},
		{"429 with an exhausted quota", ThrottleQuota429, 429, func(t *testing.T, r reply) {
			quota(t, r, now.Add(7*time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Config{Now: func() time.Time { return now }})
			f.throttle(Throttle{Mode: tc.mode, RetryAfterSeconds: 7})
			r := f.get(f.api + "/v3/index.json")
			if r.status != tc.status {
				t.Fatalf("status %d", r.status)
			}
			tc.check(t, r)
			if st := f.stub.ThrottleState(); st.Seen != 1 || st.Throttled != 1 {
				t.Errorf("%+v", st)
			}
			// The throttled request is counted like any other.
			if n := f.counts().Total(); n != 1 {
				t.Errorf("counted %d", n)
			}
		})
	}
}

func quota(t *testing.T, r reply, reset time.Time) {
	t.Helper()
	if r.header.Get("X-RateLimit-Remaining") != "0" || r.header.Get("X-RateLimit-Reset") != strconv.FormatInt(reset.Unix(), 10) || r.header.Get("Retry-After") != "" {
		t.Errorf("headers %v", r.header)
	}
}

func TestR_REPO_059_AQuotaWithoutAResetNamesNone(t *testing.T) {
	f := newFixture(t, Config{})
	f.throttle(Throttle{Mode: ThrottleQuota403})
	r := f.get(f.api + "/v3/index.json")
	if r.status != 403 || r.header.Get("X-RateLimit-Remaining") != "0" || r.header.Get("X-RateLimit-Reset") != "" {
		t.Errorf("%d %v", r.status, r.header)
	}
}

func TestR_REPO_059_TheNthRequestIsThrottledAndTheOthersPass(t *testing.T) {
	f := newFixture(t, Config{})
	f.throttle(Throttle{Mode: ThrottleTooManyRequests, RetryAfterSeconds: 1, Nth: 3, Count: 2})
	var got []int
	for range 6 {
		got = append(got, f.get(f.api+"/v3/index.json").status)
	}
	if want := []int{200, 200, 429, 429, 200, 200}; !equalInts(got, want) {
		t.Fatalf("statuses %v, want %v", got, want)
	}
	if st := f.stub.ThrottleState(); st.Seen != 6 || st.Throttled != 2 {
		t.Errorf("%+v", st)
	}
	// Setting the throttle again restarts the count.
	f.throttle(Throttle{Mode: ThrottleTooManyRequests, Nth: 1})
	if got := []int{f.get(f.api + "/v3/index.json").status, f.get(f.api + "/v3/index.json").status}; !equalInts(got, []int{429, 200}) {
		t.Errorf("statuses %v", got)
	}
	// A negative count throttles from the n-th on.
	f.throttle(Throttle{Mode: ThrottleUnavailable, Nth: 2, Count: -1})
	if got := []int{f.get(f.api + "/v3/index.json").status, f.get(f.api + "/v3/index.json").status, f.get(f.api + "/v3/index.json").status}; !equalInts(got, []int{200, 503, 503}) {
		t.Errorf("statuses %v", got)
	}
}

func TestR_REPO_059_APeriodThrottlesEveryRequestUntilItEnds(t *testing.T) {
	var clock struct{ now time.Time }
	clock.now = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	f := newFixture(t, Config{Now: func() time.Time { return clock.now }})
	f.throttle(Throttle{Mode: ThrottleSlowDown, ForMS: 5000})
	first := f.get(f.api + "/v3/index.json").status
	clock.now = clock.now.Add(4999 * time.Millisecond)
	inside := f.get(f.api + "/v3/index.json").status
	clock.now = clock.now.Add(2 * time.Millisecond)
	after := f.get(f.api + "/v3/index.json").status
	if first != 503 || inside != 503 || after != 200 {
		t.Errorf("statuses %d %d %d", first, inside, after)
	}
}

func TestR_REPO_059_WithoutAScheduleEveryRequestIsThrottledUntilTheSwitchIsCleared(t *testing.T) {
	f := newFixture(t, Config{})
	f.throttle(Throttle{Mode: ThrottleTooManyRequests, RetryAfterSeconds: 1})
	for range 3 {
		if s := f.get(f.api + "/v3/index.json").status; s != 429 {
			t.Fatalf("status %d", s)
		}
	}
	f.throttle(Throttle{})
	if s := f.get(f.api + "/v3/index.json").status; s != 200 {
		t.Errorf("after clearing: %d", s)
	}
}

func TestR_REPO_059_ADelayHoldsEveryResponseWhileTheSwitchIsSet(t *testing.T) {
	f := newFixture(t, Config{})
	f.throttle(Throttle{DelayMS: 120})
	start := time.Now()
	r := f.get(f.api + "/v3/index.json")
	if d := time.Since(start); r.status != 200 || d < 120*time.Millisecond {
		t.Errorf("status %d after %v", r.status, d)
	}
	if st := f.stub.ThrottleState(); st.Seen != 1 || st.Throttled != 0 {
		t.Errorf("%+v", st)
	}
	// A delay with a mode: the throttled answer is delayed too.
	f.throttle(Throttle{Mode: ThrottleTooManyRequests, DelayMS: 120})
	start = time.Now()
	r = f.get(f.api + "/v3/index.json")
	if d := time.Since(start); r.status != 429 || d < 120*time.Millisecond {
		t.Errorf("status %d after %v", r.status, d)
	}
}

func TestR_REPO_059_ThrottleValidationAndReset(t *testing.T) {
	f := newFixture(t, Config{})
	for name, th := range map[string]Throttle{
		"unknown mode":       {Mode: "418"},
		"negative retry":     {Mode: ThrottleTooManyRequests, RetryAfterSeconds: -1},
		"too long a retry":   {Mode: ThrottleTooManyRequests, RetryAfterSeconds: MaxThrottleSeconds + 1},
		"nth with a period":  {Mode: ThrottleTooManyRequests, Nth: 1, ForMS: 10},
		"negative delay":     {DelayMS: -1},
		"unbounded delay":    {DelayMS: int(MaxHang/time.Millisecond) + 1},
		"unbounded period":   {Mode: ThrottleTooManyRequests, ForMS: MaxThrottleSeconds*1000 + 1},
		"negative nth":       {Mode: ThrottleTooManyRequests, Nth: -1},
		"negative period ms": {Mode: ThrottleTooManyRequests, ForMS: -1},
	} {
		if err := f.stub.SetThrottle(th); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.throttle(Throttle{Mode: ThrottleTooManyRequests})
	f.stub.ResetAll()
	if st := f.stub.ThrottleState(); st.Throttle != (Throttle{}) || st.Seen != 0 {
		t.Errorf("after ResetAll: %+v", st)
	}
	if s := f.get(f.api + "/v3/index.json").status; s != 200 {
		t.Errorf("after ResetAll: %d", s)
	}
}

func TestR_REPO_059_TheThrottleIsDrivenOverTheControlAPIAndNeverThrottlesIt(t *testing.T) {
	f := newFixture(t, Config{ControlToken: "s3cret"})
	auth := map[string]string{"Authorization": "Bearer s3cret"}
	post := func(path, body string) reply {
		return f.do(http.MethodPost, f.api+path, auth, strings.NewReader(body))
	}
	if r := post("/-/stub/throttle", `{"mode":"429-date","retry_after_seconds":30,"nth":2}`); r.status != 200 {
		t.Fatalf("set: %d %s", r.status, r.body)
	}
	if s := f.get(f.api + "/v3/index.json").status; s != 200 {
		t.Errorf("request 1: %d", s)
	}
	// Reads of the control API are neither counted nor throttled: the second request is the first one to the feed.
	r := f.do(http.MethodGet, f.api+"/-/stub/throttle", auth, nil)
	if r.status != 200 || !strings.Contains(string(r.body), `"seen":1`) || !strings.Contains(string(r.body), `"mode":"429-date"`) {
		t.Errorf("state: %d %s", r.status, r.body)
	}
	r = f.get(f.api + "/v3/index.json")
	if r.status != 429 {
		t.Fatalf("request 2: %d", r.status)
	}
	if _, err := http.ParseTime(r.header.Get("Retry-After")); err != nil {
		t.Errorf("Retry-After %q is no HTTP date", r.header.Get("Retry-After"))
	}
	for _, body := range []string{`{"mode":"418"}`, `{"nope":1}`, `not json`} {
		if r := post("/-/stub/throttle", body); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d", body, r.status)
		}
	}
	if r := post("/-/stub/throttle", `{}`); r.status != 200 {
		t.Fatalf("clear: %d", r.status)
	}
	if s := f.get(f.api + "/v3/index.json").status; s != 200 {
		t.Errorf("after clearing: %d", s)
	}
	if r := f.do(http.MethodPost, f.api+"/-/stub/throttle", nil, strings.NewReader(`{}`)); r.status != http.StatusUnauthorized {
		t.Errorf("without the token: %d", r.status)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
