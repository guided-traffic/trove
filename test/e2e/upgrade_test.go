//go:build e2e

package e2e

// The upgrade tests (docs/developer/e2e.md#the-upgrade-tests):
//
//   - TestUpgrade_R_HA_064_AcrossTheCut is the exit criterion E6a.7: the last release before the cut into roles (v0.16.0, which
//     runs one `web` Deployment) is upgraded to the working tree in the two steps of R-HA-064 while npm ci and dotnet restore
//     loops and a browser journey run, and the guard of the second step is exercised.
//   - TestUpgrade_R_HA_020_RollingFromThePreviousRelease is R-HA-020's acceptance criterion and the rows "Rolling upgrade" and
//     "Rollback" of R-HA-052: the latest release before the working tree is upgraded in one helm upgrade with the migration Job
//     and rolled back, under the same loops.
//
// Both replace the installation: they remove the release, empty the database and Valkey, install the old release, and at the
// end return the cluster to the current dev release. They run in the set `upgrade` only (deploy/dev/e2e.sh).

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// upgradeCutRelease is the last release before the cut into roles (R-HA-064): its chart has no `packages:` values key and
// renders one `web` Deployment. The first release with the roles is v0.17.0.
const upgradeCutRelease = "v0.16.0"

// upgradeHold is how long the loops keep running before and after each step: the tail of an upgrade (a drained pod, a
// switched edge) must meet them too.
const upgradeHold = 45 * time.Second

// underTestVersion is the version the image under test reports (`trove version`), which the registry lists for its replicas.
func underTestVersion(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "--rm", upImage(), "version").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %s version: %v\n%s (run `make image` first)", upImage(), err, out)
	}
	m := regexp.MustCompile(`^trove (\S+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("cannot read the version from %q", out)
	}
	return m[1]
}

// upSampler asks the registry every two seconds while a step runs and keeps what it saw: the version sets (a state with two
// versions is the mixed-version window) and the status codes of its own requests through the edge.
type upSampler struct {
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	versions map[string]int // "0.16.0,dev" -> samples
	codes    map[int]int
	roles    map[string]int
}

func startSampler() *upSampler {
	s := &upSampler{stop: make(chan struct{}), done: make(chan struct{}), versions: map[string]int{}, codes: map[int]int{}, roles: map[string]int{}}
	go func() {
		defer close(s.done)
		for {
			rs, err := upReplicas()
			s.mu.Lock()
			if err != nil {
				s.codes[0]++
			} else {
				s.codes[200]++
				s.versions[strings.Join(upVersions(rs), ",")]++
				s.roles[strings.Join(upRoles(rs), ",")]++
			}
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	return s
}

func (s *upSampler) finish() *upSampler {
	close(s.stop)
	<-s.done
	return s
}

// mixed says whether any sample listed more than one version.
func (s *upSampler) mixed() bool {
	for v := range s.versions {
		if strings.Contains(v, ",") {
			return true
		}
	}
	return false
}

func requireUpgradeSetup(t *testing.T) {
	t.Helper()
	for _, name := range []string{"TROVE_E2E_NPMSTUB_HOST", "TROVE_E2E_NUGETSTUB_HOST", "TROVE_E2E_NPM_CLIENTS_PODS", "TROVE_E2E_NUGET_CLIENTS_PODS"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is not set: run the upgrade tests through `deploy/dev/e2e.sh upgrade` (make e2e SET=upgrade)", name)
		}
	}
	if err := exec.Command("docker", "image", "inspect", upImage()).Run(); err != nil {
		t.Fatalf("the image %s is not in the local Docker: run `make image`", upImage())
	}
}

// TestUpgrade_R_HA_064_AcrossTheCut is E6a.7 (R-HA-064, Q-HA-86). The release before the cut is installed with its own dev
// values, then:
//
//	guard   `helm upgrade` with web.enabled false (the default) is refused before anything changes, naming web.enabled and the two
//	        steps, and the same values render under `helm template`, which cannot read the cluster;
//	step 1  `helm upgrade --wait` with --set web.enabled=true: the registry lists the roles web, api, packages and worker;
//	step 2  `helm upgrade --wait` with web.enabled false: the registry lists no web replica, and trove-web is gone;
//
// with an npm ci loop set, an npm publish loop, dotnet restore loops and a browser journey running through all of it: no
// iteration of any loop fails.
func TestUpgrade_R_HA_064_AcrossTheCut(t *testing.T) {
	requireUpgradeSetup(t)
	prior := releaseOf(t, upgradeCutRelease)
	version := underTestVersion(t)
	t.Cleanup(func() { restoreCurrentRelease(t) })
	upKindLoad(t)

	resetInstallation(t)
	installPrior(t, prior)
	if got := kn(t, "get", "deployment", "-l", upgradeRolesSelector, "-o", "jsonpath={.items[*].metadata.name}"); !strings.Contains(" "+got+" ", " trove-web ") || strings.Contains(got, "trove-api") {
		t.Fatalf("%s is not of the shape before the cut: its Deployments are %q", prior.tag, got)
	}

	t.Run("R-HA-064 web.enabled false against trove-web without a ready trove-api is refused before anything changes", func(t *testing.T) {
		beforeDeps, beforeRev := upDeployments(t), helmRevision(t)
		_, _, out, err := upgradeToTree(t, "guard")
		if err == nil {
			t.Fatalf("helm upgrade with web.enabled false succeeded against %s; the guard must refuse it\n%s", prior.tag, tail(out, 1500))
		}
		for _, want := range []string{"web.enabled", "--set web.enabled=true", "two upgrades"} {
			if !strings.Contains(out, want) {
				t.Errorf("the guard's message does not contain %q:\n%s", want, tail(out, 1500))
			}
		}
		if afterDeps, afterRev := upDeployments(t), helmRevision(t); !deploymentsEqual(beforeDeps, afterDeps) || beforeRev != afterRev {
			t.Errorf("the refused upgrade changed the installation: Deployments %v -> %v, revision %d -> %d", beforeDeps, afterDeps, beforeRev, afterRev)
		}
		if rs, err := upReplicas(); err != nil || slices.Contains(upRoles(rs), "api") {
			t.Errorf("after the refusal the registry lists %s (%v), want only the replicas of %s", describeReplicas(rs), err, prior.tag)
		}
		// the same values render where the chart cannot read the cluster: the guard has nothing to look up there
		targs := append([]string{"template", "trove", upChart(), "-n", cfg.namespace}, upReleaseValues(t, "")...)
		tout, terr := upHelm(t, "guard-template", 3*time.Minute, targs...)
		if terr != nil {
			t.Fatalf("helm template with the same values: %v\n%s", terr, tail(tout, 1500))
		}
		if !strings.Contains(tout, "name: trove-api") || strings.Contains(tout, "name: trove-web\n") {
			t.Errorf("helm template did not render the target shape (trove-api present: %v)", strings.Contains(tout, "name: trove-api"))
		}
		t.Logf("refused: %s", firstLines(out, 3))
	})

	fx := buildUpgradeFixture(t)
	loops := startUpgradeLoops(t, fx, "cut")
	time.Sleep(upgradeHold)

	// step 1: the bridge
	sampler := startSampler()
	from1, to1, out, err := upgradeToTree(t, "step1", "--set", "web.enabled=true")
	s1 := sampler.finish()
	if err != nil {
		t.Fatalf("step 1 (helm upgrade --set web.enabled=true): %v\n%s", err, tail(out, 3000))
	}
	t.Logf("step 1 took %s; versions seen in the registry %v, role sets %v, registry reads %v", to1.Sub(from1).Round(time.Second), s1.versions, s1.roles, s1.codes)
	if !s1.mixed() {
		t.Errorf("step 1: the registry never listed two versions (%v): the upgrade did not run in the mixed-version window", s1.versions)
	}
	rs := waitReplicas(t, "step 1: the registry lists the roles web, api, packages and worker of the new version", 3*time.Minute, func(rs []upReplica) bool {
		return slices.Equal(upRoles(rs), []string{"api", "packages", "web", "worker"}) && slices.Equal(upVersions(rs), []string{version})
	})
	t.Logf("between the steps the registry lists: %s", describeReplicas(rs))
	time.Sleep(upgradeHold)

	// step 2: the cut
	sampler = startSampler()
	from2, to2, out, err := upgradeToTree(t, "step2")
	s2 := sampler.finish()
	if err != nil {
		t.Fatalf("step 2 (helm upgrade, web.enabled false): %v\n%s", err, tail(out, 3000))
	}
	t.Logf("step 2 took %s; role sets seen in the registry %v, registry reads %v", to2.Sub(from2).Round(time.Second), s2.roles, s2.codes)
	rs = waitReplicas(t, "step 2: the registry lists no web replica", 3*time.Minute, func(rs []upReplica) bool {
		return slices.Equal(upRoles(rs), []string{"api", "packages", "worker"}) && slices.Equal(upVersions(rs), []string{version})
	})
	t.Logf("after step 2 the registry lists: %s", describeReplicas(rs))
	if _, ok := upDeployments(t)["trove-web"]; ok {
		t.Errorf("trove-web is still there after step 2")
	}
	if got, err := kubectl("-n", cfg.namespace, "get", "service", "trove", "--ignore-not-found", "-o", "name"); err != nil || strings.TrimSpace(got) != "" {
		t.Errorf("the Service trove of the old shape is still there after step 2 (%q, %v)", strings.TrimSpace(got), err)
	}
	time.Sleep(upgradeHold)

	res := loops.stop(t)
	res.requireClean(t, upWindow{"step 1", from1, to1}, upWindow{"step 2", from2, to2})
	res.publishedAreReadable(t, fx)
	rolloutComplete(t, deploymentNames(upDeployments(t)))
}

// TestUpgrade_R_HA_020_RollingFromThePreviousRelease is R-HA-020's acceptance criterion and the rows "Rolling upgrade" and
// "Rollback" of R-HA-052: the latest release before the working tree is upgraded with one `helm upgrade --wait` (the
// migration Job first, then every Deployment rolling on its own) under the loops, R-HA-020's verification passes, and
// `helm rollback` returns to the release under the same loops.
func TestUpgrade_R_HA_020_RollingFromThePreviousRelease(t *testing.T) {
	requireUpgradeSetup(t)
	prior := releaseOf(t, previousReleaseTag(t))
	version := underTestVersion(t)
	added, contract := contractStepsSince(t, prior.tag)
	t.Logf("previous release %s, image under test %s; migrations added since: %v", prior.tag, version, added)
	if len(contract) > 0 {
		// R-HA-021: a contract step is applied only one release at a time or with downtime, so this release cannot be upgraded to
		// without downtime and the zero-failure condition does not apply. The test says so and fails, it does not skip: a
		// release that adds a contract step must name the release to start from or the downtime path here.
		t.Fatalf("the working tree adds the contract step(s) %v since %s: the upgrade from %s is not zero-downtime by R-HA-021 (one release at a time, or stop and upgrade). "+
			"This test cannot assert zero failed iterations for it; start it from the release that precedes the step or name the downtime path in the test", contract, prior.tag, prior.tag)
	}
	t.Cleanup(func() { restoreCurrentRelease(t) })
	upKindLoad(t)

	resetInstallation(t)
	installPrior(t, prior)
	fx := buildUpgradeFixture(t)
	loops := startUpgradeLoops(t, fx, "rolling")
	time.Sleep(upgradeHold)

	// the upgrade
	rev := helmRevision(t)
	sampler := startSampler()
	from1, to1, out, err := upgradeToTree(t, "upgrade")
	s1 := sampler.finish()
	if err != nil {
		t.Fatalf("helm upgrade --wait from %s: %v\n%s", prior.tag, err, tail(out, 3000))
	}
	t.Logf("upgrade took %s; versions seen in the registry %v, registry reads %v", to1.Sub(from1).Round(time.Second), s1.versions, s1.codes)
	if !s1.mixed() {
		t.Errorf("the registry never listed two versions (%v): the upgrade did not run in the mixed-version window", s1.versions)
	}

	// R-HA-020's verification
	t.Run("R-HA-020 verification: rollout status, the replicas list only the new version, no migration is pending", func(t *testing.T) {
		rolloutComplete(t, deploymentNames(upDeployments(t)))
		rs := waitReplicas(t, "the registry lists only the new version", 3*time.Minute, func(rs []upReplica) bool {
			return slices.Equal(upVersions(rs), []string{version})
		})
		t.Logf("the registry lists: %s", describeReplicas(rs))
		st := upMigrateStatus(t)
		if len(st.Pending) != 0 || !st.Servable || st.Binary.Version != version || st.Schema.Applied != st.Binary.RequiredSchema {
			t.Errorf("migrations after the upgrade: pending %v, servable %v (%s), binary %s, applied %d, required %d", st.Pending, st.Servable, st.Verdict, st.Binary.Version, st.Schema.Applied, st.Binary.RequiredSchema)
		}
	})
	time.Sleep(upgradeHold)

	// the rollback, to the revision the upgrade started from
	sampler = startSampler()
	fromR := time.Now()
	out, err = upHelm(t, "rollback", 20*time.Minute, "rollback", "trove", strconv.Itoa(rev), "-n", cfg.namespace, "--wait", "--timeout", "15m")
	toR := time.Now()
	sr := sampler.finish()
	if err != nil {
		t.Fatalf("helm rollback to revision %d: %v\n%s", rev, err, tail(out, 3000))
	}
	t.Logf("rollback took %s; versions seen in the registry %v, registry reads %v", toR.Sub(fromR).Round(time.Second), sr.versions, sr.codes)
	t.Run("R-HA-052 R-HA-019 rollback: the replicas list only the previous release, which serves the migrated schema", func(t *testing.T) {
		rolloutComplete(t, deploymentNames(upDeployments(t)))
		rs := waitReplicas(t, "the registry lists only the previous release", 3*time.Minute, func(rs []upReplica) bool {
			return slices.Equal(upVersions(rs), []string{prior.version})
		})
		t.Logf("the registry lists: %s", describeReplicas(rs))
		st := upMigrateStatus(t)
		if !st.Servable || st.Binary.Version != prior.version || len(st.Pending) != 0 {
			t.Errorf("the previous release on the migrated schema: servable %v (%s), binary %s, pending %v", st.Servable, st.Verdict, st.Binary.Version, st.Pending)
		}
	})
	time.Sleep(upgradeHold)

	res := loops.stop(t)
	res.requireClean(t, upWindow{"the upgrade", from1, to1}, upWindow{"the rollback", fromR, toR})
	res.publishedAreReadable(t, fx)
}

func deploymentsEqual(a, b map[string]deploymentState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
