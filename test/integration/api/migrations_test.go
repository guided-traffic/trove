//go:build integration

package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestMigrationStatus is GET /api/v1/system/migrations through the production composition root of an `api` replica (ADR-155)
// against PostgreSQL: the report of `trove migrate status`, from the same code, read on the replica's pool (R-HA-018, the
// upgrade verification of R-HA-020, step 4 of R-HA-021, R-HA-022, Q-HA-114), behind settings:read. Every answer is validated
// against api/openapi.yaml.
func TestMigrationStatus(t *testing.T) {
	e := newEnv(t, true)
	val := e.stack.Validator()
	c := e.cluster(e.newNode("api-1", nodeOpts{role: "api"}))
	admin := e.admin.header()

	type step struct {
		Version            int      `json:"version"`
		Name               string   `json:"name"`
		Class              string   `json:"class"`
		RequiresApp        *string  `json:"requiresApp"`
		RequiresBackground []string `json:"requiresBackground"`
	}
	type report struct {
		Schema struct {
			Applied     int    `json:"applied"`
			CompatFloor string `json:"compatFloor"`
		} `json:"schema"`
		Binary struct {
			Version        string `json:"version"`
			RequiredSchema int    `json:"requiredSchema"`
		} `json:"binary"`
		Pending []step `json:"pending"`
		Applied []struct {
			Version   int       `json:"version"`
			Name      string    `json:"name"`
			Class     string    `json:"class"`
			Release   string    `json:"release"`
			AppliedAt time.Time `json:"appliedAt"`
		} `json:"applied"`
		Background []struct {
			Name      string     `json:"name"`
			Release   string     `json:"release"`
			State     string     `json:"state"`
			Processed int64      `json:"processed"`
			StartedAt *time.Time `json:"startedAt"`
		} `json:"background"`
	}
	get := func(t *testing.T) report {
		t.Helper()
		a := send(t, c, "GET", "/api/v1/system/migrations", nil, admin...)
		if a.Status != 200 {
			t.Fatalf("GET /api/v1/system/migrations: %d %s", a.Status, a.Body)
		}
		var r report
		if err := json.Unmarshal(a.Body, &r); err != nil {
			t.Fatalf("the report: %v\n%s", err, a.Body)
		}
		return r
	}
	semver2 := func(r report) string {
		for _, b := range r.Background {
			if b.Name == "nuget_semver2" {
				return b.State
			}
		}
		return "absent"
	}

	t.Run("R-HA-020 R-HA-018 a migrated installation: nothing pending, every embedded step applied, the replica's binary", func(t *testing.T) {
		r := get(t)
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Pending) != 0 || len(r.Applied) != len(steps) {
			t.Fatalf("pending %v, %d applied of %d embedded steps", r.Pending, len(r.Applied), len(steps))
		}
		for i, s := range steps {
			if a := r.Applied[i]; a.Version != s.Version || a.Name != s.Name || a.Class != s.Phase || a.AppliedAt.IsZero() || a.Release == "" {
				t.Fatalf("applied[%d] = %+v, want step %s (%s)", i, a, s.Name, s.Phase)
			}
		}
		if r.Schema.Applied != postgres.RequiredSchema() || r.Binary.RequiredSchema != postgres.RequiredSchema() || r.Binary.Version != buildinfo.Get().Version {
			t.Fatalf("schema %+v, binary %+v", r.Schema, r.Binary)
		}
	})

	t.Run("R-HA-018 the endpoint and `trove migrate status` answer from the same code: the same schema, steps and background migrations", func(t *testing.T) {
		r := get(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cli, err := postgres.Status(ctx, e.stack.DatabaseConfig(), postgres.MigrateOptions{Version: buildinfo.Get().Version, Background: app.BackgroundMigrations(), Log: testenv.Quiet()})
		if err != nil {
			t.Fatal(err)
		}
		if cli.Schema.Applied != r.Schema.Applied || cli.Schema.CompatFloor != r.Schema.CompatFloor || cli.Binary.RequiredSchema != r.Binary.RequiredSchema ||
			len(cli.Pending) != len(r.Pending) || len(cli.Applied) != len(r.Applied) || len(cli.Background) != len(r.Background) {
			t.Fatalf("the endpoint says %+v, `trove migrate status` says %+v", r, cli)
		}
		for i := range cli.Background {
			if cli.Background[i].Name != r.Background[i].Name || cli.Background[i].Release != r.Background[i].Release {
				t.Fatalf("background[%d]: endpoint %+v, CLI %+v", i, r.Background[i], cli.Background[i])
			}
		}
	})

	t.Run("R-HA-022 R-HA-021 the binary's background migration is pending until a worker runs it, then succeeded", func(t *testing.T) {
		if got := semver2(get(t)); got != "pending" {
			t.Fatalf("nuget_semver2 before any worker ran: %s, want pending (no replica claims jobs yet)", got)
		}
		e.newNode("worker-1", nodeOpts{role: "worker"})
		var last report
		waitFor(t, "nuget_semver2 to succeed on the worker", 60*time.Second, func() bool {
			last = get(t)
			return semver2(last) == "succeeded"
		})
		for _, b := range last.Background {
			if b.Name == "nuget_semver2" && (b.Release != "0.10.0" || b.StartedAt == nil) {
				t.Fatalf("nuget_semver2 = %+v", b)
			}
		}
	})

	t.Run("R-HA-018 the report needs settings:read (system.read)", func(t *testing.T) {
		if a := send(t, c, "GET", "/api/v1/system/migrations", nil, e.reader.header()...); a.Status != 200 {
			t.Fatalf("a reader with settings:read: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "GET", "/api/v1/system/migrations", nil, e.alice.header()...); a.Status != 403 {
			t.Fatalf("a user without settings:read: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "GET", "/api/v1/system/migrations", nil); a.Status != 401 {
			t.Fatalf("anonymous: %d %s", a.Status, a.Body)
		}
	})

	if missing := val.Uncovered("getMigrationStatus"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}
