package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The types of the synthetic steps: a producer chain a -> b -> c.
type (
	tA struct{ n int }
	tB struct{ n int }
	tC struct{ n int }
)

func stepA(*App, context.Context) (tA, error)           { return tA{1}, nil }
func stepB(_ *App, _ context.Context, a tA) (tB, error) { return tB{a.n + 1}, nil }
func stepC(_ *App, _ context.Context, a tA, b tB) (tC, error) {
	return tC{a.n + b.n}, nil
}

func TestR_ARCH_024_AMissingPrerequisiteIsAStartupError(t *testing.T) {
	list := []step{newStep("a", everyRole, stepA), newStep("b", everyRole, stepB), newStep("c", everyRole, stepC)}

	t.Run("R-ARCH-024 a role that lacks the area a step needs is refused naming step, type, area and role", func(t *testing.T) {
		for name, tc := range map[string]struct {
			areas []Area
			want  []string
		}{
			"c without b": {[]Area{"a", "c"}, []string{"area c", "tB", "area b", "role web"}},
			"b without a": {[]Area{"b"}, []string{"area b", "tA", "area a", "role web"}},
			"c alone":     {[]Area{"c"}, []string{"area c", "tA", "area a"}},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := planSteps(list, Subsystems{Areas: tc.areas}, "web")
				if err == nil {
					t.Fatal("planned a role that cannot satisfy its steps")
				}
				for _, w := range tc.want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q does not name %q", err, w)
					}
				}
			})
		}
	})

	t.Run("R-ARCH-024 a step listed before the area it needs is refused", func(t *testing.T) {
		_, err := planSteps([]step{newStep("b", everyRole, stepB), newStep("a", everyRole, stepA)}, Subsystems{Areas: []Area{"a", "b"}}, "web")
		if err == nil || !strings.Contains(err.Error(), "listed after it") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 a need that no step builds, two steps for one area and two builders of one type are refused", func(t *testing.T) {
		if _, err := planSteps([]step{newStep("b", everyRole, stepB)}, Subsystems{Areas: []Area{"b"}}, "web"); err == nil || !strings.Contains(err.Error(), "no step builds") {
			t.Errorf("unbuilt need: %v", err)
		}
		if _, err := planSteps([]step{newStep("a", everyRole, stepA), newStep("a", everyRole, stepA)}, Subsystems{}, "web"); err == nil || !strings.Contains(err.Error(), "two steps") {
			t.Errorf("two steps: %v", err)
		}
		if _, err := planSteps([]step{newStep("a", everyRole, stepA), newStep("a2", everyRole, stepA)}, Subsystems{}, "web"); err == nil || !strings.Contains(err.Error(), "both build") {
			t.Errorf("two builders: %v", err)
		}
	})

	t.Run("R-ARCH-024 a role that enables an area without a step is refused", func(t *testing.T) {
		_, err := planSteps(list, Subsystems{Areas: []Area{"a", "typo"}}, "web")
		if err == nil || !strings.Contains(err.Error(), "typo") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 the real step list refuses a role that drops an area another area needs, naming both", func(t *testing.T) {
		// dropped area -> the area that needs what it builds
		needs := map[Area]Area{
			AreaKeys: AreaBlob, AreaPostgres: AreaAudit, AreaAudit: AreaInfra, AreaBlob: AreaInfra, AreaValkey: AreaCoordination,
			AreaCoordination: AreaInfra, AreaInfra: AreaKeyRing, AreaExposure: AreaRepository, AreaRepository: AreaVirtual, AreaInvalidation: AreaProxy,
			AreaIdentity: AreaAuthz, AreaAuthz: AreaOwnership, AreaFiles: AreaProxyEngine, AreaLimits: AreaProxyEngine, AreaProxy: AreaProxyEngine, AreaProxyEngine: AreaVirtualEngine, AreaSessions: AreaLogin,
		}
		base, _ := SubsystemsFor("web")
		for drop, by := range needs {
			sub := base
			sub.Areas = slices.DeleteFunc(slices.Clone(base.Areas), func(a Area) bool { return a == drop })
			_, err := planSteps(wiringSteps(), sub, "web")
			if err == nil {
				t.Errorf("web without %s was planned", drop)
				continue
			}
			for _, w := range []string{string(drop), "role web", "area " + string(by)} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("web without %s: error %q does not name %q", drop, err, w)
				}
			}
		}
	})

	t.Run("R-ARCH-024 a web replica without the authz area is refused, because the ownership, files and API areas need a decision point", func(t *testing.T) {
		base, _ := SubsystemsFor("web")
		base.Areas = slices.DeleteFunc(base.Areas, func(a Area) bool { return a == AreaAuthz })
		_, err := planSteps(wiringSteps(), base, "web")
		if err == nil || !strings.Contains(err.Error(), "needs authzArea from area authz, which role web does not enable") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 a web replica without the files and format areas is refused at the listener instead of serving 503 on /files/", func(t *testing.T) {
		// The role plans (no step needs the files result), so the listener check is
		// the one that catches it.
		base, _ := SubsystemsFor("web")
		// The proxy engine and the format host need the files area, so all of them go.
		base.Areas = slices.DeleteFunc(base.Areas, func(a Area) bool {
			return a == AreaFiles || a == AreaProxyEngine || a == AreaFormatHost || a == AreaVirtualEngine
		})
		if _, err := planSteps(wiringSteps(), base, "web"); err != nil {
			t.Fatalf("plan: %v", err)
		}
		err := (&App{sub: base, parts: &wiring{}}).requireServing()
		if err == nil || !strings.Contains(err.Error(), "client listener") || !strings.Contains(err.Error(), "no files service") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 the files step refuses a missing decision point by name", func(t *testing.T) {
		_, err := (&App{}).wireFiles(t.Context(), infra{}, repositoryArea{}, authzArea{}, formatsArea{})
		if err == nil || !strings.HasPrefix(err.Error(), "files: no repository reader, no authorizer, no format hooks, no format seam") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 a shell has no listener areas to require", func(t *testing.T) {
		base, _ := SubsystemsFor("web")
		if err := (&App{sub: base.shell(), parts: &wiring{}}).requireServing(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-ARCH-024 the steps run in order and pass their results by type", func(t *testing.T) {
		plan, err := planSteps(list, Subsystems{Areas: []Area{"a", "b", "c"}}, "web")
		if err != nil {
			t.Fatal(err)
		}
		a := &App{plan: plan}
		w := &wiring{}
		if err := a.runSteps(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		if c, ok := lookup[tC](w); !ok || c.n != 3 {
			t.Fatalf("c = %+v, %v", c, ok)
		}
	})

	t.Run("R-ARCH-024 a failing step stops the run and later steps do not run", func(t *testing.T) {
		boom := errors.New("boom")
		ran := false
		plan, _ := planSteps([]step{
			newStep("a", everyRole, func(*App, context.Context) (tA, error) { return tA{}, boom }),
			newStep("b", everyRole, func(*App, context.Context, tA) (tB, error) { ran = true; return tB{}, nil }),
		}, Subsystems{Areas: []Area{"a", "b"}}, "web")
		err := (&App{plan: plan}).runSteps(context.Background(), &wiring{})
		if !errors.Is(err, boom) || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})

	t.Run("R-ARCH-024 a step that is run without its input fails naming the type", func(t *testing.T) {
		err := newStep("b", everyRole, stepB).run(context.Background(), &App{}, &wiring{})
		if err == nil || !strings.Contains(err.Error(), "tA") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_ARCH_024_AStepHasTheShapeOfAWiringStep(t *testing.T) {
	t.Run("R-ARCH-024 a method of another shape is refused when the list is built", func(t *testing.T) {
		for name, fn := range map[string]any{
			"not a function":      42,
			"no context":          func(*App) (tA, error) { return tA{}, nil },
			"no error":            func(*App, context.Context) tA { return tA{} },
			"two results":         func(*App, context.Context) (tA, tB, error) { return tA{}, tB{}, nil },
			"the wrong receiver":  func(int, context.Context) (tA, error) { return tA{}, nil },
			"the context is last": func(*App, tA, context.Context) (tB, error) { return tB{}, nil },
		} {
			t.Run(name, func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Fatal("newStep accepted it")
					}
				}()
				newStep("x", everyRole, fn)
			})
		}
	})

	t.Run("R-ARCH-024 every step of the real list declares its area and a distinct output", func(t *testing.T) {
		seen := map[string]Area{}
		for _, s := range wiringSteps() {
			if prev, dup := seen[s.gives.String()]; dup {
				t.Errorf("areas %s and %s both give %s", prev, s.area, s.gives)
			}
			seen[s.gives.String()] = s.area
		}
	})
}

func TestR_ARCH_024_AStepRefusesANilInput(t *testing.T) {
	ctx := context.Background()
	t.Run("R-ARCH-024 requireInputs names every nil input, typed nils included", func(t *testing.T) {
		var db *postgres.DB
		var rec interface{ Foo() }
		err := requireInputs("files", given("database", db), given("recorder", rec), given("name", "x"))
		if err == nil || err.Error() != "files: no database, no recorder" {
			t.Fatalf("err = %v", err)
		}
		if err := requireInputs("files", given("name", "x")); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-024 the infrastructure steps refuse a missing component by name", func(t *testing.T) {
		a := &App{}
		for name, tc := range map[string]struct {
			err  error
			want string
		}{
			"audit":        {second(a.wireAudit(ctx, postgresArea{})), "audit: no database"},
			"blob":         {second(a.wireBlob(ctx, keysArea{}, postgresArea{})), "blob: no outbound factory, no database"},
			"valkey":       {second(a.wireValkey(ctx, postgresArea{})), "valkey: no database"},
			"coordination": {second(a.wireCoordination(ctx, keysArea{}, postgresArea{}, valkeyArea{})), "coordination: no key ring, no database, no valkey client"},
			"infra":        {second(a.wireInfra(ctx, keysArea{}, postgresArea{}, auditArea{}, blobArea{}, coordArea{})), "infra: no database"},
			"authz":        {second(a.wireAuthz(ctx, infra{}, repositoryArea{}, identityArea{}, exposureArea{})), "authz: no repository reader, no principal store"},
			"login":        {second(a.wireLogin(ctx, infra{}, identityArea{}, sessionsArea{}, limitsArea{})), "login: no provisioner, no session store, no flow-start limiter"},
			"auth":         {second(a.wireAuth(ctx, identityArea{}, sessionsArea{}, limitsArea{})), "auth: no token service, no session store, no failure limiter"},
			"endpoints":    {second(a.wireEndpoints(ctx, exposureArea{}, limitsArea{})), "endpoints: no exposure rule service, no limiter set"},
			"api":          {second(a.wireAPI(ctx, limitsArea{}, authzArea{})), "api: no limiter set, no authorizer"},
			"repository":   {second(a.wireRepository(ctx, infra{}, exposureArea{})), "repository: no exposure rule service"},
			"ownership":    {second(a.wireOwnership(ctx, infra{}, authzArea{})), "ownership: no decision point"},
			"formathost":   {second(a.wireFormatHost(ctx, infra{}, formatsArea{}, repositoryArea{}, exposureArea{}, filesArea{}, proxyEngineArea{}, virtualEngineArea{})), "formathost: no item store, no decision point, no repository reader, no exposure ceiling"},
			"proxy-engine": {second(a.wireProxyEngine(ctx, infra{}, proxyArea{}, invalidationArea{}, filesArea{}, limitsArea{})), "proxy-engine: no upstream clients, no invalidation set, no item store, no replica count"},
			"virtual":      {second(a.wireVirtual(ctx, infra{}, repositoryArea{})), "virtual: no repository snapshot"},
			"proxy":        {second(a.wireProxy(ctx, infra{}, repositoryArea{}, invalidationArea{})), "proxy: no repository service, no invalidation service"},
		} {
			if tc.err == nil || !strings.HasPrefix(tc.err.Error(), tc.want) {
				t.Errorf("%s: err = %v, want prefix %q", name, tc.err, tc.want)
			}
		}
	})
}

func second[T any](_ T, err error) error { return err }

func TestLateValue(t *testing.T) {
	t.Run("R-ARCH-024 a job that comes before the value is set fails with a retryable error naming it, one after runs", func(t *testing.T) {
		var l late[string]
		var got string
		h := l.handler("the thing", func(_ context.Context, v string, _ *jobs.Run) error { got = v; return nil })
		err := h(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "the thing") {
			t.Fatalf("err = %v", err)
		}
		if jobs.IsPermanent(err) {
			t.Fatalf("a job that came first must be retried, got a permanent error: %v", err)
		}
		l.set("v")
		if err := h(context.Background(), nil); err != nil || got != "v" {
			t.Fatalf("err = %v, got = %q", err, got)
		}
	})

	t.Run("R-ARCH-024 a late value is set once", func(t *testing.T) {
		var l late[int]
		l.set(1)
		defer func() {
			if recover() == nil {
				t.Fatal("setting twice did not panic")
			}
		}()
		l.set(2)
	})

	t.Run("R-ARCH-024 a value set while handlers read is seen without a race", func(t *testing.T) {
		var l late[int]
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = l.get() }()
		}
		l.set(7)
		wg.Wait()
		if v, ok := l.get(); !ok || v != 7 {
			t.Fatalf("v = %d, %v", v, ok)
		}
	})
}
