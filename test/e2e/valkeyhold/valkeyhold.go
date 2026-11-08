// Package valkeyhold holds the cluster-free logic of the hold of drain variant B (R-HA-059, R-HA-057, Q-HA-181): which answers of
// kubectl end the release of the hold, how the failover flag of a Sentinel is read, and when the test asks the Sentinels for a
// failover. The calls to the cluster are injected, so the rules run without one (test/e2e/drain_variants_hold_test.go wires them).
package valkeyhold

import (
	"strings"
	"time"
)

const (
	// AssistAfter is how long after the eviction the Sentinels get to fail over by themselves: later than the 5 s
	// down-after-milliseconds, the 1 s of random delay before a Sentinel starts and the 10 s of an election by 10 s, so a failover
	// that is still running is not interrupted.
	AssistAfter = 25 * time.Second
	// AssistRetry is the election timeout: an accepted SENTINEL FAILOVER that has not ended by then was aborted.
	AssistRetry = 10 * time.Second
	// AssistEvery spaces the attempts of every kind, a refusal included.
	AssistEvery = 3 * time.Second
	// MaxAssists is the number of accepted commands after which the test sends no more.
	MaxAssists = 2
)

// BenignPatchError reports an answer of `kubectl patch` that means the finalizer is gone for good: the pod is gone, or it is a pod
// of another UID.
func BenignPatchError(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "NotFound") || strings.Contains(m, "not found") || strings.Contains(m, "Precondition failed")
}

// ParseFlags reads the output of `valkey-cli sentinel master <name>` without a terminal, which prints a key and its value on
// consecutive lines. found is false when there is no line `flags` followed by a value (malformed or empty output).
func ParseFlags(out string) (inProgress, found bool) {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "flags" && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			return strings.Contains(lines[i+1], "failover_in_progress"), true
		}
	}
	return false, false
}

// Assister decides when to send SENTINEL FAILOVER. Step is called on every tick of the hold; the calls to the cluster are InProgress
// and Send, so a test can drive it with a fake clock. It is used by one goroutine.
type Assister struct {
	Now func() time.Time
	// InProgress reports whether a reachable Sentinel lists a failover in progress, and whether any Sentinel could be read.
	InProgress func() (inProgress, readable bool)
	// Send asks a Sentinel for the failover and returns its answer and whether it was accepted.
	Send func() (answer string, accepted bool)
	Log  func(string)

	Accepted []time.Time
	Attempts int
	last     time.Time
}

// Step sends the command when it is due. Nothing is sent before AssistAfter has passed since the eviction, within AssistEvery of the
// last attempt (accepted or not), within AssistRetry of an accepted command, after MaxAssists accepted ones, while a failover is in
// progress, or when no Sentinel could be read.
func (a *Assister) Step(evicted time.Time) {
	now := a.Now()
	n := len(a.Accepted)
	switch {
	case n >= MaxAssists:
		return
	case n == 0 && now.Sub(evicted) < AssistAfter:
		return
	case n > 0 && now.Sub(a.Accepted[n-1]) < AssistRetry:
		return
	case !a.last.IsZero() && now.Sub(a.last) < AssistEvery:
		return
	}
	a.last = now
	a.Attempts++
	inProgress, readable := a.InProgress()
	switch {
	case !readable:
		a.Log("could not read the flags of the monitored primary from any Sentinel: no command sent")
		return
	case inProgress:
		a.Log("a Sentinel reports failover_in_progress: no command sent")
		return
	}
	answer, ok := a.Send()
	if !ok {
		a.Log("SENTINEL FAILOVER refused: " + answer)
		return
	}
	a.Accepted = append(a.Accepted, now)
	a.Log("SENTINEL FAILOVER accepted")
}
