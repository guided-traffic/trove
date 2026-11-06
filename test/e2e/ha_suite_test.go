//go:build e2e

package e2e

// The failure scenarios of R-HA-052 that the pod-crash, switchover and failover tests of npm_ha_test.go do not cover,
// under the same client load (npm_load_test.go: eight npm ci loops, a publish loop, k6): the object-store outage, the
// loss of a node, the loss of the PostgreSQL primary and of the Valkey primary, and the drain criterion of R-NPM-038.
// The set is ha-suite (deploy/dev/e2e.sh); docs/developer/e2e.md#the-failure-suite-under-npm-load describes the whole.
// Q-STOR-147 (the object store keeps its data over an outage), Q-STOR-148 (what the npm loops may see of an outage),
// Q-HA-103 (the start of the node-loss bound), Q-HA-104 (the node of the node loss), Q-HA-105 (what the npm loops may
// see of a lost node) and Q-NPM-76 (the drain criterion of the audit) record the choices.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// ── R-NPM-038: the drain of the pod that forwards an audit ───────────────────

// TestHASuite_AuditDrain is the drain criterion of R-NPM-038: the test provider answers after 20 s, the pod that forwards
// the `npm audit` is deleted one second after the request arrived at the provider, the audit succeeds and the kubelet
// events show no SIGKILL.
func TestHASuite_AuditDrain(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)

	t.Run("R-NPM-038 R-NPM-046 R-ARCH-024 the packages pod that forwards an npm audit is deleted 1 s after the request reached the provider, which answers after 20 s: npm audit succeeds and the kubelet records no SIGKILL", func(t *testing.T) {
		const providerDelay, afterArrival = 20 * time.Second, time.Second
		troveSettled(t, 3, 3, 4)
		c := newNPMClient(t, fx, haRow, 0)
		dir := c.project("audit-drain", nil)
		c.configure(dir)
		// the remote must hold a stored upstream document of the package (R-NPM-038), which the install fetches; none of the
		// corpus' first packages has an advisory of the test provider, so the audit that succeeds exits 0
		pkg := corpus.Default().Names()[0]
		c.mustInstall(dir, pkg)

		s := stub(t)
		s.resetAll(t)
		t.Cleanup(func() { s.resetAll(t) })
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"audit": int(providerDelay.Milliseconds())}})

		watch := startPodWatch(t, selTrove)
		type auditRun struct {
			r        npmRun
			from, to time.Time
		}
		done := make(chan auditRun, 1)
		go func() {
			var a auditRun
			a.from = time.Now()
			a.r = c.run(dir, "audit")
			a.to = time.Now()
			done <- a
		}()

		var arrived time.Time
		waitFor(t, "the audit request to reach the test provider", 30*time.Second, 100*time.Millisecond, func() bool {
			if npmInflight(t).Open >= 1 {
				arrived = time.Now()
				return true
			}
			return false
		})
		var forwarder string
		waitFor(t, "a packages pod with a request in flight", 10*time.Second, 50*time.Millisecond, func() bool {
			forwarder = podWithClientRequestInFlight(t)
			return forwarder != ""
		})
		victim := podByName(t, forwarder)
		// the log of the pod that is deleted ends with the pod; the follow keeps what it wrote about the forward
		if f, err := os.Create(filepath.Join(cfg.out, "ha-audit-"+forwarder+".log")); err == nil {
			c := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "logs", "-f", "--tail=-1", forwarder)
			c.Stdout, c.Stderr = f, f
			_ = c.Start()
			t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait(); _ = f.Close() })
		}
		time.Sleep(time.Until(arrived.Add(afterArrival)))
		deletedAt := time.Now()
		kn(t, "delete", "pod", forwarder, "--wait=false")
		t.Logf("the request reached the provider at %s; %s deleted %s later", arrived.Format("15:04:05.000"), forwarder, deletedAt.Sub(arrived).Round(10*time.Millisecond))

		var a auditRun
		select {
		case a = <-done:
		case <-time.After(2 * time.Minute):
			t.Fatal("npm audit did not end within 2 minutes")
		}
		took := a.to.Sub(a.from)
		t.Logf("npm audit ended after %s with exit %d", took.Round(100*time.Millisecond), a.r.Code)
		if a.r.Code != 0 || !strings.Contains(a.r.Out, "found 0 vulnerabilities") {
			t.Errorf("npm audit of %s did not succeed (exit %d):\n%s", pkg, a.r.Code, tailLines(a.r.Out, 30))
		}
		if answered := a.to.Sub(arrived); answered < providerDelay-time.Second {
			t.Errorf("npm audit ended %s after the request reached the provider, which holds its answer for %s: it was not the delayed answer that ended it", answered.Round(100*time.Millisecond), providerDelay)
		}
		if log := s.audits(t); len(log) != 1 || !npmstub.Asked(log, pkg) {
			t.Errorf("the provider logged %d audit requests (asking about %s: %v), want exactly 1 that asks about it (npm does not retry a POST)", len(log), pkg, npmstub.Asked(log, pkg))
		}
		waitFor(t, "the deleted pod to be gone", 3*time.Minute, 2*time.Second, func() bool {
			for _, p := range podsOf(t, selTrove) {
				if p.Name == forwarder {
					return false
				}
			}
			return true
		})
		watch.stop()
		if len(watch.killed) != 0 {
			t.Errorf("containers were killed: %v", watch.killed)
		}
		checkEvents(t, []pod{victim})
		noteResult(t, "ha-suite-audit-drain", map[string]any{
			"pod": forwarder, "delete_after_arrival_ms": deletedAt.Sub(arrived).Milliseconds(), "audit_seconds": took.Seconds(), "killed": watch.killed,
		})
		troveSettled(t, 3, 3, 4)
	})
}

// podWithClientRequestInFlight is the serving pod that has a request open on its client listener, "" when there is none.
func podWithClientRequestInFlight(t testing.TB) string {
	t.Helper()
	pods := readyPackagesPods(t)
	found := make([]bool, len(pods))
	var wg sync.WaitGroup
	for i, p := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := ops(p, "/metrics")
			if code != 200 {
				return
			}
			for _, l := range strings.Split(body, "\n") {
				if !strings.HasPrefix(l, "trove_http_requests_in_flight{") || !strings.Contains(l, `listener="client"`) {
					continue
				}
				if v, err := strconv.ParseFloat(l[strings.LastIndex(l, " ")+1:], 64); err == nil && v > 0 {
					found[i] = true
				}
			}
		}()
	}
	wg.Wait()
	for i, ok := range found {
		if ok {
			return pods[i]
		}
	}
	return ""
}

func podByName(t testing.TB, name string) pod {
	t.Helper()
	for _, p := range podsOf(t, selTrove) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("pod %s not found", name)
	return pod{}
}

// ── R-HA-052: the object-store outage ────────────────────────────────────────

// s3Probe is one request of the outage prober.
type s3Probe struct {
	At         time.Time
	Kind       string // document, api, tarball, upload
	Status     int
	RetryAfter string
	Sent       int64 // upload: the bytes curl sent
	Path       string
	Err        string
}

// s3Prober asks, once a second, for what the object-store outage of R-HA-052 names: a package document and the management
// API (metadata: 200 throughout), a tarball (a blob: 503 with Retry-After) and, every fifth second, an upload with
// `Expect: 100-continue` (503 with Retry-After before the body is sent).
type s3Prober struct {
	h      *npmHAFixture
	upload string // the file of the upload probe
	token  string // the transfer account's token
	// uploads is set once the store is down: before it, a probe would store 64 MiB per fifth second
	uploads atomic.Bool
	mu      sync.Mutex
	got     []s3Probe
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func startS3Prober(t *testing.T, h *npmHAFixture, uploadFile, transfer string) *s3Prober {
	p := &s3Prober{h: h, upload: uploadFile, token: transfer, stopCh: make(chan struct{})}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for n := 0; ; n++ {
			select {
			case <-p.stopCh:
				return
			case <-time.After(time.Second):
			}
			p.probeMetadata()
			p.probeTarball()
			if n%5 == 0 && p.uploads.Load() {
				p.add(p.probeUpload(fmt.Sprintf("e2e-ha-s3/probe-%d.bin", n)))
			}
		}
	}()
	t.Cleanup(p.stop)
	return p
}

func (p *s3Prober) add(s s3Probe) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, s)
}

func (p *s3Prober) stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
	p.wg.Wait()
}

func (p *s3Prober) probes() []s3Probe {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]s3Probe(nil), p.got...)
}

func (p *s3Prober) probeMetadata() {
	for _, q := range []struct{ kind, path, token string }{
		{"document", "/api/npm/" + npmAllKey + "/" + url.PathEscape(p.h.locals[0]), p.h.k6Token()},
		{"api", "/api/v1/repositories?limit=5", ""},
	} {
		a := p.h.fx.adm
		if q.token != "" {
			a = a.withToken(q.token)
		}
		s := s3Probe{At: time.Now(), Kind: q.kind}
		if r, err := a.send(http.MethodGet, q.path, nil); err != nil {
			s.Err = err.Error()
		} else {
			s.Status, s.RetryAfter = r.Status, r.Header.Get("Retry-After")
		}
		p.add(s)
	}
}

func (p *s3Prober) probeTarball() { p.add(p.tarball()) }

func (p *s3Prober) tarball() s3Probe {
	s := s3Probe{At: time.Now(), Kind: "tarball"}
	if r, err := p.h.fx.adm.withToken(p.h.k6Token()).send(http.MethodGet, p.h.blob1MiB, nil); err != nil {
		s.Err = err.Error()
	} else {
		s.Status, s.RetryAfter = r.Status, r.Header.Get("Retry-After")
	}
	return s
}

// probeUpload is `curl -T` of the upload file with `Expect: 100-continue`, as the acceptance criterion of R-HA-010 names it.
func (p *s3Prober) probeUpload(path string) s3Probe {
	s := s3Probe{At: time.Now(), Kind: "upload", Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "curl", "-sS", "--cacert", cfg.caFile, "-T", p.upload, "-H", "Authorization: Bearer "+p.token,
		"-H", "Expect: 100-continue", "-D", "-", "-o", "/dev/null", "-w", "\n%{http_code} %{size_upload}", cfg.edgeURL+"/files/generic-local/"+path)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		s.Err = fmt.Sprintf("%v: %s", err, strings.TrimSpace(errb.String()))
		return s
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, l := range lines {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Retry-After") {
			s.RetryAfter = strings.TrimSpace(v)
		}
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) == 2 {
		s.Status, _ = strconv.Atoi(f[0])
		s.Sent, _ = strconv.ParseInt(f[1], 10, 64)
	}
	return s
}

func TestHASuite_ObjectStoreOutage(t *testing.T) {
	h := npmHAFx(t)
	for _, c := range []struct {
		d      time.Duration
		delete bool
		name   string
	}{
		{30 * time.Second, false, "R-HA-052 R-HA-010 R-STOR-036 R-HA-008 the object store stopped for 30 s under the npm load: metadata and the API answer throughout, blob transfers 503 with Retry-After, no replica turns unready, full recovery within open_duration + 10 s"},
		{5 * time.Minute, true, "R-HA-052 R-HA-010 R-STOR-036 R-STOR-035 R-HA-008 the object store stopped for 5 min with a packages pod deleted meanwhile: the replacement stays not ready with its restart count unchanged and is ready within 60 s of the return; the rest as for 30 s"},
	} {
		t.Run(c.name, func(t *testing.T) { objectStoreOutage(t, h, c.d, c.delete) })
	}
}

func objectStoreOutage(t *testing.T, h *npmHAFixture, d time.Duration, deleteReplica bool) {
	var info struct {
		OpenMS int64 `json:"s3_open_duration_ms"`
	}
	if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
		t.Fatal(err)
	}
	open := time.Duration(info.OpenMS) * time.Millisecond
	bound := open + 10*time.Second
	retryAfter := strconv.Itoa(max(5, int((open+time.Second-1)/time.Second))) // R-HA-010: open_duration rounded up, at least 5
	uploadFile, _ := dataFile(t, "ha-s3-64mb.bin", 64<<20)
	transfer := transferToken(t)

	var downAt, upAt, backAt time.Time
	var deleted, replacement string
	var replacementReady time.Duration
	var recovery struct{ blob, upload time.Duration }
	var prober *s3Prober
	initial := map[string]bool{}
	var replacementSamples int

	r := runNPMFault(t, h, fmt.Sprintf("s3-outage-%ds", int(d.Seconds())), haFullLoad(), time.Minute, 2*time.Minute, func() {
		for _, p := range livePods(t, selTrove) {
			initial[p.Name] = true
		}
		prober = startS3Prober(t, h, uploadFile, transfer)
		downAt = time.Now() // the store is unreachable from the first second of s3-stop.sh: its pod leaves the Service when it terminates
		script(t, "s3-stop.sh")
		prober.uploads.Store(true)
		restarted := false
		defer func() {
			if !restarted { // a failure below ends the test; the store comes back all the same
				script(t, "s3-start.sh")
			}
		}()
		if deleteReplica {
			time.Sleep(time.Minute)
			deleted = deletePackagesPod(t)
			waitFor(t, "a replacement trove pod", 2*time.Minute, 2*time.Second, func() bool {
				for _, p := range livePods(t, selTrove) {
					if !initial[p.Name] {
						replacement = p.Name
						return true
					}
				}
				return false
			})
		}
		for time.Now().Before(downAt.Add(d)) {
			if replacement != "" {
				// R-STOR-035: the startup check retries without exiting, so the replacement is neither ready nor restarted
				p := podByName(t, replacement)
				replacementSamples++
				if p.Ready {
					t.Errorf("R-HA-010: the replacement %s turned ready at %s while the object store was down", replacement, time.Now().Format("15:04:05"))
				}
				if p.Restarts != 0 {
					t.Errorf("R-STOR-035: the replacement %s restarted %d times during the outage", replacement, p.Restarts)
				}
			}
			time.Sleep(2 * time.Second)
		}
		upAt = time.Now() // the store's pod starts to serve while s3-start.sh still waits for it
		script(t, "s3-start.sh")
		restarted = true
		backAt = time.Now()
		start := time.Now()
		blobOK, uploadOK := false, false
		waitFor(t, "a tarball and an upload to succeed again", 3*time.Minute, 250*time.Millisecond, func() bool {
			if !blobOK && prober.tarball().Status == 200 {
				blobOK, recovery.blob = true, time.Since(start)
			}
			if !uploadOK {
				if s := prober.probeUpload("e2e-ha-s3/recovery.bin"); s.Status == 200 || s.Status == 201 {
					uploadOK, recovery.upload = true, time.Since(start)
				}
			}
			return blobOK && uploadOK
		})
		if replacement != "" {
			waitFor(t, "the replacement pod to turn ready", 3*time.Minute, time.Second, func() bool { return podByName(t, replacement).Ready })
			replacementReady = time.Since(backAt) // from the return of s3-start.sh, which waits for the store's pod
		}
	})
	prober.stop()
	xfer := h.fx.adm.withToken(transfer)
	for _, p := range prober.probes() {
		if p.Kind == "upload" && (p.Status == 200 || p.Status == 201) {
			_, _ = xfer.send(http.MethodDelete, "/files/generic-local/"+p.Path, nil)
		}
	}
	_, _ = xfer.send(http.MethodDelete, "/files/generic-local/e2e-ha-s3/recovery.bin", nil)

	recoveredBy := backAt.Add(bound)
	t.Logf("outage %s to %s; blobs served again %s and uploads accepted %s after the return (bound open_duration %s + 10 s = %s); replacement %q ready %s after the return, %d samples while down; replica deleted %q",
		downAt.Format("15:04:05"), backAt.Format("15:04:05"), recovery.blob.Round(10*time.Millisecond), recovery.upload.Round(10*time.Millisecond), open, bound, replacement, replacementReady.Round(time.Second), replacementSamples, deleted)
	noteResult(t, "ha-suite-s3-outage", map[string]any{
		"outage_seconds": d.Seconds(), "blob_recovery_ms": recovery.blob.Milliseconds(), "upload_recovery_ms": recovery.upload.Milliseconds(),
		"replacement": replacement, "replacement_ready_s": replacementReady.Seconds(), "probes": len(prober.probes()),
	})

	// no running replica turns unready (R-HA-008); the deleted pod drains with its readiness down
	for p, n := range r.mon.ready {
		if initial[p] && p != deleted {
			t.Errorf("R-HA-008: /readyz of %s answered non-200 %d times during the outage", p, n)
		}
	}
	r.steady(t, deleted)
	r.res.loopsSpan(t, r.faultAt, r.faultEnd)
	if deleteReplica {
		if replacementSamples < 20 {
			t.Errorf("the replacement %s was sampled %d times during the outage, want at least 20", replacement, replacementSamples)
		}
		if replacementReady > 60*time.Second {
			t.Errorf("R-HA-010: the replacement became ready %s after the store returned, want within 60 s", replacementReady.Round(time.Second))
		}
		if p := podByName(t, replacement); p.Restarts != 0 {
			t.Errorf("R-STOR-035: the replacement restarted %d times", p.Restarts)
		}
	}
	if recovery.blob > bound || recovery.upload > bound {
		t.Errorf("R-HA-052: full recovery took %s for blobs and %s for uploads, bound open_duration + 10 s = %s", recovery.blob, recovery.upload, bound)
	}

	// the prober: metadata throughout, blobs and uploads 503 with Retry-After once the circuit is open
	settle := 15 * time.Second
	var badMeta, badBlob, badUpload []string
	uploadsDown, uploadsEarly := 0, 0
	for _, p := range prober.probes() {
		inDown := !p.At.Before(downAt.Add(settle)) && p.At.Before(upAt)
		switch {
		case p.Kind == "document" || p.Kind == "api":
			if p.Status != 200 {
				badMeta = append(badMeta, fmt.Sprintf("%s %d %s at %s", p.Kind, p.Status, p.Err, p.At.Format("15:04:05")))
			}
		case p.Kind == "tarball" && inDown:
			if p.Status != 503 || p.RetryAfter != retryAfter {
				badBlob = append(badBlob, fmt.Sprintf("%d Retry-After %q %s at %s", p.Status, p.RetryAfter, p.Err, p.At.Format("15:04:05")))
			}
		case p.Kind == "upload" && inDown:
			uploadsDown++
			if p.Sent <= 1<<20 {
				uploadsEarly++
			}
			if p.Status != 503 || p.RetryAfter != retryAfter {
				badUpload = append(badUpload, fmt.Sprintf("%d Retry-After %q sent %d bytes %s at %s", p.Status, p.RetryAfter, p.Sent, p.Err, p.At.Format("15:04:05")))
			}
		case p.Kind == "tarball" && !p.At.Before(recoveredBy):
			if p.Status != 200 {
				badBlob = append(badBlob, fmt.Sprintf("%d after the recovery bound, at %s", p.Status, p.At.Format("15:04:05")))
			}
		}
	}
	// Between the end of the circuit's open duration and the probe that the next call to the store is, an upload is let
	// through (Guard.Rejecting): at least three in five are refused before their body, the rest after it, all with the 503
	if uploadsDown < 3 || uploadsEarly*5 < uploadsDown*3 {
		t.Errorf("R-HA-010: %d of %d uploads with Expect: 100-continue were refused before their body while the store was down, want at least three in five of at least three", uploadsEarly, uploadsDown)
	}
	t.Logf("uploads with Expect: 100-continue while the store was down: %d, refused before the body: %d", uploadsDown, uploadsEarly)
	for _, c := range []struct {
		what string
		bad  []string
	}{{"R-HA-052 metadata and API", badMeta}, {"R-STOR-036 tarball", badBlob}, {"R-HA-010 upload with Expect: 100-continue", badUpload}} {
		if len(c.bad) != 0 {
			t.Errorf("%s: %d unexpected answers, first: %v", c.what, len(c.bad), c.bad[:min(5, len(c.bad))])
		}
	}

	// k6: documents never fail; every failure is a tarball 503 inside the outage and the recovery bound
	window := haWindow{downAt, recoveredBy}
	var k6Bad []npmK6Line
	for _, l := range r.res.K6.Failures {
		if l.Kind != "tarball" || l.Status != 503 || !window.has(l.At) {
			k6Bad = append(k6Bad, l)
		}
	}
	if len(k6Bad) != 0 {
		t.Errorf("R-HA-052: %d k6 requests failed other than as a tarball 503 within the outage and the recovery bound (%d failures in all): first %+v", len(k6Bad), len(r.res.K6.Failures), k6Bad[:min(5, len(k6Bad))])
	}
	if r.res.K6.Status["document 200"] == 0 || r.res.K6.Status["tarball 200"] == 0 {
		t.Errorf("k6 did not exercise documents and tarballs: %v", r.res.K6.Status)
	}
	// the npm loops (Q-STOR-148)
	r.res.outageCommands(t, window)
	r.res.installPublished(t, h)
	troveSettled(t, 3, 3, 4)
}

// networkFailure matches what npm prints of a connection that was cut or never answered.
var networkFailure = regexp.MustCompile(`ECONNRESET|ETIMEDOUT|ECONNREFUSED|socket hang up|network aborted|E50[234]`)

// haWindow is a span of time a fault is allowed to show in.
type haWindow struct{ from, to time.Time }

func (w haWindow) has(at time.Time) bool { return !at.Before(w.from) && at.Before(w.to) }

// outageCommands is the npm condition of an object-store outage (Q-STOR-148): a command that does not overlap the window
// exits 0; one that does may fail, and then only with the registry's 503; every npm ci loop completes a command after the
// window, which reads the tarballs of the whole lockfile from the restored store.
func (r npmLoadResult) outageCommands(t *testing.T, w haWindow) {
	t.Helper()
	failed, overlapping := 0, 0
	afterOK := map[string]int{}
	for _, c := range r.Commands {
		overlap := c.Start.Before(w.to) && c.End.After(w.from)
		if overlap {
			overlapping++
		}
		if !overlap && !c.Start.Before(w.to) && c.Kind == "ci" && c.Code == 0 {
			afterOK[c.Loop]++
		}
		if c.Code == 0 {
			continue
		}
		failed++
		switch {
		case !overlap:
			t.Errorf("npm %s (%s, command %d) exited %d outside the outage, started %s:\n%s", c.Kind, c.Loop, c.N, c.Code, c.Start.Format("15:04:05.000"), c.Log)
		case !strings.Contains(c.Log, "503") && !networkFailure.MatchString(c.Log):
			t.Errorf("npm %s (%s, command %d) exited %d during the outage with neither a 503 nor a network error in its output, started %s:\n%s", c.Kind, c.Loop, c.N, c.Code, c.Start.Format("15:04:05.000"), c.Log)
		case !strings.Contains(c.Log, "503"):
			t.Logf("npm %s (%s, command %d) exited %d during the outage with a network error and no 503, started %s:\n%s", c.Kind, c.Loop, c.N, c.Code, c.Start.Format("15:04:05.000"), tailLines(c.Log, 6))
		}
	}
	for _, lp := range r.loopNames("ci") {
		if afterOK[lp] == 0 {
			t.Errorf("the %s loop completed no npm ci after the outage and its recovery bound", lp)
		}
	}
	t.Logf("npm commands in all %d, overlapping the outage window %d, failed %d (each with the 503); successful npm ci after the window per loop %v", len(r.Commands), overlapping, failed, afterOK)
}

func (r npmLoadResult) loopNames(kind string) []string {
	seen := map[string]bool{}
	for _, c := range r.Commands {
		if c.Kind == kind {
			seen[c.Loop] = true
		}
	}
	return sortedKeys(seen)
}

// ── R-HA-052: node loss ──────────────────────────────────────────────────────

func TestHASuite_NodeLoss(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-012 R-HA-006 docker stop of a worker node under the npm load: replacement pods ready within nodeFailureTolerationSeconds + 60 s of the node being declared lost, one holder of the scheduler lease at any time, no scheduler slot twice, every npm command exits 0", func(t *testing.T) {
		var info struct {
			LeaseReleaseMS int64 `json:"lease_release_bound_ms"`
		}
		if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
			t.Fatal(err)
		}
		troveSettled(t, 3, 3, 4)
		node := nodeLossNode(t) // Q-HA-104
		troveSettled(t, 3, 3, 4)
		var onNode []pod
		for _, p := range livePods(t, selTrove) {
			if p.Node == node {
				onNode = append(onNode, p)
			}
		}
		tol := 0
		for _, p := range onNode {
			tol = max(tol, tolerationSecondsOf(t, p.Name))
		}
		holders := schedulerHolders(t)
		if len(holders) != 1 {
			t.Fatalf("scheduler lease holders %v", holders)
		}
		holderOnNode := false
		for _, p := range onNode {
			holderOnNode = holderOnNode || p.Name == holders[0]
		}
		t.Logf("stopping %s, which hosts %v; the scheduler lease is held by %s (%v on that node); nodeFailureTolerationSeconds %d", node, podNames(onNode), holders[0], holderOnNode, tol)
		watch := startHolderWatch(t)
		t.Cleanup(func() { _, _ = exec.Command("docker", "start", node).CombinedOutput() })

		var stopAt, taintAt, readyAt, leaseAt, backAt time.Time
		r := runNPMFault(t, h, "node-loss", haFullLoad(), time.Minute, 2*time.Minute, func() {
			stopAt = time.Now()
			if out, err := exec.Command("docker", "stop", node).CombinedOutput(); err != nil {
				t.Fatalf("docker stop %s: %v: %s", node, err, out)
			}
			t.Logf("docker stop returned after %s", time.Since(stopAt).Round(time.Second))
			waitFor(t, "the unreachable taint on "+node, 5*time.Minute, time.Second, func() bool {
				at, ok := unreachableTaintAt(t, node)
				taintAt = at
				return ok
			})
			waitFor(t, "every replacement pod to be ready", 10*time.Minute, 2*time.Second, func() bool {
				a, p, w := readyRoles(t, node)
				return a == 3 && p == 3 && w == 4
			})
			readyAt = time.Now()
			waitFor(t, "one ready worker outside "+node+" to hold the scheduler lease", 5*time.Minute, time.Second, func() bool {
				hs := schedulerHolders(t)
				if len(hs) != 1 {
					return false
				}
				for _, p := range podsOf(t, selTrove) {
					if p.Name == hs[0] {
						return p.Node != node && p.Ready && !p.Deleting
					}
				}
				return false
			})
			leaseAt = time.Now()
			time.Sleep(10 * time.Second)
			if out, err := exec.Command("docker", "start", node).CombinedOutput(); err != nil {
				t.Fatalf("docker start %s: %v: %s", node, err, out)
			}
			backAt = time.Now()
			waitFor(t, "node "+node+" to be ready", 5*time.Minute, 2*time.Second, func() bool {
				return strings.TrimSpace(kn2(t, "get", "node", node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)) == "True"
			})
			waitFor(t, "the pods of the lost node to be removed", 5*time.Minute, 2*time.Second, func() bool {
				for _, p := range podsOf(t, selTrove) {
					if p.Node == node {
						return false
					}
				}
				return true
			})
			troveSettled(t, 3, 3, 4)
			clusterWhole(t)
		})
		samples := watch.stop()
		maxHolders := 0
		for _, s := range samples {
			maxHolders = max(maxHolders, s.n)
		}
		replaced := readyAt.Sub(taintAt)
		bound := time.Duration(tol)*time.Second + 60*time.Second
		t.Logf("docker stop at %s; the node was declared unreachable at %s (%s later); replacements ready %s after the declaration and %s after the stop (bound nodeFailureTolerationSeconds + 60 s = %s); scheduler lease held again by one live worker %s after the stop; node started %s after the stop; most lease holders at once %d over %d samples",
			stopAt.Format("15:04:05"), taintAt.Format("15:04:05"), taintAt.Sub(stopAt).Round(time.Second), replaced.Round(time.Second), readyAt.Sub(stopAt).Round(time.Second), bound,
			leaseAt.Sub(stopAt).Round(time.Second), backAt.Sub(stopAt).Round(time.Second), maxHolders, len(samples))
		noteResult(t, "ha-suite-node-loss", map[string]any{
			"node": node, "pods": podNames(onNode), "toleration_s": tol, "declared_after_s": taintAt.Sub(stopAt).Seconds(), "replacements_ready_after_declaration_s": replaced.Seconds(),
			"replacements_ready_after_stop_s": readyAt.Sub(stopAt).Seconds(), "lease_after_stop_s": leaseAt.Sub(stopAt).Seconds(), "holder_on_node": holderOnNode,
			"max_lease_holders": maxHolders, "k6": k6Summary(r.res.K6), "commands": len(r.res.Commands),
		})
		if replaced > bound {
			t.Errorf("R-HA-052: the replacement pods were ready %s after the node was declared lost, bound nodeFailureTolerationSeconds + 60 s = %s", replaced.Round(time.Second), bound)
		}
		if maxHolders > 1 {
			t.Errorf("R-HA-006: %d connections held the scheduler lease at once", maxHolders)
		}
		if holderOnNode {
			if lim := time.Duration(info.LeaseReleaseMS)*time.Millisecond + 30*time.Second; leaseAt.Sub(stopAt) > lim+time.Duration(tol)*time.Second {
				t.Errorf("R-HA-006: the scheduler lease of the lost node moved %s after the stop, bound idle + interval x count of the server keepalive + 30 s = %s (+ %d s toleration)", leaseAt.Sub(stopAt).Round(time.Second), lim, tol)
			}
		}
		if n := sql(t, `SELECT count(*) FROM (SELECT task, scheduled_for FROM trove.job WHERE task IS NOT NULL GROUP BY 1, 2 HAVING count(*) > 1) d`); n != "0" {
			t.Errorf("R-HA-006: %s scheduler slots were enqueued twice", n)
		}
		if n := sql(t, `SELECT count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE r.roles && ARRAY['api','packages','web']`); n != "0" {
			t.Errorf("%s jobs are locked by an api or packages replica", n)
		}
		// the running replicas outside the lost node stay ready
		gone := map[string]bool{}
		for _, p := range onNode {
			gone[p.Name] = true
		}
		for p, n := range r.mon.ready {
			if r.initial[p] && !gone[p] {
				t.Errorf("R-HA-008: /readyz of %s answered non-200 %d times while node %s was lost", p, n, node)
			}
		}
		r.steady(t, podNames(onNode)...)
		r.res.nodeLossCommands(t, stopAt) // Q-HA-105
		r.res.loopsSpan(t, stopAt, readyAt)
		var late []npmK6Line
		for _, l := range r.res.K6.Failures {
			if l.At.After(readyAt.Add(10 * time.Second)) {
				late = append(late, l)
			}
		}
		if len(late) != 0 {
			t.Errorf("%d k6 requests failed more than 10 s after the replacements were ready: first %+v", len(late), late[:min(5, len(late))])
		}
		t.Logf("k6 under the node loss: %d requests, status %v, %d failures (%v)", r.res.K6.Total, r.res.K6.Status, len(r.res.K6.Failures), failureStatuses(r.res.K6.Failures))
		r.res.installPublished(t, h)
	})
}

// nodeLossNode picks the node whose loss is trove's to survive (Q-HA-104): it hosts an api, a packages and a worker pod
// and none of what the loss of a node takes away from trove's own dependencies for longer than the scenario's bound. That is
// the PostgreSQL primary (the PostgreSQL row of R-HA-052), the Valkey primary (the Valkey row), the PostgreSQL standby
// (the primary's commits wait for it until the operator drops it from the synchronous set, which needs the operator) and
// the pods of the two operators, which the dev profile runs once and which Kubernetes replaces only after its default 300 s
// toleration. An operator or a primary in the way is moved first, before the load starts; a node with the standby is taken
// only when no other node qualifies, and the test then reports it.
func nodeLossNode(t *testing.T) string {
	t.Helper()
	podNodes := func(namespace, selector string) map[string]bool {
		out := map[string]bool{}
		for _, p := range podsOfIn(t, namespace, selector) {
			out[p.Node] = true
		}
		return out
	}
	for attempt := 0; attempt < 4; attempt++ {
		hosts := nodesHosting(t, selAPI, selPackages, selMainWorker)
		pgNode := strings.TrimSpace(kn(t, "get", "pod", pgPrimary(t), "-o", "jsonpath={.spec.nodeName}"))
		vkNode := strings.TrimSpace(kn(t, "get", "pod", valkeyPrimaryBySentinel(t), "-o", "jsonpath={.spec.nodeName}"))
		standby := podNodes(cfg.namespace, "cnpg.io/cluster=trove-db,cnpg.io/instanceRole=replica")
		cnpgOp := podNodes("cnpg-system", "app.kubernetes.io/name=cloudnative-pg")
		vkOp := podNodes("valkey-operator-system", "app.kubernetes.io/component=operator")
		var clean, withStandby []string
		blocked := map[string][]string{}
		for n := range hosts {
			switch {
			case n == pgNode:
				blocked["pg"] = append(blocked["pg"], n)
			case n == vkNode:
				blocked["valkey"] = append(blocked["valkey"], n)
			case cnpgOp[n] || vkOp[n]:
				blocked["operator"] = append(blocked["operator"], n)
			case standby[n]:
				withStandby = append(withStandby, n)
			default:
				clean = append(clean, n)
			}
		}
		sort.Strings(clean)
		sort.Strings(withStandby)
		switch {
		case len(clean) > 0:
			t.Logf("node %s: PostgreSQL primary on %s, Valkey primary on %s, standby on %v, operators on %v and %v", clean[0], pgNode, vkNode, sortedKeys(standby), sortedKeys(cnpgOp), sortedKeys(vkOp))
			return clean[0]
		case len(blocked["operator"]) > 0:
			t.Logf("the only candidates host an operator (%v and %v); deleting the operators' pods so the scheduler places them again", sortedKeys(cnpgOp), sortedKeys(vkOp))
			kubectlMust(t, "-n", "cnpg-system", "delete", "pod", "-l", "app.kubernetes.io/name=cloudnative-pg", "--wait=true")
			kubectlMust(t, "-n", "valkey-operator-system", "delete", "pod", "-l", "app.kubernetes.io/component=operator", "--wait=true")
			kubectlMust(t, "-n", "cnpg-system", "rollout", "status", "deployment", "--timeout=3m")
			kubectlMust(t, "-n", "valkey-operator-system", "rollout", "status", "deployment", "--timeout=3m")
		case len(blocked["valkey"]) > 0:
			t.Logf("the candidates host the Valkey primary (%s); failing it over first", vkNode)
			script(t, "valkey-failover.sh")
		case len(blocked["pg"]) > 0:
			t.Logf("the candidates host the PostgreSQL primary (%s); switching it over first", pgNode)
			script(t, "failover.sh")
		case len(withStandby) > 0:
			t.Logf("node %s hosts the PostgreSQL standby (no other node qualifies): its loss stalls the primary's commits until the operator drops the synchronous standby", withStandby[0])
			return withStandby[0]
		}
	}
	t.Fatal("no node hosts an api, a packages and a worker pod without the PostgreSQL and Valkey primaries and the operators")
	return ""
}

// kubectlMust runs kubectl without the release's namespace and fails the test on an error.
func kubectlMust(t testing.TB, args ...string) {
	t.Helper()
	if out, err := kubectl(args...); err != nil {
		t.Fatalf("kubectl %v: %v: %s", args, err, out)
	}
}

// nodeLossCommands is the npm condition of a lost node (Q-HA-105): a command that was running when the node stopped may fail,
// and then with a network error (its requests on the node's pods are black-holed and the cluster declares the node lost
// only after the controller manager's grace period, which is longer than npm's default retries span); every command that
// started after the stop exits 0.
func (r npmLoadResult) nodeLossCommands(t *testing.T, stopAt time.Time) {
	t.Helper()
	if len(r.Commands) == 0 {
		t.Errorf("the npm load %s ran no command", r.Name)
	}
	running, failedRunning, failedAfter := 0, 0, 0
	for _, c := range r.Commands {
		wasRunning := c.Start.Before(stopAt) && c.End.After(stopAt)
		if wasRunning {
			running++
		}
		if c.Code == 0 {
			continue
		}
		switch {
		case !wasRunning:
			failedAfter++
			t.Errorf("npm %s (%s, command %d) exited %d and was not running when the node stopped, started %s:\n%s", c.Kind, c.Loop, c.N, c.Code, c.Start.Format("15:04:05.000"), c.Log)
		case !networkFailure.MatchString(c.Log):
			t.Errorf("npm %s (%s, command %d) was running when the node stopped and exited %d without a network error, started %s:\n%s", c.Kind, c.Loop, c.N, c.Code, c.Start.Format("15:04:05.000"), c.Log)
		default:
			failedRunning++
			t.Logf("npm %s (%s, command %d) was running when the node stopped and exited %d after %s with a network error", c.Kind, c.Loop, c.N, c.Code, c.End.Sub(c.Start).Round(100*time.Millisecond))
		}
	}
	t.Logf("npm commands running when the node stopped: %d, of them failed with a network error: %d; failed that started later: %d", running, failedRunning, failedAfter)
}

// kn2 is kn for a condition poll: an error is an empty answer.
func kn2(t testing.TB, args ...string) string {
	out, _ := kubectl(append([]string{"-n", cfg.namespace}, args...)...)
	return out
}

func failureStatuses(fs []npmK6Line) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[fmt.Sprintf("%s %d", f.Kind, f.Status)]++
	}
	return out
}

// tolerationSecondsOf is the unreachable toleration of the Deployment that owns the pod.
func tolerationSecondsOf(t testing.TB, podName string) int {
	t.Helper()
	var l struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct {
				Template struct {
					Spec struct {
						Tolerations []struct {
							Key               string
							TolerationSeconds *int
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(kn(t, "get", "deployments", "-l", "app.kubernetes.io/instance=trove", "-o", "json")), &l); err != nil {
		t.Fatal(err)
	}
	best, secs := "", -1
	for _, d := range l.Items {
		if !strings.HasPrefix(podName, d.Metadata.Name+"-") || len(d.Metadata.Name) <= len(best) {
			continue
		}
		for _, tl := range d.Spec.Template.Spec.Tolerations {
			if tl.Key == "node.kubernetes.io/unreachable" && tl.TolerationSeconds != nil {
				best, secs = d.Metadata.Name, *tl.TolerationSeconds
			}
		}
	}
	if secs < 0 {
		t.Fatalf("no unreachable toleration for %s", podName)
	}
	return secs
}

// unreachableTaintAt is when the node controller put the NoExecute taint `unreachable` on the node: the moment the
// cluster declares the node lost and the toleration clocks of its pods start (Q-HA-103).
func unreachableTaintAt(t testing.TB, node string) (time.Time, bool) {
	t.Helper()
	raw, err := kubectl("get", "node", node, "-o", "json")
	if err != nil {
		return time.Time{}, false
	}
	var n struct {
		Spec struct {
			Taints []struct {
				Key, Effect string
				TimeAdded   *time.Time
			}
		}
	}
	if json.Unmarshal([]byte(raw), &n) != nil {
		return time.Time{}, false
	}
	for _, tn := range n.Spec.Taints {
		if tn.Key == "node.kubernetes.io/unreachable" && tn.Effect == "NoExecute" && tn.TimeAdded != nil {
			return *tn.TimeAdded, true
		}
	}
	return time.Time{}, false
}

// readyRoles counts the ready, not terminating api, packages and worker pods that are not on the lost node: the status of
// a pod on an unreachable node is stale, so it is never counted.
func readyRoles(t testing.TB, lost string) (api, packages, worker int) {
	t.Helper()
	for _, p := range livePods(t, selTrove) {
		if p.Node == lost || !p.Ready {
			continue
		}
		switch {
		case strings.HasPrefix(p.Name, "trove-api-"):
			api++
		case strings.HasPrefix(p.Name, "trove-packages-"):
			packages++
		default:
			worker++
		}
	}
	return
}

// clusterWhole waits until PostgreSQL has two ready instances in a healthy state and Valkey has both pods and all three
// Sentinels ready: what a returned node has to bring back before the next scenario.
func clusterWhole(t testing.TB) {
	t.Helper()
	waitFor(t, "PostgreSQL, Valkey and the Sentinels to be whole", 10*time.Minute, 3*time.Second, func() bool {
		ready := strings.TrimSpace(kn2(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.readyInstances}"))
		phase := strings.TrimSpace(kn2(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.phase}"))
		vk := strings.TrimSpace(kn2(t, "get", "statefulset", "trove-valkey", "-o", "jsonpath={.status.readyReplicas}"))
		sn := strings.TrimSpace(kn2(t, "get", "statefulset", "trove-valkey-sentinel", "-o", "jsonpath={.status.readyReplicas}"))
		_, sentinelOK := sentinelPrimary()
		return ready == "2" && phase == "Cluster in healthy state" && vk == "2" && sn == "3" && sentinelOK
	})
}

// ── R-HA-052: PostgreSQL primary loss and Valkey primary loss under the npm load ─

func TestHASuite_PostgresPrimaryLoss(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-009 R-HA-015 the PostgreSQL primary force-deleted under the npm load: k6 failures only within retry.max_elapsed and all 503, every npm command exits 0, recovery without intervention", func(t *testing.T) {
		var info struct {
			RetryMaxElapsedMS int64 `json:"retry_max_elapsed_ms"`
		}
		if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
			t.Fatal(err)
		}
		maxElapsed := time.Duration(info.RetryMaxElapsedMS) * time.Millisecond
		r := runNPMFault(t, h, "pg-primary-loss", haFullLoad(), time.Minute, time.Minute, func() { script(t, "pg-kill.sh") })
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		k := r.res.K6
		var first, last time.Time
		for _, f := range k.Failures {
			if f.Status != 503 {
				t.Errorf("R-HA-052: a k6 request failed with %d, not 503: %+v", f.Status, f)
			}
			if f.At.After(r.faultAt.Add(maxElapsed)) {
				t.Errorf("R-HA-052: a k6 request failed %s after the kill, later than retry.max_elapsed (%s): %+v", f.At.Sub(r.faultAt).Round(100*time.Millisecond), maxElapsed, f)
			}
			if first.IsZero() || f.At.Before(first) {
				first = f.At
			}
			last = f.At
		}
		if n := len(k.Failures); n > 0 {
			t.Logf("%d of %d k6 requests failed (%v), from %s to %s after the kill, retry.max_elapsed %s", n, k.Total, failureStatuses(k.Failures), first.Sub(r.faultAt).Round(100*time.Millisecond), last.Sub(r.faultAt).Round(100*time.Millisecond), maxElapsed)
		} else {
			t.Logf("no k6 request failed over %d requests", k.Total)
		}
		for _, f := range k.Failures {
			if f.At.After(r.faultEnd) {
				t.Errorf("R-HA-052: a k6 request failed after the cluster reported healthy: %+v", f)
			}
		}
		if secs := k.Last.Sub(k.First).Seconds(); secs < 20 || k.Total < int(0.5*haK6Rate*secs) {
			t.Errorf("k6 sent %d requests in %.0f s", k.Total, secs)
		}
		if code, _, body, err := edge("/api/v1/system/ping"); err != nil || code != 200 {
			t.Errorf("ping after the recovery: %d %q %v", code, body, err)
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

func TestHASuite_ValkeyPrimaryLoss(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 the Valkey primary force-deleted under the npm load: every npm command exits 0, k6 only 2xx and 304, /readyz 200 throughout, valkey degraded for at most down-after-milliseconds + 15 s on a replica", func(t *testing.T) {
		const downAfter = 5 * time.Second // down-after-milliseconds of the local profile's Sentinels (valkey.yaml)
		r := runNPMFault(t, h, "valkey-primary-loss", haFullLoad(), time.Minute, time.Minute, func() { script(t, "valkey-kill.sh") })
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("R-HA-057: /readyz answered non-200 during the Valkey primary loss: %v", r.mon.ready)
		}
		// the monitor asks each replica about once a second; a replica's degraded samples are its seconds of degraded
		limit := int((downAfter+15*time.Second)/time.Second) + 3
		pods := sortedKeys(r.mon.degrade)
		for _, p := range pods {
			if n := r.mon.degrade[p]; n > limit {
				t.Errorf("R-HA-057: %s reported valkey degraded in %d samples, bound down-after-milliseconds + 15 s = %d s (+ 3 samples of polling)", p, n, limit-3)
			}
		}
		t.Logf("replicas that reported degraded while ready (samples about one second apart): %v", r.mon.degrade)
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}
