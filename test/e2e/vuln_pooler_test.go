//go:build e2e

package e2e

// The blocks of the vulnerability scanning through drain variant D of R-HA-059 (E5.13): an instance of the request pooler on the
// drained node. The set `vuln-pooler` runs the cluster in the pooler profile (TROVE_POOLER=1, deploy/dev/pooler.yaml,
// values-pooler.yaml, R-HA-058); the harness that picks the node and drains it is drain_variants_test.go.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	poolerRW      = "trove-db-pooler-rw"
	poolerControl = "trove-db-pooler-control"
)

// poolerSel selects the pods of one Pooler by the label the operator puts on them.
func poolerSel(name string) string { return "cnpg.io/poolerName=" + name }

func nodeNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// requirePoolerProfile fails (it never skips) unless the release reaches PostgreSQL through the two poolers of R-HA-058 and each
// pooler meets rule 5 of R-HA-059: two ready instances on two nodes and PgBouncer 1.23 or later.
func requirePoolerProfile(t *testing.T) {
	t.Helper()
	conf := kn(t, "get", "configmap", "trove-config", "-o", "jsonpath={.data.trove\\.yaml}")
	for _, want := range []string{
		`mode: transaction`,
		`host: ` + poolerRW + `.` + cfg.namespace + `.svc`,
		`control_host: ` + poolerControl + `.` + cfg.namespace + `.svc`,
		`session_host: trove-db-rw.` + cfg.namespace + `.svc`,
	} {
		if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(want) + `\s*$`).MatchString(strings.ReplaceAll(conf, `"`, "")) {
			t.Fatalf("the release is not in the pooler profile: %q is not in the trove ConfigMap (start the cluster with TROVE_POOLER=1):\n%s", want, conf)
		}
	}
	for _, name := range []string{poolerRW, poolerControl} {
		var ready []pod
		nodes := map[string]bool{}
		for _, p := range livePods(t, poolerSel(name)) {
			if p.Ready {
				ready = append(ready, p)
				nodes[p.Node] = true
			}
		}
		if len(ready) != 2 || len(nodes) != 2 {
			t.Fatalf("R-HA-059 rule 5: pooler %s has %d ready instances on %d nodes (%v), want 2 on 2", name, len(ready), len(nodes), podNames(ready))
		}
		// the PodDisruptionBudget selects the pooler's pods (expectedPods counts what it selects): two, or it protects something else
		if got := strings.TrimSpace(kn(t, "get", "poddisruptionbudget", name, "-o", "jsonpath={.status.expectedPods}")); got != "2" {
			t.Fatalf("R-HA-059 rule 5: the PodDisruptionBudget %s selects %q pods, want 2", name, got)
		}
		out := kn(t, "exec", ready[0].Name, "-c", "pgbouncer", "--", "pgbouncer", "--version")
		m := regexp.MustCompile(`(\d+)\.(\d+)\.\d+`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("pooler %s: cannot read the PgBouncer version from %q", name, strings.TrimSpace(out))
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		t.Logf("pooler %s: %s on %v", name, strings.TrimSpace(out), nodeNames(nodes))
		if major < 1 || (major == 1 && minor < 23) {
			t.Fatalf("R-HA-059 rule 5: pooler %s runs PgBouncer %s, want 1.23 or later", name, m[0])
		}
	}
}

// TestVulnPooler_R_VULN_015_BlockHoldsThroughDrainVariantD is E5.13's "a malicious version stays blocked on every replica
// throughout drain variants A to E" for D: four versions (npm and NuGet, blocked in triage and malicious) are requested through the
// edge by four loops while a node that hosts an instance of the request pooler is drained (drain.sh --also pooler). The pooler's
// instance stops gracefully: its preStop sleep of 90 s waits until trove's pooled connections have aged out (60 s + 15 s lifetime and
// jitter + 5 s health check = 80 s, grace period 120 s, R-HA-058), so every answer must be the refusal of R-VULN-016 from a replica, and the replacement must refuse from its first request.
// The variant has no condition of its own beyond R-HA-059's: no SIGKILL, nothing Pending for more than 60 s, the drain within 10 minutes.
func TestVulnPooler_R_VULN_015_BlockHoldsThroughDrainVariantD(t *testing.T) {
	e := vulnFx(t)
	requirePoolerProfile(t)
	probes := e.blockedPathsLabeled(t, "poolerd")

	t.Run("R-HA-059 R-HA-058 R-VULN-015 R-VULN-027 E5.13 drain variant D (an instance of the request pooler on the node): every replica refuses, the replacement from its first request", func(t *testing.T) {
		troveSettled(t, 3, 3, 4)
		d := prepareVariantDrain(t, variantD)
		// the pick may have moved the PostgreSQL and the Valkey primary: the replicas are unready for a few seconds after that,
		// and the probes are the drain's, not the pick's
		troveSettled(t, 3, 3, 4)
		start := time.Now()
		tally := probeDuring(t, probes, 20*time.Second, 40*time.Second, func() { d.Run(t) })
		t.Logf("the probes ran %s, the drain included", time.Since(start).Round(time.Second))
		// the three packages replicas and the replacement of the one on the node
		tally.report(t, 4)
		d.Verify(t)
		d.Settle(t)
	})
}
