package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The background data migrations of R-HA-022 as far as they exist before its table `background_migration`: each is
// declared by the package that runs it, with the release that introduced it and the system_state key that records its
// completion, and its state is read from that record and from the newest job of its kind (Q-HA-114). The report of GET
// /api/v1/system/migrations and `trove migrate status` and the contract guard read the same states, so a contract step that
// depends on a migration is refused for exactly the state the report shows (Q-HA-115).

// BackgroundKindPrefix is the prefix of a background data migration's job kind: data_migration_<name> (R-HA-022).
const BackgroundKindPrefix = "data_migration_"

// The states of a background data migration (R-HA-022). BackgroundPaused is set by the pause operation of R-HA-022, which
// is not built: no state reads it yet.
const (
	BackgroundPending   = "pending"
	BackgroundRunning   = "running"
	BackgroundPaused    = "paused"
	BackgroundSucceeded = "succeeded"
	BackgroundFailed    = "failed"
)

// BackgroundMigration declares a background data migration (R-HA-022, Q-HA-114).
type BackgroundMigration struct {
	// Name is the migration's name; its job kind is data_migration_<Name>.
	Name string
	// Release is the release that introduced it: it starts only once every live replica runs that release or a newer one.
	Release string
	// DoneKey is the system_state key (R-OPS-024) whose row records the completion.
	DoneKey string
	// Processed reads the count of a completed migration from the value of its completion record. Nil, or false, leaves
	// the count of its newest job, which is 0 once jobs.retention has removed the job rows.
	Processed func(done []byte) (int64, bool)
}

// Kind is the job kind of the migration.
func (m BackgroundMigration) Kind() string { return BackgroundKindPrefix + m.Name }

// BackgroundInfo is the state of a background data migration (the shape of GET /api/v1/system/migrations).
type BackgroundInfo struct {
	Name    string `json:"name"`
	Release string `json:"release"`
	State   string `json:"state"`
	// Processed counts the rows the migration has converted so far.
	Processed int64 `json:"processed"`
	// EstimatedTotal is the migration's own estimate of its rows; nil while it has none.
	EstimatedTotal *int64 `json:"estimatedTotal"`
	// StartedAt is the start of its first run; nil while none started (or the job rows are gone).
	StartedAt *time.Time `json:"startedAt"`
}

var backgroundName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkBackground checks the declarations: a name each, valid and once, a release version and a completion key.
func checkBackground(known []BackgroundMigration) error {
	seen := map[string]bool{}
	for _, m := range known {
		if !backgroundName.MatchString(m.Name) {
			return fmt.Errorf("background migration %q: the name must be lower-case letters, digits and underscores", m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("background migration %q is declared twice", m.Name)
		}
		seen[m.Name] = true
		if _, err := parseVersion(m.Release); err != nil || m.Release == "dev" {
			return fmt.Errorf("background migration %q: %q is not a release version", m.Name, m.Release)
		}
		if m.DoneKey == "" {
			return fmt.Errorf("background migration %q has no completion key", m.Name)
		}
	}
	return nil
}

// CheckBackgroundDependencies reports a contract step that names a background migration the binary does not declare: the
// guard could not read its state. It is a defect of the build, which a unit test over the embedded steps and the declared
// migrations keeps out of a release.
func CheckBackgroundDependencies(steps []Step, known []BackgroundMigration) error {
	if err := checkBackground(known); err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, m := range known {
		declared[m.Name] = true
	}
	for _, s := range steps {
		for _, name := range s.RequiresBackground {
			if !declared[name] {
				return fmt.Errorf("migration %s.sql depends on the background migration %q, which this binary does not declare", s.Name, name)
			}
		}
	}
	return nil
}

// backgroundState is the state of a migration from its records: the completion record decides; without it the newest job
// of its kind does (queued is pending, running or cancelling is running, failed is failed); a run that ended without the
// record (a run that waited for older replicas and queued the next) and a cancelled run are pending, because a worker
// queues the migration again when it starts; no job at all is pending.
func backgroundState(done bool, jobState string) string {
	switch {
	case done:
		return BackgroundSucceeded
	case jobState == "running" || jobState == "cancelling":
		return BackgroundRunning
	case jobState == "failed":
		return BackgroundFailed
	}
	return BackgroundPending
}

// ReadBackground reads the state of each declared migration, in the order given. A database without the job table or
// system_state (before the first migration) reports every migration pending.
func ReadBackground(ctx context.Context, db q.DBTX, known []BackgroundMigration) ([]BackgroundInfo, error) {
	out := make([]BackgroundInfo, 0, len(known))
	if len(known) == 0 {
		return out, nil
	}
	qs := q.New(db)
	ready := true
	for _, table := range []string{"system_state", "job"} {
		found, err := qs.TableExists(ctx, table)
		if err != nil {
			return nil, err
		}
		ready = ready && found
	}
	for _, m := range known {
		info := BackgroundInfo{Name: m.Name, Release: m.Release, State: BackgroundPending}
		if ready {
			if err := readOneBackground(ctx, qs, m, &info); err != nil {
				return nil, fmt.Errorf("background migration %s: %w", m.Name, err)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func readOneBackground(ctx context.Context, qs *q.Queries, m BackgroundMigration, info *BackgroundInfo) error {
	done, err := qs.GetSystemState(ctx, m.DoneKey)
	switch {
	case isNoRows(err):
		done.Value = nil
	case err != nil:
		return err
	}
	jobs, err := qs.BackgroundMigrationJob(ctx, m.Kind())
	if err != nil {
		return err
	}
	jobState := ""
	if len(jobs) > 0 {
		j := jobs[0]
		jobState = j.State
		if j.ProgressDone.Valid {
			info.Processed = j.ProgressDone.Int64
		}
		if j.ProgressTotal.Valid && j.ProgressTotal.Int64 > 0 {
			total := j.ProgressTotal.Int64
			info.EstimatedTotal = &total
		}
		if j.FirstStartedAt.Valid {
			at := j.FirstStartedAt.Time.UTC()
			info.StartedAt = &at
		}
	}
	isDone := done.Value != nil
	info.State = backgroundState(isDone, jobState)
	if isDone && m.Processed != nil {
		if n, ok := m.Processed(done.Value); ok {
			info.Processed = n
		}
	}
	return nil
}

// backgroundGuard refuses a contract step that depends on a background migration that has not succeeded (R-HA-021 step 4,
// R-HA-022): the step would remove what the migration has not converted yet.
func backgroundGuard(ctx context.Context, db q.DBTX, s Step, known []BackgroundMigration) error {
	if len(s.RequiresBackground) == 0 {
		return nil
	}
	byName := map[string]BackgroundMigration{}
	for _, m := range known {
		byName[m.Name] = m
	}
	var need []BackgroundMigration
	for _, name := range s.RequiresBackground {
		m, ok := byName[name]
		if !ok {
			return fmt.Errorf("migration %s.sql depends on the background migration %q, which this binary does not declare", s.Name, name)
		}
		need = append(need, m)
	}
	states, err := ReadBackground(ctx, db, need)
	if err != nil {
		return err
	}
	var open []BackgroundRef
	for _, st := range states {
		if st.State != BackgroundSucceeded {
			open = append(open, BackgroundRef{Name: st.Name, State: st.State})
		}
	}
	if len(open) == 0 {
		return nil
	}
	names := make([]string, 0, len(open))
	for _, b := range open {
		names = append(names, fmt.Sprintf("%s (%s)", b.Name, b.State))
	}
	return &Refusal{Step: s.Name, Background: open, Reason: fmt.Sprintf(
		"contract step %s.sql depends on background migrations that have not succeeded: %s; nothing was applied. "+
			"Let them finish on the running release (GET /api/v1/system/migrations shows their state), then upgrade again (R-HA-021, R-HA-022)",
		s.Name, strings.Join(names, ", "))}
}

// BackgroundRef names a background migration and its state in a refusal.
type BackgroundRef struct {
	Name  string `json:"name"`
	State string `json:"state"`
}
