package rotationhold

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func hold(retryAfter string) Answer {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return Answer{Status: 503, Header: h, Body: []byte(`{"status":503,"code":"break_glass_rotating"}`)}
}

// R-AUTH-014 Q-AUTH-14: a login inside the hold waits for the Retry-After and succeeds after it.
func TestWaitsForTheRetryAfterOfTheHold(t *testing.T) {
	calls := 0
	var slept []time.Duration
	got, err := Wait(func() Answer {
		calls++
		if calls <= 2 {
			return hold("30")
		}
		return Answer{Status: 204}
	}, MaxWait, func(d time.Duration) { slept = append(slept, d) }, nil)
	if err != nil || got.Status != 204 || calls != 3 {
		t.Fatalf("got %d, %v after %d calls; want 204 after 3", got.Status, err, calls)
	}
	if len(slept) != 2 || slept[0] != 31*time.Second {
		t.Fatalf("slept %v, want two waits of 31 s", slept)
	}
}

// Only the designed answer is waited for: another 503, a 401, a 429 and a 500 return at once.
func TestOtherAnswersAreNotRetried(t *testing.T) {
	for name, a := range map[string]Answer{
		"server_busy":      {Status: 503, Header: http.Header{}, Body: []byte(`{"code":"server_busy"}`)},
		"no body":          {Status: 503, Header: http.Header{}},
		"wrong":            {Status: 401, Header: http.Header{}, Body: []byte(`{"code":"invalid_credentials"}`)},
		"locked":           {Status: 429, Header: http.Header{}, Body: []byte(`{"code":"break_glass_locked"}`)},
		"hold code on 500": {Status: 500, Header: http.Header{}, Body: []byte(`{"code":"break_glass_rotating"}`)},
	} {
		calls := 0
		got, err := Wait(func() Answer { calls++; return a }, MaxWait, func(time.Duration) { t.Errorf("%s: slept", name) }, nil)
		if err != nil || got.Status != a.Status || calls != 1 {
			t.Errorf("%s: %d, %v after %d calls; want the answer after one call", name, got.Status, err, calls)
		}
	}
}

// A hold that outlasts the cap fails with a message that names the cause.
func TestGivesUpAfterTheCap(t *testing.T) {
	var total time.Duration
	_, err := Wait(func() Answer { return hold("60") }, MaxWait, func(d time.Duration) { total += d }, nil)
	if err == nil || !strings.Contains(err.Error(), "break_glass_rotating") {
		t.Fatalf("err = %v, want one naming break_glass_rotating", err)
	}
	if total > MaxWait {
		t.Fatalf("waited %s, cap %s", total, MaxWait)
	}
}

func TestMissingRetryAfterWaitsTwoSeconds(t *testing.T) {
	calls := 0
	var slept time.Duration
	_, _ = Wait(func() Answer {
		calls++
		if calls == 1 {
			return hold("")
		}
		return Answer{Status: 204}
	}, MaxWait, func(d time.Duration) { slept += d }, nil)
	if slept != 2*time.Second {
		t.Fatalf("slept %s, want 2s", slept)
	}
}
