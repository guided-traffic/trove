package app

import (
	"slices"
	"testing"
)

// listenerOnly are the areas every role with a client listener has and a worker has not.
func listenerOnly() []Area {
	return []Area{AreaLimits, AreaVirtualEngine, AreaAuth, AreaEndpoints, AreaAPI}
}

// servingRoles are the roles that run the step list; the shell profile is not one.
var servingRoles = []string{"api", "packages", "worker", "web", "all"}

func TestR_ARCH_001_RolesEnableAreas(t *testing.T) {
	steps := wiringSteps()

	t.Run("R-ARCH-001 a role's areas are the steps of the list that run in it, in list order, and the plan holds one step per area", func(t *testing.T) {
		for _, role := range servingRoles {
			sub, err := SubsystemsFor(role)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planSteps(steps, sub, role)
			if err != nil {
				t.Fatalf("%s: %v", role, err)
			}
			var got []Area
			for _, s := range plan {
				got = append(got, s.area)
			}
			if !slices.Equal(got, sub.Areas) {
				t.Errorf("%s: plan %v, areas %v", role, got, sub.Areas)
			}
			seen := map[Area]bool{}
			for _, a := range got {
				if seen[a] {
					t.Errorf("%s: area %s twice", role, a)
				}
				seen[a] = true
			}
		}
	})

	t.Run("R-ARCH-001 every serving role has the infrastructure and the domain areas of its jobs, and a worker has no listener area", func(t *testing.T) {
		worker, _ := SubsystemsFor("worker")
		for _, role := range servingRoles {
			sub, _ := SubsystemsFor(role)
			for _, a := range []Area{
				AreaKeys, AreaPostgres, AreaAudit, AreaBlob, AreaValkey, AreaCoordination, AreaInfra,
				AreaKeyRing, AreaExposure, AreaRepository, AreaIdentity, AreaAuthz, AreaFiles,
			} {
				if !sub.Has(a) {
					t.Errorf("role %s lacks area %s", role, a)
				}
			}
			for _, a := range listenerOnly() {
				if sub.Has(a) != (role != "worker") {
					t.Errorf("area %s: role %s has it %v", a, role, sub.Has(a))
				}
			}
			if sub.Has(AreaShell) {
				t.Errorf("role %s has the shell stage, which is the shell profile's only", role)
			}
		}
		if worker.Families != nil {
			t.Errorf("a worker serves no family: %v", worker.Families)
		}
	})

	t.Run("R-ARCH-001 the shell profile is explicit: the listeners, the key ring and the shell stage, no infrastructure", func(t *testing.T) {
		for _, role := range servingRoles {
			base, _ := SubsystemsFor(role)
			sh := base.shell()
			if !sh.Shell || sh.MainPool || sh.Valkey || sh.LeasePool || sh.JobLoops || sh.ChangePoll || sh.Registry {
				t.Errorf("%s shell = %+v", role, sh)
			}
			if sh.ClientListener != base.ClientListener || sh.OpsListener != base.OpsListener {
				t.Errorf("%s shell lost its listeners: %+v", role, sh)
			}
			if !slices.Equal(sh.Areas, []Area{AreaKeys, AreaShell}) {
				t.Errorf("%s shell areas = %v", role, sh.Areas)
			}
			plan, err := planSteps(steps, sh, role)
			if err != nil || len(plan) != 2 || plan[0].area != AreaKeys || plan[1].area != AreaShell {
				t.Errorf("%s shell plan = %v, %v", role, plan, err)
			}
		}
	})

	t.Run("R-ARCH-001 the roles differ in the flags: only a job claimer has the lease pool and the collector", func(t *testing.T) {
		for _, role := range servingRoles {
			sub, _ := SubsystemsFor(role)
			claims := role == "worker" || role == "all"
			if sub.JobLoops != claims || sub.LeasePool != claims {
				t.Errorf("%s: JobLoops %v, LeasePool %v, want %v", role, sub.JobLoops, sub.LeasePool, claims)
			}
			if sub.ClientListener != (role != "worker") {
				t.Errorf("%s: ClientListener %v", role, sub.ClientListener)
			}
			if !sub.OpsListener || !sub.MainPool || !sub.Valkey || !sub.ChangePoll || !sub.Registry {
				t.Errorf("%s lacks a subsystem every role has: %+v", role, sub)
			}
		}
	})
}
