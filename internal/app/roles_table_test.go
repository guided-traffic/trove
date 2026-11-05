package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

// roleTable is the area table of R-ARCH-001 written out as data, independent of the roleSet constants of the step list: the
// test below compares SubsystemsFor with it, so a step that moves to another role set changes the table on purpose or
// fails.
func roleTable() map[string][]Area {
	common := []Area{
		AreaKeys, AreaPostgres, AreaAudit, AreaBlob, AreaValkey, AreaCoordination, AreaInfra,
		AreaKeyRing, AreaFormatPorts, AreaExposure, AreaRepository, AreaVirtual, AreaInvalidation, AreaProxy,
		AreaIdentity, AreaAuthz, AreaOwnership, AreaCI, AreaVuln, AreaMirror, AreaFormats, AreaFiles,
	}
	listener := []Area{AreaLimits, AreaVirtualEngine, AreaAuth, AreaEndpoints, AreaAPI}
	filling := []Area{AreaProxyEngine, AreaFormatHost}
	management := []Area{AreaPackagesAPI, AreaVulnAPI, AreaSessions, AreaLogin, AreaCIExchange}
	// The background engine of the mirror's passes runs where jobs are claimed (R-REPO-053, Q-ARCH-316): worker and all.
	jobs := []Area{AreaMirrorEngine}
	join := func(parts ...[]Area) []Area {
		var out []Area
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	return map[string][]Area{
		"api":      join(common, listener, management),
		"packages": join(common, listener, filling),
		"worker":   join(common, jobs),
		"web":      join(common, listener, filling, management),
		"all":      join(common, listener, filling, management, jobs),
	}
}

// inStepOrder returns the areas of want in the order of the step list.
func inStepOrder(want []Area) []Area {
	var out []Area
	for _, s := range wiringSteps() {
		if slices.Contains(want, s.area) {
			out = append(out, s.area)
		}
	}
	return out
}

// TestR_ARCH_001_RoleTableIsData is the architecture test of the role table (Q-ARCH-294, E6a.1): the areas of each role equal
// its column of the table, in step order; packages has none of sessions, login, ci_exchange, packages-api and vuln-api; api has
// neither proxy-engine nor formathost; web is the union of api and packages; and planSteps accepts every role.
func TestR_ARCH_001_RoleTableIsData(t *testing.T) {
	table := roleTable()
	t.Run("R-ARCH-001 the areas of every role equal its column of the table, in step order", func(t *testing.T) {
		for role, want := range table {
			sub, err := SubsystemsFor(role)
			if err != nil {
				t.Fatalf("%s: %v", role, err)
			}
			if got := inStepOrder(want); !slices.Equal(sub.Areas, got) {
				t.Errorf("%s: areas\n got %v\nwant %v", role, sub.Areas, got)
			}
			if len(sub.Areas) != len(want) {
				t.Errorf("%s: %d areas, the table has %d: a step of the list has no column", role, len(sub.Areas), len(want))
			}
		}
	})

	t.Run("R-ARCH-001 packages builds none of sessions, login, ci_exchange, packages-api and vuln-api; api builds neither proxy-engine nor formathost", func(t *testing.T) {
		packages, _ := SubsystemsFor("packages")
		api, _ := SubsystemsFor("api")
		for _, a := range []Area{AreaSessions, AreaLogin, AreaCIExchange, AreaPackagesAPI, AreaVulnAPI} {
			if packages.Has(a) {
				t.Errorf("packages builds %s", a)
			}
			if !api.Has(a) {
				t.Errorf("api does not build %s", a)
			}
		}
		for _, a := range []Area{AreaProxyEngine, AreaFormatHost} {
			if api.Has(a) {
				t.Errorf("api builds %s", a)
			}
			if !packages.Has(a) {
				t.Errorf("packages does not build %s", a)
			}
		}
		// The authentication area is on both: with the session credential on api, over tokens only on packages.
		if !api.Has(AreaAuth) || !packages.Has(AreaAuth) {
			t.Error("api and packages both build the auth area")
		}
		// The virtual engine is on both: the whole engine on packages, the explanation over the read-only view on api.
		if !api.Has(AreaVirtualEngine) || !packages.Has(AreaVirtualEngine) {
			t.Error("api and packages both build the virtual-engine area")
		}
	})

	t.Run("R-ARCH-001 web is the union of api and packages, and all enables every area", func(t *testing.T) {
		api, _ := SubsystemsFor("api")
		packages, _ := SubsystemsFor("packages")
		web, _ := SubsystemsFor("web")
		all, _ := SubsystemsFor("all")
		union := inStepOrder(append(slices.Clone(api.Areas), packages.Areas...))
		if !slices.Equal(web.Areas, union) {
			t.Errorf("web %v is not the union %v", web.Areas, union)
		}
		var every []Area
		for _, s := range wiringSteps() {
			if s.roles&onShell == 0 || s.roles&everyRole != 0 {
				every = append(every, s.area)
			}
		}
		if got := inStepOrder(every); !slices.Equal(all.Areas, got) {
			t.Errorf("all %v does not enable every area %v", all.Areas, got)
		}
		worker, _ := SubsystemsFor("worker")
		if worker.Has(AreaLimits) || worker.Has(AreaAuth) || worker.Has(AreaVirtualEngine) {
			t.Error("a worker builds a listener area")
		}
	})

	t.Run("R-ARCH-001 planSteps accepts api, packages, worker, web and all", func(t *testing.T) {
		for role := range table {
			sub, err := SubsystemsFor(role)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := planSteps(wiringSteps(), sub, role); err != nil {
				t.Errorf("%s: %v", role, err)
			}
		}
	})

	t.Run("R-ARCH-001 the families of a role", func(t *testing.T) {
		for role, want := range map[string][]string{
			"api":      {config.SurfaceUI, config.SurfaceAPI, config.SurfaceCIExchange},
			"packages": {config.SurfacePackages, config.SurfaceFiles},
			"web":      config.Surfaces(),
			"all":      config.Surfaces(),
			"worker":   nil,
		} {
			sub, _ := SubsystemsFor(role)
			if !slices.Equal(sub.Families, want) {
				t.Errorf("%s: families %v, want %v", role, sub.Families, want)
			}
		}
		api, _ := SubsystemsFor("api")
		packages, _ := SubsystemsFor("packages")
		if got := slices.Concat(api.Families, packages.Families); len(got) != len(config.Surfaces()) {
			t.Errorf("api and packages together serve %v: every family exactly once is the cut", got)
		}
	})

	t.Run("R-ARCH-001 an unknown role is refused with the message of process.role", func(t *testing.T) {
		for _, role := range []string{"", "backup", "bastion", "WEB", "fetch"} {
			_, err := SubsystemsFor(role)
			if err == nil || !strings.Contains(err.Error(), config.RoleError) {
				t.Errorf("role %q: %v", role, err)
			}
		}
	})
}

// TestR_ARCH_001_StepNeedsFitTheRoleSets keeps the step list role sets honest: a step that runs on packages must not need the
// result of a step that runs only on api, and the other way round, unless the need is optional (Q-ARCH-300). planSteps proves
// it for the real list; this test names the property for each pair.
func TestR_ARCH_001_StepNeedsFitTheRoleSets(t *testing.T) {
	steps := wiringSteps()
	owner := map[string]step{}
	for _, s := range steps {
		owner[s.gives.String()] = s
	}
	for _, s := range steps {
		for _, n := range s.needs {
			o := owner[n.String()]
			if s.optional[n] {
				continue
			}
			if s.roles&^o.roles&everyRole != 0 {
				t.Errorf("area %s runs on roles that area %s, which it needs %s from, does not: %b against %b", s.area, o.area, n.Name(), s.roles, o.roles)
			}
		}
	}
}

// TestR_ARCH_001_OptionalNeeds covers the optional input of a step (Q-ARCH-300): a role that does not enable the area of an
// optional need is planned and the step is handed the zero value; a role that enables it must list it before the step.
func TestR_ARCH_001_OptionalNeeds(t *testing.T) {
	steps := wiringSteps()
	byArea := map[Area]step{}
	for _, s := range steps {
		byArea[s.area] = s
	}
	t.Run("R-ARCH-001 the auth area takes the session store optionally, the virtual engine the remote engine", func(t *testing.T) {
		auth := byArea[AreaAuth]
		if !auth.optional[byArea[AreaSessions].gives] || len(auth.optional) != 1 {
			t.Errorf("auth optional = %v", auth.optional)
		}
		ve := byArea[AreaVirtualEngine]
		if !ve.optional[byArea[AreaProxyEngine].gives] || len(ve.optional) != 1 {
			t.Errorf("virtual-engine optional = %v", ve.optional)
		}
	})

	t.Run("R-ARCH-001 an optional need whose area the role leaves out is planned; one that is listed after the step is refused", func(t *testing.T) {
		list := wiringSteps()
		packages, _ := SubsystemsFor("packages")
		if _, err := planSteps(list, packages, "packages"); err != nil {
			t.Fatalf("packages: %v", err)
		}
		// Move the sessions step behind the auth step for a role that enables both.
		api, _ := SubsystemsFor("api")
		var swapped []step
		for _, s := range list {
			if s.area != AreaSessions {
				swapped = append(swapped, s)
			}
		}
		swapped = append(swapped, byArea[AreaSessions])
		_, err := planSteps(swapped, api, "api")
		if err == nil || !strings.Contains(err.Error(), "which is listed after it") {
			t.Errorf("sessions after auth: %v", err)
		}
	})

	t.Run("R-ARCH-001 the step receives the zero value of an optional need the role does not build, and a result when it does", func(t *testing.T) {
		type given struct{ n int }
		type extra struct{ n int }
		var got []int
		first := newStep("a", everyRole, func(*App, context.Context) (given, error) { return given{7}, nil })
		second := newStep("b", everyRole, func(_ *App, _ context.Context, g given, e extra) (struct{ ok bool }, error) {
			got = append(got, g.n, e.n)
			return struct{ ok bool }{true}, nil
		}, withOptional(extra{}))
		w := &wiring{}
		for _, s := range []step{first, second} {
			if err := s.run(t.Context(), &App{}, w); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(got, []int{7, 0}) {
			t.Errorf("inputs = %v, want the result and the zero value", got)
		}
	})

	t.Run("R-ARCH-001 marking a type optional that the step does not take is a programming error", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		type other struct{}
		newStep("a", everyRole, func(*App, context.Context) (struct{}, error) { return struct{}{}, nil }, withOptional(other{}))
	})
}
