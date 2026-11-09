//go:build e2e

package e2e

// The harness of drain variants B to E of R-HA-059 (Q-HA-170 to Q-HA-174): it picks the node of a variant, drains it with
// deploy/dev/drain.sh and judges the variant's own condition together with the conditions every variant shares (no SIGKILL of a trove
// container, no trove pod Pending for more than 60 s, the drain within 10 minutes). It brings no load of its own: the caller starts
// its probes or its load after prepareVariantDrain, runs Run as the fault and calls Verify and Settle after it. The blocks of the
// vulnerability scanning use it under their probes (vuln_drain_variants_test.go, vuln_pooler_test.go), E5.9 under the full load.
//
//	d := prepareVariantDrain(t, variantB)        // picks the node, moves a primary that is in the way, starts the pod watch (the caller settles the release after it)
//	... start the probes or the load ...
//	d.Run(t)                                     // the fault: drain.sh [--also ...] <node>; never fails the test itself
//	... stop the probes or the load ...
//	d.Verify(t)                                  // the variant's condition and the shared ones
//	d.Settle(t)                                  // the release and the evicted dependency whole again

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type drainVariant string

const (
	variantA drainVariant = "A" // a node with an api, a packages and a worker pod and neither primary (the alerts tests use it; npm-ha runs variant A with its own load)
	variantB drainVariant = "B" // the Valkey primary and a Sentinel on the node
	variantC drainVariant = "C" // the PostgreSQL primary on the node
	variantD drainVariant = "D" // an instance of the request pooler on the node (pooler profile)
	variantE drainVariant = "E" // a running backup pod on the node
)

const (
	// the dependencies drain.sh --also evicts, by the labels of the operators that own them
	selValkeyDeps     = "app.kubernetes.io/instance=trove-valkey" // the Valkey data pods and the Sentinels
	selPostgresDeps   = "cnpg.io/cluster=trove-db,cnpg.io/podRole=instance"
	selValkeyDataPods = "app.kubernetes.io/instance=trove-valkey,app.kubernetes.io/component=valkey"
	selPoolerDeps     = "cnpg.io/poolerName"                    // the pods of both poolers
	selPoolerRW       = "cnpg.io/poolerName=trove-db-pooler-rw" // the request pooler
)

// variantDrain is one drain of a variant: prepared before the caller's load or probes start, run as their fault, judged after.
type variantDrain struct {
	V      drainVariant
	Node   string
	OnNode []pod // the release's pods on the node when prepared

	counts     [3]int // api, packages and worker pods of the release when prepared
	watch      *podWatch
	evictedDep []pod // the dependency's pods on the node when prepared: each must be replaced

	// B: the monitor of /readyz and the counter of Valkey primary changes of every replica that runs through the drain
	mon      *monitor
	monOnce  sync.Once
	changes  map[string]float64
	vkBefore string // the Valkey primary the Sentinels named when the drain started
	hold     *primaryHold

	// C: the old primary and the poll of the cluster's status and of that pod
	oldPrimary string
	poll       *primaryPoll

	// E: the backup Job
	job string

	runErr   error
	runOut   string
	started  time.Time
	took     time.Duration
	finished bool
}

// primaryPoll reads the cluster's .status.targetPrimary with its server-side timestamp, .status.currentPrimary and the old primary
// pod's deletionTimestamp every 250 ms. The server's own timestamps are compared, so the order does not depend on the poll.
type primaryPoll struct {
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	targetAt    time.Time // .status.targetPrimaryTimestamp once targetPrimary names another instance: when the switchover was requested
	switchedAt  time.Time // currentPrimary no longer the old one, first seen by the poll (logged)
	deletedAt   time.Time // when the old primary pod's deletion was requested: deletionTimestamp minus its grace period (the poll's time when the pod vanished between two polls)
	targetFirst time.Time // when the poll first saw targetPrimary name another instance (logged)
}

func (p *primaryPoll) times() (target, switched, deleted time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.targetAt, p.switchedAt, p.deletedAt
}

func startPrimaryPoll(old string) *primaryPoll {
	ctx, cancel := context.WithCancel(context.Background())
	p := &primaryPoll{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for ctx.Err() == nil {
			raw, err := kubectlCtx(ctx, "-n", cfg.namespace, "get", "cluster/trove-db", "pod/"+old, "--ignore-not-found", "-o", "json")
			at := time.Now()
			if err == nil {
				var l struct {
					Items []struct {
						Kind     string
						Metadata struct {
							DeletionTimestamp          *time.Time
							DeletionGracePeriodSeconds *int64
						}
						Status struct {
							CurrentPrimary         string     `json:"currentPrimary"`
							TargetPrimary          string     `json:"targetPrimary"`
							TargetPrimaryTimestamp *time.Time `json:"targetPrimaryTimestamp"`
						}
					}
				}
				if json.Unmarshal([]byte(raw), &l) == nil {
					seenPod := false
					p.mu.Lock()
					for _, it := range l.Items {
						switch it.Kind {
						case "Cluster":
							if it.Status.TargetPrimary != "" && it.Status.TargetPrimary != old && p.targetAt.IsZero() && it.Status.TargetPrimaryTimestamp != nil {
								p.targetAt, p.targetFirst = *it.Status.TargetPrimaryTimestamp, at
							}
							if it.Status.CurrentPrimary != "" && it.Status.CurrentPrimary != old && p.switchedAt.IsZero() {
								p.switchedAt = at
							}
						case "Pod":
							seenPod = true
							if it.Metadata.DeletionTimestamp != nil && p.deletedAt.IsZero() {
								// metadata.deletionTimestamp is the end of the grace period (CloudNativePG's stop delay is 1800 s); the request is
								// that much earlier
								p.deletedAt = it.Metadata.DeletionTimestamp.Add(-time.Duration(ptrOr(it.Metadata.DeletionGracePeriodSeconds, 0)) * time.Second)
							}
						}
					}
					if !seenPod && p.deletedAt.IsZero() && len(l.Items) > 0 {
						p.deletedAt = at // the pod is gone
					}
					p.mu.Unlock()
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return p
}

func (p *primaryPoll) stop() {
	p.cancel()
	<-p.done
}

// nodeOf is the node of a pod of the release's namespace.
func nodeOf(t testing.TB, podName string) string { return podNode(t, cfg.namespace, podName) }

// prepareVariantDrain picks the node of the variant (moving a primary that is in the way), records the pods on it and starts the
// watches the variant's conditions read. Call it before the caller's load or probes start: the moves of a primary are not part of
// the measured window.
func prepareVariantDrain(t *testing.T, v drainVariant) *variantDrain {
	t.Helper()
	d := &variantDrain{V: v}
	// kubectl drain --pod-selector matches pods of every namespace: an installation left in trove-restore (its PostgreSQL cluster is
	// named trove-db too, and its PodDisruptionBudget would hold the drain) is removed first
	if out, err := kubectl("get", "namespace", restoreNS, "-o", "name"); err == nil && strings.TrimSpace(out) != "" {
		t.Logf("removing the installation in %s before the drain", restoreNS)
		restoreNSScript(t, "down")
	}
	switch v {
	case variantA:
		d.Node = drainNodeVariantA(t)
	case variantB:
		d.Node = pickNodeB(t)
	case variantC:
		d.Node = pickNodeC(t)
	case variantD:
		d.Node = pickNodeD(t)
	case variantE:
		d.Node = drainNodeVariantA(t)
	default:
		t.Fatalf("unknown drain variant %q", v)
	}
	t.Cleanup(func() { _, _ = kubectl("uncordon", d.Node) })
	for _, p := range livePods(t, selTrove) {
		switch {
		case strings.HasPrefix(p.Name, "trove-api-"):
			d.counts[0]++
		case strings.HasPrefix(p.Name, "trove-packages-"):
			d.counts[1]++
		default:
			d.counts[2]++
		}
		if p.Node == d.Node {
			d.OnNode = append(d.OnNode, p)
		}
	}
	t.Logf("variant %s drains %s, which hosts %v", v, d.Node, podNames(d.OnNode))

	switch v {
	case variantB:
		d.evictedDep = podsOnNode(t, selValkeyDeps, d.Node)
		var data, sentinels int
		for _, p := range d.evictedDep {
			if strings.Contains(p.Name, "sentinel") {
				sentinels++
			} else {
				data++
			}
		}
		if data == 0 || sentinels == 0 {
			t.Fatalf("variant B needs the Valkey primary and a Sentinel on %s, it hosts %v", d.Node, podNames(d.evictedDep))
		}
	case variantC:
		d.oldPrimary = pgPrimary(t)
		d.evictedDep = podsOnNode(t, selPostgresDeps, d.Node)
		if len(d.evictedDep) == 0 {
			t.Fatalf("variant C: %s hosts no PostgreSQL instance", d.Node)
		}
	case variantD:
		d.evictedDep = podsOnNode(t, selPoolerDeps, d.Node)
		if len(d.evictedDep) == 0 {
			t.Fatalf("variant D: %s hosts no pooler instance", d.Node)
		}
	case variantE:
		d.prepareBackupJob(t)
	}
	d.watch = startPodWatch(t, selTrove)
	return d
}

// podsOnNode lists the live pods of a selector on a node.
func podsOnNode(t testing.TB, selector, node string) []pod {
	t.Helper()
	var out []pod
	for _, p := range livePods(t, selector) {
		if p.Node == node {
			out = append(out, p)
		}
	}
	return out
}

// nodeSet is the set of node names of a list of pods.
func nodeSet(ps []pod) map[string]bool {
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Node] = true
	}
	return out
}

// pickNodeB picks the node of the Valkey primary among those that host an api, a packages and a main worker pod, not the node of
// the PostgreSQL primary: a drain of both primaries at once is not a variant (variant C asks for the PostgreSQL primary alone). A
// primary in the way is moved before the load starts. With two Valkey data pods and two PostgreSQL instances every candidate needs at
// most two moves.
func pickNodeB(t *testing.T) string { return pickPrimaryNode(t, variantB, false) }

// pickNodeC picks the node of the PostgreSQL primary among those that host an api, a packages and a main worker pod, not the node of
// the Valkey primary. Once the node is picked the primary is not switched over: CloudNativePG does that at the cordon, which is the
// condition of the variant (Q-HA-172; a switchover asked for at the cordon named the old primary as its target, F-65).
func pickNodeC(t *testing.T) string { return pickPrimaryNode(t, variantC, true) }

// pickPrimaryNode finds a node that hosts an api, a packages and a main worker pod and a pod of the dependency whose primary the
// variant drains (the PostgreSQL instances if wantPG, else the Valkey data pods), puts that primary there and the other primary
// elsewhere. Each move is the dev cluster's own script (failover.sh, valkey-failover.sh) and switches to the only other pod.
func pickPrimaryNode(t *testing.T, v drainVariant, wantPG bool) string {
	t.Helper()
	depSel := selValkeyDataPods
	if wantPG {
		depSel = selPostgresDeps
	}
	for attempt := 0; attempt < 4; attempt++ {
		hosts := nodesHosting(t, selAPI, selPackages, selMainWorker)
		pgNode, vkNode := nodeOf(t, pgPrimary(t)), nodeOf(t, valkeyPrimaryBySentinel(t))
		wantNode, otherNode := vkNode, pgNode
		if wantPG {
			wantNode, otherNode = pgNode, vkNode
		}
		var candidates []string
		for n := range nodeSet(livePods(t, depSel)) {
			if hosts[n] {
				candidates = append(candidates, n)
			}
		}
		sort.Strings(candidates)
		if len(candidates) == 0 {
			t.Fatalf("variant %s: no node hosts a pod of the dependency, an api, a packages and a worker pod (hosts %v)", v, hosts)
		}
		for _, n := range candidates {
			if n == wantNode && n != otherNode {
				t.Logf("variant %s: the %s primary is on %s, the other primary on %s", v, map[bool]string{true: "PostgreSQL", false: "Valkey"}[wantPG], n, otherNode)
				return n
			}
		}
		n := candidates[0]
		moveWanted := func() {
			if wantPG {
				script(t, "failover.sh")
			} else {
				script(t, "valkey-failover.sh")
			}
		}
		moveOther := func() {
			if wantPG {
				script(t, "valkey-failover.sh")
			} else {
				script(t, "failover.sh")
			}
		}
		if otherNode == n {
			t.Logf("variant %s: the other primary is on %s; moving it away", v, n)
			moveOther()
		} else {
			t.Logf("variant %s: the primary is not on %s; moving it there", v, n)
			moveWanted()
		}
	}
	t.Fatalf("variant %s: no node with the primary, an api, a packages and a worker pod without the other primary after four moves", v)
	return ""
}

// pickNodeD picks a node with an instance of the request pooler, an api, a packages and a main worker pod and neither primary.
func pickNodeD(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		hosts := nodesHosting(t, selAPI, selPackages, selMainWorker)
		poolers := nodeSet(livePods(t, selPoolerRW))
		pgNode, vkNode := nodeOf(t, pgPrimary(t)), nodeOf(t, valkeyPrimaryBySentinel(t))
		var free, inWay []string
		for n := range poolers {
			switch {
			case !hosts[n]:
			case n == pgNode:
				inWay = append(inWay, "postgres")
			case n == vkNode:
				inWay = append(inWay, "valkey")
			default:
				free = append(free, n)
			}
		}
		sort.Strings(free)
		if len(free) > 0 {
			t.Logf("variant D: %s hosts an instance of the request pooler, the PostgreSQL primary is on %s, the Valkey primary on %s", free[0], pgNode, vkNode)
			return free[0]
		}
		if len(inWay) == 0 {
			t.Fatalf("variant D: no node with an api, a packages and a worker pod hosts an instance of the request pooler (poolers on %v)", poolers)
		}
		sort.Strings(inWay)
		t.Logf("variant D: the nodes of the request pooler hold a primary (%v); moving %s first", inWay, inWay[0])
		if inWay[0] == "postgres" {
			script(t, "failover.sh")
		} else {
			script(t, "valkey-failover.sh")
		}
	}
	t.Fatal("variant D: no node hosts an instance of the request pooler without a primary")
	return ""
}

// prepareBackupJob puts at least 16 MiB of fresh files into the installation and starts a backup Job whose pod runs on the node
// (a preferred node affinity, Q-HA-174), slowed to 512 KiB/s into a fresh target prefix so that the copy outlasts the probes' lead.
func (d *variantDrain) prepareBackupJob(t *testing.T) {
	t.Helper()
	run := bakRun()
	adm := adminAPI(t)
	repo := "vdrain-" + run
	ensureRepo(t, adm, repo)
	for i := range 16 {
		p := fmt.Sprintf("variant-e/%02d.bin", i)
		if r, err := retrying(func() (reply, error) {
			return adm.send("PUT", filesAddr(repo)+"/"+p, randomBytes(1<<20), "Content-Type", "application/octet-stream")
		}); err != nil || r.Status != 201 {
			t.Fatalf("put %s/%s: %v %s", repo, p, err, r)
		}
	}
	zero := 0
	for attempt := 1; attempt <= 2; attempt++ {
		d.job = fmt.Sprintf("var-e-%s-%d", run, attempt)
		bakCreateJob(t, bakJob{
			name: d.job, backoff: &zero, preferNode: d.Node,
			env: map[string]string{
				"TROVE_BACKUP_TARGET_PREFIX": bakFreshPrefix(run, "var-e"), "TROVE_BACKUP_COPY_BYTES_PER_SECOND": "512KiB", "TROVE_BACKUP_COPY_CONCURRENCY": "1",
			},
		})
		bakWaitLog(t, cfg.namespace, d.job, "backup run started", 3*time.Minute)
		on := podNode(t, cfg.namespace, bakPodOf(t, cfg.namespace, d.job))
		if on == d.Node {
			d.evictedDep = podsOf(t, "job-name="+d.job)
			t.Logf("variant E: the backup pod %v runs on %s", podNames(d.evictedDep), d.Node)
			return
		}
		if attempt == 2 {
			t.Fatalf("variant E: the backup pod landed on %s, not on %s, twice", on, d.Node)
		}
		t.Logf("variant E: the backup pod landed on %s, not on %s; starting the Job again", on, d.Node)
		kn(t, "delete", "job", d.job, "--cascade=foreground", "--wait=true")
		job := d.job
		waitFor(t, "the pod of Job "+job+" to be gone", 2*time.Minute, time.Second, func() bool { return len(podsOf(t, "job-name="+job)) == 0 })
	}
}

func (d *variantDrain) stopMonitor() {
	if d.mon != nil {
		d.monOnce.Do(func() { d.mon.stop() })
	}
}

// alsoArgs are the --also arguments of drain.sh for the variant's dependency.
func (d *variantDrain) alsoArgs() []string {
	switch d.V {
	case variantB:
		return []string{"--also", "valkey"}
	case variantC:
		return []string{"--also", "postgres"}
	case variantD:
		return []string{"--also", "pooler"}
	}
	return nil
}

// Run is the fault: deploy/dev/drain.sh [--also ...] <node>. It does not fail the test (a probe goroutine of the caller may still
// run); Verify reports a failed drain.
func (d *variantDrain) Run(t *testing.T) {
	t.Helper()
	if d.V == variantC {
		d.poll = startPrimaryPoll(d.oldPrimary)
	}
	if d.V == variantB {
		// started here and not in the preparation: the monitor must not see the recovery of the primaries the preparation moved, and the
		// counters are those of the release as the caller left it settled
		d.mon = startMonitor(t)
		t.Cleanup(d.stopMonitor)
		d.changes = replicaMetric(t, "trove_valkey_primary_changes_total")
		d.vkBefore, _ = sentinelPrimary()
		// the primary stays down until the Sentinels have failed over (Q-HA-181): its replacement would otherwise be back within
		// down-after-milliseconds and no failover would happen
		onPrimaryNode := false
		for _, p := range d.evictedDep {
			onPrimaryNode = onPrimaryNode || p.Name == d.vkBefore
		}
		if !onPrimaryNode {
			d.runErr, d.finished = fmt.Errorf("the Sentinels name %q as the Valkey primary, which is not among the pods evicted from %s (%v)", d.vkBefore, d.Node, podNames(d.evictedDep)), true
			return
		}
		d.hold = holdValkeyPrimary(t, d.vkBefore)
	}
	args := append(d.alsoArgs(), d.Node)
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev", "drain.sh"), args...)
	cmd.Dir = cfg.repo
	d.started = time.Now()
	t.Logf("variant %s: the drain of %s starts %s", d.V, d.Node, d.started.Format("15:04:05"))
	out, err := cmd.CombinedOutput()
	d.took = time.Since(d.started)
	d.runOut, d.runErr, d.finished = strings.TrimSpace(string(out)), err, true
	t.Logf("drain.sh %s:\n%s", strings.Join(args, " "), d.runOut)
	t.Logf("variant %s: drain.sh returned after %s (err %v)", d.V, d.took.Round(time.Second), err)
}

// Verify judges the drain after the caller's probes or load have stopped: the variant's own condition and the conditions of every
// variant (R-HA-059): no SIGKILL, nothing Pending for more than 60 s, the drain within 10 minutes, the evicted pods replaced.
func (d *variantDrain) Verify(t *testing.T) {
	t.Helper()
	if !d.finished {
		t.Fatalf("variant %s: Run was not called", d.V)
	}
	if d.runErr != nil {
		t.Errorf("variant %s: drain.sh failed: %v\n%s", d.V, d.runErr, tail(d.runOut, 2500))
	}
	d.watch.stop()
	if len(d.watch.killed) != 0 {
		t.Errorf("variant %s: containers were killed: %v", d.V, d.watch.killed)
	}
	if name, p := d.watch.longestPending(); p > 60*time.Second {
		t.Errorf("variant %s: trove pod %s was Pending for %s", d.V, name, p.Round(time.Second))
	} else {
		t.Logf("variant %s: longest Pending of a trove pod: %s (%s)", d.V, p.Round(100*time.Millisecond), name)
	}
	if d.took > 10*time.Minute {
		t.Errorf("R-HA-059: kubectl drain returned after %s, more than 10 minutes", d.took.Round(time.Second))
	}
	if unc := strings.TrimSpace(kn(t, "get", "node", d.Node, "-o", "jsonpath={.spec.unschedulable}")); unc == "true" {
		t.Errorf("node %s is still cordoned", d.Node)
	}
	checkEvents(t, d.OnNode)

	// the drain evicted what it was meant to: a selector that matches nothing would drain nothing and pass
	if sel := map[drainVariant]string{variantB: selValkeyDeps, variantC: selPostgresDeps, variantD: selPoolerDeps}[d.V]; sel != "" {
		now := map[string]bool{}
		for _, p := range podsOf(t, sel) {
			now[p.UID] = true
		}
		for _, p := range d.evictedDep {
			if now[p.UID] {
				t.Errorf("variant %s: the dependency pod %s (%s) on %s was not evicted", d.V, p.Name, p.UID, d.Node)
			}
		}
	}

	switch d.V {
	case variantB:
		d.verifyB(t)
	case variantC:
		d.verifyC(t)
	case variantE:
		d.verifyE(t)
	}
}

// verifyB: the primary change reaches every replica that ran through the drain (Q-HA-171) and none of them turned unready.
func (d *variantDrain) verifyB(t *testing.T) {
	t.Helper()
	d.stopMonitor()
	after := replicaMetric(t, "trove_valkey_primary_changes_total")
	vkAfter, _ := sentinelPrimary()
	t.Logf("variant B: the Sentinels named %s as the Valkey primary before the drain and name %s after it", d.vkBefore, vkAfter)
	holdReported := false
	if d.hold != nil {
		holdReported = d.hold.verify(t)
	}
	logSentinelEvents(t, d.started)
	if d.vkBefore != "" && vkAfter == d.vkBefore && !holdReported {
		t.Errorf("variant B caused no failover: the Sentinels still name the evicted primary %s", vkAfter)
	}
	onNode := map[string]bool{}
	for _, p := range d.OnNode {
		onNode[p.Name] = true
	}
	d.mon.mu.Lock()
	notReady := map[string]int{}
	for p, n := range d.mon.ready {
		notReady[p] = n
	}
	d.mon.mu.Unlock()
	ranThrough := 0
	for name, was := range d.changes {
		if onNode[name] {
			continue
		}
		now, ok := after[name]
		if !ok {
			t.Errorf("variant B: replica %s that was not on the drained node is gone", name)
			continue
		}
		ranThrough++
		if now-was < 1 {
			t.Errorf("R-HA-057: trove_valkey_primary_changes_total on %s rose by %v, want at least 1 (before %v, after %v)", name, now-was, was, now)
		}
		if notReady[name] != 0 {
			t.Errorf("R-HA-057: /readyz of %s answered non-200 %d times during the drain", name, notReady[name])
		}
	}
	if ranThrough == 0 {
		t.Error("variant B: no replica ran through the drain")
	}
	repl := map[string]float64{}
	for name, v := range after {
		if _, was := d.changes[name]; !was {
			repl[name] = v
		}
	}
	t.Logf("variant B: the counter before %v, after %v; replacements (counter at the end, not a condition): %v", d.changes, after, repl)
}

// verifyC: CloudNativePG switched the primary over before the old primary's pod was evicted (Q-HA-172).
func (d *variantDrain) verifyC(t *testing.T) {
	t.Helper()
	d.poll.stop()
	target, switched, deleted := d.poll.times()
	t.Logf("variant C: the old primary %s; the switchover was requested at %s (targetPrimaryTimestamp), the status named the new primary at %s (as the poll saw it), the pod's deletion was requested at %s (deletionTimestamp minus the grace period)",
		d.oldPrimary, variantStamp(target), variantStamp(switched), variantStamp(deleted))
	switch {
	case target.IsZero():
		t.Errorf("variant C: the cluster never named another target primary than %s", d.oldPrimary)
	case deleted.IsZero():
		t.Errorf("variant C: the old primary %s was never evicted", d.oldPrimary)
	// a deletionTimestamp has the resolution of a second: the instant it stands for may be up to a second later
	case !target.Before(deleted.Add(time.Second)):
		t.Errorf("R-HA-059: the eviction of %s (%s) came before the switchover was requested (%s)", d.oldPrimary, variantStamp(deleted), variantStamp(target))
	}
}

func variantStamp(at time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return at.Format("15:04:05.000")
}

// verifyE: the backup Job completes although its pod was evicted, and the eviction is no failure (backoffLimit 0).
func (d *variantDrain) verifyE(t *testing.T) {
	t.Helper()
	end := bakWaitJob(t, cfg.namespace, d.job, 15*time.Minute)
	if end.Condition != "Complete" {
		t.Errorf("variant E: the backup Job ended %s (exits %v, %d pods):\n%s", end.Condition, end.Exits, end.Pods, tail(bakJobLogs(cfg.namespace, d.job), 2500))
		return
	}
	t.Logf("variant E: the backup Job ended Complete with %d pods, exits %v", end.Pods, end.Exits)
	// R-HA-059: the evicted backup pod is stopped by SIGTERM; a SIGKILL (exit 137) or a kill event is a failure
	for _, code := range end.Exits {
		if code == 137 {
			t.Errorf("variant E: a backup pod of Job %s exited 137 (SIGKILL)", d.job)
		}
	}
	checkEvents(t, d.evictedDep)
	if end.Pods < 2 {
		t.Errorf("variant E: the backup was not interrupted: the Job ran %d pod(s)", end.Pods)
	}
}

// Settle waits until the release has its pods again and the evicted dependency is whole: it is the point after which the next
// fault or the next variant may start.
func (d *variantDrain) Settle(t *testing.T) {
	t.Helper()
	troveSettled(t, d.counts[0], d.counts[1], d.counts[2])
	switch d.V {
	case variantB:
		waitFor(t, "two Valkey data pods and three Sentinels to be ready", 5*time.Minute, 2*time.Second, func() bool {
			return stsReady(t, "trove-valkey") == 2 && stsReady(t, "trove-valkey-sentinel") == 3
		})
	case variantC:
		waitFor(t, "two ready PostgreSQL instances", 10*time.Minute, 2*time.Second, func() bool {
			return strings.TrimSpace(kn(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.readyInstances}")) == "2"
		})
	case variantD:
		for _, dep := range []string{"trove-db-pooler-rw", "trove-db-pooler-control"} {
			kn(t, "rollout", "status", "deployment/"+dep, "--timeout=5m")
		}
	}
}

func stsReady(t testing.TB, name string) int {
	t.Helper()
	var n int
	_, _ = fmt.Sscan(strings.TrimSpace(kn(t, "get", "statefulset", name, "-o", "jsonpath={.status.readyReplicas}")), &n)
	return n
}

func ptrOr(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}
