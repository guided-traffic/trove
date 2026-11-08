//go:build e2e

package e2e

// The readings of the clients-first npm subtest (R-REPO-055): what the database, the standby and the runner were doing while the
// installs ran, and what the test was doing then, as JSON lines in the artifact directory. They are instruments: a failure of one is
// logged and never fails the test.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	readingsPG       = "clients-first-npm-pg.jsonl"
	readingsPressure = "clients-first-npm-pressure.jsonl"
	readingsNodes    = "clients-first-npm-nodes.jsonl"
	readingsEvents   = "clients-first-npm-events.jsonl"
	readingsLogs     = "clients-first-npm-cluster-logs"

	// pgSampleInterval is the PostgreSQL sampler's period in seconds and pgSampleCount the number of samples one session takes at most
	// (\watch ends then, a safety net against a session that outlives its test); pgSamplerRestarts is the number of times the session is
	// started again when its exec ends (a change of the primary), after the first start. pgSamplerApp is its application_name, by which
	// leftover sessions are found.
	pgSampleInterval  = "0.25"
	pgSampleCount     = "14400"
	pgSamplerRestarts = 5
	pgSamplerApp      = "trove-e2e-readings"
)

// readings holds the samplers and the event log of one subtest.
type readings struct {
	t    *testing.T
	stop sync.Once
	end  context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	events *os.File
}

// startReadings starts the PostgreSQL sampler, the pressure sampler and the nodes' cgroup sampler and opens the event log. stop is registered as a cleanup too.
func startReadings(t *testing.T) *readings {
	ctx, cancel := context.WithCancel(context.Background())
	r := &readings{t: t, end: cancel}
	if f, err := os.OpenFile(filepath.Join(cfg.out, readingsEvents), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err != nil {
		t.Logf("readings: the event log is not written: %v", err)
	} else {
		r.events = f
	}
	r.wg.Add(3)
	go func() { defer r.wg.Done(); r.samplePG(ctx) }()
	go func() { defer r.wg.Done(); r.samplePressure(ctx) }()
	go func() { defer r.wg.Done(); r.sampleNodes(ctx) }()
	t.Cleanup(r.close)
	return r
}

// close stops the samplers and closes the event log; it is idempotent.
func (r *readings) close() {
	r.stop.Do(func() {
		r.end()
		r.wg.Wait()
		// the sessions of the sampler end with their exec; one that outlived it (the stream cut before psql noticed) is ended here
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		terminateSamplerSessions(ctx)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.events != nil {
			_ = r.events.Close()
			r.events = nil
		}
	})
}

// event writes one line with the wall time to the event log and to the test log. pair is the index of the pair, -1 for an event of
// no pair.
func (r *readings) event(pair int, name string, fields map[string]any) {
	r.t.Helper()
	line := map[string]any{"at": stamp(time.Now()), "pair": pair, "event": name}
	for k, v := range fields {
		line[k] = v
	}
	raw, _ := json.Marshal(line)
	r.t.Logf("event %s", raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		_, _ = r.events.Write(append(raw, '\n'))
	}
}

// stamp is RFC 3339 in UTC with milliseconds, the format every record of the readings uses.
func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000Z") }

// pgSettingsQuery is the first line of the PostgreSQL file: the settings the other lines are read against.
const pgSettingsQuery = `SELECT jsonb_build_object('kind', 'settings', 'at', to_char(clock_timestamp() AT TIME ZONE 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
  'log_min_duration_statement', coalesce((SELECT split_part(c, '=', 2) FROM pg_db_role_setting s, unnest(s.setconfig) c
      WHERE s.setdatabase = 0 AND s.setrole = 'trove'::regrole AND c LIKE 'log_min_duration_statement=%'), current_setting('log_min_duration_statement')), 'log_parameter_max_length', current_setting('log_parameter_max_length'),
  'track_wal_io_timing', current_setting('track_wal_io_timing'), 'synchronous_standby_names', current_setting('synchronous_standby_names'),
  'server_version', current_setting('server_version'));
`

// pgSampleQuery is one sample, as one line of JSON (jsonb prints no line break): the sessions that do something, counted by what they
// wait for (the sampler's own session is left out, and so is a process that waits for work: wait event type Activity), the standby's lag, the WAL counters, the rows of pg_stat_io that wrote, evicted or
// synced (the WAL rows among them, with their write and fsync times) and the checkpointer.
const pgSampleQuery = `SELECT jsonb_build_object(
  'kind', 'sample', 'at', to_char(clock_timestamp() AT TIME ZONE 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
  'activity', (SELECT coalesce(jsonb_agg(a), '[]'::jsonb) FROM (
      SELECT backend_type, state, wait_event_type, wait_event, left(query, 48) AS query, count(*) AS n
      FROM pg_stat_activity
      WHERE pid <> pg_backend_pid() AND state IS DISTINCT FROM 'idle' AND wait_event_type IS DISTINCT FROM 'Activity' AND (backend_type <> 'client backend' OR datname = 'trove')
      GROUP BY 1, 2, 3, 4, 5) a),
  'replication', (SELECT coalesce(jsonb_agg(r), '[]'::jsonb) FROM (
      SELECT application_name, sync_state,
        round(extract(epoch FROM write_lag) * 1000) AS write_lag_ms, round(extract(epoch FROM flush_lag) * 1000) AS flush_lag_ms,
        round(extract(epoch FROM replay_lag) * 1000) AS replay_lag_ms, pg_wal_lsn_diff(pg_current_wal_lsn(), flush_lsn) AS flush_behind_bytes
      FROM pg_stat_replication) r),
  'wal', (SELECT to_jsonb(w) FROM (SELECT wal_records, wal_fpi, wal_bytes, wal_buffers_full FROM pg_stat_wal) w),
  'io', (SELECT coalesce(jsonb_agg(i), '[]'::jsonb) FROM (
      SELECT backend_type, object, context, writes, round(write_time::numeric, 1) AS write_time_ms, fsyncs, round(fsync_time::numeric, 1) AS fsync_time_ms, evictions
      FROM pg_stat_io
      WHERE coalesce(writes, 0) > 0 OR coalesce(evictions, 0) > 0 OR coalesce(fsyncs, 0) > 0) i),
  'checkpointer', (SELECT to_jsonb(c) FROM (SELECT num_timed, num_requested, write_time, sync_time, buffers_written FROM pg_stat_checkpointer) c));
\watch i=` + pgSampleInterval + ` c=` + pgSampleCount + `
`

// terminateSamplerSessions ends the sessions of the PostgreSQL sampler by their application_name, with one exec on the primary. A failure
// is ignored: there is nothing to end then, or no primary to ask.
func terminateSamplerSessions(ctx context.Context) {
	primary, err := currentPrimary(ctx)
	if err != nil {
		return
	}
	_, _ = kubectlCtx(ctx, "-n", cfg.namespace, "exec", primary, "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-c",
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = '"+pgSamplerApp+"' AND pid <> pg_backend_pid()")
}

func currentPrimary(ctx context.Context) (string, error) {
	out, err := kubectlCtx(ctx, "-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
	if primary := strings.TrimSpace(out); err == nil && primary != "" {
		return primary, nil
	} else if err == nil {
		err = fmt.Errorf("the cluster names no primary")
	}
	return "", err
}

// pause waits for d or the end of ctx and reports whether ctx is still alive.
func pause(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// samplePG keeps one psql session on the primary (application_name pgSamplerApp) that runs pgSampleQuery every 250 ms and writes each
// line to the PostgreSQL file. Sessions of an earlier run are ended first. When the exec ends before the sampler is stopped (the
// primary changed), the session is started again at most pgSamplerRestarts times.
func (r *readings) samplePG(ctx context.Context) {
	f, err := os.OpenFile(filepath.Join(cfg.out, readingsPG), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.t.Logf("readings: the PostgreSQL samples are not written: %v", err)
		return
	}
	defer f.Close()
	terminateSamplerSessions(ctx)
	failures := 0
	for starts := 0; starts <= pgSamplerRestarts && failures < 30 && ctx.Err() == nil; {
		primary, err := currentPrimary(ctx)
		if err != nil {
			failures++
			if ctx.Err() == nil {
				r.t.Logf("readings: no PostgreSQL primary for the sampler: %v", err)
			}
			pause(ctx, 2*time.Second)
			continue
		}
		starts++
		cmd := exec.CommandContext(ctx, "kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", primary, "-c", "postgres", "--",
			"env", "PGAPPNAME="+pgSamplerApp, "psql", "-U", "postgres", "-d", "trove", "-At")
		cmd.Stdin = strings.NewReader(pgSettingsQuery + pgSampleQuery)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			r.t.Logf("readings: the PostgreSQL sampler: %v", err)
			return
		}
		if err := cmd.Start(); err != nil {
			r.t.Logf("readings: the PostgreSQL sampler does not start: %v", err)
			return
		}
		lines := 0
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			// psql prints a title line above each result of \watch unless it is told not to; only the JSON lines are samples
			if line := sc.Bytes(); len(line) > 0 && line[0] == '{' {
				_, _ = f.Write(append(append([]byte(nil), line...), '\n'))
				lines++
			}
		}
		_ = cmd.Wait()
		if ctx.Err() == nil {
			r.t.Logf("readings: the PostgreSQL sampler on %s ended after %d lines (start %d of at most %d): %s", primary, lines, starts, pgSamplerRestarts+1, strings.TrimSpace(stderr.String()))
			pause(ctx, time.Second)
		}
	}
}

// nodesScript is the helper that reads the cgroup which holds the kind nodes: the root of the cgroup tree as the job's Docker daemon
// sees it (cgroup namespace of the host). It prints one block per second after a marker line, which the test stamps on arrival (the date of the helper image has no sub-second field).
// On the large runner the nodes run inside the job's Docker-in-Docker container, whose cgroup is not the test process's.
const nodesScript = `i=0; while [ $i -lt 3600 ]; do echo @; for f in cpu.stat cpu.pressure io.pressure; do echo "# $f"; cat /sys/fs/cgroup/$f 2>/dev/null; done; sleep 1; i=$((i+1)); done`

// sampleNodes runs the helper container through the Docker daemon the kind nodes run in and writes one JSON line per block to the nodes
// file: the cgroup's CPU time and throttling, and its CPU and IO pressure. A file the cgroup does not have is recorded once as absent. The
// container has a fixed name per cluster, which the next start removes first (docker rm -f), and is removed again when the readings stop;
// a run that is killed leaves it, and its loop ends by itself after an hour (3600 blocks).
func (r *readings) sampleNodes(ctx context.Context) {
	image := os.Getenv("TROVE_E2E_TOOLS_IMAGE")
	if image == "" {
		r.t.Logf("readings: the nodes' cgroup is not sampled: TROVE_E2E_TOOLS_IMAGE is not set; run the suite through deploy/dev/e2e.sh")
		return
	}
	f, err := os.OpenFile(filepath.Join(cfg.out, readingsNodes), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.t.Logf("readings: the nodes' cgroup samples are not written: %v", err)
		return
	}
	defer f.Close()
	name := "trove-e2e-readings-nodes-" + strings.TrimPrefix(cfg.kubeContext, "kind-")
	remove := func() { _ = exec.Command("docker", "rm", "-f", name).Run() }
	remove()
	defer remove()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "--cgroupns=host", "-v", "/sys/fs/cgroup:/sys/fs/cgroup:ro", image, "sh", "-c", nodesScript)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		r.t.Logf("readings: the nodes' cgroup sampler: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		r.t.Logf("readings: the nodes' cgroup sampler does not start: %v", err)
		return
	}
	var (
		section string
		block   map[string]any
		body    strings.Builder
		seen    = map[string]bool{}
	)
	flushSection := func() {
		if block == nil || section == "" {
			return
		}
		text := body.String()
		body.Reset()
		if strings.TrimSpace(text) == "" {
			if !seen[section] {
				seen[section] = true
				raw, _ := json.Marshal(map[string]any{"at": block["at"], "kind": "absent", "file": section})
				_, _ = f.Write(append(raw, '\n'))
			}
			return
		}
		if section == "cpu.stat" {
			block[section] = parseCPUStat(text)
		} else {
			block[section] = parsePressure(text)
		}
	}
	flushBlock := func() {
		flushSection()
		if block != nil {
			raw, _ := json.Marshal(block)
			_, _ = f.Write(append(raw, '\n'))
		}
		block, section = nil, ""
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "@":
			flushBlock()
			block = map[string]any{"at": stamp(time.Now()), "kind": "sample"}
		case strings.HasPrefix(line, "# "):
			flushSection()
			section = strings.TrimPrefix(line, "# ")
		default:
			body.WriteString(line + "\n")
		}
	}
	flushBlock()
	_ = cmd.Wait()
	if ctx.Err() == nil {
		r.t.Logf("readings: the nodes' cgroup sampler ended: %s", strings.TrimSpace(stderr.String()))
	}
}

// pressureFiles are read every second from the runner; a file that does not exist is recorded once as absent.
var pressureFiles = []string{"/proc/pressure/cpu", "/proc/pressure/io", "/proc/pressure/memory"}

// samplePressure writes, once a second, the pressure of the runner (the test process, the kind nodes and the clients share it), the
// CPU use and throttling of the test process's own cgroup and the load average.
func (r *readings) samplePressure(ctx context.Context) {
	f, err := os.OpenFile(filepath.Join(cfg.out, readingsPressure), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.t.Logf("readings: the pressure samples are not written: %v", err)
		return
	}
	defer f.Close()
	cgroup := cgroupDir()
	absent := map[string]bool{}
	note := func(path string) {
		if absent[path] {
			return
		}
		absent[path] = true
		raw, _ := json.Marshal(map[string]any{"at": stamp(time.Now()), "kind": "absent", "file": path})
		_, _ = f.Write(append(raw, '\n'))
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		line := map[string]any{"at": stamp(time.Now()), "kind": "sample"}
		for _, p := range pressureFiles {
			raw, err := os.ReadFile(p)
			if err != nil {
				note(p)
				continue
			}
			line[filepath.Base(p)] = parsePressure(string(raw))
		}
		if cgroup == "" {
			note("/proc/self/cgroup")
		} else {
			if raw, err := os.ReadFile(filepath.Join(cgroup, "cpu.stat")); err != nil {
				note(filepath.Join(cgroup, "cpu.stat"))
			} else {
				line["cgroup_cpu_stat"] = parseCPUStat(string(raw))
			}
			if raw, err := os.ReadFile(filepath.Join(cgroup, "cpu.pressure")); err != nil {
				note(filepath.Join(cgroup, "cpu.pressure"))
			} else {
				line["cgroup_cpu_pressure"] = parsePressure(string(raw))
			}
		}
		if raw, err := os.ReadFile("/proc/loadavg"); err != nil {
			note("/proc/loadavg")
		} else {
			line["loadavg"] = strings.TrimSpace(string(raw))
		}
		raw, _ := json.Marshal(line)
		_, _ = f.Write(append(raw, '\n'))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// cgroupDir is the cgroup (version 2) directory of the test process, or "" when /proc/self/cgroup names none.
func cgroupDir() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(l, "0::"); ok {
			return filepath.Join("/sys/fs/cgroup", rest)
		}
	}
	return ""
}

// parsePressure reads the lines `some avg10=0.00 avg60=0.00 avg300=0.00 total=0` and `full ...` of a pressure file into
// {"some": {"avg10": 0, "total": 0}, "full": {...}}.
func parsePressure(s string) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || (f[0] != "some" && f[0] != "full") {
			continue
		}
		m := map[string]float64{}
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || (k != "avg10" && k != "total") {
				continue
			}
			m[k], _ = strconv.ParseFloat(v, 64)
		}
		out[f[0]] = m
	}
	return out
}

// parseCPUStat keeps the CPU time and the throttling of a cgroup's cpu.stat.
func parseCPUStat(s string) map[string]int64 {
	out := map[string]int64{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "usage_usec", "nr_throttled", "throttled_usec":
			out[f[0]], _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return out
}

// captureTimeout bounds deploy/dev/cluster-logs.sh; captureGrace is how long its output pipes are waited for after the kill.
const (
	captureTimeout = 5 * time.Minute
	captureGrace   = 10 * time.Second
)

// captureClusterLogs keeps the cluster's logs in the artifact directory, with deploy/dev/cluster-logs.sh, while the profile's processes
// (the pool that ran the pass) still exist. It never fails or hangs the test: a failure or a timeout is logged.
func captureClusterLogs(t *testing.T) {
	t.Helper()
	dir := filepath.Join(cfg.out, readingsLogs)
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(cfg.repo, "deploy/dev/cluster-logs.sh"), dir)
	cmd.Dir = cfg.repo
	cmd.Env = append(os.Environ(), "TROVE_DEV_CLUSTER="+strings.TrimPrefix(cfg.kubeContext, "kind-"))
	cmd.WaitDelay = captureGrace // the script's kubectl children keep its pipe open after the kill
	start := time.Now()
	out, err := cmd.CombinedOutput()
	switch {
	case ctx.Err() != nil:
		t.Logf("readings: the cluster logs were not finished after %s (what was written is in %s): %v", captureTimeout, dir, ctx.Err())
	case err != nil:
		t.Logf("readings: the cluster logs are not kept: %v: %s", err, strings.TrimSpace(string(out)))
	default:
		t.Logf("readings: the cluster logs of a slow install are in %s (%s)", dir, time.Since(start).Round(time.Second))
	}
}
