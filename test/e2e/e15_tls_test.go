//go:build e2e

package e2e

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// E0.15, TLS on every connection, on the dev cluster: nothing answers in plaintext, and the listener
// certificate is renewed by cert-manager under load without a failed request, a restart or a stale
// certificate (R-ARCH-035, R-HA-063).

// probeScript runs inside an ephemeral container next to the trove container of one pod (image: the
// pinned openssl image, same user as trove, process namespace shared with it). Every 100 ms it reads the
// serial of the certificate file the kubelet mounts into the trove container (through /proc/1/root) and
// the serial that the pod's two listeners hand out in a TLS handshake (openssl s_client, no session
// reuse; "-" when nothing listens, as on a worker's :8080). It prints a line when any of the three changes: "<uptime s> <file> <:8080> <:8081>". The clock is
// the kernel's uptime, so lines of one container are comparable to 10 ms. The loop ends after %d seconds.
const probeScript = `
f=/proc/1/root/var/run/secrets/trove/tls/tls.crt
serial() { openssl x509 -noout -serial 2>/dev/null | cut -d= -f2; }
dial() { s="$(echo | openssl s_client -connect 127.0.0.1:"$1" -servername trove.trove.svc 2>/dev/null | serial)"; echo "${s:--}"; }
start=$(cut -d. -f1 /proc/uptime); last=""
while [ $(( $(cut -d. -f1 /proc/uptime) - start )) -lt %d ]; do
  now="$(serial < $f) $(dial 8080) $(dial 8081)"
  if [ "$now" != "$last" ]; then echo "$(cut -d' ' -f1 /proc/uptime) $now"; last="$now"; fi
  sleep 0.1
done
`

type probeLine struct {
	at                   float64
	file, client, opsSer string
}

// probeLines parses the log of a probe container.
func probeLines(logs string) []probeLine {
	var out []probeLine
	for _, l := range strings.Split(logs, "\n") {
		f := strings.Fields(l)
		if len(f) != 4 {
			continue
		}
		at, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		out = append(out, probeLine{at: at, file: strings.TrimLeft(f[1], "0"), client: strings.TrimLeft(f[2], "0"), opsSer: strings.TrimLeft(f[3], "0")})
	}
	return out
}

// firstAt is the uptime of the first line for which pick returns want, or -1.
func firstAt(ls []probeLine, want string, pick func(probeLine) string) float64 {
	for _, l := range ls {
		if pick(l) == want {
			return l.at
		}
	}
	return -1
}

// secretSerial is the serial (upper-case hex) of the leaf certificate in tls.crt of a Secret.
func secretSerial(t testing.TB, secret string) string {
	t.Helper()
	raw := kn(t, "get", "secret", secret, "-o", "json")
	var s struct{ Data map[string]string }
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(s.Data["tls.crt"])
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatalf("secret %s: no certificate in tls.crt", secret)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToUpper(c.SerialNumber.Text(16))
}

// metricSum adds up the samples of one metric family of a pod's :8081/metrics whose line contains all
// of the given fragments.
func metricSum(t testing.TB, podName, family string, fragments ...string) float64 {
	t.Helper()
	code, body := ops(podName, "/metrics")
	if code != 200 {
		t.Fatalf("%s :8081/metrics = %d", podName, code)
	}
	sum := 0.0
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, family+"{") && !strings.HasPrefix(l, family+" ") {
			continue
		}
		ok := true
		for _, f := range fragments {
			ok = ok && strings.Contains(l, f)
		}
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(l[strings.LastIndex(l, " ")+1:], 64)
		if err == nil {
			sum += v
		}
	}
	return sum
}

// configuredReloadInterval reads server.tls.reload_interval from the release's ConfigMap.
func configuredReloadInterval(t testing.TB) time.Duration {
	t.Helper()
	cm := kn(t, "get", "configmap", "trove-config", "-o", "jsonpath={.data.trove\\.yaml}")
	m := regexp.MustCompile(`(?m)^\s+reload_interval:\s*(\S+)\s*$`).FindStringSubmatch(cm)
	if m == nil {
		t.Fatalf("server.tls.reload_interval is not in the trove ConfigMap:\n%s", cm)
	}
	d, err := time.ParseDuration(strings.Trim(m[1], `"'`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// runProbePod runs a shell script in a pod of the namespace default (not selected by any trove policy),
// waits for it to finish and returns its output.
func runProbePod(t testing.TB, name, script string) string {
	t.Helper()
	image := os.Getenv("TROVE_E2E_OPENSSL_IMAGE")
	if image == "" {
		t.Fatal("TROVE_E2E_OPENSSL_IMAGE is not set; run the suite through deploy/dev/e2e.sh")
	}
	_, _ = kubectl("-n", "default", "delete", "pod", name, "--ignore-not-found", "--wait=true")
	t.Cleanup(func() { _, _ = kubectl("-n", "default", "delete", "pod", name, "--ignore-not-found", "--wait=false") })
	if _, err := kubectl("-n", "default", "run", name, "--image="+image, "--restart=Never", "--command", "--", "sh", "-c", script); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the probe pod "+name+" to finish", 3*time.Minute, 2*time.Second, func() bool {
		ph, _ := kubectl("-n", "default", "get", "pod", name, "-o", "jsonpath={.status.phase}")
		return ph == "Succeeded" || ph == "Failed"
	})
	out, _ := kubectl("-n", "default", "logs", name)
	return out
}

// isWeb: only a web replica opens the client listener :8080; a worker serves the ops listener only.
func isWeb(p pod) bool { return strings.Contains(p.Name, "-web-") }

func TestE15TLS(t *testing.T) {
	t.Run("R-ARCH-035 R-HA-063 E0.15 a plaintext request to a trove pod's :8080 and :8081 and to the object store is not answered", func(t *testing.T) {
		web := livePods(t, selWeb)[0]
		ip := strings.TrimSpace(kn(t, "get", "pod", web.Name, "-o", "jsonpath={.status.podIP}"))
		script := fmt.Sprintf(`for u in http://%[1]s:8080/api/v1/system/ping http://%[1]s:8081/readyz http://trove-s3.%[2]s.svc:9000/; do `+
			`line="$(wget -T 5 -S -qO- "$u" 2>&1 | grep -m1 'HTTP/')"; echo "$u => ${line:-no response}"; done`, ip, cfg.namespace)
		out := runProbePod(t, "e2e-plaintext-probe", script)
		t.Logf("plaintext requests from namespace default:\n%s", strings.TrimSpace(out))
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.Contains(l, " 200 ") || strings.HasSuffix(strings.TrimSpace(l), " 200") {
				t.Errorf("a plaintext request was answered with 200: %s", l)
			}
		}
		if got := strings.Count(out, "=>"); got != 3 {
			t.Errorf("the probe reported %d of 3 requests: %q", got, out)
		}
	})

	t.Run("R-ARCH-035 R-HA-063 E0.15 the listener certificate is renewed under load: only 200, the new certificate is served within reload_interval + 1 s on every pod, reloads are counted, no restart", func(t *testing.T) {
		interval := configuredReloadInterval(t)
		bound := interval + time.Second
		image := os.Getenv("TROVE_E2E_OPENSSL_IMAGE")
		if image == "" {
			t.Fatal("TROVE_E2E_OPENSSL_IMAGE is not set; run the suite through deploy/dev/e2e.sh")
		}
		before := secretSerial(t, "trove-tls")
		pods := livePods(t, selTrove)
		if len(pods) < 4 {
			t.Fatalf("%d trove pods, want at least 4", len(pods))
		}
		type base struct {
			uid     string
			restart int
			start   float64
			reloads float64
			errors  float64
		}
		bases := map[string]base{}
		for _, p := range pods {
			bases[p.Name] = base{
				p.UID, p.Restarts, metricSum(t, p.Name, "process_start_time_seconds"),
				metricSum(t, p.Name, "trove_tls_reloads_total", `kind="listener"`), metricSum(t, p.Name, "trove_tls_reload_errors_total", `kind="listener"`),
			}
		}

		// One probe container per pod, with a unique name (an ephemeral container cannot be removed or reused).
		custom := filepath.Join(cfg.out, "e15-probe-custom.json")
		if err := os.WriteFile(custom, []byte(`{"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"runAsGroup":65532,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"seccompProfile":{"type":"RuntimeDefault"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cname := fmt.Sprintf("e2e-tls-%d", time.Now().Unix())
		const probeSeconds = 480
		for _, p := range pods {
			if _, err := kubectl("-n", cfg.namespace, "debug", p.Name, "--image="+image, "--target=trove", "--profile=restricted",
				"--custom="+custom, "-c", cname, "--", "sh", "-c", fmt.Sprintf(probeScript, probeSeconds)); err != nil {
				t.Fatal(err)
			}
		}
		logs := func(p pod) []probeLine {
			out, _ := kubectl("-n", cfg.namespace, "logs", p.Name, "-c", cname)
			return probeLines(out)
		}
		waitFor(t, "every probe to see the current certificate on the file and both listeners", 3*time.Minute, 2*time.Second, func() bool {
			for _, p := range pods {
				ls := logs(p)
				if len(ls) == 0 {
					return false
				}
				l := ls[len(ls)-1]
				if l.file != before || l.opsSer != before || (isWeb(p) && l.client != before) {
					return false
				}
			}
			return true
		})

		k := startK6(t, "rotate", 10*time.Minute)
		time.Sleep(20 * time.Second)

		// cert-manager's own renewal trigger, what `cmctl renew` does: the Issuing condition on the Certificate.
		patch := fmt.Sprintf(`[{"op":"add","path":"/status/conditions/-","value":{"type":"Issuing","status":"True","reason":"ManuallyTriggered","message":"Certificate re-issuance manually triggered","lastTransitionTime":%q}}]`,
			time.Now().UTC().Format(time.RFC3339))
		renewedAt := time.Now()
		kn(t, "patch", "certificate", "trove-tls", "--subresource=status", "--type=json", "-p", patch)
		var after string
		waitFor(t, "cert-manager to issue a new certificate into Secret trove-tls", 3*time.Minute, 200*time.Millisecond, func() bool {
			after = secretSerial(t, "trove-tls")
			return after != before
		})
		issued := time.Since(renewedAt)
		t.Logf("renewal triggered; cert-manager wrote serial %s (was %s) after %s", after, before, issued.Round(100*time.Millisecond))

		type timing struct{ file, client, opsL float64 }
		times := map[string]timing{}
		waitFor(t, "every pod to serve the new certificate on its listeners (kubelet volume refresh plus reload_interval)", 5*time.Minute, 2*time.Second, func() bool {
			done := true
			for _, p := range pods {
				ls := logs(p)
				tm := timing{
					file:   firstAt(ls, after, func(l probeLine) string { return l.file }),
					client: firstAt(ls, after, func(l probeLine) string { return l.client }),
					opsL:   firstAt(ls, after, func(l probeLine) string { return l.opsSer }),
				}
				times[p.Name] = tm
				done = done && tm.file >= 0 && (tm.client >= 0 || !isWeb(p)) && tm.opsL >= 0
			}
			return done
		})
		total := time.Since(renewedAt)
		time.Sleep(10 * time.Second) // load continues over the swap and after it
		res := k.stop(t)
		res.only200(t)

		summary := map[string]any{
			"serial_before": before, "serial_after": after, "reload_interval_s": interval.Seconds(), "bound_s": bound.Seconds(),
			"issued_after_s": issued.Seconds(), "everywhere_after_s": total.Seconds(), "k6": res.Status,
		}
		perPod := map[string]any{}
		for _, p := range pods {
			tm := times[p.Name]
			dClient, dOps := tm.client-tm.file, tm.opsL-tm.file
			if isWeb(p) {
				t.Logf("%s: file changed at uptime %.2f s; :8080 served the new serial %.2f s later, :8081 %.2f s later (bound %s)", p.Name, tm.file, dClient, dOps, bound)
				perPod[p.Name] = map[string]float64{"file_to_client_listener_s": dClient, "file_to_ops_listener_s": dOps}
			} else {
				t.Logf("%s (worker, ops listener only): file changed at uptime %.2f s; :8081 served the new serial %.2f s later (bound %s)", p.Name, tm.file, dOps, bound)
				perPod[p.Name] = map[string]float64{"file_to_ops_listener_s": dOps}
			}
			if isWeb(p) && dClient > bound.Seconds() {
				t.Errorf("%s: the client listener served the new certificate %.2f s after the file changed, bound %s", p.Name, dClient, bound)
			}
			if dOps > bound.Seconds() {
				t.Errorf("%s: the ops listener served the new certificate %.2f s after the file changed, bound %s", p.Name, dOps, bound)
			}
		}
		summary["per_pod"] = perPod

		now := livePods(t, selTrove)
		if len(now) != len(pods) {
			t.Errorf("%d trove pods after the rotation, %d before", len(now), len(pods))
		}
		for _, p := range now {
			b, ok := bases[p.Name]
			if !ok {
				t.Errorf("pod %s is new", p.Name)
				continue
			}
			if p.UID != b.uid || p.Restarts != b.restart {
				t.Errorf("%s was replaced or restarted (uid %s -> %s, restarts %d -> %d)", p.Name, b.uid, p.UID, b.restart, p.Restarts)
			}
			if st := metricSum(t, p.Name, "process_start_time_seconds"); st != b.start {
				t.Errorf("%s: process start time changed (%v -> %v)", p.Name, b.start, st)
			}
			rl := metricSum(t, p.Name, "trove_tls_reloads_total", `kind="listener"`)
			re := metricSum(t, p.Name, "trove_tls_reload_errors_total", `kind="listener"`)
			t.Logf("%s: trove_tls_reloads_total{kind=listener} %v -> %v, trove_tls_reload_errors_total %v -> %v", p.Name, b.reloads, rl, b.errors, re)
			if rl <= b.reloads {
				t.Errorf("%s: trove_tls_reloads_total did not increase (%v -> %v)", p.Name, b.reloads, rl)
			}
			if re != b.errors {
				t.Errorf("%s: trove_tls_reload_errors_total changed (%v -> %v)", p.Name, b.errors, re)
			}
		}
		noteResult(t, "e15-rotation", summary)
		t.Logf("rotation: cert-manager issued after %s, every pod serving the new certificate %s after the trigger; k6 %v", issued.Round(time.Second), total.Round(time.Second), res.Status)
	})
}
