//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// E1.13: drains with raw transfers. R-HA-011's upload criterion, R-ARCH-024's two generic-local download criteria and
// R-HA-002's GC criterion. The clients are curl, as the criteria name them. The drain test of E0.14 carries the upload
// and download loop (e14_drain_test.go, k6/transfer.js).

// transferToken returns a token of the service account e2e-xfer, which may read, write and delete in generic-local.
func transferToken(t testing.TB) string {
	t.Helper()
	adm := adminAPI(t)
	ensureRepo(t, adm, "generic-local")
	ensureSA(t, adm, "e2e-xfer")
	ensurePolicy(t, adm, "e2e-xfer", []string{"generic-local"}, [2]any{"sa:e2e-xfer", []string{"full"}})
	adm.do(http.MethodDelete, "/api/v1/service-accounts/e2e-xfer/tokens", nil)
	return newSAToken(t, adm, "e2e-xfer", 4*3600, map[string]any{"inherit": true}).Token
}

// dataDir holds the large files of the transfer tests (900 MB). It is next to the artifact directory and not in it:
// CI uploads the artifacts.
func dataDir() string { return filepath.Join(filepath.Dir(filepath.Clean(cfg.out)), "e2e-data") }

// storeNonce identifies the object store instance (the UID of its pod). Content is stored once per SHA-256 and the
// metadata says a blob exists whether or not the object store still holds it, so bytes uploaded to an earlier store
// instance would be "deduplicated" onto objects a restarted store (an emptyDir) has lost: the file would then answer
// 503 content_unavailable. The test content therefore depends on the store instance.
func storeNonce(t testing.TB) string {
	t.Helper()
	uid := strings.TrimSpace(kn(t, "get", "pod", "-l", "app.kubernetes.io/name=trove-s3", "-o", "jsonpath={.items[0].metadata.uid}"))
	if uid == "" {
		t.Fatal("the object store pod has no UID")
	}
	return uid
}

// dataFile returns a file of pseudo-random bytes of the size in the data directory and its SHA-256. The bytes depend on
// the object store instance (storeNonce); the file is made once per instance and reused by later runs, and the files
// of other instances are removed.
func dataFile(t testing.TB, name string, size int64) (string, string) {
	t.Helper()
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	nonce := storeNonce(t)
	stem := strings.TrimSuffix(name, ".bin")
	path := filepath.Join(dir, stem+"-"+nonce[:8]+".bin")
	old, _ := filepath.Glob(filepath.Join(dir, stem+"-*"))
	for _, o := range old {
		if !strings.HasPrefix(o, path) {
			_ = os.Remove(o)
		}
	}
	if st, err := os.Stat(path); err == nil && st.Size() == size {
		if sum, err := os.ReadFile(path + ".sha256"); err == nil {
			return path, strings.TrimSpace(string(sum))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	seed := sha256.Sum256([]byte(nonce + name))
	rng := rand.NewChaCha8(seed)
	buf := make([]byte, 1<<20)
	for written := int64(0); written < size; {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		_, _ = rng.Read(buf[:n])
		h.Write(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	sum := hex.EncodeToString(h.Sum(nil))
	_ = os.WriteFile(path+".sha256", []byte(sum), 0o644)
	return path, sum
}

func fileSHA(t testing.TB, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// curlRun is a curl process with its timing.
type curlRun struct {
	cmd        *exec.Cmd
	out, err   bytes.Buffer
	start, end time.Time
	done       chan struct{}
	exit       int
}

func startCurl(t testing.TB, args ...string) *curlRun {
	t.Helper()
	full := append([]string{"-sS", "--cacert", cfg.caFile}, args...)
	c := &curlRun{cmd: exec.Command("curl", full...), done: make(chan struct{}), start: time.Now()}
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.err
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := c.cmd.Wait()
		c.end = time.Now()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			c.exit = ee.ExitCode()
		} else if err != nil {
			c.exit = -1
		}
		close(c.done)
	}()
	t.Cleanup(func() {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	})
	return c
}

func (c *curlRun) wait(t testing.TB, within time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(within):
		t.Fatalf("curl did not finish within %s", within)
	}
}

// headerOf reads a header from curl's -D dump (the last response block).
func headerOf(dump []byte, name string) string {
	blocks := strings.Split(strings.TrimSpace(string(dump)), "\r\n\r\n")
	last := blocks[len(blocks)-1]
	for _, l := range strings.Split(last, "\r\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func statusLine(dump []byte) string {
	blocks := strings.Split(strings.TrimSpace(string(dump)), "\r\n\r\n")
	last := blocks[len(blocks)-1]
	return strings.SplitN(last, "\r\n", 2)[0]
}

func waitDump(t testing.TB, path string) []byte {
	t.Helper()
	var b []byte
	waitFor(t, "the response headers of the download", 30*time.Second, 200*time.Millisecond, func() bool {
		b, _ = os.ReadFile(path)
		return bytes.Contains(b, []byte("\r\n\r\n"))
	})
	return b
}

func podExists(t testing.TB, name string) (exists, terminating bool) {
	out, err := kubectl("-n", cfg.namespace, "get", "pod", name, "-o", "jsonpath={.metadata.deletionTimestamp}")
	if err != nil {
		return false, false
	}
	return true, strings.TrimSpace(out) != ""
}

// traceTransfer streams the log of the serving pod into the artifacts (the pod is gone when the test looks) and
// samples the size of the file being written once a second: it returns a function that stops both and says when the
// last bytes arrived and every 10 s of the timeline.
func traceTransfer(t *testing.T, serving, file, tag string) func() (lastGrowth time.Time, timeline []string) {
	t.Helper()
	logf, _ := os.Create(filepath.Join(cfg.out, "e1-13-"+tag+"-"+serving+".log"))
	logc := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "logs", "-f", "--tail=0", serving)
	logc.Stdout, logc.Stderr = logf, logf
	_ = logc.Start()
	var mu sync.Mutex
	var last time.Time
	var timeline []string
	done := make(chan struct{})
	finished := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(finished)
		var prev int64 = -1
		for n := 0; ; n++ {
			if st, err := os.Stat(file); err == nil {
				mu.Lock()
				if st.Size() != prev {
					prev = st.Size()
					last = time.Now()
				}
				if n%10 == 0 {
					timeline = append(timeline, fmt.Sprintf("+%s %d MiB", time.Since(start).Round(time.Second), st.Size()>>20))
				}
				mu.Unlock()
			}
			select {
			case <-done:
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return func() (time.Time, []string) {
		close(done)
		<-finished
		time.Sleep(time.Second)
		_ = logc.Process.Kill()
		_ = logc.Wait()
		_ = logf.Close()
		mu.Lock()
		defer mu.Unlock()
		return last, timeline
	}
}

func TestE1_13Transfers(t *testing.T) {
	useScatter(t)
	adm := adminAPI(t)
	tok := transferToken(t)
	base := cfg.edgeURL + "/files/generic-local/"
	auth := "Authorization: Bearer " + tok
	// A large transfer is not the management API's business: the write class does not apply to /files/ in the
	// way a loop of small requests would hit it; the token is the one of the transfer account.

	t.Run("R-HA-011 E1.13 no curl -T of 500 MB fails during kubectl rollout restart deploy/trove-web", func(t *testing.T) {
		file, sum := dataFile(t, "xfer-500mb.bin", 500<<20)
		type up struct {
			path       string
			start, end time.Time
			code       string
			exit       int
			err        string
		}
		var mu sync.Mutex
		var ups []up
		var stop atomic.Bool
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for n := 0; !stop.Load(); n++ {
					p := fmt.Sprintf("e2e-ha-011/w%d-%d.bin", w, n)
					c := startCurl(t, "-T", file, "--limit-rate", "50M", "-H", auth, "-o", "/dev/null", "-w", "%{http_code}", base+p)
					<-c.done
					mu.Lock()
					ups = append(ups, up{p, c.start, c.end, strings.TrimSpace(c.out.String()), c.exit, strings.TrimSpace(c.err.String())})
					mu.Unlock()
				}
			}(w)
		}
		time.Sleep(8 * time.Second) // uploads in flight
		restartAt := time.Now()
		kn(t, "rollout", "restart", "deployment/trove-web")
		kn(t, "rollout", "status", "deployment/trove-web", "--timeout=10m")
		waitWebSettled(t)
		restartDone := time.Now()
		stop.Store(true)
		wg.Wait()

		spanned, failed := 0, 0
		for _, u := range ups {
			if u.exit != 0 || (u.code != "200" && u.code != "201") {
				failed++
				t.Errorf("upload %s: curl exit %d, HTTP %q, %s", u.path, u.exit, u.code, u.err)
			}
			if u.start.Before(restartAt) && u.end.After(restartAt) || u.start.Before(restartDone) && u.end.After(restartDone) || u.start.After(restartAt) && u.end.Before(restartDone) {
				spanned++
			}
		}
		t.Logf("%d uploads of 500 MB at 50 MB/s, %d failed; the rollout took %s and %d uploads overlapped it", len(ups), failed, restartDone.Sub(restartAt).Round(time.Second), spanned)
		if len(ups) < 4 || spanned < 2 {
			t.Errorf("%d uploads, %d overlapping the rollout: too few to say anything", len(ups), spanned)
		}
		// what arrived is what was sent
		if len(ups) > 0 {
			last := ups[len(ups)-1]
			out := filepath.Join(dataDir(), "ha011-check.bin")
			c := startCurl(t, "-H", auth, "-o", out, base+last.path)
			c.wait(t, 5*time.Minute)
			if c.exit != 0 || fileSHA(t, out) != sum {
				t.Errorf("the download of %s (curl exit %d) does not match the upload", last.path, c.exit)
			}
			_ = os.Remove(out)
		}
		for _, u := range ups {
			adm.do(http.MethodDelete, "/files/generic-local/"+u.path, nil)
		}
	})

	big, bigSum := dataFile(t, "xfer-400mib.bin", 400<<20)
	upload := func(t *testing.T) {
		t.Helper()
		adm.do(http.MethodDelete, "/files/generic-local/e2e-xfer/400MiB.bin", nil)
		c := startCurl(t, "-T", big, "-H", "Authorization: Bearer "+adminToken(t), "-o", "/dev/null", "-w", "%{http_code}", base+"e2e-xfer/400MiB.bin")
		c.wait(t, 10*time.Minute)
		if c.exit != 0 || (strings.TrimSpace(c.out.String()) != "201" && strings.TrimSpace(c.out.String()) != "200") {
			t.Fatalf("upload of the 400 MiB file: curl exit %d, HTTP %s %s", c.exit, c.out.String(), c.err.String())
		}
	}
	downloaded := filepath.Join(dataDir(), "download.bin")

	t.Run("R-ARCH-024 E1.13 a download of a generic-local file survives a rollout restart of trove-web and its SHA-256 matches", func(t *testing.T) {
		upload(t)
		_ = os.Remove(downloaded)
		dump := filepath.Join(dataDir(), "download-1.hdr")
		_ = os.Remove(dump) // the headers of an earlier run would pass for this one
		// The criterion's 20 MB/s (about 20 s) would let the restart miss the serving pod two times in three, so
		// the transfer is slowed to outlast the rollout (about 50 s): 6 MB/s, about 70 s for 400 MiB, well inside the
		// 110 s shutdown timeout of a drained web pod (grace 120 s - drain delay 5 s - 5 s). At 4 MB/s (100 s) a slow
		// object store made the transfer outlast that timeout and the drain cut it, correctly.
		c := startCurl(t, "--limit-rate", "6M", "-H", auth, "-D", dump, "-o", downloaded, base+"e2e-xfer/400MiB.bin")
		hdr := waitDump(t, dump)
		serving := headerOf(hdr, "X-Trove-Replica")
		if serving == "" {
			t.Fatalf("the download names no replica: %q", hdr)
		}
		stopTrace := traceTransfer(t, serving, downloaded, "rollout-download")
		var drained atomic.Pointer[time.Time]
		watchDone := make(chan struct{})
		defer close(watchDone)
		go func() {
			for drained.Load() == nil {
				if ex, term := podExists(t, serving); !ex || term {
					now := time.Now()
					drained.Store(&now)
				}
				select {
				case <-watchDone:
					return
				case <-time.After(500 * time.Millisecond):
				}
			}
		}()
		restartAt := time.Now()
		kn(t, "rollout", "restart", "deployment/trove-web")
		kn(t, "rollout", "status", "deployment/trove-web", "--timeout=10m")
		c.wait(t, 5*time.Minute)
		lastBytes, timeline := stopTrace()
		t.Logf("the file grew until +%s after the restart; timeline %v", lastBytes.Sub(restartAt).Round(time.Second), timeline)
		waitWebSettled(t)
		var drainedAt time.Time
		if d := drained.Load(); d != nil {
			drainedAt = *d
		}
		t.Logf("download served by %s; after the restart that pod drained at +%s and the download ended at +%s with curl exit %d", serving,
			drainedAt.Sub(restartAt).Round(time.Second), c.end.Sub(restartAt).Round(time.Second), c.exit)
		if c.exit != 0 {
			t.Fatalf("curl exit %d: %s", c.exit, c.err.String())
		}
		if got := fileSHA(t, downloaded); got != bigSum {
			t.Errorf("sha256 %s, want %s", got, bigSum)
		}
		if drainedAt.IsZero() || drainedAt.After(c.end) {
			t.Errorf("the serving pod %s was not drained while the transfer ran (drained %v, transfer ended %v): the restart did not exercise it", serving, drainedAt, c.end)
		}
	})

	t.Run("R-ARCH-024 E1.13 a download longer than the grace period is cut once and resumes with 206 from another replica with a matching SHA-256", func(t *testing.T) {
		upload(t)
		_ = os.Remove(downloaded)
		dump := filepath.Join(dataDir(), "download-2.hdr")
		_ = os.Remove(dump) // the headers of an earlier run would pass for this one
		c := startCurl(t, "--limit-rate", "2M", "-H", auth, "-D", dump, "-o", downloaded, base+"e2e-xfer/400MiB.bin")
		hdr := waitDump(t, dump)
		serving := headerOf(hdr, "X-Trove-Replica")
		// The serving pod's own account of its shutdown, and when the bytes stopped arriving: the pod is gone after
		// this, the log is what is left.
		logf, _ := os.Create(filepath.Join(cfg.out, "e1-13-cut-download-"+serving+".log"))
		logc := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "logs", "-f", "--tail=0", serving)
		logc.Stdout, logc.Stderr = logf, logf
		_ = logc.Start()
		t.Cleanup(func() { _ = logc.Process.Kill(); _ = logc.Wait(); _ = logf.Close() })
		time.Sleep(3 * time.Second)
		killAt := time.Now()
		var lastGrowth atomic.Pointer[time.Time]
		var podGone atomic.Pointer[time.Time]
		sampleDone := make(chan struct{})
		defer close(sampleDone)
		go func() {
			var prev int64 = -1
			for {
				if st, err := os.Stat(downloaded); err == nil && st.Size() != prev {
					prev = st.Size()
					now := time.Now()
					lastGrowth.Store(&now)
				}
				if podGone.Load() == nil {
					if ex, _ := podExists(t, serving); !ex {
						now := time.Now()
						podGone.Store(&now)
					}
				}
				select {
				case <-sampleDone:
					return
				case <-time.After(time.Second):
				}
			}
		}()
		kn(t, "delete", "pod", serving, "--wait=false")
		c.wait(t, 6*time.Minute)
		cutAfter := c.end.Sub(killAt)
		if g, p := lastGrowth.Load(), podGone.Load(); g != nil && p != nil {
			t.Logf("after the deletion: the last bytes arrived at +%s, the pod was gone at +%s, curl ended at +%s", g.Sub(killAt).Round(time.Second), p.Sub(killAt).Round(time.Second), cutAfter.Round(time.Second))
		}
		st, _ := os.Stat(downloaded)
		got := int64(0)
		if st != nil {
			got = st.Size()
		}
		t.Logf("serving pod %s deleted; curl ended after %s with exit %d and %d of %d bytes", serving, cutAfter.Round(time.Second), c.exit, got, 400<<20)
		if c.exit == 0 || got >= 400<<20 {
			t.Fatalf("the transfer was not cut (curl exit %d, %d bytes): it should outlast the 110 s shutdown timeout at 2 MB/s", c.exit, got)
		}
		// The cut is trove's: at the shutdown deadline (grace 120 s - drain delay 5 s - 5 s = 110 s after SIGTERM) the
		// replica logs the request it cut and stops. The bytes stop arriving then. How long the client waits after
		// that is the edge's: HAProxy tells it of a backend that vanished only when its own timeout fires (measured
		// in e2e.md, "Findings of the suite"), so the end of curl is only bounded loosely.
		if g := lastGrowth.Load(); g == nil || g.Sub(killAt) < 100*time.Second || g.Sub(killAt) > 140*time.Second {
			t.Errorf("the last bytes arrived at %v after the deletion, want about the shutdown timeout of 110 s", g)
		}
		if cutAfter > 5*time.Minute {
			t.Errorf("curl ended %s after the deletion", cutAfter)
		}
		time.Sleep(time.Second) // kubectl logs -f ends with the pod; give it the last lines
		if raw, _ := os.ReadFile(filepath.Join(cfg.out, "e1-13-cut-download-"+serving+".log")); !strings.Contains(string(raw), "requests cut at the shutdown deadline") {
			t.Errorf("the log of %s does not say that it cut a request at the shutdown deadline", serving)
		}
		waitWebSettled(t)

		dump2 := filepath.Join(dataDir(), "download-2b.hdr")
		_ = os.Remove(dump2) // the headers of an earlier run would pass for this one
		r := startCurl(t, "-C", "-", "--limit-rate", "20M", "-H", auth, "-D", dump2, "-o", downloaded, base+"e2e-xfer/400MiB.bin")
		r.wait(t, 5*time.Minute)
		d2, _ := os.ReadFile(dump2)
		line, replica := statusLine(d2), headerOf(d2, "X-Trove-Replica")
		t.Logf("resume: %s from %s, curl exit %d", line, replica, r.exit)
		if r.exit != 0 {
			t.Fatalf("the resume failed: curl exit %d: %s", r.exit, r.err.String())
		}
		if !strings.Contains(line, " 206") {
			t.Errorf("the resume answered %q, want 206", line)
		}
		if replica == "" || replica == serving {
			t.Errorf("the resume came from %q, want another replica than %s", replica, serving)
		}
		if got := fileSHA(t, downloaded); got != bigSum {
			t.Errorf("sha256 after the resume %s, want %s", got, bigSum)
		}
	})

	t.Run("R-HA-002 E1.13 a GC run triggered through a web pod executes on a worker pod", func(t *testing.T) {
		r := adm.do(http.MethodPost, "/api/v1/system/tasks/gc/run", map[string]any{"dryRun": true}).want(t, "run gc", 202, 409)
		if r.Status == 409 { // a run is queued or running: wait for it and ask again
			waitFor(t, "the running gc job", 3*time.Minute, 2*time.Second, func() bool {
				r = adm.do(http.MethodPost, "/api/v1/system/tasks/gc/run", map[string]any{"dryRun": true})
				return r.Status == 202
			})
		}
		webPod := r.Replica()
		var j struct {
			Job struct {
				ID   string `json:"id"`
				Href string `json:"href"`
			} `json:"job"`
		}
		r.JSON(t, &j)
		var job struct {
			State   string  `json:"state"`
			Replica *string `json:"replica"`
		}
		waitFor(t, "the gc job to end", 3*time.Minute, time.Second, func() bool {
			adm.get("/api/v1/system/jobs/"+j.Job.ID).want(t, "read the job", 200).JSON(t, &job)
			return job.State == "succeeded" || job.State == "failed" || job.State == "cancelled"
		})
		ran := ""
		if job.Replica != nil {
			ran = *job.Replica
		}
		t.Logf("gc job %s enqueued through %s, ran on %q, state %s", j.Job.ID, webPod, ran, job.State)
		if job.State != "succeeded" {
			t.Errorf("the gc job ended %s", job.State)
		}
		if !strings.Contains(ran, "-worker-") || strings.Contains(ran, "-web-") {
			t.Errorf("the gc job ran on %q, want a worker pod", ran)
		}
		if !strings.Contains(webPod, "-web-") {
			t.Errorf("the run was accepted by %q, want a web pod", webPod)
		}
		by := sql(t, `SELECT r.name FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.ran_by WHERE j.id = '`+j.Job.ID+`'`)
		if !strings.Contains(by, "-worker-") {
			t.Errorf("job.ran_by of the gc job resolves to %q, want a worker pod", by)
		}
		webRuns := sql(t, `SELECT count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.ran_by WHERE 'web' = ANY(r.roles)`)
		if n, _ := strconv.Atoi(webRuns); n != 0 {
			t.Errorf("%d jobs were run by a web replica", n)
		}
	})
}
