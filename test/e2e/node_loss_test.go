//go:build e2e

package e2e

import (
	"sort"
	"strings"
	"testing"
)

// What the loss of a node takes away from trove's own dependencies for longer than a node-loss scenario's bound, and the
// steps that put it elsewhere first (Q-HA-104, Q-HA-106): the pods of the two operators, which the dev profile runs once and
// which Kubernetes replaces only after its default toleration of 300 s, and the two primaries. The PostgreSQL standby is the
// primary's synchronous partner: while it is gone the commits wait until the CloudNativePG operator drops it from the set, which
// needs the operator and the cluster's detection time.

// nodeDependents names what runs on the node among those dependencies.
type nodeDependents struct {
	PG, Valkey, Standby, CNPG, ValkeyOp bool
	// Instance is a PostgreSQL instance of either role.
	Instance bool
}

func (d nodeDependents) blockers() int {
	n := 0
	for _, b := range []bool{d.PG, d.Valkey, d.CNPG, d.ValkeyOp} {
		if b {
			n++
		}
	}
	return n
}

func (d nodeDependents) String() string {
	var out []string
	for _, e := range []struct {
		on   bool
		name string
	}{{d.PG, "PostgreSQL primary"}, {d.Valkey, "Valkey primary"}, {d.CNPG, "CloudNativePG operator"}, {d.ValkeyOp, "Valkey operator"}, {d.Standby, "PostgreSQL standby"}} {
		if e.on {
			out = append(out, e.name)
		}
	}
	if len(out) == 0 {
		return "none of the dependencies"
	}
	return strings.Join(out, ", ")
}

// dependentsOn reads what runs on every node.
func dependentsOn(t testing.TB) map[string]nodeDependents {
	t.Helper()
	nodesOf := func(namespace, selector string) []string {
		var out []string
		for _, p := range podsOfIn(t, namespace, selector) {
			out = append(out, p.Node)
		}
		return out
	}
	out := map[string]nodeDependents{}
	mark := func(nodes []string, set func(*nodeDependents)) {
		for _, n := range nodes {
			d := out[n]
			set(&d)
			out[n] = d
		}
	}
	mark(nodesOf(cfg.namespace, "cnpg.io/cluster=trove-db"), func(d *nodeDependents) { d.Instance = true })
	mark(nodesOf(cfg.namespace, "cnpg.io/cluster=trove-db,cnpg.io/instanceRole=replica"), func(d *nodeDependents) { d.Standby = true })
	mark(nodesOf("cnpg-system", "app.kubernetes.io/name=cloudnative-pg"), func(d *nodeDependents) { d.CNPG = true })
	mark(nodesOf("valkey-operator-system", "app.kubernetes.io/component=operator"), func(d *nodeDependents) { d.ValkeyOp = true })
	mark([]string{strings.TrimSpace(kn(t, "get", "pod", pgPrimary(t), "-o", "jsonpath={.spec.nodeName}"))}, func(d *nodeDependents) { d.PG = true })
	mark([]string{strings.TrimSpace(kn(t, "get", "pod", valkeyPrimaryBySentinel(t), "-o", "jsonpath={.spec.nodeName}"))}, func(d *nodeDependents) { d.Valkey = true })
	return out
}

// clearNode puts what the loss of the node would take away for longer than the scenario's bound elsewhere, and leaves the
// node as it was found: it is cordoned while the pods are replaced, so that the scheduler cannot put them back, and
// uncordoned before the function returns. The standby stays (a node that hosts it stalls the primary's commits for the
// cluster's detection time, which the PostgreSQL rows of R-HA-052 cover); the PostgreSQL primary is switched over, which
// makes the standby the primary and the old primary the standby.
func clearNode(t *testing.T, node string) {
	t.Helper()
	d := dependentsOn(t)[node]
	t.Logf("node %s hosts: %s", node, d)
	if d.blockers() == 0 {
		return
	}
	kubectlMust(t, "cordon", node)
	defer func() {
		if out, err := kubectl("uncordon", node); err != nil {
			t.Errorf("uncordon %s: %v: %s", node, err, out)
		}
	}()
	if d.CNPG || d.ValkeyOp {
		t.Logf("deleting the operators' pods so that they start on another node")
		kubectlMust(t, "-n", "cnpg-system", "delete", "pod", "-l", "app.kubernetes.io/name=cloudnative-pg", "--wait=true")
		kubectlMust(t, "-n", "valkey-operator-system", "delete", "pod", "-l", "app.kubernetes.io/component=operator", "--wait=true")
		kubectlMust(t, "-n", "cnpg-system", "rollout", "status", "deployment", "--timeout=3m")
		kubectlMust(t, "-n", "valkey-operator-system", "rollout", "status", "deployment", "--timeout=3m")
	}
	if d.Valkey {
		t.Logf("the Valkey primary runs on %s: failing it over first", node)
		script(t, "valkey-failover.sh")
	}
	if d.PG {
		t.Logf("the PostgreSQL primary runs on %s: switching it over first", node)
		script(t, "failover.sh")
	}
	if after := dependentsOn(t)[node]; after.blockers() != 0 {
		t.Fatalf("node %s still hosts %s after they were moved", node, after)
	}
}

// pickByLeastBlockers returns the candidate with the fewest blockers, one without the standby first; ties by name.
func pickByLeastBlockers(candidates map[string]bool, deps map[string]nodeDependents) string {
	names := sortedKeys(candidates)
	sort.SliceStable(names, func(i, j int) bool {
		a, b := deps[names[i]], deps[names[j]]
		if a.blockers() != b.blockers() {
			return a.blockers() < b.blockers()
		}
		if a.Standby != b.Standby {
			return !a.Standby
		}
		return names[i] < names[j]
	})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// workerNodes are the nodes that run workloads (not the control plane).
func workerNodes(t testing.TB) []string {
	t.Helper()
	out := strings.Fields(kubectlOut(t, "get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "-o", "jsonpath={.items[*].metadata.name}"))
	sort.Strings(out)
	return out
}

// ensureMainWorkerOn makes the node host a pod of the main worker: the other workers are cordoned while one worker pod is
// deleted, so that its replacement can only start on the node, and uncordoned again.
func ensureMainWorkerOn(t *testing.T, node string) {
	t.Helper()
	pods := livePods(t, selMainWorker)
	for _, p := range pods {
		if p.Node == node {
			return
		}
	}
	if len(pods) == 0 {
		t.Fatal("no main worker pod")
	}
	var others []string
	for _, n := range workerNodes(t) {
		if n != node {
			others = append(others, n)
		}
	}
	for _, n := range others {
		kubectlMust(t, "cordon", n)
	}
	defer func() {
		for _, n := range others {
			if out, err := kubectl("uncordon", n); err != nil {
				t.Errorf("uncordon %s: %v: %s", n, err, out)
			}
		}
	}()
	t.Logf("node %s hosts no worker pod: replacing %s there", node, pods[0].Name)
	kn(t, "delete", "pod", pods[0].Name, "--wait=true")
	kn(t, "rollout", "status", "deployment/trove-worker", "--timeout=3m")
	if nodesHosting(t, selMainWorker)[node] {
		return
	}
	t.Fatalf("the replacement of %s did not start on %s", pods[0].Name, node)
}

// kubectlOut runs kubectl without the release's namespace and fails the test on an error.
func kubectlOut(t testing.TB, args ...string) string {
	t.Helper()
	out, err := kubectl(args...)
	if err != nil {
		t.Fatalf("kubectl %v: %v: %s", args, err, out)
	}
	return out
}
