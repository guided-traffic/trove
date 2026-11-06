package nuget

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/jobs"
)

// storedMetadata is nuget_version.metadata as a push writes it, with the dependency groups of
// the given ranges (rows.Metadata, the type the push marshals).
func storedMetadata(t *testing.T, ranges ...string) []byte {
	t.Helper()
	m := rows.Metadata{Description: "Widgets", Authors: "Acme"}
	for i, r := range ranges {
		m.DependencyGroups = append(m.DependencyGroups, rows.DependencyGroup{
			TargetFramework: []string{"net8.0", "netstandard2.0"}[i%2],
			Dependencies:    []rows.Dependency{{ID: "Acme.Dep", Version: r}},
		})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestR_NUGET_004_TheRecomputationDecidesLikeAPush: a stored row is classified from its
// original version and the dependency ranges of its metadata with the rule of a push, over the
// table the push test reads (version/testdata/dependency-semver2.json, Q-NUGET-93).
func TestR_NUGET_004_TheRecomputationDecidesLikeAPush(t *testing.T) {
	raw, err := os.ReadFile("../../formats/nuget/version/testdata/dependency-semver2.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Name    string `json:"name"`
			Range   string `json:"range"`
			Semver2 bool   `json:"semver2"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil || len(table.Cases) == 0 {
		t.Fatalf("the case table: %d cases, %v", len(table.Cases), err)
	}
	for _, c := range table.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := semver2Of("1.0.0", storedMetadata(t, "[13.0.1,)", c.Range))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.Semver2 {
				t.Errorf("a stored 1.0.0 with a dependency on %q: is_semver2 = %v, want %v", c.Range, got, c.Semver2)
			}
		})
	}

	t.Run("the version alone decides", func(t *testing.T) {
		if got, err := semver2Of("1.0.0-ci.5", storedMetadata(t)); err != nil || !got {
			t.Errorf("1.0.0-ci.5 without dependencies: %v, %v", got, err)
		}
		if got, err := semver2Of("01.0.0.0+sha.1", storedMetadata(t, "[1.0.0,)")); err != nil || !got {
			t.Errorf("an original version with build metadata: %v, %v", got, err)
		}
	})
	t.Run("no dependency, or one without a range, leaves a SemVer 1 version SemVer 1", func(t *testing.T) {
		if got, err := semver2Of("1.0.0", []byte(`{"description":"Widgets"}`)); err != nil || got {
			t.Errorf("no dependency groups: %v, %v", got, err)
		}
		if got, err := semver2Of("1.0.0", storedMetadata(t, "")); err != nil || got {
			t.Errorf("a dependency on any version: %v, %v", got, err)
		}
	})
	t.Run("a row that does not parse is an error, not a classification", func(t *testing.T) {
		if _, err := semver2Of("not.a.version", storedMetadata(t)); err == nil {
			t.Error("an unreadable original version was classified")
		}
		if _, err := semver2Of("1.0.0", []byte(`{"dependencyGroups":`)); err == nil {
			t.Error("unreadable metadata was classified")
		}
	})
}

// TestR_HA_022_TheRecomputationIsASingletonOnTheMaintenanceQueue: the kind is named as
// R-HA-022 names a background migration, runs on the maintenance queue and coalesces, so a
// waiting run can queue the one after it while two never run together.
func TestR_HA_022_TheRecomputationIsASingletonOnTheMaintenanceQueue(t *testing.T) {
	spec := Semver2JobSpec(func(context.Context, *jobs.Run) error { return nil })
	if spec.Queue != "maintenance" || spec.DedupMode != jobs.Coalesce || spec.Handler == nil {
		t.Errorf("spec = queue %q, mode %q", spec.Queue, spec.DedupMode)
	}
	if Semver2JobKind != "data_migration_nuget_semver2" {
		t.Errorf("kind = %q", Semver2JobKind)
	}
	r := jobs.NewRegistry()
	r.RegisterKind(Semver2JobKind, spec) // panics on a name the registry refuses
	if _, err := NewSemver2(Semver2Options{BatchSize: 1}); err == nil {
		t.Error("a recomputation without a runner and a queue was built")
	}
}

// TestR_HA_022_ThePauseBetweenBatchesEndsWithTheRun: the pause between two batches returns at
// once when the run ends, so a release at shutdown does not wait for it.
func TestR_HA_022_ThePauseBetweenBatchesEndsWithTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := pause(ctx, time.Hour); err == nil || time.Since(start) > time.Second {
		t.Errorf("pause after the end of the run: %v after %s", err, time.Since(start))
	}
	if err := pause(context.Background(), time.Millisecond); err != nil {
		t.Errorf("pause: %v", err)
	}
	if err := pause(context.Background(), 0); err != nil {
		t.Errorf("no pause: %v", err)
	}
}

// TestR_HA_022_TheRecomputationIsDeclaredAsABackgroundMigration: the declaration the report of GET
// /api/v1/system/migrations and the contract guard read (Q-HA-114) names the kind's migration, the
// release after the last one that classified the old way, and the completion record, whose
// examined rows are the count of a completed run.
func TestR_HA_022_TheRecomputationIsDeclaredAsABackgroundMigration(t *testing.T) {
	m := Semver2Background()
	if m.Name != "nuget_semver2" || m.Kind() != Semver2JobKind || m.Release != "0.10.0" || m.DoneKey != Semver2StateKey {
		t.Fatalf("declaration = %+v", m)
	}
	done, _ := json.Marshal(Semver2Result{State: Semver2Recomputed, Examined: 7403, Changed: 12})
	if n, ok := m.Processed(done); !ok || n != 7403 {
		t.Errorf("Processed(%s) = %d, %v; want 7403 (the examined rows)", done, n, ok)
	}
	if _, ok := m.Processed([]byte(`"not an object"`)); ok {
		t.Error("a completion record of another shape gave a count")
	}
}
