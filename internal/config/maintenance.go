package config

// Maintenance schedules the maintenance tasks (R-OPS-019, R-STOR-025). The
// task names are map keys, so the whole map is one key: in the environment it
// is JSON (TROVE_MAINTENANCE_TASKS).
type Maintenance struct {
	Tasks map[string]MaintenanceTask `yaml:"tasks" default:"{\"gc\":{\"enabled\":true,\"schedule\":\"*/30 * * * *\"},\"gc-full\":{\"enabled\":true,\"schedule\":\"0 2 * * 0\"},\"orphan-sweep\":{\"enabled\":true,\"schedule\":\"0 5 * * 6\"},\"scrub\":{\"enabled\":true,\"schedule\":\"0 4 * * *\"},\"storage-summary\":{\"enabled\":true,\"schedule\":\"17 * * * *\"},\"remote-cache-cleanup\":{\"enabled\":true,\"schedule\":\"0 3 * * *\"}}"`
}

// MaintenanceTask is the schedule of one task.
type MaintenanceTask struct {
	Enabled  bool   `yaml:"enabled"`
	Schedule string `yaml:"schedule"` // five-field cron, UTC
}

// Validate checks that every task has a five-field schedule. The full schedule
// grammar is checked by the scheduler of the jobs package when it registers
// the task.
func (m *Maintenance) Validate(r *Report) {
	for name, t := range m.Tasks {
		if t.Enabled && len(splitFields(t.Schedule)) != 5 {
			r.Add("tasks", "task %q: schedule %q must have five cron fields", name, t.Schedule)
		}
	}
}

func splitFields(s string) []string {
	var out []string
	field := false
	start := 0
	for i := 0; i <= len(s); i++ {
		sep := i == len(s) || s[i] == ' ' || s[i] == '\t'
		switch {
		case !sep && !field:
			field, start = true, i
		case sep && field:
			field = false
			out = append(out, s[start:i])
		}
	}
	return out
}
