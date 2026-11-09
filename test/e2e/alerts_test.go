//go:build e2e

package e2e

// The alert tests (exit criterion E5.5, docs/developer/e2e.md#the-edge-and-alert-tests): the PrometheusRule of the chart (R-HA-045) against a
// Prometheus that scrapes the profile through the chart's ServiceMonitor (profile `alerts`, deploy/dev/up.sh, values-alerts.yaml), in the scenarios
// of R-HA-052 and R-OPS-046 that name an alert, and the metric bullets of R-OPS-045's failover, outage and slow-Valkey criteria.
//
// The order is the order of the file, and it matters: "Maintenance stays quiet" runs first, because the alert of three Valkey failovers within an
// hour (TroveValkeyFailoverFlapping) stays firing for an hour after its own scenario. Each scenario that fires an alert leaves the cluster whole
// again and waits for the alert to resolve.
//
// Reading the Prometheus: its web interface is plain HTTP inside the cluster, so the tests reach it through the API server's Service proxy, as
// they reach the pods' ops listeners. What the Prometheus scrapes is trove's ops listener over TLS, verified against the dev CA by the
// ServiceMonitor's tlsConfig.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const promProxy = "/api/v1/namespaces/monitoring/services/prometheus-kube-prometheus-prometheus:9090/proxy"

func promGet(path string, q url.Values) ([]byte, error) {
	u := cfg.proxy + promProxy + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	code, _, body, err := httpGet(u)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("GET %s answered %d: %s", path, code, tail(body, 300))
	}
	return []byte(body), nil
}

type promSample struct {
	Labels map[string]string
	Value  float64
}

// promVector evaluates an instant query.
func promVector(expr string) ([]promSample, error) {
	raw, err := promGet("/api/v1/query", url.Values{"query": {expr}})
	if err != nil {
		return nil, err
	}
	var r struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("query %q: %w", expr, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("query %q: %s", expr, tail(string(raw), 300))
	}
	var out []promSample
	for _, it := range r.Data.Result {
		s, _ := it.Value[1].(string)
		v, _ := strconv.ParseFloat(s, 64)
		out = append(out, promSample{Labels: it.Metric, Value: v})
	}
	return out, nil
}

// alertStates maps the name of every alert of the chart that is pending or firing to its state ("firing" wins over "pending").
func alertStates() (map[string]string, error) {
	raw, err := promGet("/api/v1/alerts", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
				State  string            `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range r.Data.Alerts {
		n := a.Labels["alertname"]
		if !strings.HasPrefix(n, "Trove") {
			continue
		}
		if out[n] != "firing" {
			out[n] = a.State
		}
	}
	return out, nil
}

func firingAlerts() ([]string, error) {
	st, err := alertStates()
	if err != nil {
		return nil, err
	}
	var out []string
	for n, s := range st {
		if s == "firing" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// waitFiring waits until the alert fires.
func waitFiring(t testing.TB, name string, within time.Duration) time.Duration {
	t.Helper()
	return waitFor(t, "the alert "+name+" to fire", within, 5*time.Second, func() bool {
		st, err := alertStates()
		return err == nil && st[name] == "firing"
	})
}

// waitResolved waits until the alert is neither pending nor firing.
func waitResolved(t testing.TB, name string, within time.Duration) {
	t.Helper()
	waitFor(t, "the alert "+name+" to resolve", within, 5*time.Second, func() bool {
		st, err := alertStates()
		return err == nil && st[name] == ""
	})
}

var (
	alertsOnce sync.Once
	alertsErr  error
)

// alertsInherited are the alerts that fire when the fixture is ready, before any scenario. On the fresh cluster of CI there are none; on a cluster that
// earlier suites used they are the residue of those suites (the claimed-name match of the vulnerability tests stays in the database, for one), and
// "Maintenance stays quiet" ignores exactly these names, which no maintenance step can have caused.
var alertsInherited []string

// alertsFx puts the cluster into the profile `alerts`, waits for the Prometheus to scrape every replica and to load the rules, and records the alerts
// that already fire (alertsInherited).
func alertsFx(t testing.TB) {
	t.Helper()
	alertsOnce.Do(func() {
		alertsErr = fmt.Errorf("the alerts fixture did not finish (see the first failure)")
		alertsErr = buildAlertsFixture(t)
	})
	if alertsErr != nil {
		t.Fatalf("alerts fixture: %v", alertsErr)
	}
}

func buildAlertsFixture(t testing.TB) error {
	switchProfile(t, "alerts")
	for _, d := range []string{"trove-api", "trove-packages", "trove-worker"} {
		kn(t, "rollout", "status", "deployment/"+d, "--timeout=5m")
	}
	waitServingSettled(t)
	waitFor(t, "the rules of the chart to be loaded by the Prometheus", 4*time.Minute, 3*time.Second, func() bool {
		raw, err := promGet("/api/v1/rules", url.Values{"type": {"alert"}})
		return err == nil && strings.Contains(string(raw), "TroveValkeyUnavailable") && strings.Contains(string(raw), "TroveBackupStale")
	})
	want := len(livePods(t, selTrove))
	waitFor(t, "the Prometheus to scrape every replica", 5*time.Minute, 5*time.Second, func() bool {
		v, err := promVector(`count(trove_valkey_available{namespace="` + cfg.namespace + `"})`)
		return err == nil && len(v) == 1 && int(v[0].Value) == want
	})
	waitFor(t, "kube-state-metrics series of the Deployments", 4*time.Minute, 5*time.Second, func() bool {
		v, err := promVector(`count(kube_deployment_status_replicas_available{namespace="` + cfg.namespace + `",deployment=~"trove-.*"})`)
		return err == nil && len(v) == 1 && v[0].Value >= 3
	})
	// two minutes for what the start itself set off (a rollout, a scrape that began late) to settle
	deadline := time.Now().Add(2 * time.Minute)
	for {
		f, err := firingAlerts()
		if err == nil && len(f) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("the alerts cannot be read: %w", err)
			}
			alertsInherited = f
			return nil
		}
		time.Sleep(10 * time.Second)
	}
}

// runUpWith runs deploy/dev/up.sh for the profile with more environment (TROVE_HELM_EXTRA, for example).
func runUpWith(t testing.TB, profile string, env ...string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(os.Getenv("TROVE_E2E_DEV_DIR"), "up.sh"))
	cmd.Dir = cfg.repo
	cmd.Env = append(append(os.Environ(), "TROVE_PROFILE="+profile), env...)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "profile-"+profile+"-alerts.log"), out, 0o644)
	if err != nil {
		t.Fatalf("up.sh with TROVE_PROFILE=%s %v: %v\n%s", profile, env, err, tail(string(out), 2000))
	}
	profileMu.Lock()
	currentProfile = profile
	profileMu.Unlock()
}

// background runs a command and returns a function that waits for it and returns its error.
func background(t testing.TB, name string, args ...string) func() error {
	t.Helper()
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev", name), args...)
	cmd.Dir = cfg.repo
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() error {
		err := cmd.Wait()
		t.Logf("%s %s:\n%s", name, strings.Join(args, " "), strings.TrimSpace(buf.String()))
		return err
	}
}

// valkeyFailovers is the largest number of primary changes any replica counted since the baseline.
func valkeyFailovers(t *testing.T, base map[string]float64) float64 {
	t.Helper()
	most := 0.0
	for pod, v := range replicaMetric(t, "trove_valkey_primary_changes_total") {
		if d := v - base[pod]; d > most {
			most = d
		}
	}
	return most
}

// TestAlerts_R_OPS_046_MaintenanceStaysQuiet (R-OPS-046, R-HA-052 "Maintenance stays quiet", R-HA-045): the designed maintenance paths fire no
// alert of the chart: the drain variants A, B, C and E of R-HA-059, the planned failover of R-HA-057 and the rolling restart of every role, with the
// Prometheus scraping through the ServiceMonitor. The window is read back from the Prometheus: no alert of the chart was firing at any evaluation
// since the first step began. The exception is the one alert whose definition is more than two Valkey failovers in an hour
// (TroveValkeyFailoverFlapping): when the steps and the moves that prepare them changed the Valkey primary more than twice within the hour, it fires by its own
// rule, and the test says so instead of reading that against maintenance. Two changes do not fire it (Q-OPS-198: the expression counts, it does not extrapolate).
func TestAlerts_R_OPS_046_MaintenanceStaysQuiet(t *testing.T) {
	alertsFx(t)
	start := time.Now()
	failBase := replicaMetric(t, "trove_valkey_primary_changes_total")

	variant := func(v drainVariant) {
		t.Run("R-HA-059 drain variant "+string(v)+" fires no alert", func(t *testing.T) {
			d := prepareVariantDrain(t, v)
			d.Run(t)
			// The variant's own conditions (no SIGKILL, nothing Pending, the time of the drain) are the subject of the drain tests, which run
			// the variants under load; here the drain has to have happened, and the alerts are read afterwards (H-798).
			if d.runErr != nil {
				t.Errorf("variant %s: drain.sh failed: %v\n%s", v, d.runErr, tail(d.runOut, 2000))
			}
			d.watch.stop()
			d.Settle(t)
			waitServingSettled(t)
		})
	}
	variant(variantA)
	variant(variantE)
	variant(variantC)
	variant(variantB)

	t.Run("R-HA-057 the planned failover of Valkey fires no alert", func(t *testing.T) {
		script(t, "valkey-failover.sh")
		waitServingSettled(t)
	})
	t.Run("R-HA-052 a rolling restart of every role fires no alert", func(t *testing.T) {
		for _, d := range []string{"trove-api", "trove-packages", "trove-worker"} {
			kn(t, "rollout", "restart", "deployment/"+d)
			kn(t, "rollout", "status", "deployment/"+d, "--timeout=10m")
		}
		waitServingSettled(t)
	})

	// two evaluations and two scrapes after the last step: an alert that needs a `for` has had its time
	time.Sleep(90 * time.Second)
	window := int(time.Since(start).Seconds()) + 60
	fired, err := promVector(fmt.Sprintf(`max by (alertname) (max_over_time(ALERTS{alertstate="firing",alertname=~"Trove.*"}[%ds]))`, window))
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := promVector(fmt.Sprintf(`max by (alertname) (max_over_time(ALERTS{alertstate="pending",alertname=~"Trove.*"}[%ds]))`, window))
	for _, p := range pending {
		t.Logf("pending at some point of the window (not firing, so allowed): %s", p.Labels["alertname"])
	}
	flips := valkeyFailovers(t, failBase)
	t.Logf("the Valkey primary changed %.0f times on the replica that counted most during the window of %ds", flips, window)
	if len(alertsInherited) > 0 {
		t.Logf("alerts that fired before the first step and are left out of the reading: %v", alertsInherited)
	}
	for _, f := range fired {
		name := f.Labels["alertname"]
		if slices.Contains(alertsInherited, name) {
			continue
		}
		if name == "TroveValkeyFailoverFlapping" && flips > 2 {
			t.Logf("%s fired by its own definition: %.0f primary changes within the hour", name, flips)
			continue
		}
		t.Errorf("R-OPS-046: the alert %s fired during maintenance", name)
	}
}

// TestAlerts_R_OPS_046_ValkeyOutage (R-OPS-046, R-OPS-045, R-HA-052 "Valkey outage"): Valkey and the Sentinels scaled to 0 for 5 minutes fire
// TroveValkeyUnavailable, every replica reports trove_valkey_available 0 and the degraded counters grow; after the return the alert resolves.
func TestAlerts_R_OPS_046_ValkeyOutage(t *testing.T) {
	alertsFx(t)
	before := replicaSum(t, "trove_valkey_degraded_total")
	wait := background(t, "valkey-outage.sh", "5m")
	took := waitFiring(t, "TroveValkeyUnavailable", 5*time.Minute)
	t.Logf("TroveValkeyUnavailable fired %s into the outage", took.Round(time.Second))
	t.Run("R-OPS-045 trove_valkey_available is 0 on every replica and the degraded counters grow", func(t *testing.T) {
		for pod, v := range replicaMetric(t, "trove_valkey_available") {
			if v != 0 {
				t.Errorf("%s reports trove_valkey_available %v during the outage", pod, v)
			}
		}
		if now := replicaSum(t, "trove_valkey_degraded_total"); now <= before {
			t.Errorf("trove_valkey_degraded_total did not grow during the outage: %v then, %v now", before, now)
		}
	})
	if err := wait(); err != nil {
		t.Fatalf("valkey-outage.sh: %v", err)
	}
	waitServingSettled(t)
	waitResolved(t, "TroveValkeyUnavailable", 6*time.Minute)
	waitResolved(t, "TroveValkeySentinelsDegraded", 8*time.Minute)
	t.Run("R-OPS-045 every replica uses Valkey again", func(t *testing.T) {
		waitFor(t, "trove_valkey_available 1 on every replica", 3*time.Minute, 3*time.Second, func() bool {
			for _, v := range replicaMetric(t, "trove_valkey_available") {
				if v != 1 {
					return false
				}
			}
			return true
		})
	})
}

// TestAlerts_R_OPS_046_PostgreSQLOutage (R-OPS-046, R-HA-052 "PostgreSQL outage"): PostgreSQL fenced for 3 minutes while a client reads the
// management API fires TroveDatabaseUnreachable (connection retries and 503s together, for 2 minutes), and the alert resolves after the return.
func TestAlerts_R_OPS_046_PostgreSQLOutage(t *testing.T) {
	alertsFx(t)
	adm := adminAPI(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = adm.send("GET", "/api/v1/projects", nil)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	wait := background(t, "pg-stop.sh", "3m")
	took := waitFiring(t, "TroveDatabaseUnreachable", 6*time.Minute)
	t.Logf("TroveDatabaseUnreachable fired %s into the outage", took.Round(time.Second))
	if err := wait(); err != nil {
		t.Errorf("pg-stop.sh: %v", err)
	}
	close(stop)
	wg.Wait()
	waitServingSettled(t)
	waitResolved(t, "TroveDatabaseUnreachable", 8*time.Minute)
}

// replicaSum is the sum of one series over the replicas, by prefix (the labels of the series are summed).
func replicaSum(t *testing.T, prefix string) float64 {
	t.Helper()
	sum := 0.0
	for _, p := range livePods(t, selTrove) {
		for name, v := range metricsOf(t, p.Name) {
			if strings.HasPrefix(name, prefix) {
				sum += v
			}
		}
	}
	return sum
}

// TestAlerts_R_OPS_046_OneReplicaCutOffFromValkey (R-OPS-046, R-HA-052 "Replica bound to a stale primary" is another scenario; this is the row "HA
// Valkey, unavailable for one replica"): a NetworkPolicy that denies the egress of one packages pod to the Valkey pods fires
// TroveValkeyUnavailableSingleReplica and not TroveValkeyUnavailable; lifting it resolves the alert.
func TestAlerts_R_OPS_046_OneReplicaCutOffFromValkey(t *testing.T) {
	alertsFx(t)
	pods := readyPackagesPods(t)
	if len(pods) < 2 {
		t.Fatalf("need two ready packages pods, have %v", pods)
	}
	victim := pods[0]
	kn(t, "label", "pod", victim, "e2e-cut=valkey", "--overwrite")
	policy := `{"apiVersion":"networking.k8s.io/v1","kind":"NetworkPolicy","metadata":{"name":"e2e-cut-valkey","namespace":"` + cfg.namespace + `"},
"spec":{"podSelector":{"matchLabels":{"e2e-cut":"valkey"}},"policyTypes":["Egress"],"egress":[
 {"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}}}],"ports":[{"protocol":"UDP","port":53},{"protocol":"TCP","port":53}]},
 {"to":[{"podSelector":{"matchExpressions":[{"key":"app.kubernetes.io/instance","operator":"NotIn","values":["trove-valkey"]}]}}]}]}}`
	lift := func() {
		_, _ = kubectl("-n", cfg.namespace, "delete", "networkpolicy", "e2e-cut-valkey", "--ignore-not-found")
		_, _ = kubectl("-n", cfg.namespace, "label", "pod", victim, "e2e-cut-")
	}
	t.Cleanup(lift)
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(policy)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply the NetworkPolicy: %v: %s", err, out)
	}
	// A policy applies to new connections; the ones the pod holds stay open. Closing them from the Valkey side makes the pod reconnect, into the policy.
	ip := ""
	for _, p := range podsOf(t, selPackages) {
		if p.Name == victim {
			ip = p.IP
		}
	}
	for _, line := range strings.Split(valkeyPrimaryCLI(t, "client", "list"), "\n") {
		if !strings.Contains(line, " addr="+ip+":") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if id, ok := strings.CutPrefix(f, "id="); ok {
				valkeyPrimaryCLI(t, "client", "kill", "id", id)
			}
		}
	}
	waitFor(t, victim+" to lose Valkey", 3*time.Minute, 3*time.Second, func() bool { return metricsOf(t, victim)["trove_valkey_available"] == 0 })
	for _, p := range pods[1:] {
		if v := metricsOf(t, p)["trove_valkey_available"]; v != 1 {
			t.Fatalf("%s lost Valkey too (%v): the cut reaches more than one pod", p, v)
		}
	}
	waitFiring(t, "TroveValkeyUnavailableSingleReplica", 8*time.Minute)
	if st, _ := alertStates(); st["TroveValkeyUnavailable"] != "" {
		t.Errorf("TroveValkeyUnavailable is %q although the other replicas use Valkey", st["TroveValkeyUnavailable"])
	}
	lift()
	waitFor(t, victim+" to use Valkey again", 3*time.Minute, 3*time.Second, func() bool { return metricsOf(t, victim)["trove_valkey_available"] == 1 })
	waitResolved(t, "TroveValkeyUnavailableSingleReplica", 4*time.Minute)
}

// TestAlerts_R_OPS_046_ValkeyFailoverFlapping (R-OPS-046, R-OPS-045, R-HA-052 "Valkey failover flapping"): three SENTINEL FAILOVER within an
// hour fire TroveValkeyFailoverFlapping. Each failover raises trove_valkey_primary_changes_total by one on every replica and the subscription of
// the fast path and the agreement on the primary are back within the bounds of R-OPS-045. The alert stays firing until the hour is over.
func TestAlerts_R_OPS_046_ValkeyFailoverFlapping(t *testing.T) {
	alertsFx(t)
	for i := 1; i <= 3; i++ {
		before := replicaMetric(t, "trove_valkey_primary_changes_total")
		script(t, "valkey-failover.sh")
		t.Run(fmt.Sprintf("R-OPS-045 failover %d raises trove_valkey_primary_changes_total by one on every replica", i), func(t *testing.T) {
			waitFor(t, "the primary change on every replica", 2*time.Minute, 2*time.Second, func() bool {
				for pod, v := range replicaMetric(t, "trove_valkey_primary_changes_total") {
					if v < before[pod]+1 {
						return false
					}
				}
				return true
			})
			for pod, v := range replicaMetric(t, "trove_valkey_primary_changes_total") {
				if v != before[pod]+1 {
					t.Errorf("%s counted %v primary changes for one failover", pod, v-before[pod])
				}
			}
			// valkey.sentinel.refresh_interval (5s) plus 1 s, with the scrape of the test's own read in it
			waitFor(t, "trove_valkey_pubsub_subscribed 1 on every replica", 15*time.Second, time.Second, func() bool {
				for _, v := range replicaMetric(t, "trove_valkey_pubsub_subscribed") {
					if v != 1 {
						return false
					}
				}
				return true
			})
			// system.heartbeat_interval (10s) plus metrics.db_gauge_interval (30s)
			waitFor(t, "trove_valkey_primary_views back to 1", 60*time.Second, 2*time.Second, func() bool {
				for _, v := range replicaMetric(t, "trove_valkey_primary_views") {
					if v > 1 {
						return false
					}
				}
				return true
			})
		})
		time.Sleep(15 * time.Second)
	}
	took := waitFiring(t, "TroveValkeyFailoverFlapping", 5*time.Minute)
	t.Logf("TroveValkeyFailoverFlapping fired %s after the third failover", took.Round(time.Second))
}

// TestAlerts_R_OPS_045_SlowValkey (R-OPS-045, R-HA-052 "Slow Valkey"): every Valkey command held for 4 seconds (CLIENT PAUSE, the injection of
// E1.8; the profile has no fault proxy) makes the calls time out: trove_valkey_errors_total{kind="timeout"} grows and the circuit opens on a
// replica.
func TestAlerts_R_OPS_045_SlowValkey(t *testing.T) {
	alertsFx(t)
	timeouts := func() float64 {
		sum := 0.0
		for _, p := range livePods(t, selTrove) {
			for name, v := range metricsOf(t, p.Name) {
				if strings.HasPrefix(name, "trove_valkey_errors_total") && strings.Contains(name, `kind="timeout"`) {
					sum += v
				}
			}
		}
		return sum
	}
	before := timeouts()
	paused := make(chan struct{})
	go func() {
		defer close(paused)
		_, _ = exec.Command(filepath.Join(cfg.repo, "deploy/dev/valkey-cli.sh"), "client", "pause", "4000", "all").CombinedOutput()
	}()
	// requests whose limiter asks Valkey, from several clients at once, and a sampler per replica that reads the circuit's gauge while the pause lasts
	stopLoad := make(chan struct{})
	var wg sync.WaitGroup
	var opened atomic.Bool
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := newAPI(t)
			for {
				select {
				case <-stopLoad:
					return
				default:
				}
				_, _ = a.send("GET", "/api/v1/system/ping", nil)
			}
		}()
	}
	for _, p := range livePods(t, selServing) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopLoad:
					return
				default:
				}
				if _, body := ops(p.Name, "/metrics"); strings.Contains(body, "\ntrove_valkey_circuit_open 1\n") {
					opened.Store(true)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}
	time.Sleep(9 * time.Second)
	close(stopLoad)
	wg.Wait()
	<-paused
	if after := timeouts(); after <= before {
		t.Errorf("trove_valkey_errors_total{kind=\"timeout\"} did not grow during the pause: %v then, %v now", before, after)
	}
	if !opened.Load() {
		t.Error("no replica's Valkey circuit opened during the pause")
	}
	waitServingSettled(t)
}

// TestAlerts_R_OPS_046_RoleBelowTwoReplicas (R-OPS-046 "Drain without outage, a role below two ready replicas", R-HA-045): the Deployment of the
// worker scaled to one replica fires TroveReplicasNotReady for it, and only for it; scaled back, the alert resolves. The PodDisruptionBudgets keep
// a drain from doing this; the scale is what an operator's mistake or a lost node pair would do.
func TestAlerts_R_OPS_046_RoleBelowTwoReplicas(t *testing.T) {
	alertsFx(t)
	kn(t, "scale", "deployment/trove-worker", "--replicas=1")
	t.Cleanup(func() { _, _ = kubectl("-n", cfg.namespace, "scale", "deployment/trove-worker", "--replicas=2") })
	waitFiring(t, "TroveReplicasNotReady", 9*time.Minute)
	v, err := promVector(`ALERTS{alertname="TroveReplicasNotReady",alertstate="firing"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		if d := s.Labels["deployment"]; d != "trove-worker" {
			t.Errorf("TroveReplicasNotReady fires for %s, which still has its replicas", d)
		}
	}
	kn(t, "scale", "deployment/trove-worker", "--replicas=2")
	kn(t, "rollout", "status", "deployment/trove-worker", "--timeout=5m")
	waitResolved(t, "TroveReplicasNotReady", 4*time.Minute)
}

// TestAlerts_R_OPS_046_BackupFailedAndStale (R-OPS-046, R-BAK-014, R-HA-045): a backup run whose process is killed with kill -9 from the node during the copy is
// left with an expired lease, which the gauge counts as a failure (the Job ends Failed with backoffLimit 0): TroveBackupFailed fires and a later
// successful run resolves it. With the expected interval lowered to its minimum of a minute and the CronJob suspended, the age of the last success
// passes one and a half intervals and TroveBackupStale fires; the profile with its own interval puts it right. The scenario names a run that exits
// 1; the abandoned run is the failure this suite can make without a defect of the product (Q-OPS-196).
func TestAlerts_R_OPS_046_BackupFailedAndStale(t *testing.T) {
	alertsFx(t)
	adm := adminAPI(t)
	repo := "alerts-backup"
	ensureRepo(t, adm, repo)
	run := bakRun()
	for i := range 16 {
		if r, err := retrying(func() (reply, error) {
			return adm.send("PUT", filesAddr(repo)+fmt.Sprintf("/alerts-%s/%02d.bin", run, i), randomBytes(1<<20), "Content-Type", "application/octet-stream")
		}); err != nil || r.Status != 201 {
			t.Fatalf("put file %d: %v %s", i, err, r)
		}
	}
	zero := 0
	job := "alerts-bak-kill-" + run
	bakCreateJob(t, bakJob{name: job, backoff: &zero, env: map[string]string{
		"TROVE_BACKUP_TARGET_PREFIX": bakFreshPrefix(run, "alerts"), "TROVE_BACKUP_COPY_BYTES_PER_SECOND": "256KiB", "TROVE_BACKUP_COPY_CONCURRENCY": "1",
		"TROVE_BACKUP_LEASE_DURATION": "20s", "TROVE_BACKUP_HEARTBEAT_INTERVAL": "2s", "TROVE_BACKUP_FINALIZE_TIMEOUT": "5s",
	}})
	bakWaitLog(t, cfg.namespace, job, "backup run started", 3*time.Minute)
	time.Sleep(5 * time.Second)
	pod := bakPodOf(t, cfg.namespace, job)
	// kill -9 of the process itself, from the node: a deleted pod gets a termination signal first and the run records itself `interrupted`, which is
	// no failure (R-BAK-012); a process that vanishes leaves the run active with a lease that runs out.
	node := podNode(t, cfg.namespace, pod)
	killScript := `for p in /proc/[0-9]*; do if tr '\0' ' ' < $p/cmdline 2>/dev/null | grep -q '[b]ackup create'; then kill -9 ${p#/proc/}; fi; done`
	if out, err := exec.Command("docker", "exec", node, "sh", "-c", killScript).CombinedOutput(); err != nil {
		t.Fatalf("kill -9 of the backup process on %s: %v: %s", node, err, out)
	}
	if end := bakWaitJob(t, cfg.namespace, job, 5*time.Minute); end.Condition != "Failed" {
		t.Fatalf("the killed run's Job ended %s, want Failed", end.Condition)
	}
	t.Run("R-BAK-014 TroveBackupFailed fires for a run that was killed", func(t *testing.T) {
		took := waitFiring(t, "TroveBackupFailed", 5*time.Minute)
		t.Logf("TroveBackupFailed fired %s after the Job failed", took.Round(time.Second))
	})

	ok := "alerts-bak-ok-" + run
	bakCreateJob(t, bakJob{name: ok})
	if end := bakWaitJob(t, cfg.namespace, ok, 20*time.Minute); end.Condition != "Complete" {
		t.Fatalf("the successful run ended %s:\n%s", end.Condition, tail(bakJobLogs(cfg.namespace, ok), 2000))
	}
	t.Run("R-BAK-014 a successful run resolves TroveBackupFailed", func(t *testing.T) { waitResolved(t, "TroveBackupFailed", 5*time.Minute) })

	t.Run("R-BAK-014 R-OPS-046 TroveBackupStale fires with the interval at a minute and the CronJob suspended", func(t *testing.T) {
		runUpWith(t, "alerts", "TROVE_HELM_EXTRA=--set config.backup.expected_interval=1m --set backup.suspend=true")
		t.Cleanup(func() { runUpWith(t, "alerts") })
		for _, d := range []string{"trove-api", "trove-packages", "trove-worker"} {
			kn(t, "rollout", "status", "deployment/"+d, "--timeout=10m")
		}
		waitServingSettled(t)
		if got := strings.TrimSpace(kn(t, "get", "cronjob", "trove-backup", "-o", "jsonpath={.spec.suspend}")); got != "true" {
			t.Fatalf("the CronJob is not suspended: %q", got)
		}
		waitFiring(t, "TroveBackupStale", 8*time.Minute)
		runUpWith(t, "alerts")
		for _, d := range []string{"trove-api", "trove-packages", "trove-worker"} {
			kn(t, "rollout", "status", "deployment/"+d, "--timeout=10m")
		}
		waitServingSettled(t)
		waitResolved(t, "TroveBackupStale", 8*time.Minute)
	})
}
