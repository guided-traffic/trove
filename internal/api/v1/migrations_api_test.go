package v1

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// fakeMigrations answers the migration report the test sets.
type fakeMigrations struct {
	rep   postgres.StatusReport
	err   error
	calls int
}

func (f *fakeMigrations) MigrationReport(context.Context) (postgres.StatusReport, error) {
	f.calls++
	return f.rep, f.err
}

func migrationsSetup(t *testing.T, val *apitest.Validator, acc Access, src MigrationSource) setup {
	t.Helper()
	s := newSetup(t, func(o *Options) {
		o.Access = acc
		if src != nil {
			o.API.Migrations = src
		}
	}, authed)
	s.handler = val.Handler(t, s.handler)
	return s
}

func TestGetMigrationStatus(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	applied := time.Date(2026, 10, 28, 12, 0, 3, 114e6, time.UTC)
	started := time.Date(2026, 10, 28, 12, 5, 0, 0, time.UTC)
	total := int64(480000)
	rep := postgres.StatusReport{
		Pending: []postgres.StepInfo{
			{Version: 58, Name: "00058_tags_index", Class: "expand"},
			{Version: 59, Name: "00059_drop_old_tags", Class: "contract", RequiresApp: "0.4.0", RequiresBackground: []string{"backfill_package_tags"}},
		},
		Applied: []postgres.AppliedInfo{{Version: 57, Name: "00057_package_tags", Class: "expand", Release: "0.4.0", AppliedAt: applied, DurationMs: 412}},
		Background: []postgres.BackgroundInfo{
			{Name: "backfill_package_tags", Release: "0.4.0", State: postgres.BackgroundRunning, Processed: 120000, EstimatedTotal: &total, StartedAt: &started},
			{Name: "nuget_semver2", Release: "0.10.0", State: postgres.BackgroundPending},
		},
		Servable: true,
	}
	rep.Schema.Applied, rep.Schema.CompatFloor = 57, "0.3.0"
	rep.Binary.Version, rep.Binary.RequiredSchema = "0.4.0", 57

	t.Run("R-HA-018 R-HA-021 the report has the members of R-HA-018, each pending step with what the guard checks", func(t *testing.T) {
		acc := &actionAccess{}
		s := migrationsSetup(t, val, acc, &fakeMigrations{rep: rep})
		w := s.get("/api/v1/system/migrations")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if acc.permission != PermissionSystemRead {
			t.Fatalf("the operation asked for %q, want %q (system.read)", acc.permission, PermissionSystemRead)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control = %q", cc)
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"servable", "verdict"} {
			if _, ok := got[k]; ok {
				t.Errorf("the answer carries the CLI's member %q: a replica that answers is servable by construction", k)
			}
		}
		schema, binary := got["schema"].(map[string]any), got["binary"].(map[string]any)
		if schema["applied"] != float64(57) || schema["compatFloor"] != "0.3.0" || binary["version"] != "0.4.0" || binary["requiredSchema"] != float64(57) {
			t.Fatalf("schema %v, binary %v", schema, binary)
		}
		pending := got["pending"].([]any)
		expand, contract := pending[0].(map[string]any), pending[1].(map[string]any)
		if expand["requiresApp"] != nil || len(expand["requiresBackground"].([]any)) != 0 {
			t.Errorf("an expand step: requiresApp %v, requiresBackground %v; want null and []", expand["requiresApp"], expand["requiresBackground"])
		}
		if contract["class"] != "contract" || contract["requiresApp"] != "0.4.0" || contract["requiresBackground"].([]any)[0] != "backfill_package_tags" {
			t.Errorf("the contract step = %v", contract)
		}
		a := got["applied"].([]any)[0].(map[string]any)
		if a["appliedAt"] != "2026-10-28T12:00:03.114Z" || a["durationMs"] != float64(412) || a["release"] != "0.4.0" {
			t.Errorf("applied = %v", a)
		}
		bg := got["background"].([]any)
		running, pendingBg := bg[0].(map[string]any), bg[1].(map[string]any)
		if running["state"] != "running" || running["processed"] != float64(120000) || running["estimatedTotal"] != float64(480000) || running["startedAt"] != "2026-10-28T12:05:00Z" {
			t.Errorf("a running migration = %v", running)
		}
		if pendingBg["state"] != "pending" || pendingBg["estimatedTotal"] != nil || pendingBg["startedAt"] != nil || pendingBg["processed"] != float64(0) {
			t.Errorf("a pending migration = %v", pendingBg)
		}
	})

	t.Run("R-HA-020 after a completed upgrade the lists are empty arrays, never null", func(t *testing.T) {
		empty := postgres.StatusReport{}
		empty.Schema.CompatFloor, empty.Binary.Version = "0.0.0", "0.4.0"
		w := migrationsSetup(t, val, allowAll{}, &fakeMigrations{rep: empty}).get("/api/v1/system/migrations")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		for _, k := range []string{"pending", "applied", "background"} {
			if l, ok := got[k].([]any); !ok || len(l) != 0 {
				t.Errorf("%s = %v, want []", k, got[k])
			}
		}
	})

	t.Run("R-HA-018 a caller without system.read is refused before the report is read", func(t *testing.T) {
		src := &fakeMigrations{rep: rep}
		acc := &actionAccess{err: errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")}
		w := migrationsSetup(t, val, acc, src).get("/api/v1/system/migrations")
		if w.Code != 403 || src.calls != 0 {
			t.Fatalf("%d (report read %d times) %s", w.Code, src.calls, w.Body.String())
		}
	})

	t.Run("R-HA-018 a replica without a database answers 503", func(t *testing.T) {
		w := migrationsSetup(t, val, allowAll{}, nil).get("/api/v1/system/migrations")
		if w.Code != 503 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("R-HA-018 a report that cannot be read is a problem, not a partial report", func(t *testing.T) {
		w := migrationsSetup(t, val, allowAll{}, &fakeMigrations{err: errors.New("migration 00002_x.sql was changed after it was applied")}).get("/api/v1/system/migrations")
		if w.Code != 500 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	if missing := val.Uncovered("getMigrationStatus"); len(missing) > 0 {
		t.Fatalf("not reached: %v", missing)
	}
}
