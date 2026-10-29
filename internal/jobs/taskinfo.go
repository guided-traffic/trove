package jobs

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/guided-traffic/trove/internal/config"
)

// TaskInfo is a registered task with the configuration applied: whether it is
// enabled, its effective schedule, and the key it shares with other tasks. The
// scheduler runs the enabled, scheduled tasks; the task list of the management
// API (R-OPS-019) shows all of them.
type TaskInfo struct {
	Name string
	// Kind is the job kind a run enqueues.
	Kind string
	// Schedule is the effective five-field cron expression; empty for a
	// manual-only task. Sched is its parsed form, nil for a manual-only task.
	Schedule string
	Sched    *Schedule
	Enabled  bool
	// ManualOnly is true for a task without a schedule.
	ManualOnly bool
	// DedupKey is the exclusive key a run carries: the task's own, or the name.
	DedupKey string
	// Args are the task's default job args, JSON-encoded ("{}" when none).
	Args []byte
}

// ResolveTasks merges the registered tasks with maintenance.tasks.<name> of the
// configuration and parses every schedule with the one parser, in name order. A
// bad schedule, and any entry for a manual-only task, is a configuration error
// naming the key (R-OPS-019, R-OPS-028).
func (r *Registry) ResolveTasks(m config.Maintenance) ([]TaskInfo, error) {
	if err := ValidateSchedules(m); err != nil {
		return nil, err
	}
	reg := r.Tasks()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]TaskInfo, 0, len(names))
	for _, name := range names {
		spec := reg[name]
		args := []byte("{}")
		if spec.Args != nil {
			b, err := json.Marshal(spec.Args)
			if err != nil {
				return nil, fmt.Errorf("jobs: args of task %q: %w", name, err)
			}
			args = b
		}
		key := spec.DedupKey
		if key == "" {
			key = name
		}
		ti := TaskInfo{Name: name, Kind: spec.Kind, DedupKey: key, Args: args, ManualOnly: spec.ManualOnly}
		if _, ok := m.Tasks[name]; ok && spec.ManualOnly {
			return nil, fmt.Errorf("maintenance.tasks.%s: %s is a manual-only task and has no schedule", name, name)
		}
		if spec.ManualOnly {
			out = append(out, ti)
			continue
		}
		ti.Enabled, ti.Schedule = !spec.Disabled, spec.Schedule
		if ct, ok := m.Tasks[name]; ok {
			ti.Enabled = ct.Enabled
			if ct.Schedule != "" {
				ti.Schedule = ct.Schedule
			}
		}
		if !ti.Enabled {
			out = append(out, ti)
			continue
		}
		if _, ok := r.Kind(spec.Kind); !ok {
			return nil, fmt.Errorf("jobs: task %q enqueues kind %q, which is not registered", name, spec.Kind)
		}
		sched, err := ParseSchedule(ti.Schedule)
		if err != nil {
			return nil, fmt.Errorf("maintenance.tasks.%s.schedule: %w", name, err)
		}
		ti.Sched = sched
		out = append(out, ti)
	}
	return out, nil
}
