package jobs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// DedupMode is how an enqueue treats a job with the same key (R-ARCH-016).
type DedupMode string

const (
	// NoDedup has no key.
	NoDedup DedupMode = ""
	// Exclusive allows no new job while one with the same key is queued or
	// running; a conflicting enqueue returns the existing job.
	Exclusive DedupMode = "exclusive"
	// Coalesce allows at most one waiting job while another runs, so work that
	// arrives during a run is not lost. Jobs with one key never run together.
	Coalesce DedupMode = "coalesce"
)

// Handler runs one attempt of a job. It must be idempotent (delivery is at
// least once) and stop when ctx ends; context.Cause(ctx) tells why: a lost
// lease, a cancellation, a release for shutdown or a timeout. A handler that
// returns an error because of a release has its job released, not failed.
type Handler func(ctx context.Context, r *Run) error

// KindSpec registers one job kind.
type KindSpec struct {
	// Handler is required.
	Handler Handler
	// Queue defaults to "default". Its worker slots come from jobs.workers.
	Queue string
	// Timeout bounds one attempt; zero means none.
	Timeout time.Duration
	// MaxAttempts defaults to 10.
	MaxAttempts int
	// DedupMode is the enqueue mode of the kind. With a mode, an enqueue without
	// an explicit key uses the kind's name as the key.
	DedupMode DedupMode
	// Priority defaults to 100; lower runs first.
	Priority int
}

// TaskSpec registers a periodic task (R-ARCH-016, R-OPS-019). The scheduler
// enqueues the task's latest due slot as a job of Kind with dedup key = the task
// name (Exclusive) unless the kind says otherwise. maintenance.tasks.<name> in
// the configuration overrides Enabled and Schedule.
type TaskSpec struct {
	// Kind is the job kind to enqueue; defaults to the task's name, because a
	// kind that a maintenance task enqueues carries the task's name.
	Kind string
	// Schedule is the default five-field cron expression (UTC).
	Schedule string
	// Disabled makes the task off unless the configuration enables it.
	Disabled bool
	// Args is the job's args, any JSON-encodable value.
	Args any
	// DedupKey overrides the default key (the task name), for tasks that share
	// one key, such as gc and gc-full.
	DedupKey string
	// ManualOnly marks a task without a schedule: the scheduler never enqueues
	// it and it runs only through POST /api/v1/system/tasks/{name}/run
	// (R-OPS-019, for example secrets-reencrypt). Schedule must be empty, and a
	// maintenance.tasks.<name> entry for it is a configuration error.
	ManualOnly bool
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,62}$`)

// Registry holds the kinds and tasks of a process. It is filled at
// initialisation and frozen before the replica serves; registering afterwards
// panics. [Default] is the process-wide registry; tests use their own.
type Registry struct {
	mu     sync.RWMutex
	kinds  map[string]KindSpec
	tasks  map[string]TaskSpec
	frozen bool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]KindSpec{}, tasks: map[string]TaskSpec{}}
}

// Default is the process-wide registry that RegisterKind and RegisterTask fill.
var Default = NewRegistry()

// RegisterKind registers a kind in [Default].
func RegisterKind(kind string, spec KindSpec) { Default.RegisterKind(kind, spec) }

// RegisterTask registers a task in [Default].
func RegisterTask(name string, spec TaskSpec) { Default.RegisterTask(name, spec) }

// Freeze freezes [Default]; the composition root calls it before it serves.
func Freeze() { Default.Freeze() }

// RegisterKind registers a kind. It panics on a programming error: a bad or
// duplicate name, a missing handler, an unknown dedup mode, or a registration
// after Freeze.
func (r *Registry) RegisterKind(kind string, spec KindSpec) {
	if !namePattern.MatchString(kind) {
		panic(kindError(kind, "invalid name"))
	}
	if spec.Handler == nil {
		panic(kindError(kind, "no handler"))
	}
	switch spec.DedupMode {
	case NoDedup, Exclusive, Coalesce:
	default:
		panic(kindError(kind, "unknown dedup mode %q", spec.DedupMode))
	}
	if spec.Queue == "" {
		spec.Queue = "default"
	}
	if spec.MaxAttempts == 0 {
		spec.MaxAttempts = 10
	}
	if spec.MaxAttempts < 1 {
		panic(kindError(kind, "MaxAttempts %d", spec.MaxAttempts))
	}
	if spec.Priority == 0 {
		spec.Priority = 100
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		panic(kindError(kind, "registered after the registry was frozen"))
	}
	if _, dup := r.kinds[kind]; dup {
		panic(kindError(kind, "registered twice"))
	}
	r.kinds[kind] = spec
}

// RegisterTask registers a periodic task. It panics on a bad name, a duplicate,
// an invalid default schedule or a registration after Freeze.
func (r *Registry) RegisterTask(name string, spec TaskSpec) {
	if !namePattern.MatchString(name) {
		panic(fmt.Sprintf("jobs: invalid task name %q", name))
	}
	if spec.ManualOnly {
		if spec.Schedule != "" {
			panic(fmt.Sprintf("jobs: task %q is manual-only and has a schedule", name))
		}
	} else if _, err := ParseSchedule(spec.Schedule); err != nil {
		panic(fmt.Sprintf("jobs: task %q: %v", name, err))
	}
	if spec.Kind == "" {
		spec.Kind = name
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		panic(fmt.Sprintf("jobs: task %q registered after the registry was frozen", name))
	}
	if _, dup := r.tasks[name]; dup {
		panic(fmt.Sprintf("jobs: task %q registered twice", name))
	}
	r.tasks[name] = spec
}

// Freeze ends registration.
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Kind returns a registered kind.
func (r *Registry) Kind(kind string) (KindSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.kinds[kind]
	return s, ok
}

// Kinds lists the registered kind names, sorted.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Tasks returns a copy of the registered tasks.
func (r *Registry) Tasks() map[string]TaskSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]TaskSpec, len(r.tasks))
	for k, v := range r.tasks {
		out[k] = v
	}
	return out
}
