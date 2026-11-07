package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/gauges/gaugestest"
	"github.com/guided-traffic/trove/internal/errs"
)

// Tests of the backup operations (R-API-050, R-BAK-014) and of the backup block of the system information (R-OPS-004) over an
// in-memory reader. The SQL behind backup.RunReader is the integration package test/integration/backup's.

var bakNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// denyAccess refuses every capability, as the caller without settings:read.
type denyAccess struct{}

func (denyAccess) Require(context.Context, string) error {
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func (denyAccess) RequireAction(context.Context, authz.Coordinate) error {
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func bakID(n int) string {
	return "0193" + "0000-0000-7000-8000-0000000000" + string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func bakFinished(n int, op backup.Operation, st backup.State, ago, took time.Duration, result, runErr string) backup.Run {
	end := bakNow.Add(-ago)
	r := backup.Run{
		ID: bakID(n), Operation: op, State: st, Hold: backup.HoldNone, Trigger: backup.TriggerSchedule, Target: "s3://trove-backups/trove",
		Pod: "trove-backup-" + bakID(n)[30:], TroveVersion: "0.4.0", StartedAt: end.Add(-took), HeartbeatAt: end, LeaseExpiresAt: end, FinishedAt: &end,
	}
	if result != "" {
		r.Result = json.RawMessage(result)
	}
	if runErr != "" {
		r.Error = json.RawMessage(runErr)
	}
	return r
}

func bakActive(n int, st backup.State, started, lease time.Duration) backup.Run {
	return backup.Run{
		ID: bakID(n), Operation: backup.OpCreate, State: st, Hold: backup.HoldPinned, Trigger: backup.TriggerManual, Target: "s3://trove-backups/trove",
		Pod: "by-hand", TroveVersion: "0.4.0", StartedAt: bakNow.Add(-started), HeartbeatAt: bakNow.Add(-time.Minute), LeaseExpiresAt: bakNow.Add(lease),
		SnapshotAt: ptr(bakNow.Add(-started + time.Second)),
		Progress:   backup.Progress{Phase: "copying", TablesDone: 41, TablesTotal: 58, BlobsToCopy: 1204, BlobsCopied: 830, BytesCopied: 9123456789},
	}
}

type bakEnv struct {
	setup
	mem *gaugestest.Mem
}

func newBakEnv(t *testing.T, val *apitest.Validator, access Access, restored *gen.BackupRestoredFrom, runs ...backup.Run) bakEnv {
	t.Helper()
	mem := &gaugestest.Mem{Now: bakNow, Runs: runs}
	e := bakEnv{mem: mem}
	e.setup = newSetup(t, func(o *Options) {
		if access != nil {
			o.Access = access
		}
		o.API.Config = apiConfigForTest()
		o.Facts = fakeFacts{Facts{
			InstallationID: ptr("0b7c3f5e"), Incarnation: ptr("5d10e2a4"),
			Valkey:      gen.ValkeyFacts{Mode: gen.Sentinel, PrimaryName: "trove", Sentinels: 3, Tls: "verify-full", KeyPrefix: "trove:"},
			ObjectStore: gen.ObjectStoreFacts{Endpoint: "https://s3.example.com", Bucket: "trove", Prefix: "blobs", CredentialSource: "static"},
		}}
		o.Backup = BackupServices{
			Runs: mem, ExpectedInterval: 24 * time.Hour, Namespace: "trove", CronJob: "trove-backup",
			RestoredFrom: func(context.Context) (*gen.BackupRestoredFrom, error) { return restored, nil },
		}
	}, authed)
	e.handler = val.Handler(t, e.handler)
	return e
}

func (e bakEnv) json(t *testing.T, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	w := e.get(path)
	if into != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v\n%s", path, err, w.Body.String())
		}
	}
	return w
}

const retained = `{"retention":{"keepLast":7,"keepWithinSeconds":0},"retained":[
 {"backupId":"20261027T020000Z-1c9e","snapshotAt":"2026-10-27T02:00:08.913Z","troveVersion":"0.4.0","schemaStep":42,"databaseBytes":183500800,"blobCount":51234,"blobBytes":412316860416,"damaged":false}]}`

func TestR_API_050_BackupRuns(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())

	t.Run("R-API-050 a caller without settings:read gets 403 on all three operations", func(t *testing.T) {
		e := newBakEnv(t, val, denyAccess{}, nil, bakFinished(1, backup.OpCreate, backup.StateCompleted, time.Hour, time.Hour, "", ""))
		for _, p := range []string{"/api/v1/system/backups", "/api/v1/system/backups/summary", "/api/v1/system/backups/" + bakID(1)} {
			if w := e.get(p); w.Code != http.StatusForbidden {
				t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
			}
		}
		if e.mem.Total() != 0 {
			t.Error("a refused caller reached the reader")
		}
	})

	t.Run("R-API-050 a replica without the reader answers 503", func(t *testing.T) {
		s := newSetup(t, nil, authed)
		for _, p := range []string{"/api/v1/system/backups", "/api/v1/system/backups/summary", "/api/v1/system/backups/" + bakID(1)} {
			if w := s.get(p); w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: %d", p, w.Code)
			}
		}
	})

	t.Run("R-API-050 the list is newest first with null fields, the progress object and leaseExpired", func(t *testing.T) {
		done := bakFinished(1, backup.OpCreate, backup.StateCompleted, 26*time.Hour, 40*time.Minute, `{"backupId":"20261026T100000Z-aaaa"}`, "")
		done.SnapshotAt = ptr(done.StartedAt.Add(5 * time.Second))
		live := bakActive(2, backup.StateCopying, 42*time.Minute, 5*time.Minute)
		dead := bakActive(3, backup.StateSnapshot, 3*time.Hour, -time.Hour)
		dead.Progress = backup.Progress{}
		e := newBakEnv(t, val, nil, nil, done, dead, live)
		var list gen.BackupRunList
		if w := e.json(t, "/api/v1/system/backups", &list); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if len(list.Items) != 3 || list.NextCursor != nil || list.Total != nil {
			t.Fatalf("list = %+v", list)
		}
		gotLive, gotDead, gotDone := list.Items[0], list.Items[1], list.Items[2]
		if gotLive.Id.String() != live.ID || gotDead.Id.String() != dead.ID || gotDone.Id.String() != done.ID {
			t.Errorf("order: %s %s %s", gotLive.Id, gotDead.Id, gotDone.Id)
		}
		if gotLive.LeaseExpired || !gotDead.LeaseExpired || gotDone.LeaseExpired {
			t.Errorf("leaseExpired: live %v dead %v done %v", gotLive.LeaseExpired, gotDead.LeaseExpired, gotDone.LeaseExpired)
		}
		if gotLive.FinishedAt != nil || gotLive.Result != nil || gotLive.Error != nil || gotLive.BackupId != nil {
			t.Errorf("the running run carries no end: %+v", gotLive)
		}
		if p := gotLive.Progress; p.Phase == nil || *p.Phase != "copying" || p.TablesDone == nil || *p.TablesDone != 41 || *p.BytesCopied != 9123456789 || p.PinsLeft != nil {
			t.Errorf("progress = %+v", p)
		}
		if gotDead.Progress.Phase != nil {
			t.Errorf("an empty progress has members: %+v", gotDead.Progress)
		}
		if gotDone.Result == nil || (*gotDone.Result)["backupId"] != "20261026T100000Z-aaaa" || gotDone.FinishedAt == nil || gotDone.Error != nil {
			t.Errorf("finished run = %+v", gotDone)
		}
		if gotDone.Target != "s3://trove-backups/trove" || gotLive.Trigger != "manual" || gotLive.Pod != "by-hand" {
			t.Errorf("columns: %+v", gotLive)
		}
	})

	t.Run("R-API-050 the filters select by operation, state, trigger and start time", func(t *testing.T) {
		runs := []backup.Run{
			bakFinished(1, backup.OpCreate, backup.StateCompleted, 72*time.Hour, time.Hour, "", ""),
			bakFinished(2, backup.OpCreate, backup.StateFailed, 48*time.Hour, time.Hour, "", `{"code":"stalled"}`),
			bakFinished(3, backup.OpVerify, backup.StateCompleted, 24*time.Hour, time.Minute, "", ""),
			bakFinished(4, backup.OpDelete, backup.StateCompleted, 12*time.Hour, time.Minute, "", ""),
		}
		runs[3].Trigger = backup.TriggerManual
		e := newBakEnv(t, val, nil, nil, runs...)
		ids := func(q string) []string {
			var list gen.BackupRunList
			if w := e.json(t, "/api/v1/system/backups?"+q, &list); w.Code != 200 {
				t.Fatalf("%s: %d %s", q, w.Code, w.Body.String())
			}
			var out []string
			for _, it := range list.Items {
				out = append(out, it.Id.String()[len(it.Id.String())-2:])
			}
			return out
		}
		eq := func(q string, want ...string) {
			t.Helper()
			got := ids(q)
			if len(got) != len(want) {
				t.Errorf("%s: got %v, want %v", q, got, want)
				return
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("%s: got %v, want %v", q, got, want)
				}
			}
		}
		eq("operation=create", "02", "01")
		eq("operation=verify", "03")
		eq("state=failed", "02")
		eq("trigger=manual", "04")
		from := url.QueryEscape(bakNow.Add(-60 * time.Hour).Format(time.RFC3339))
		to := url.QueryEscape(bakNow.Add(-18 * time.Hour).Format(time.RFC3339))
		eq("startedFrom="+from, "04", "03", "02")
		eq("startedFrom="+from+"&startedTo="+to, "03", "02")
		eq("operation=create&state=failed&trigger=schedule", "02")
		eq("operation=delete&state=failed")
	})

	t.Run("R-API-050 an unknown operation, state or trigger is a 400, not an empty list", func(t *testing.T) {
		e := newBakEnv(t, val, nil, nil)
		for _, q := range []string{"operation=restore", "state=running", "trigger=cron", "color=red"} {
			if w := e.get("/api/v1/system/backups?" + q); w.Code != http.StatusBadRequest {
				t.Errorf("%s: %d", q, w.Code)
			}
		}
	})

	t.Run("R-API-050 the list is cursor-paged with a Link header and includeTotal", func(t *testing.T) {
		var runs []backup.Run
		for i := 1; i <= 5; i++ {
			runs = append(runs, bakFinished(i, backup.OpCreate, backup.StateCompleted, time.Duration(i)*24*time.Hour, time.Hour, "", ""))
		}
		// two runs started at the same instant: the ID breaks the tie
		runs[4].StartedAt = runs[3].StartedAt
		e := newBakEnv(t, val, nil, nil, runs...)
		var seen []string
		cursor := ""
		for page := 0; page < 5; page++ {
			path := "/api/v1/system/backups?limit=2&includeTotal=true"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			var list gen.BackupRunList
			w := e.json(t, path, &list)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if list.Total == nil || *list.Total != 5 {
				t.Fatalf("total = %v", list.Total)
			}
			for _, it := range list.Items {
				seen = append(seen, it.Id.String())
			}
			if list.NextCursor == nil {
				if w.Header().Get("Link") != "" {
					t.Error("Link on the last page")
				}
				break
			}
			if w.Header().Get("Link") == "" {
				t.Error("no Link header with a next page")
			}
			cursor = *list.NextCursor
		}
		if len(seen) != 5 {
			t.Fatalf("seen %v", seen)
		}
		uniq := map[string]bool{}
		for _, id := range seen {
			uniq[id] = true
		}
		if len(uniq) != 5 {
			t.Errorf("a run was served twice: %v", seen)
		}
		if w := e.get("/api/v1/system/backups?cursor=not-a-cursor"); w.Code != http.StatusBadRequest {
			t.Errorf("bad cursor: %d", w.Code)
		}
		if w := e.get("/api/v1/system/backups?limit=2&cursor=" + url.QueryEscape(cursor) + "&state=failed"); w.Code != http.StatusBadRequest {
			t.Errorf("cursor with other filters: %d", w.Code)
		}
	})

	t.Run("R-API-050 one run by ID; an unknown ID is 404; summary is not a run", func(t *testing.T) {
		failed := bakFinished(1, backup.OpCreate, backup.StateFailed, time.Hour, 10*time.Minute, "", `{"code":"pool_damaged","title":"The pool is damaged"}`)
		e := newBakEnv(t, val, nil, nil, failed)
		var run gen.BackupRun
		if w := e.json(t, "/api/v1/system/backups/"+failed.ID, &run); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if run.State != "failed" || run.Error == nil || (*run.Error)["code"] != "pool_damaged" || run.Result != nil || run.LeaseExpired {
			t.Errorf("run = %+v", run)
		}
		if w := e.get("/api/v1/system/backups/" + bakID(9)); w.Code != http.StatusNotFound {
			t.Errorf("unknown: %d", w.Code)
		}
		if w := e.get("/api/v1/system/backups/not-a-uuid"); w.Code != http.StatusBadRequest {
			t.Errorf("malformed: %d", w.Code)
		}
	})

	t.Run("R-API-050 leaseExpired of one run uses the database's clock", func(t *testing.T) {
		dead := bakActive(1, backup.StateCopying, 3*time.Hour, -time.Second)
		e := newBakEnv(t, val, nil, nil, dead)
		var run gen.BackupRun
		e.json(t, "/api/v1/system/backups/"+dead.ID, &run)
		if !run.LeaseExpired {
			t.Error("a run whose lease passed is not marked")
		}
		e.mem.Now = bakNow.Add(-2 * time.Second) // the replica's clock does not matter, the database's does
		e.json(t, "/api/v1/system/backups/"+dead.ID, &run)
		if run.LeaseExpired {
			t.Error("the lease had not passed at the database's time")
		}
	})
}

func TestR_API_050_BackupSummary(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	summary := func(t *testing.T, e bakEnv) gen.BackupSummary {
		t.Helper()
		var s gen.BackupSummary
		if w := e.json(t, "/api/v1/system/backups/summary", &s); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return s
	}

	t.Run("R-API-050 an installation that never ran a backup: nulls, the interval and the manual-run command", func(t *testing.T) {
		e := newBakEnv(t, val, nil, nil)
		w := e.get("/api/v1/system/backups/summary")
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &raw)
		for _, k := range []string{"lastSuccessAt", "overdue", "lastFailure", "active", "target", "retention", "storedBackups", "restoredFrom"} {
			if string(raw[k]) != "null" {
				t.Errorf("%s = %s, want null", k, raw[k])
			}
		}
		s := summary(t, e)
		if s.ExpectedIntervalSeconds != 86400 || s.ManualRun.Namespace == nil || *s.ManualRun.Namespace != "trove" || *s.ManualRun.CronJob != "trove-backup" {
			t.Errorf("summary = %+v", s)
		}
	})

	t.Run("R-API-050 a success within 1.5 times the interval is not overdue, one beyond it is", func(t *testing.T) {
		for _, c := range []struct {
			ago     time.Duration
			overdue bool
		}{{20 * time.Hour, false}, {36 * time.Hour, false}, {36*time.Hour + time.Minute, true}} {
			ok := bakFinished(1, backup.OpCreate, backup.StateCompleted, c.ago, 30*time.Minute, "", "")
			s := summary(t, newBakEnv(t, val, nil, nil, ok))
			if s.Overdue == nil || *s.Overdue != c.overdue || s.LastSuccessAt == nil || !s.LastSuccessAt.Equal(*ok.FinishedAt) {
				t.Errorf("age %v: overdue %v, lastSuccessAt %v", c.ago, s.Overdue, s.LastSuccessAt)
			}
		}
	})

	t.Run("R-API-050 the overdue mark and the info block agree on the same rule", func(t *testing.T) {
		ok := bakFinished(1, backup.OpCreate, backup.StateCompleted, 40*time.Hour, 30*time.Minute, "", "")
		e := newBakEnv(t, val, nil, nil, ok)
		var info gen.SystemInfo
		if w := e.json(t, "/api/v1/system/info", &info); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		s := summary(t, e)
		if info.Backup == nil || info.Backup.Overdue == nil || s.Overdue == nil || *info.Backup.Overdue != *s.Overdue || !*s.Overdue {
			t.Errorf("info %+v summary %v", info.Backup, s.Overdue)
		}
	})

	t.Run("R-API-050 the last failure shows when newer than the last success and carries the code; interrupted never counts", func(t *testing.T) {
		ok := bakFinished(1, backup.OpCreate, backup.StateCompleted, 30*time.Hour, 30*time.Minute, "", "")
		bad := bakFinished(2, backup.OpCreate, backup.StateFailed, 6*time.Hour, 30*time.Minute, "", `{"code":"stalled"}`)
		drained := bakFinished(3, backup.OpCreate, backup.StateInterrupted, time.Hour, 30*time.Minute, "", `{"code":"interrupted"}`)
		s := summary(t, newBakEnv(t, val, nil, nil, ok, bad, drained))
		if s.LastFailure == nil || s.LastFailure.RunId.String() != bad.ID || s.LastFailure.Code == nil || *s.LastFailure.Code != "stalled" || !s.LastFailure.At.Equal(*bad.FinishedAt) {
			t.Errorf("lastFailure = %+v", s.LastFailure)
		}
		s = summary(t, newBakEnv(t, val, nil, nil, ok, drained))
		if s.LastFailure != nil {
			t.Errorf("an interrupted run is a failure: %+v", s.LastFailure)
		}
		later := bakFinished(4, backup.OpCreate, backup.StateCompletedWithWarning, 2*time.Hour, 30*time.Minute, "", "")
		s = summary(t, newBakEnv(t, val, nil, nil, ok, bad, later))
		if s.LastFailure != nil {
			t.Errorf("a failure older than the last success is shown: %+v", s.LastFailure)
		}
	})

	t.Run("R-API-050 an active create run whose lease expired is the last failure with the code lease_expired and shows as active", func(t *testing.T) {
		ok := bakFinished(1, backup.OpCreate, backup.StateCompleted, 30*time.Hour, 30*time.Minute, "", "")
		dead := bakActive(2, backup.StateCopying, 3*time.Hour, -time.Hour)
		s := summary(t, newBakEnv(t, val, nil, nil, ok, dead))
		if s.LastFailure == nil || s.LastFailure.Code == nil || *s.LastFailure.Code != "lease_expired" || !s.LastFailure.At.Equal(dead.LeaseExpiresAt) {
			t.Errorf("lastFailure = %+v", s.LastFailure)
		}
		if s.Active == nil || s.Active.Id.String() != dead.ID || !s.Active.LeaseExpired || s.Active.State != "copying" {
			t.Errorf("active = %+v", s.Active)
		}
	})

	t.Run("R-API-050 a live run is active with a valid lease", func(t *testing.T) {
		live := bakActive(2, backup.StateSnapshot, time.Hour, 5*time.Minute)
		s := summary(t, newBakEnv(t, val, nil, nil, live))
		if s.Active == nil || s.Active.LeaseExpired || s.Active.Operation != "create" || s.Target == nil || *s.Target != "s3://trove-backups/trove" {
			t.Errorf("summary = %+v", s)
		}
		if s.LastFailure != nil || s.Overdue != nil || s.LastSuccessAt != nil {
			t.Errorf("a first run in flight: %+v", s)
		}
	})

	t.Run("R-API-050 the stored backups and the retention come from the newest run that listed the target", func(t *testing.T) {
		older := bakFinished(1, backup.OpCreate, backup.StateCompleted, 50*time.Hour, 30*time.Minute, `{"retention":{"keepLast":3,"keepWithinSeconds":0},"retained":[]}`, "")
		listed := bakFinished(2, backup.OpDelete, backup.StateCompleted, 30*time.Hour, time.Minute, retained, "")
		noPrune := bakFinished(3, backup.OpCreate, backup.StateCompleted, 5*time.Hour, 30*time.Minute, `{"backupId":"x"}`, "")
		e := newBakEnv(t, val, nil, nil, older, listed, noPrune)
		s := summary(t, e)
		if s.Retention == nil || s.Retention.KeepLast != 7 || s.StoredBackups == nil {
			t.Fatalf("summary = %+v", s)
		}
		sb := s.StoredBackups
		if sb.RunId.String() != listed.ID || !sb.AsOf.Equal(*listed.FinishedAt) || len(sb.Items) != 1 {
			t.Errorf("storedBackups = %+v", sb)
		}
		if it := sb.Items[0]; it.BackupId != "20261027T020000Z-1c9e" || it.SchemaStep != 42 || it.BlobBytes != 412316860416 || it.Damaged {
			t.Errorf("item = %+v", it)
		}
		// the create without pruning is the newest run: the target still comes from it
		if s.Target == nil {
			t.Error("no target")
		}

		later := bakFinished(4, backup.OpCreate, backup.StateCompletedWithWarning, time.Hour, 30*time.Minute, `{"retention":{"keepLast":5,"keepWithinSeconds":86400},"retained":[]}`, "")
		s = summary(t, newBakEnv(t, val, nil, nil, older, listed, noPrune, later))
		if s.StoredBackups == nil || s.StoredBackups.RunId.String() != later.ID || len(s.StoredBackups.Items) != 0 || s.Retention.KeepWithinSeconds != 86400 {
			t.Errorf("the newest listing run was not taken: %+v", s.StoredBackups)
		}
		failedLater := bakFinished(5, backup.OpCreate, backup.StateFailed, 30*time.Minute, 5*time.Minute, retained, `{"code":"x"}`)
		s = summary(t, newBakEnv(t, val, nil, nil, older, listed, later, failedLater))
		if s.StoredBackups == nil || s.StoredBackups.RunId.String() != later.ID {
			t.Errorf("a failed run recorded the stored backups: %+v", s.StoredBackups)
		}
	})

	t.Run("R-API-050 restoredFrom is the origin a restore recorded", func(t *testing.T) {
		from := &gen.BackupRestoredFrom{InstallationId: "inst", Incarnation: "inc", BackupId: "20261027T020000Z-1c9e", SnapshotAt: bakNow.Add(-48 * time.Hour)}
		s := summary(t, newBakEnv(t, val, nil, from))
		if s.RestoredFrom == nil || s.RestoredFrom.BackupId != from.BackupId || s.RestoredFrom.Incarnation != "inc" {
			t.Errorf("restoredFrom = %+v", s.RestoredFrom)
		}
	})
}

func TestR_OPS_004_BackupInfo(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	info := func(t *testing.T, e bakEnv) gen.SystemInfo {
		t.Helper()
		var i gen.SystemInfo
		if w := e.json(t, "/api/v1/system/info", &i); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return i
	}
	t.Run("R-OPS-004 the block is the short form of the summary: interval, last success, overdue and the state of the newest run", func(t *testing.T) {
		ok := bakFinished(1, backup.OpCreate, backup.StateCompleted, 20*time.Hour, 30*time.Minute, "", "")
		verify := bakFinished(2, backup.OpVerify, backup.StateFailed, time.Hour, time.Minute, "", `{"code":"findings"}`)
		i := info(t, newBakEnv(t, val, nil, nil, ok, verify))
		b := i.Backup
		if b == nil || b.ExpectedIntervalSeconds != 86400 || b.LastSuccessAt == nil || !b.LastSuccessAt.Equal(*ok.FinishedAt) ||
			b.Overdue == nil || *b.Overdue || b.LastRunState == nil || *b.LastRunState != "failed" {
			t.Errorf("backup = %+v", b)
		}
	})
	t.Run("R-OPS-004 without a run the block has null members, not a null block", func(t *testing.T) {
		e := newBakEnv(t, val, nil, nil)
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(e.get("/api/v1/system/info").Body.Bytes(), &raw)
		var b map[string]json.RawMessage
		if err := json.Unmarshal(raw["backup"], &b); err != nil || b == nil {
			t.Fatalf("backup = %s", raw["backup"])
		}
		for _, k := range []string{"lastSuccessAt", "overdue", "lastRunState"} {
			if string(b[k]) != "null" {
				t.Errorf("%s = %s", k, b[k])
			}
		}
		if string(b["expectedIntervalSeconds"]) != "86400" {
			t.Errorf("interval = %s", b["expectedIntervalSeconds"])
		}
	})
	t.Run("R-OPS-004 an unreadable table makes the block null and the answer 200", func(t *testing.T) {
		e := newBakEnv(t, val, nil, nil)
		e.mem.Err = errs.E(errs.Unavailable, errs.CodeUnavailable, "down")
		if i := info(t, e); i.Backup != nil {
			t.Errorf("backup = %+v", i.Backup)
		}
	})
}
