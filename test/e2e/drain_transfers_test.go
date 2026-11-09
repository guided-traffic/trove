//go:build e2e

package e2e

// The long-transfer probes of R-HA-059's load table (E5.9): "curl --limit-rate 2M --retry 5 --retry-all-errors -C - -o big.bin
// .../files/web/generic-local/1gib.bin (about 512 s, longer than the packages grace period), 2 parallel loops", and a large upload
// that the client repeats when it is cut (rule 6: "downloads resume with a Range request; uploads are repeated by the client").
//
// Pass conditions (R-HA-059): a probe is cut at most once per evicted packages pod that served it, completes through resume, and its
// SHA-256 matches; a cut on a pod that was not evicted fails the test.
//
// The cut is a property of the pod that serves the transfer when it is told to stop: it keeps serving for the drain delay and then
// for server.shutdown_timeout (110 s), then resets the connection (R-HA-011, Q-ARCH-187). A probe that no evicted pod serves is cut by
// nothing, and the test would pass without the path having run. Download probe 0 therefore starts again until the packages pod of the
// drained node answers it (X-Trove-Replica), so that it is in flight on the evicted pod when the drain starts: its cut is a
// precondition the test proves (exactly one cut, on that pod). Probe 1 takes whichever replica the edge gives it.
//
// Both download probes start with the load and take about 512 s; the drain is the third to fifth minute of the load and the test
// waits for the probes to finish after the load stops. The upload probes start right before the fault and last 200 s at 2 MiB/s, so
// the evicted pod's cut (about 115 s after the fault) falls into them; an upload is repeated from the start after a cut.

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	dlProbeBytes = 1 << 30   // 1gib.bin: 512 s at 2 MiB/s
	ulProbeBytes = 400 << 20 // 200 s at 2 MiB/s
	dlProbes     = 2
	ulProbes     = 2
)

var (
	transferSeedOnce sync.Once
	transferSeedErr  error
)

// longTransfers are the probes of one drain.
type longTransfers struct {
	auth, adminAuth, base string
	dlFile, dlSum         string
	ulFile, ulSum         string
	run                   string
	pin                   string // the packages pod of the drained node

	downs []*dlProbe
	ups   []*ulProbe
}

func newLongTransfers() *longTransfers { return &longTransfers{} }

func (x *longTransfers) prepare(t *testing.T) {
	t.Helper()
	x.base = cfg.edgeURL + filesBase + "generic-local/"
	x.auth = "Authorization: Bearer " + transferToken(t)
	x.adminAuth = "Authorization: Bearer " + adminToken(t)
	x.run = bakRun()
	x.dlFile, x.dlSum = dataFile(t, "xfer-1gib.bin", dlProbeBytes)
	x.ulFile, x.ulSum = dataFile(t, "xfer-400mib.bin", ulProbeBytes)
	// the source of the downloads is put once per process, at full speed, before any load runs
	transferSeedOnce.Do(func() {
		c := startCurl(t, "-T", x.dlFile, "-H", x.adminAuth, "-o", "/dev/null", "-w", "%{http_code}", x.base+"drain-load/1gib.bin")
		c.wait(t, 15*time.Minute)
		if code := strings.TrimSpace(c.out.String()); c.exit != 0 || (code != "201" && code != "200") {
			transferSeedErr = fmt.Errorf("upload of the 1 GiB source: curl exit %d, HTTP %s %s", c.exit, code, c.err.String())
		}
	})
	if transferSeedErr != nil {
		t.Fatal(transferSeedErr)
	}
}

// dlProbe is one download probe: curl with the flags of the table, its header dump (every response of the retries), its result.
type dlProbe struct {
	idx      int
	pin      string
	hdr, out string
	done     chan struct{}

	mu       sync.Mutex
	pinTries int
	exit     int
	stderr   string
	started  time.Time
	ended    time.Time
}

func (p *dlProbe) run(x *longTransfers) {
	defer close(p.done)
	for try := 1; try <= 60; try++ {
		_ = os.Remove(p.hdr)
		_ = os.Remove(p.out)
		args := []string{"-sS", "--cacert", cfg.caFile, "--limit-rate", "2M", "--retry", "5", "--retry-all-errors", "-C", "-", "-H", x.auth, "-D", p.hdr, "-o", p.out, x.base + "drain-load/1gib.bin"}
		cmd := exec.Command("curl", args...)
		var errb bytes.Buffer
		cmd.Stderr = &errb
		start := time.Now()
		if err := cmd.Start(); err != nil {
			p.finish(start, -1, err.Error())
			return
		}
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		if p.pin != "" {
			// the pod that answers is in the first response's header; a probe that another replica answers is dropped and started again
			var replica string
			deadline := time.After(20 * time.Second)
		wait:
			for replica == "" {
				select {
				case err := <-finished:
					finished <- err
					break wait
				case <-deadline:
					break wait
				case <-time.After(200 * time.Millisecond):
					replica = firstReplica(p.hdr)
				}
			}
			if replica != p.pin && replica != "" {
				_ = cmd.Process.Kill()
				<-finished
				p.mu.Lock()
				p.pinTries = try
				p.mu.Unlock()
				continue
			}
		}
		err := <-finished
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		p.finish(start, code, strings.TrimSpace(errb.String()))
		return
	}
	p.finish(time.Now(), -1, fmt.Sprintf("no response was served by %s in 60 tries", p.pin))
}

func (p *dlProbe) finish(start time.Time, exit int, stderr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started, p.ended, p.exit, p.stderr = start, time.Now(), exit, stderr
}

// firstReplica is the X-Trove-Replica header of the first response in a curl header dump, "" while there is none.
func firstReplica(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, b := range responseBlocks(raw) {
		if b.replica != "" {
			return b.replica
		}
	}
	return ""
}

// responseBlock is one response of a curl header dump: its status and the replica that sent it.
type responseBlock struct {
	status  string
	code    int
	replica string
}

// responseBlocks splits a curl -D dump, which holds the headers of every response of a transfer that retried, in order.
func responseBlocks(raw []byte) []responseBlock {
	var out []responseBlock
	for _, blk := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n\n") {
		blk = strings.TrimSpace(blk)
		if !strings.HasPrefix(blk, "HTTP/") {
			continue
		}
		lines := strings.Split(blk, "\n")
		b := responseBlock{status: lines[0]}
		if f := strings.Fields(lines[0]); len(f) >= 2 {
			_, _ = fmt.Sscan(f[1], &b.code)
		}
		for _, l := range lines[1:] {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "X-Trove-Replica") {
				b.replica = strings.TrimSpace(v)
			}
		}
		out = append(out, b)
	}
	return out
}

// ulProbe is one upload probe: curl -T at 2 MiB/s, repeated from the start after a failure (at most three attempts).
type ulProbe struct {
	idx  int
	path string
	done chan struct{}

	mu       sync.Mutex
	attempts []ulAttempt
}

type ulAttempt struct {
	start, end time.Time
	exit       int
	code       string
	stderr     string
}

func (u *ulAttempt) ok() bool { return u.exit == 0 && (u.code == "200" || u.code == "201") }

func (p *ulProbe) run(x *longTransfers) {
	defer close(p.done)
	for range 3 {
		args := []string{"-sS", "--cacert", cfg.caFile, "-T", x.ulFile, "--limit-rate", "2M", "-H", x.auth, "-o", "/dev/null", "-w", "%{http_code}", x.base + p.path}
		cmd := exec.Command("curl", args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		a := ulAttempt{start: time.Now()}
		err := cmd.Run()
		a.end = time.Now()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			a.exit = ee.ExitCode()
		} else if err != nil {
			a.exit = -1
		}
		a.code, a.stderr = strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
		p.mu.Lock()
		p.attempts = append(p.attempts, a)
		p.mu.Unlock()
		if a.ok() {
			return
		}
	}
}

func (x *longTransfers) begin(t *testing.T, d *variantDrain, started time.Time) {
	t.Helper()
	for _, p := range d.OnNode {
		if strings.HasPrefix(p.Name, "trove-packages-") {
			x.pin = p.Name
		}
	}
	if x.pin == "" {
		t.Fatalf("no packages pod runs on %s: the drain evicts none", d.Node)
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range dlProbes {
		p := &dlProbe{idx: i, hdr: filepath.Join(dataDir(), fmt.Sprintf("drain-dl-%d.hdr", i)), out: filepath.Join(dataDir(), fmt.Sprintf("drain-dl-%d.bin", i)), done: make(chan struct{})}
		if i == 0 {
			p.pin = x.pin
		}
		x.downs = append(x.downs, p)
		go p.run(x)
	}
	t.Logf("E5.9: %d download probes of 1 GiB at 2 MiB/s started; probe 0 is held on %s, the packages pod the drain evicts", dlProbes, x.pin)
}

// beforeDrain starts the upload probes: they run through the fault.
func (x *longTransfers) beforeDrain(t *testing.T, d *variantDrain) {
	t.Helper()
	for i := range ulProbes {
		p := &ulProbe{idx: i, path: fmt.Sprintf("drain-load/up-%s-%s-%d.bin", x.run, d.V, i), done: make(chan struct{})}
		x.ups = append(x.ups, p)
		go p.run(x)
	}
	t.Logf("E5.9: %d upload probes of 400 MiB at 2 MiB/s started", ulProbes)
}

func (x *longTransfers) verify(t *testing.T, d *variantDrain, firstEviction, drainEnd time.Time) {
	t.Helper()
	d.watch.mu.Lock()
	evicted := map[string]time.Time{}
	for n, at := range d.watch.deleted {
		evicted[n] = at
	}
	d.watch.mu.Unlock()

	for _, p := range x.downs {
		select {
		case <-p.done:
		case <-time.After(10 * time.Minute):
			t.Errorf("download probe %d did not finish within 10 minutes after the load stopped", p.idx)
			continue
		}
		x.verifyDownload(t, p, evicted)
	}
	for _, p := range x.ups {
		select {
		case <-p.done:
		case <-time.After(10 * time.Minute):
			t.Errorf("upload probe %d did not finish within 10 minutes after the load stopped", p.idx)
			continue
		}
		x.verifyUpload(t, p, firstEviction)
	}
}

func (x *longTransfers) verifyDownload(t *testing.T, p *dlProbe, evicted map[string]time.Time) {
	t.Helper()
	raw, _ := os.ReadFile(p.hdr)
	blocks := responseBlocks(raw)
	p.mu.Lock()
	exit, stderr, pinTries, started, ended := p.exit, p.stderr, p.pinTries, p.started, p.ended
	p.mu.Unlock()
	var trail []string
	for _, b := range blocks {
		trail = append(trail, fmt.Sprintf("%s from %s", strings.TrimPrefix(b.status, "HTTP/"), b.replica))
	}
	t.Logf("E5.9: download probe %d: curl exit %d after %s (%d starts to hold it on %s), responses [%s], curl said it would retry %d times (stderr: %q)",
		p.idx, exit, ended.Sub(started).Round(time.Second), pinTries+1, p.pin, strings.Join(trail, "; "), strings.Count(stderr, "retry in"), tail(stderr, 600))
	if exit != 0 {
		t.Errorf("R-HA-059: download probe %d: curl exit %d: %s", p.idx, exit, stderr)
		return
	}
	if got := fileSHA(t, p.out); got != x.dlSum {
		t.Errorf("R-HA-059: download probe %d: sha256 %s, want %s", p.idx, got, x.dlSum)
	}
	_ = os.Remove(p.out)
	perPod := map[string]int{}
	for i, b := range blocks {
		last := i == len(blocks)-1
		switch {
		case last && i > 0 && b.code != 206:
			t.Errorf("R-HA-059: download probe %d: the resume after a cut answered %q, want 206", p.idx, b.status)
		case last && b.code != 200 && b.code != 206:
			t.Errorf("R-HA-059: download probe %d: the last response is %q", p.idx, b.status)
		case !last && b.code != 200 && b.code != 206:
			t.Errorf("R-HA-059: download probe %d: a response of the transfer was refused: %q from %s", p.idx, b.status, b.replica)
		case !last:
			perPod[b.replica]++
			if _, ok := evicted[b.replica]; !ok {
				t.Errorf("R-HA-059: download probe %d was cut on %s, which was not evicted (evicted: %v)", p.idx, b.replica, evictedNames(evicted))
			}
		}
	}
	for pod, n := range perPod {
		if n > 1 {
			t.Errorf("R-HA-059: download probe %d was cut %d times on %s, want at most once per evicted pod", p.idx, n, pod)
		}
	}
	if p.pin != "" && perPod[p.pin] != 1 {
		t.Errorf("R-HA-059: download probe %d was held on %s, the pod the drain evicted, and was cut %d times by it, want once (responses [%s])", p.idx, p.pin, perPod[p.pin], strings.Join(trail, "; "))
	}
}

func evictedNames(m map[string]time.Time) []string {
	var out []string
	for n := range m {
		out = append(out, n)
	}
	return out
}

func (x *longTransfers) verifyUpload(t *testing.T, p *ulProbe, firstEviction time.Time) {
	t.Helper()
	p.mu.Lock()
	attempts := append([]ulAttempt{}, p.attempts...)
	p.mu.Unlock()
	for i, a := range attempts {
		t.Logf("E5.9: upload probe %d attempt %d: %s to %s, curl exit %d, HTTP %s %s", p.idx, i+1, a.start.Format("15:04:05"), a.end.Format("15:04:05"), a.exit, a.code, a.stderr)
	}
	if len(attempts) == 0 || !attempts[len(attempts)-1].ok() {
		t.Errorf("R-HA-059: upload probe %d did not complete in %d attempts: %+v", p.idx, len(attempts), attempts)
		return
	}
	// a cut upload is repeated by the client: one cut at most (the one evicted packages pod), after the eviction began, and not an
	// answer given at once (a refused request is not a cut)
	if len(attempts) > 2 {
		t.Errorf("R-HA-059: upload probe %d was cut %d times, want at most once per evicted packages pod", p.idx, len(attempts)-1)
	}
	for _, a := range attempts[:len(attempts)-1] {
		if a.end.Before(firstEviction) {
			t.Errorf("R-HA-059: upload probe %d failed at %s, before the first eviction at %s: curl exit %d, HTTP %s %s", p.idx, a.end.Format("15:04:05"), firstEviction.Format("15:04:05"), a.exit, a.code, a.stderr)
		}
		if a.end.Sub(a.start) < 30*time.Second {
			t.Errorf("R-HA-059: upload probe %d failed after %s, which is no cut but a refusal: curl exit %d, HTTP %s %s", p.idx, a.end.Sub(a.start).Round(time.Second), a.exit, a.code, a.stderr)
		}
	}
	// what arrived is what was sent
	out := filepath.Join(dataDir(), fmt.Sprintf("drain-ul-%d.check", p.idx))
	c := startCurl(t, "-H", x.auth, "-o", out, x.base+p.path)
	c.wait(t, 5*time.Minute)
	if c.exit != 0 || fileSHA(t, out) != x.ulSum {
		t.Errorf("R-HA-059: the download of %s (curl exit %d) does not match the upload", p.path, c.exit)
	}
	_ = os.Remove(out)
	adm := adminAPI(t)
	adm.do(http.MethodDelete, filesBase+"generic-local/"+p.path, nil)
}
