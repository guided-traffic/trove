//go:build e2e

package e2e

// The hold of drain variant B (R-HA-059, R-HA-057, Q-HA-181): the evicted Valkey primary stays lost until the Sentinels name another
// primary. Without it the variant depends on two races of the platform. The Valkey operator's sidecar asks a Sentinel for a failover
// when its SIGTERM finds Valkey still running, and does nothing when the kubelet delivered the signal to Valkey first. Without that
// request the Sentinels fail over after down-after-milliseconds (5 s), but the Sentinel of the drained node is evicted with the
// primary, the two that remain can start the election in the same epoch and abort it with -failover-abort-not-elected, and Sentinel
// starts no further automatic failover before twice failover-timeout (120 s) have passed. The pods have no volume claim, so the
// primary meanwhile comes back under its own name and empty, and the Sentinels never name another one (F-78).
//
// The hold is a finalizer on the primary's pod: the eviction stops the container and sets the deletion timestamp, the pod object
// stays, and the StatefulSet controller creates no pod of that name before the object is gone. The finalizer is removed once a
// Sentinel names another primary. When the Sentinels have not done so holdAssistAfter after the eviction, the test asks a Sentinel
// for the failover (SENTINEL FAILOVER, which Sentinel runs without the 120 s delay), logs every attempt and prints a ::warning::
// line. The cap holdCap ends the hold otherwise. A finalizer a killed test left behind is removed by sweepHeldPods at the start of
// the next run.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/e2e/valkeyhold"
)

const (
	holdFinalizer = "e2e.trove.guided-traffic.com/hold-valkey-primary"
	// holdCap bounds the hold: Sentinel's down-after-milliseconds (5 s) plus the election and the promotion take about 10 s, so a
	// failover that has not happened after this long did not happen because of the Sentinels, not because of the hold. It stays below
	// the 2 minutes past its grace period after which the Valkey operator reports PodTerminationStalled for a Terminating pod.
	holdCap = 90 * time.Second
	// holdCallTimeout bounds every kubectl call of the hold, so a terminating Sentinel cannot stall the cap or the assist
	holdCallTimeout = 15 * time.Second
	// holdReleaseFor is how long the release is retried
	holdReleaseFor = 30 * time.Second
)

func holdKubectl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), holdCallTimeout)
	defer cancel()
	return kubectlCtx(ctx, append([]string{"-n", cfg.namespace}, args...)...)
}

// patchPod applies a merge patch to a pod, for up to retryFor, and reports a benign answer as success.
func patchPod(pod, body string, retryFor time.Duration) error {
	deadline := time.Now().Add(retryFor)
	for {
		out, err := holdKubectl("patch", "pod", pod, "--type=merge", "-p", body)
		if err == nil || valkeyhold.BenignPatchError(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
		}
		time.Sleep(time.Second)
	}
}

// sweepHeldPods removes the hold finalizer from every pod of the namespace: a test that was killed (SIGKILL, a CI cancel, a timeout)
// cannot run its cleanup, and a pod that stays Terminating blocks the StatefulSet. TestMain calls it before the first test.
func sweepHeldPods() {
	out, err := holdKubectl("get", "pods", "-o", "json")
	if err != nil {
		fmt.Printf("e2e: sweep of held Valkey primaries: %v\n", err)
		return
	}
	var l struct {
		Items []struct {
			Metadata struct {
				Name       string   `json:"name"`
				UID        string   `json:"uid"`
				Finalizers []string `json:"finalizers"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(out), &l) != nil {
		return
	}
	for _, it := range l.Items {
		var keep []string
		held := false
		for _, f := range it.Metadata.Finalizers {
			if f == holdFinalizer {
				held = true
			} else {
				keep = append(keep, f)
			}
		}
		if !held {
			continue
		}
		b, _ := json.Marshal(map[string]any{"metadata": map[string]any{"uid": it.Metadata.UID, "finalizers": keep}})
		if err := patchPod(it.Metadata.Name, string(b), holdReleaseFor); err != nil {
			fmt.Printf("e2e: a stale hold on pod %s stays: %v\n", it.Metadata.Name, err)
			continue
		}
		fmt.Printf("e2e: removed a stale hold from pod %s\n", it.Metadata.Name)
	}
}

// holdSentinelPrimary is sentinelPrimary with a bound on every call.
func holdSentinelPrimary() (string, bool) {
	for _, s := range sentinelPods {
		out, err := holdKubectl("exec", s, "-c", "sentinel", "--", "sh", "-c",
			`exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 36379 --no-auth-warning -a "$VALKEY_PASSWORD" sentinel get-master-addr-by-name trove-valkey`)
		host := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		if err == nil && host != "" && host != "(nil)" && !strings.Contains(host, "ERR") {
			return strings.SplitN(host, ".", 2)[0], true
		}
	}
	return "", false
}

var sentinelPods = []string{"trove-valkey-sentinel-0", "trove-valkey-sentinel-1", "trove-valkey-sentinel-2"}

const sentinelCLI = `exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 36379 --no-auth-warning -a "$VALKEY_PASSWORD" `

// failoverInProgress reports whether any reachable Sentinel lists failover_in_progress among the flags of the monitored primary, and
// whether the flags of any Sentinel could be read. SENTINEL FAILOVER answers -INPROG per Sentinel, and one that is not leading would
// accept and start a new epoch.
func failoverInProgress() (inProgress, readable bool) {
	for _, s := range sentinelPods {
		out, err := holdKubectl("exec", s, "-c", "sentinel", "--", "sh", "-c", sentinelCLI+"sentinel master trove-valkey")
		if err != nil {
			continue
		}
		in, found := valkeyhold.ParseFlags(out)
		if found {
			readable = true
		}
		if in {
			return true, true
		}
	}
	return false, readable
}

// sentinelFailoverCommand asks the first Sentinel that answers for a failover. A Sentinel that cannot be reached is skipped; any
// answer from one, OK or not, ends the attempt (-INPROG means a failover is running, and another Sentinel would not know).
func sentinelFailoverCommand() (string, bool) {
	var last string
	for _, s := range sentinelPods {
		out, err := holdKubectl("exec", s, "-c", "sentinel", "--", "sh", "-c", sentinelCLI+"sentinel failover trove-valkey")
		last = strings.TrimSpace(out)
		if err != nil {
			last = strings.TrimSpace(last + " " + err.Error())
			continue
		}
		return last, last == "OK"
	}
	return last, false
}

// primaryHold is the hold of one evicted Valkey primary.
type primaryHold struct {
	pod, uid, before string

	mu         sync.Mutex
	evictedAt  time.Time // when the poll first saw the pod's deletion timestamp
	failoverAt time.Time // when a Sentinel first named another primary
	releasedAt time.Time
	named      string   // the primary the Sentinels named at the release
	capped     bool     // the cap ended the hold, not a failover
	assists    []string // every attempt of SENTINEL FAILOVER with its time and answer
	assister   *valkeyhold.Assister
	err        error
	done       chan struct{}
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// holdValkeyPrimary puts the finalizer on the primary's pod and starts the poll that removes it. The pod must be the one on the
// drained node (the caller checked).
func holdValkeyPrimary(t *testing.T, podName string) *primaryHold {
	t.Helper()
	out, err := holdKubectl("get", "pod", podName, "-o", "jsonpath={.metadata.uid}{\" \"}{.metadata.finalizers}")
	if err != nil {
		t.Fatalf("variant B: reading the Valkey primary's pod %s: %v", podName, err)
	}
	f := strings.Fields(out)
	if len(f) != 1 {
		t.Fatalf("variant B: the Valkey primary's pod %s has finalizers already (%q), the hold would replace them", podName, out)
	}
	h := &primaryHold{pod: podName, uid: f[0], before: podName, done: make(chan struct{}), stopCh: make(chan struct{})}
	h.assister = &valkeyhold.Assister{
		Now:        time.Now,
		InProgress: failoverInProgress,
		Send:       sentinelFailoverCommand,
		Log: func(msg string) {
			h.mu.Lock()
			h.assists = append(h.assists, time.Now().Format("15:04:05.000")+" "+msg)
			h.mu.Unlock()
		},
	}
	if err := patchPod(podName, fmt.Sprintf(`{"metadata":{"uid":%q,"finalizers":[%q]}}`, h.uid, holdFinalizer), holdCallTimeout); err != nil {
		t.Fatalf("variant B: holding the Valkey primary's pod %s: %v", podName, err)
	}
	t.Cleanup(h.stop)
	go h.run()
	return h
}

// release removes the finalizer, retrying for holdReleaseFor.
func (h *primaryHold) release() {
	body := fmt.Sprintf(`{"metadata":{"uid":%q,"finalizers":null}}`, h.uid)
	if err := patchPod(h.pod, body, holdReleaseFor); err != nil {
		h.mu.Lock()
		h.err = fmt.Errorf("releasing the hold on %s failed for %s (the next run's sweep removes it): %w", h.pod, holdReleaseFor, err)
		h.mu.Unlock()
	}
}

func (h *primaryHold) run() {
	defer close(h.done)
	defer func() {
		h.release()
		h.mu.Lock()
		h.releasedAt = time.Now()
		h.mu.Unlock()
	}()
	for {
		select {
		case <-h.stopCh:
			return
		case <-time.After(500 * time.Millisecond):
		}
		now := time.Now()
		h.mu.Lock()
		evicted := h.evictedAt
		h.mu.Unlock()
		if evicted.IsZero() {
			out, err := holdKubectl("get", "pod", h.pod, "-o", "jsonpath={.metadata.uid} {.metadata.deletionTimestamp}")
			if err != nil {
				continue
			}
			if f := strings.Fields(out); len(f) == 2 && f[0] == h.uid {
				h.mu.Lock()
				h.evictedAt = now
				h.mu.Unlock()
			}
			continue
		}
		if name, ok := holdSentinelPrimary(); ok && name != h.before {
			h.mu.Lock()
			h.failoverAt, h.named = time.Now(), name
			h.mu.Unlock()
			return
		}
		h.assister.Step(evicted)
		if now.Sub(evicted) > holdCap {
			h.mu.Lock()
			h.capped = true
			h.mu.Unlock()
			return
		}
	}
}

// stop ends the hold at once (the test ended before the failover): the finalizer is removed so no pod stays Terminating.
func (h *primaryHold) stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
	<-h.done
}

// verify logs the hold's timings and reports its own failures. It returns true when it reported that no failover happened, so the
// caller does not report the same fact a second time.
func (h *primaryHold) verify(t *testing.T) bool {
	t.Helper()
	h.stop()
	h.mu.Lock()
	defer h.mu.Unlock()
	stamp := func(x time.Time) string {
		if x.IsZero() {
			return "never"
		}
		return x.Format("15:04:05.000")
	}
	after := "never"
	if !h.failoverAt.IsZero() && !h.evictedAt.IsZero() {
		after = h.failoverAt.Sub(h.evictedAt).Round(100*time.Millisecond).String() + " after"
	}
	t.Logf("variant B: the Valkey primary %s was held in Terminating: the eviction was seen at %s, a Sentinel named %q at %s (%s), the hold ended at %s",
		h.pod, stamp(h.evictedAt), h.named, stamp(h.failoverAt), after, stamp(h.releasedAt))
	for _, a := range h.assists {
		t.Logf("variant B: assist: %s", a)
	}
	if acc := len(h.assister.Accepted); acc > 0 {
		msg := fmt.Sprintf("variant B: the Sentinels had not failed over by themselves %s after the eviction of %s; SENTINEL FAILOVER was sent %d time(s) (split vote or lockout of Sentinel, Q-HA-181; the Sentinel events below show which)",
			valkeyhold.AssistAfter, h.pod, acc)
		t.Log(msg)
		fmt.Printf("\n::warning::%s\n", msg)
		appendStepSummary(msg)
	}
	if h.err != nil {
		t.Errorf("variant B: %v", h.err)
	}
	if h.evictedAt.IsZero() {
		t.Errorf("variant B: the Valkey primary's pod %s was never evicted", h.pod)
	}
	if h.capped {
		t.Errorf("variant B caused no failover: the Sentinels still name the evicted primary %s %s after its eviction, with %d SENTINEL FAILOVER attempt(s) accepted", h.pod, holdCap, len(h.assister.Accepted))
		return true
	}
	return false
}

var sentinelEventRE = regexp.MustCompile(`\+(sdown|odown|try-failover|elected-leader|selected-slave|promoted-slave|switch-master)|-sdown|-odown|-failover-abort|failover delay`)

// logSentinelEvents logs the failover events of the Sentinels that are running now, since the drain started (the Sentinel of the
// drained node was replaced and its log with it). It is evidence, not a condition.
func logSentinelEvents(t *testing.T, since time.Time) {
	t.Helper()
	for _, s := range sentinelPods {
		out, err := holdKubectl("logs", "--timestamps", "--since-time="+since.UTC().Format(time.RFC3339), "-c", "sentinel", s)
		if err != nil {
			t.Logf("variant B: the log of %s: %v", s, err)
			continue
		}
		var ev []string
		for _, l := range strings.Split(out, "\n") {
			if sentinelEventRE.MatchString(l) {
				ev = append(ev, strings.TrimSpace(l))
			}
		}
		b, _ := json.Marshal(ev)
		t.Logf("variant B: failover events of %s since the drain started: %s", s, b)
	}
}

// appendStepSummary adds a line to the job summary of GitHub Actions when the run has one, so the nightly summary shows that the
// assist fired (the ::warning:: line is an annotation only).
func appendStepSummary(msg string) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Printf("e2e: job summary: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "- %s\n", msg)
}
