//go:build e2e

package e2e

// The endpoint-removal time of the edge under load (R-HA-059 rule 4, Q-HA-110). A replica fails readiness at SIGTERM and
// keeps serving for health.shutdown_drain_delay; the delay must exceed the time the edge needs to stop sending requests
// to it. The harness measures that time for every evicted api and packages pod from the pod's own log: the line
// "shutting down" is the readiness flip (Shutdown sets the drain state right after it), the "request completed" lines of
// the client listeners are the requests the edge sent, each stamped at its completion with the duration, so the time it
// arrived is the stamp minus the duration. Both come from the same pod and the same clock, so nothing has to be aligned
// between the pod, the node and the edge, and the measurement needs no access log of the edge (which names the backend
// server by an index, not by the pod).
//
// The last request that arrived after the flip is the edge's endpoint-removal time seen from the pod. A pod that got no
// request after the flip, or whose stream missed the flip, yields no sample for the maximum and is no failure; only a run
// that measured nothing at all is one, because then the rule was not tested.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// drainSilence is the quiet time the edge must leave before the listener closes: the last request after the flip has to
// arrive at least this long before the delay ends. A delay that is too short shows up as requests until its very end (the
// listener accepts until then), so "the last request is before the delay" alone could not fail.
const drainSilence = time.Second

// podLogStream is `kubectl logs -f` of one pod into a file, so that the log outlives the pod.
type podLogStream struct {
	Pod  string
	File string
	cmd  *exec.Cmd
	f    *os.File
	done chan struct{}
	once sync.Once
}

// streamPodLogs starts a stream per pod. The pods' last ten minutes are read first, so the stream holds the load before
// the eviction as well; a stream ends by itself when its container does.
func streamPodLogs(t testing.TB, prefix string, pods []pod) []*podLogStream {
	var out []*podLogStream
	for _, p := range pods {
		file := filepath.Join(cfg.out, prefix+p.Name+".log")
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		s := &podLogStream{Pod: p.Name, File: file, f: f, done: make(chan struct{})}
		s.cmd = exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "logs", "-f", "--since=10m", p.Name)
		s.cmd.Stdout, s.cmd.Stderr = f, f
		if err := s.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = s.cmd.Wait(); close(s.done) }()
		t.Cleanup(func() { s.finish(0) })
		out = append(out, s)
	}
	return out
}

// finish waits up to wait for the stream to end by itself, then stops it and closes the file.
func (s *podLogStream) finish(wait time.Duration) {
	s.once.Do(func() {
		select {
		case <-s.done:
		case <-time.After(wait):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
		_ = s.f.Close()
	})
}

// drainSample is the measurement of one evicted pod.
type drainSample struct {
	Pod  string `json:"pod"`
	Role string `json:"role"`
	// FlippedAt is the pod's "shutting down" line: the readiness flip.
	FlippedAt time.Time `json:"flipped_at"`
	// DelayMS is the drain delay the pod logged ("draining"); 0 when it logged none.
	DelayMS int64 `json:"delay_ms"`
	// Requests is the number of requests of the client listeners that arrived after the flip.
	Requests int `json:"requests_after_flip"`
	// LastAfterFlipMS is the arrival of the last of them, from the flip: the endpoint-removal time seen from the pod.
	LastAfterFlipMS int64 `json:"last_request_after_flip_ms"`
	// Buckets counts the arrivals after the flip per half second, "0.0" being the first half second.
	Buckets map[string]int `json:"requests_per_half_second"`
}

// readDrainSample reads one pod's log. ok is false when the log holds no flip or the pod serves no client requests
// (a worker): there is nothing to measure.
func readDrainSample(file string) (s drainSample, ok bool, err error) {
	f, err := os.Open(file)
	if err != nil {
		return s, false, err
	}
	defer f.Close()
	var arrivals []time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, `"msg":"shutting down"`):
			var r struct {
				Time time.Time `json:"time"`
				Role string    `json:"role"`
				Pod  string    `json:"replica"`
			}
			if json.Unmarshal([]byte(line), &r) == nil && s.FlippedAt.IsZero() {
				s.FlippedAt, s.Role, s.Pod = r.Time, r.Role, r.Pod
			}
		case strings.Contains(line, `"msg":"draining"`):
			var r struct {
				Delay string `json:"delay"`
			}
			if json.Unmarshal([]byte(line), &r) == nil {
				if d, perr := time.ParseDuration(r.Delay); perr == nil {
					s.DelayMS = d.Milliseconds()
				}
			}
		case strings.Contains(line, `"log":"request"`):
			var r struct {
				Time     time.Time `json:"time"`
				Listener string    `json:"listener"`
				Duration float64   `json:"duration_ms"`
			}
			if json.Unmarshal([]byte(line), &r) != nil || r.Listener == "ops" {
				continue
			}
			arrivals = append(arrivals, r.Time.Add(-time.Duration(r.Duration*float64(time.Millisecond))))
		}
	}
	if err := sc.Err(); err != nil {
		return s, false, err
	}
	if s.FlippedAt.IsZero() {
		return s, false, nil
	}
	s.Buckets = map[string]int{}
	var last time.Duration
	for _, a := range arrivals {
		d := a.Sub(s.FlippedAt)
		if d < 0 {
			continue
		}
		s.Requests++
		s.Buckets[fmt.Sprintf("%.1f", float64(d/(500*time.Millisecond))/2)]++
		last = max(last, d)
	}
	s.LastAfterFlipMS = last.Milliseconds()
	return s, s.Requests > 0, nil
}

// checkEndpointRemoval ends the streams, measures every pod that served requests, writes the samples to
// <name>-endpoint-removal.json and the test log, and fails when a configured delay does not exceed the longest measured
// time by drainSilence. It returns the longest time measured and the delay it was measured against (0 and 0 without a sample).
func checkEndpointRemoval(t testing.TB, name string, streams []*podLogStream) (time.Duration, time.Duration) {
	t.Helper()
	var samples []drainSample
	var skipped []string
	for _, s := range streams {
		s.finish(30 * time.Second)
		smp, ok, err := readDrainSample(s.File)
		if err != nil {
			t.Errorf("reading the log of %s: %v", s.Pod, err)
			continue
		}
		if !ok {
			skipped = append(skipped, s.Pod)
			continue
		}
		samples = append(samples, smp)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Pod < samples[j].Pod })
	var longest, delay time.Duration
	var worst string
	failed := false
	for _, s := range samples {
		last, d := time.Duration(s.LastAfterFlipMS)*time.Millisecond, time.Duration(s.DelayMS)*time.Millisecond
		t.Logf("endpoint removal of %s (%s): the last of %d requests after the readiness flip arrived %s after it, drain delay %s", s.Pod, s.Role, s.Requests, last, d)
		if last > longest {
			longest, delay, worst = last, d, s.Pod
		}
		if last+drainSilence > d {
			failed = true
		}
	}
	if len(skipped) > 0 {
		t.Logf("no request after the readiness flip, or no flip in the log, for %v: nothing to measure there", skipped)
	}
	noteResult(t, name+"-endpoint-removal", map[string]any{
		"samples": samples, "skipped": skipped, "longest_ms": longest.Milliseconds(), "longest_pod": worst,
		"required_silence_ms": drainSilence.Milliseconds(),
	})
	if len(samples) == 0 {
		t.Errorf("R-HA-059 rule 4: no drained pod showed requests after its readiness flip in %d logs, so the endpoint-removal time was not measured", len(streams))
		return 0, 0
	}
	if failed {
		t.Errorf("R-HA-059 rule 4: health.shutdown_drain_delay (%s) does not exceed the edge's endpoint-removal time (%s, pod %s) by the required %s of silence", delay, longest, worst, drainSilence)
	}
	return longest, delay
}
