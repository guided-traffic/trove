package app

import (
	"slices"
	"testing"
)

// listenerOnly are the areas only a role with a client listener has.
func listenerOnly() []Area {
	return []Area{AreaLimits, AreaSessions, AreaLogin, AreaAuth, AreaEndpoints, AreaAPI}
}

func TestR_ARCH_001_RolesEnableAreas(t *testing.T) {
	steps := wiringSteps()

	t.Run("R-ARCH-001 a role's areas are the steps of the list that run in it, in list order, and the plan holds one step per area", func(t *testing.T) {
		for _, role := range []string{"web", "worker", "all"} {
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
		web, _ := SubsystemsFor("web")
		worker, _ := SubsystemsFor("worker")
		all, _ := SubsystemsFor("all")
		for _, a := range []Area{
			AreaKeys, AreaPostgres, AreaAudit, AreaBlob, AreaValkey, AreaCoordination, AreaInfra,
			AreaKeyRing, AreaExposure, AreaRepository, AreaIdentity, AreaAuthz, AreaFiles,
		} {
			if !web.Has(a) || !worker.Has(a) || !all.Has(a) {
				t.Errorf("area %s: web %v worker %v all %v", a, web.Has(a), worker.Has(a), all.Has(a))
			}
		}
		for _, a := range listenerOnly() {
			if !web.Has(a) || worker.Has(a) || !all.Has(a) {
				t.Errorf("area %s: web %v worker %v all %v", a, web.Has(a), worker.Has(a), all.Has(a))
			}
		}
		if web.Has(AreaShell) || worker.Has(AreaShell) || all.Has(AreaShell) {
			t.Error("the shell stage is the shell profile's only")
		}
	})

	t.Run("R-ARCH-001 the shell profile is explicit: the listeners, the key ring and the shell stage, no infrastructure", func(t *testing.T) {
		for _, role := range []string{"web", "worker"} {
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
		web, _ := SubsystemsFor("web")
		worker, _ := SubsystemsFor("worker")
		all, _ := SubsystemsFor("all")
		if web.JobLoops || web.LeasePool || !worker.JobLoops || !worker.LeasePool || !all.JobLoops || !all.LeasePool {
			t.Errorf("web = %+v, worker = %+v, all = %+v", web, worker, all)
		}
		if worker.ClientListener || !web.ClientListener || !all.ClientListener {
			t.Errorf("listeners: web %v worker %v all %v", web.ClientListener, worker.ClientListener, all.ClientListener)
		}
	})
}
