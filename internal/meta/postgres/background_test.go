package postgres

import (
	"strings"
	"testing"
)

func TestBackgroundState(t *testing.T) {
	cases := []struct {
		name     string
		done     bool
		jobState string
		want     string
	}{
		{"R-HA-022 the completion record decides, whatever the newest job says", true, "failed", BackgroundSucceeded},
		{"R-HA-022 the completion record without a job left (jobs.retention) is succeeded", true, "", BackgroundSucceeded},
		{"R-HA-022 no job and no record is pending", false, "", BackgroundPending},
		{"R-HA-022 a queued job is pending", false, "queued", BackgroundPending},
		{"R-HA-022 a running job is running", false, "running", BackgroundRunning},
		{"R-HA-022 a job being cancelled still runs", false, "cancelling", BackgroundRunning},
		{"R-HA-022 a failed newest job is failed", false, "failed", BackgroundFailed},
		{"R-HA-022 a run that ended without the record (it waited for older replicas) is pending", false, "succeeded", BackgroundPending},
		{"R-HA-022 a cancelled run is pending: a worker queues it again when it starts", false, "cancelled", BackgroundPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := backgroundState(c.done, c.jobState); got != c.want {
				t.Fatalf("backgroundState(%v, %q) = %q, want %q", c.done, c.jobState, got, c.want)
			}
		})
	}
}

func TestCheckBackgroundDependencies(t *testing.T) {
	known := []BackgroundMigration{{Name: "backfill_a", Release: "0.4.0", DoneKey: "backfill_a_done"}}
	step := func(names ...string) []Step {
		return []Step{{Name: "00002_drop_old", Phase: phaseContract, RequiresApp: "0.4.0", RequiresBackground: names}}
	}
	cases := []struct {
		name  string
		steps []Step
		known []BackgroundMigration
		want  string // substring of the error; empty means success
	}{
		{"R-HA-022 a step that depends on a declared migration passes", step("backfill_a"), known, ""},
		{"R-HA-022 a step without dependencies passes without declarations", step(), nil, ""},
		{"R-HA-022 a step that names an undeclared migration is a defect of the build", step("backfill_b"), known, `"backfill_b", which this binary does not declare`},
		{"R-HA-022 a declaration needs a valid name", step(), []BackgroundMigration{{Name: "Backfill", Release: "0.4.0", DoneKey: "k"}}, "lower-case"},
		{"R-HA-022 a migration is declared once", step(), append(append([]BackgroundMigration{}, known...), known...), "declared twice"},
		{"R-HA-022 a declaration names the release that introduced it", step(), []BackgroundMigration{{Name: "backfill_a", Release: "dev", DoneKey: "k"}}, "not a release version"},
		{"R-HA-022 a declaration names its completion record", step(), []BackgroundMigration{{Name: "backfill_a", Release: "0.4.0"}}, "no completion key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckBackgroundDependencies(c.steps, c.known)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
	t.Run("R-HA-022 the job kind of a migration is data_migration_<name>", func(t *testing.T) {
		if got := known[0].Kind(); got != "data_migration_backfill_a" {
			t.Fatalf("Kind() = %q", got)
		}
	})
}
