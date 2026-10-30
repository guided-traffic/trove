package app

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/jobs"
)

// The wiring of a replica is a list of typed steps (R-ARCH-024 steps 3 to 8).
// A step is a method of App in its own wire_<area>.go. It takes the typed
// results of earlier steps and returns the typed result of its area, or an
// error that stops the start-up:
//
//	func (a *App) wireBlob(ctx context.Context, keys keysArea, pg postgresArea) (blobArea, error)
//
// wiringSteps lists them in start order. newStep reads the input and output
// types off the method, so the list needs no repeated signature, and planSteps
// checks the list against the role's areas when the replica is built. A missing
// prerequisite is therefore an error naming the step, the type and the role
// before anything starts, never a nil component that answers 503 later.

// wiring holds the results of the steps run so far, by type. Only the bootstrap
// goroutine reads and writes it.
type wiring struct {
	vals map[reflect.Type]reflect.Value
}

func (w *wiring) put(v reflect.Value) {
	if w.vals == nil {
		w.vals = map[reflect.Type]reflect.Value{}
	}
	w.vals[v.Type()] = v
}

// lookup returns a result by type; false if no step produced it, which is the
// case of a result whose area the role does not enable (the shell profile has no
// infrastructure).
func lookup[T any](w *wiring) (T, bool) {
	if w != nil {
		if v, ok := w.vals[reflect.TypeFor[T]()]; ok {
			return v.Interface().(T), true
		}
	}
	var zero T
	return zero, false
}

// part returns a result by type, the zero value when no step produced it: the
// zero value of an area has nil members, which is how the code that serves
// requests reads an area a role does not build. Use [lookup] to tell.
func part[T any](w *wiring) T {
	v, _ := lookup[T](w)
	return v
}

// roleSet is the set of roles (and the shell profile) a step runs in. It is data
// of the step, so a role's areas are whatever the list says and an area is added
// in one line.
type roleSet uint8

const (
	onWorker roleSet = 1 << iota
	onWeb
	onAll
	onShell

	// everyRole is a serving role of any kind; withListener is a role that opens a
	// client listener.
	everyRole    = onWorker | onWeb | onAll
	withListener = onWeb | onAll
)

// areasFor lists the areas the step list enables for a role, in list order.
func areasFor(role roleSet) []Area {
	var out []Area
	for _, s := range wiringSteps() {
		if s.roles&role != 0 {
			out = append(out, s.area)
		}
	}
	return out
}

// step is one area's wiring with the types it consumes and produces.
type step struct {
	area  Area
	roles roleSet
	needs []reflect.Type
	gives reflect.Type
	fn    reflect.Value
}

var (
	appType = reflect.TypeFor[*App]()
	ctxType = reflect.TypeFor[context.Context]()
	errType = reflect.TypeFor[error]()
)

// newStep builds the step of an area from its method expression, for example
// (*App).wireBlob. The method must be func(*App, context.Context, In...) (Out,
// error); anything else is a programming error and panics when the list is
// built, which every test of the package does.
func newStep(area Area, roles roleSet, fn any) step {
	v := reflect.ValueOf(fn)
	t := v.Type()
	if t.Kind() != reflect.Func || t.NumIn() < 2 || t.In(0) != appType || t.In(1) != ctxType ||
		t.NumOut() != 2 || t.Out(1) != errType {
		panic(fmt.Sprintf("app: the step of area %s is %s, want func(*App, context.Context, inputs...) (output, error)", area, t))
	}
	s := step{area: area, roles: roles, gives: t.Out(0), fn: v}
	for i := 2; i < t.NumIn(); i++ {
		s.needs = append(s.needs, t.In(i))
	}
	return s
}

// run calls the step with the results of the steps before it and records its
// result. A result that an earlier step did not produce is an error naming the
// area and the type; planSteps has proven that cannot happen for a planned list,
// so this is the second line of defence.
func (s step) run(ctx context.Context, a *App, w *wiring) error {
	args := make([]reflect.Value, 0, 2+len(s.needs))
	args = append(args, reflect.ValueOf(a), reflect.ValueOf(ctx))
	for _, n := range s.needs {
		v, ok := w.vals[n]
		if !ok {
			return fmt.Errorf("%s: %s is not wired", s.area, n.Name())
		}
		args = append(args, v)
	}
	out := s.fn.Call(args)
	if err, _ := out[1].Interface().(error); err != nil {
		return err
	}
	w.put(out[0])
	return nil
}

// wiringSteps is the step list, in start order. Adding an area is its
// wire_<area>.go, with the area's name and result type, and one line here that
// also says which roles run it (docs/developer/architecture.md).
func wiringSteps() []step {
	return []step{
		newStep(AreaKeys, everyRole|onShell, (*App).wireKeys),
		newStep(AreaPostgres, everyRole, (*App).wirePostgres),
		newStep(AreaAudit, everyRole, (*App).wireAudit),
		newStep(AreaBlob, everyRole, (*App).wireBlob),
		newStep(AreaValkey, everyRole, (*App).wireValkey),
		newStep(AreaCoordination, everyRole, (*App).wireCoordination),
		newStep(AreaInfra, everyRole, (*App).wireInfra),
		newStep(AreaKeyRing, everyRole, (*App).wireKeyRing),
		newStep(AreaExposure, everyRole, (*App).wireExposure),
		newStep(AreaRepository, everyRole, (*App).wireRepository),
		newStep(AreaVirtual, everyRole, (*App).wireVirtual),
		newStep(AreaInvalidation, everyRole, (*App).wireInvalidation),
		newStep(AreaProxy, everyRole, (*App).wireProxy),
		newStep(AreaIdentity, everyRole, (*App).wireIdentity),
		newStep(AreaAuthz, everyRole, (*App).wireAuthz),
		newStep(AreaOwnership, everyRole, (*App).wireOwnership),
		newStep(AreaFormats, everyRole, (*App).wireFormats),
		newStep(AreaFiles, everyRole, (*App).wireFiles),
		newStep(AreaLimits, withListener, (*App).wireLimits),
		newStep(AreaProxyEngine, withListener, (*App).wireProxyEngine),
		newStep(AreaVirtualEngine, withListener, (*App).wireVirtualEngine),
		newStep(AreaFormatHost, withListener, (*App).wireFormatHost),
		newStep(AreaPackagesAPI, withListener, (*App).wirePackagesAPI),
		newStep(AreaSessions, withListener, (*App).wireSessions),
		newStep(AreaLogin, withListener, (*App).wireLogin),
		newStep(AreaAuth, withListener, (*App).wireAuth),
		newStep(AreaEndpoints, withListener, (*App).wireEndpoints),
		newStep(AreaAPI, withListener, (*App).wireAPI),
		newStep(AreaShell, onShell, (*App).wireShell),
	}
}

// planSteps selects the steps a role runs and refuses a list the role cannot
// satisfy. It runs in New, so a replica whose role lacks an area that another
// step needs does not start at all: the error names the step, the missing type,
// the area that would build it and the role.
func planSteps(list []step, sub Subsystems, role string) ([]step, error) {
	owner := map[reflect.Type]int{}
	areas := map[Area]bool{}
	for i, s := range list {
		if areas[s.area] {
			return nil, fmt.Errorf("wiring: area %s has two steps", s.area)
		}
		areas[s.area] = true
		if j, dup := owner[s.gives]; dup {
			return nil, fmt.Errorf("wiring: areas %s and %s both build %s", list[j].area, s.area, s.gives.Name())
		}
		owner[s.gives] = i
	}
	for _, a := range sub.Areas {
		if !areas[a] {
			return nil, fmt.Errorf("wiring: role %s enables area %s, which has no step", role, a)
		}
	}
	var plan []step
	for i, s := range list {
		if !sub.Has(s.area) {
			continue
		}
		for _, n := range s.needs {
			j, ok := owner[n]
			switch {
			case !ok:
				return nil, fmt.Errorf("wiring: area %s needs %s, which no step builds", s.area, n.Name())
			case !sub.Has(list[j].area):
				return nil, fmt.Errorf("wiring: area %s needs %s from area %s, which role %s does not enable", s.area, n.Name(), list[j].area, role)
			case j > i:
				return nil, fmt.Errorf("wiring: area %s needs %s from area %s, which is listed after it", s.area, n.Name(), list[j].area)
			}
		}
		plan = append(plan, s)
	}
	return plan, nil
}

// runSteps runs the planned steps in order. The first error stops the start-up.
func (a *App) runSteps(ctx context.Context, w *wiring) error {
	for _, s := range a.plan {
		if err := s.run(ctx, a, w); err != nil {
			return err
		}
	}
	return nil
}

// input is one value a wire function needs, named for the error.
type input struct {
	name string
	v    any
}

func given(name string, v any) input { return input{name, v} }

// requireInputs returns an error naming every nil input of an area. A step is
// handed its inputs by the list, and the list proves the types are there; this
// proves the values in them are, so that a component a caller left out fails
// the start-up with the name of what is missing.
func requireInputs(area Area, ins ...input) error {
	var missing []string
	for _, in := range ins {
		if isNil(in.v) {
			missing = append(missing, "no "+in.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", area, strings.Join(missing, ", "))
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// late is a value a step sets once and a job handler reads at run time. The job
// registry is built, and frozen, before the area that owns the service is wired,
// so a handler cannot capture it. A job claimed before set ran is retried like
// any failure; one claimed after finds the value.
type late[T any] struct{ p atomic.Pointer[T] }

func (l *late[T]) set(v T) {
	if !l.p.CompareAndSwap(nil, &v) {
		panic("app: late value set twice")
	}
}

func (l *late[T]) get() (T, bool) {
	if p := l.p.Load(); p != nil {
		return *p, true
	}
	var zero T
	return zero, false
}

// handler adapts fn to a job handler that runs it with the value. what names
// the value in the error of a job that came first.
func (l *late[T]) handler(what string, fn func(ctx context.Context, v T, run *jobs.Run) error) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		v, ok := l.get()
		if !ok {
			return fmt.Errorf("%s: this replica has not wired it yet", what)
		}
		return fn(ctx, v, run)
	}
}
