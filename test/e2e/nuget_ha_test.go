//go:build e2e

package e2e

// Drain and failover under the NuGet load (Q-NUGET-73, Q-HA-100): variant A of R-HA-059's drain test and the PostgreSQL-switchover
// and Valkey-failover scenarios of R-HA-052, each under the whole load of R-HA-059's table: the npm part of npm_load_test.go
// and the NuGet part of nuget_load_test.go together, in the scatter profile with three api, three packages and four worker
// replicas. The drain scenario is also R-ARCH-024's first acceptance criterion (a drain during 100 npm ci, 20 npm publish,
// 100 dotnet restore and 20 dotnet nuget push). The scenarios are those of TestNPMHA, and so are their pass conditions; the
// shared harness (drainVariantA, runLoadFault) is in npm_ha_test.go; the drain flow of every variant is drainUnderLoad (drain_load_test.go), which this test runs with the long-transfer probes and
// the storage job of R-STOR-046 (E5.9). Variants B to E under the same load: TestDrainLoad, TestDrainLoadPooler.

import (
	"testing"
	"time"
)

// R-ARCH-024's drain criterion: the load of the drain run reaches at least these commands.
const (
	archMinCI      = 100
	archMinPublish = 20
	archMinRestore = 100
	archMinPush    = 20
)

func TestNuGetHA(t *testing.T) {
	h := npmHAFx(t)
	nh := nugetHAFx(t)

	t.Run("R-HA-059 R-HA-052 R-STOR-046 R-ARCH-024 R-NUGET-030 Q-NUGET-73 E5.9 drain variant A under the npm and NuGet load: every npm and dotnet command exits 0, k6 only 2xx and 304, p99 at most twice the warm-up's, every acknowledged publish and push installs, a long transfer is cut at most once per evicted packages pod and completes by resume, jobs resume with their attempt, gc-full finishes on the other worker, no SIGKILL, nothing Pending for 60 s, and the run reaches 100 npm ci, 20 npm publish, 100 dotnet restore and 20 dotnet nuget push", func(t *testing.T) {
		drainVariantA(t, "nuget", func() drainLoad { return startFullLoad(t, h, nh, "drain") }, drainExtras()...)
	})

	t.Run("R-HA-051 R-HA-052 R-HA-015 R-NUGET-030 dev-failover (PostgreSQL switchover) during the npm ci and dotnet restore loops: no failed iteration, k6 only 2xx and 304", func(t *testing.T) {
		r := runFullFault(t, h, nh, "pg-switchover", time.Minute, time.Minute, func() { script(t, "failover.sh") })
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.k6Holds(t)
		r.res.installPublished(t)
	})

	t.Run("R-HA-051 R-HA-052 R-HA-057 R-NUGET-030 dev-valkey-failover during the npm ci and dotnet restore loops: no failed iteration, k6 only 2xx and 304, /readyz 200 throughout, every replica follows the new primary once", func(t *testing.T) {
		const changes = "trove_valkey_primary_changes_total"
		var before map[string]float64
		r := runFullFault(t, h, nh, "valkey-failover", time.Minute, time.Minute, func() {
			before = replicaMetric(t, changes)
			script(t, "valkey-failover.sh")
		})
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.k6Holds(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("/readyz answered non-200 during the Valkey failover: %v", r.mon.ready)
		}
		after := replicaMetric(t, changes)
		for p, v := range after {
			if d := v - before[p]; d != 1 {
				t.Errorf("R-HA-057: %s on %s rose by %v, want 1", changes, p, d)
			}
		}
		t.Logf("%s before %v, after %v", changes, before, after)
		r.res.installPublished(t)
	})
}

// fullHALoad is the whole load of R-HA-059's table that the two clients make: the npm part and the NuGet part, started
// together.
type fullHALoad struct {
	npm   *npmLoad
	nuget *nugetLoad
	h     *npmHAFixture
	nh    *nugetHAFixture
}

func startFullLoad(t *testing.T, h *npmHAFixture, nh *nugetHAFixture, name string) *fullHALoad {
	t.Helper()
	l := &fullHALoad{h: h, nh: nh}
	l.nuget = startNuGetLoad(t, nh, name, nugetFullLoad())
	l.npm = startNPMLoad(t, h, name, haFullLoad())
	return l
}

// begun is the moment both parts run: the later of their starts.
func (l *fullHALoad) begun() time.Time {
	if l.npm.started.After(l.nuget.started) {
		return l.npm.started
	}
	return l.nuget.started
}

func (l *fullHALoad) stop(t *testing.T) drainOutcome { return l.stopBoth(t) }

// stopBoth ends both parts: each part's k6 run first, then its loops finish their command.
func (l *fullHALoad) stopBoth(t *testing.T) fullHAResult {
	t.Helper()
	return fullHAResult{npm: l.npm.stop(t), nuget: l.nuget.stop(t), h: l.h, nh: l.nh}
}

// fullHAResult is what the whole load returned; it is the drainOutcome of the drain and the result of a fault run.
type fullHAResult struct {
	npm   npmLoadResult
	nuget nugetLoadResult
	h     *npmHAFixture
	nh    *nugetHAFixture
}

func (r fullHAResult) allExitZero(t *testing.T) {
	t.Helper()
	r.npm.allExitZero(t)
	r.nuget.allExitZero(t)
}

func (r fullHAResult) loopsSpan(t *testing.T, from, to time.Time) {
	t.Helper()
	r.npm.loopsSpan(t, from, to)
	r.nuget.loopsSpan(t, from, to)
}

func (r fullHAResult) k6Holds(t *testing.T) {
	t.Helper()
	r.npm.K6.only2xx304(t)
	r.nuget.k6Holds(t)
}

// mixed is the k6 requests of both scripts as one set: the 150 requests/s of R-HA-059. Its first request is the later of the
// two scripts' first, so the warm-up holds both.
func (r fullHAResult) mixed() npmK6Result {
	m := npmK6Result{First: r.npm.K6.First, Last: r.npm.K6.Last}
	if r.nuget.K6.First.After(m.First) {
		m.First = r.nuget.K6.First
	}
	m.Lines = append(append([]npmK6Line{}, r.npm.K6.Lines...), r.nuget.K6.Lines...)
	return m
}

func (r fullHAResult) k6P99(from, to time.Time) (time.Duration, int) {
	m := r.mixed()
	return m.p99(from, to)
}

func (r fullHAResult) k6First() time.Time { return r.mixed().First }

func (r fullHAResult) installPublished(t *testing.T) {
	t.Helper()
	r.npm.installPublished(t, r.h)
	r.nuget.installPublished(t, r.nh)
}

// conditions are the minimum counts of R-ARCH-024's drain criterion, and the p99 of each stream on its own, reported: the
// tarballs of 1 to 5 MiB set the p99 of the mix (Q-HA-94), so the bound of R-HA-059 holds for the mix and the NuGet requests'
// own p99 is read next to it.
func (r fullHAResult) conditions(t *testing.T, firstEviction, drainEnd time.Time) {
	t.Helper()
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"npm ci", r.npm.count("ci"), archMinCI},
		{"npm publish", r.npm.count("publish"), archMinPublish},
		{"dotnet restore", r.nuget.count("restore"), archMinRestore},
		{"dotnet nuget push", r.nuget.count("push"), archMinPush},
	} {
		if c.got < c.want {
			t.Errorf("R-ARCH-024: the drain ran during %d %s, want at least %d", c.got, c.what, c.want)
		}
	}
	t.Logf("R-ARCH-024: the load of the drain ran %d npm ci, %d npm publish, %d dotnet restore and %d dotnet nuget push", r.npm.count("ci"), r.npm.count("publish"), r.nuget.count("restore"), r.nuget.count("push"))
	for _, s := range []struct {
		name string
		k    *npmK6Result
	}{{"npm", r.npm.K6}, {"NuGet", r.nuget.K6}} {
		warm, nWarm := s.k.p99(s.k.First, firstEviction)
		during, nDuring := s.k.p99(firstEviction, drainEnd.Add(2*time.Minute))
		t.Logf("k6 %s requests alone, p99: warm-up %s over %d requests, from the first eviction until 2 minutes after the drain %s over %d", s.name, warm, nWarm, during, nDuring)
	}
}

func (r fullHAResult) note() map[string]any {
	return map[string]any{
		"npm_k6": k6Summary(r.npm.K6), "nuget_k6": k6Summary(r.nuget.K6), "npm_commands": len(r.npm.Commands), "nuget_commands": len(r.nuget.Commands),
		"npm_ci": r.npm.count("ci"), "npm_publish": r.npm.count("publish"), "dotnet_restore": r.nuget.count("restore"), "dotnet_push": r.nuget.count("push"),
		"npm_published": len(r.npm.Published), "nuget_published": len(r.nuget.Published),
	}
}

// runFullFault is runLoadFault under the whole load.
func runFullFault(t *testing.T, h *npmHAFixture, nh *nugetHAFixture, name string, before, after time.Duration, fault func()) loadFaultRun[fullHAResult] {
	t.Helper()
	return runLoadFault(t, "nuget-ha-"+name, name, before, after, func() func(*testing.T) fullHAResult {
		return startFullLoad(t, h, nh, name).stopBoth
	}, fault, fullHAResult.note)
}
