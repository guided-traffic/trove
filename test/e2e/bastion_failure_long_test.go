//go:build e2e

package e2e

// The Bastion under the failure suite (R-HA-052, R-BAS-049; set bastion-long: nightly, on dispatch and locally, a red long leg is a
// finding, not a blocker, owner 2026-11-01): a Bastion replica lost, the main installation failing over while the Bastion serves, and
// a rolling upgrade of either side, each under the client load of bastion_load_test.go. The pass condition is the one of R-HA-052:
// no client command fails after its own retries, which for the Bastion means, per R-BAS-021 and R-BAS-027, that

//   - what the Bastion holds (the answers of its period and its cached artifacts and documents) is served without the main
//     installation, so a fault of the main installation reaches a client only through a revalidation or a miss;
//   - a forward that meets the fault answers 502 or 504 and keeps nothing (the Bastion does not retry), and the client's retry
//     (npm fetch-retries, NuGet's retries) is what makes it succeed: a Bastion that retried would only hold the client longer;
//   - the link may report the main installation unreachable (R-BAS-027) but never for the two minutes of the alert, and the
//     interface never mismatches (R-BAS-028).

import (
	"strconv"
	"testing"
	"time"
)

// basRollTag is a value that differs on every call, for a pod annotation that rolls a Deployment through `helm upgrade`.
func basRollTag(env *basEnv, what string) string {
	return env.run + "-" + what + "-" + strconv.FormatInt(time.Now().Unix(), 10)
}

// TestBastionLong_R_BAS_049_ReplicaLost (R-HA-052 pod crash, R-BAS-043, R-BAS-033, R-BAS-020): one replica of the Bastion is
// force-deleted (`--grace-period=0 --force`, no drain, no readiness flip) under the load, with the volume backend (the cache is the
// pod's: the replacement starts empty, routeByPath moves the lost replica's paths) and with the bucket backend (the cache and the
// answers are outside the pod). Clients succeed with their default retries, the replacement becomes ready, and warm content keeps
// being served: with the bucket the main installation sends no artifact again, with the volume at most once per replica.
func TestBastionLong_R_BAS_049_ReplicaLost(t *testing.T) {
	env := basFx(t)
	warm := env.npmSets["npm"].tarballs() + len(env.nuget.ids)
	for _, site := range env.sites() {
		t.Run(site.variant, func(t *testing.T) {
			var survivor string
			var hitsBefore, hitsAfter float64
			var replacement time.Duration
			env.underLoad(t, site, "lost", 100, []basFault{{
				name:        "R-HA-052 R-BAS-043 R-BAS-049 a replica force-deleted under load (" + site.variant + ")",
				measureMain: true,
				run: func() {
					ps := basReadyPods(t, site)
					if len(ps) != 2 {
						t.Fatalf("%s has %d ready replicas, want 2", site.release, len(ps))
					}
					victim := ps[0].Name
					survivor = ps[1].Name
					hitsBefore = msum(basMetrics(t, survivor), "trove_bastion_requests_total", `class="artifact"`, `result="hit"`)
					start := time.Now()
					kb(t, "delete", "pod", victim, "--grace-period=0", "--force")
					waitBastionReady(t, site, 2)
					replacement = time.Since(start)
					t.Logf("%s was force-deleted; the pair was ready again after %s", victim, replacement.Round(time.Second))
				},
				check: func(t *testing.T, f *basFaultRecord) {
					hitsAfter = msum(basMetrics(t, survivor), "trove_bastion_requests_total", `class="artifact"`, `result="hit"`)
					if hitsAfter <= hitsBefore {
						t.Errorf("the surviving replica %s served no artifact from its cache during the loss (hits %v to %v): warm content was not served", survivor, hitsBefore, hitsAfter)
					}
					fills := warmFills(f.Main)
					t.Logf("%d warm artifacts; %d of them sent by the main installation again (hits of %s %v to %v); the replacement was ready after %s",
						warm, len(fills), survivor, hitsBefore, hitsAfter, replacement.Round(time.Second))
					switch site.variant {
					case "bucket":
						if len(fills) != 0 {
							t.Errorf("%d warm artifacts were fetched from the main installation again although the bucket holds them (R-BAS-030):\n%s", len(fills), describeReqs(fills))
						}
					default:
						// each replica fetches an artifact at most once while it is fresh: the lost replica's paths move to the survivor
						// and, when the replacement is ready, back to a replica with an empty volume
						if len(fills) > 2*warm {
							t.Errorf("%d warm artifacts were fetched again, want at most %d (once per replica):\n%s", len(fills), 2*warm, describeReqs(fills))
						}
					}
				},
			}})
		})
	}
}

// TestBastionLong_R_BAS_049_MainFailover (R-HA-052, R-HA-015, R-HA-057, R-BAS-021, R-BAS-027): the main installation fails over
// while the Bastion with the bucket backend serves the load: the PostgreSQL switchover (`failover.sh`), the Valkey failover
// (`valkey-failover.sh`) and the crash of a `packages` pod (force-deleted). The main installation absorbs the first two itself
// (R-HA-015, R-HA-057); a forward that meets the crash is a 502 or 504 of the Bastion and the client's retry repeats it.
func TestBastionLong_R_BAS_049_MainFailover(t *testing.T) {
	env := basFx(t)
	env.underLoad(t, env.bkt, "failover", 150, []basFault{
		{
			name: "R-HA-052 R-HA-015 R-BAS-021 R-BAS-027 the PostgreSQL switchover while the Bastion serves",
			run:  func() { script(t, "failover.sh") },
		},
		{
			name: "R-HA-052 R-HA-057 R-BAS-021 R-BAS-027 the Valkey failover while the Bastion serves",
			run:  func() { script(t, "valkey-failover.sh") },
		},
		{
			name: "R-HA-052 R-HA-011 R-BAS-021 R-BAS-027 a packages pod of the main installation force-deleted while the Bastion serves",
			run: func() {
				ps := livePods(t, selPackages)
				if len(ps) < 2 {
					t.Fatalf("the main installation has %d packages pods, want at least 2", len(ps))
				}
				kn(t, "delete", "pod", ps[0].Name, "--grace-period=0", "--force")
				kn(t, "rollout", "status", "deployment/trove-packages", "--timeout=5m")
				waitServingSettled(t)
			},
		},
	})
}

// TestBastionLong_R_BAS_049_RollingUpgradeMain (R-HA-052 rolling restart and rolling upgrade, R-HA-020, R-BAS-008, R-BAS-028):
// `kubectl rollout restart` of the main installation's packages and api Deployments, and a `helm upgrade` of the release with a
// changed pod annotation of both (the migration Job runs, the pods roll), each under the load through the Bastion with the bucket
// backend. The two sides keep the same interface version: the mismatch gauge never rises.
func TestBastionLong_R_BAS_049_RollingUpgradeMain(t *testing.T) {
	env := basFx(t)
	restart := func(role string) func() {
		return func() {
			kn(t, "rollout", "restart", "deployment/trove-"+role)
			kn(t, "rollout", "status", "deployment/trove-"+role, "--timeout=10m")
			waitServingSettled(t)
		}
	}
	env.underLoad(t, env.bkt, "roll-main", 450, []basFault{
		{name: "R-HA-052 R-BAS-021 R-BAS-027 rollout restart of the main installation's packages", run: restart("packages")},
		{name: "R-HA-052 R-BAS-008 R-BAS-027 rollout restart of the main installation's api", run: restart("api")},
		{
			name: "R-HA-052 R-HA-020 R-BAS-028 helm upgrade of the main installation with a changed value that rolls api and packages",
			run: func() {
				tag := basRollTag(env, "main")
				upMain(t, "--set-string api.podAnnotations.e2e-roll="+tag+" --set-string packages.podAnnotations.e2e-roll="+tag)
			},
		},
	})
}

// TestBastionLong_R_BAS_049_RollingUpgradeBastion (R-HA-052 rolling restart, R-BAS-043, R-BAS-045, R-BAS-028): `kubectl rollout restart`
// of the Bastion's Deployment and a `helm upgrade` of its release with a changed pod annotation (bastion.sh install), each under
// the load, once per backend. maxSurge 1 and maxUnavailable 0 keep two ready replicas throughout; the drain of the replaced pod
// (R-BAS-043) finishes its transfers.
func TestBastionLong_R_BAS_049_RollingUpgradeBastion(t *testing.T) {
	env := basFx(t)
	for _, site := range env.sites() {
		t.Run(site.variant, func(t *testing.T) {
			env.underLoad(t, site, "roll-bastion", 300, []basFault{
				{
					name: "R-HA-052 R-BAS-043 R-BAS-049 rollout restart of the Bastion (" + site.variant + ")",
					run: func() {
						kb(t, "rollout", "restart", "deployment/"+site.release)
						kb(t, "rollout", "status", "deployment/"+site.release, "--timeout=10m")
						waitBastionReady(t, site, 2)
					},
				},
				{
					name: "R-HA-052 R-BAS-045 R-BAS-028 helm upgrade of the Bastion with a changed value that rolls its pods (" + site.variant + ")",
					run: func() {
						out, err := basScriptTryEnv([]string{"TROVE_BASTION_HELM_EXTRA=--set-string podAnnotations.e2e-roll=" + basRollTag(env, site.variant)}, "install", site.variant)
						if err != nil {
							t.Fatalf("bastion.sh install %s: %v\n%s", site.variant, err, tail(out, 2000))
						}
						waitBastionReady(t, site, 2)
					},
				},
			})
		})
	}
}
