// Package rotationhold waits out the break-glass rotation hold for the e2e suites (R-AUTH-014, Q-AUTH-14).
//
// For login.break_glass.rotation_hold (2 minutes) after the replicas noticed a new break-glass password, the local
// login answers 503 break_glass_rotating with a Retry-After. A long-lived dev cluster meets that window whenever the
// internal-secrets-operator rotates the Secret (every 24 h, Q-AUTH-194); a fresh cluster never does. The answer is a
// designed one, so a sign-in that gets it waits for the Retry-After and asks again. Nothing else is retried: any
// other status, and any other 503 (server_busy, a down replica), is returned to the caller as it is.
package rotationhold

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Code is the problem code of the hold (errs.CodeBreakGlassRotating).
const Code = "break_glass_rotating"

// MaxWait caps the total wait of one sign-in: the hold is 2 minutes, a minute more covers the 10 s the replicas
// need to notice the file and a Retry-After that rounds up.
const MaxWait = 3 * time.Minute

// Answer is what the caller's request got.
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
}

// InHold reports whether the answer is the hold: 503 with the problem code break_glass_rotating.
func (a Answer) InHold() bool {
	if a.Status != http.StatusServiceUnavailable {
		return false
	}
	var p struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(a.Body, &p) == nil && p.Code == Code
}

// RetryAfter is the Retry-After header in seconds, 1 when it is absent or unusable.
func (a Answer) RetryAfter() time.Duration {
	if s, err := strconv.Atoi(a.Header.Get("Retry-After")); err == nil && s >= 1 {
		return time.Duration(s) * time.Second
	}
	return time.Second
}

// Wait is the shared helper: it calls do, and while the answer is the hold it sleeps the Retry-After (plus one
// second for the replica that answers the next request) and calls again. The total sleep is capped at maxWait; a hold
// that lasts longer returns an error. logf says what is waited for; sleep is time.Sleep outside tests.
func Wait(do func() Answer, maxWait time.Duration, sleep func(time.Duration), logf func(string, ...any)) (Answer, error) {
	var waited time.Duration
	for {
		a := do()
		if !a.InHold() {
			return a, nil
		}
		d := a.RetryAfter() + time.Second
		if waited+d > maxWait {
			return a, fmt.Errorf("the break-glass login is still in its rotation hold (503 %s) after waiting %s, limit %s", Code, waited, maxWait)
		}
		if logf != nil {
			logf("break-glass rotation hold (R-AUTH-014): waiting %s for the Retry-After, %s waited so far", d, waited)
		}
		sleep(d)
		waited += d
	}
}
