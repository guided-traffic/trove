//go:build integration

package backup

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/gauges"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The read side of the backup job against real backup_run rows (R-API-050, R-BAK-014, R-OPS-004, R-OPS-007): the API of a
// replica, its gauges and the collector over backup.PGStore. The rows are what the backup pod writes; this suite inserts them
// as the superuser, so it does not wait for the create pipeline.

// runRow is a backup_run row; times are seconds before the database's now (the lease: seconds after it).
type runRow struct {
	ID, Op, State, Hold, Trigger string
	StartedAgo                   float64
	LeaseIn                      float64
	FinishedAgo                  *float64
	BackupID, Result, Error      string
	Progress                     string
}

func ago(s float64) *float64 { return &s }

func (r runRow) insert(st *testenv.Stack) {
	if r.Hold == "" {
		r.Hold = "none"
	}
	if r.Trigger == "" {
		r.Trigger = "schedule"
	}
	if r.Progress == "" {
		r.Progress = "{}"
	}
	st.Exec(`INSERT INTO backup_run (id, operation, backup_id, state, hold, trigger, target, pod, trove_version, progress, result, error,
	                                  started_at, heartbeat_at, lease_expires_at, finished_at)
	         VALUES ($1::uuid, $2, NULLIF($3, ''), $4, $5, $6, 's3://trove-backups/trove', 'trove-backup-x7k2q', '0.4.0', $7::jsonb,
	                 NULLIF($8, '')::jsonb, NULLIF($9, '')::jsonb,
	                 now() - make_interval(secs => $10::float8), now() - make_interval(secs => $10::float8),
	                 now() + make_interval(secs => $11::float8),
	                 CASE WHEN $12::float8 IS NULL THEN NULL ELSE now() - make_interval(secs => $12::float8) END)`,
		r.ID, r.Op, r.BackupID, r.State, r.Hold, r.Trigger, r.Progress, r.Result, r.Error, r.StartedAgo, r.LeaseIn, r.FinishedAgo)
}

func rid(n int) string { return fmt.Sprintf("0193a000-0000-7000-8000-%012d", n) }

func epoch(st *testenv.Stack, id, column string) float64 {
	return float64(st.Number(fmt.Sprintf("SELECT (extract(epoch from %s) * 1000000)::bigint FROM backup_run WHERE id = $1::uuid", column), id)) / 1e6
}

const (
	hour = 3600.0
	// listed is the result of a create that pruned: the retention it applied and the backups it left.
	listed = `{"backupId":"20261107T020000Z-aaaa","outcome":"completed","databaseBytes":183500800,"blobCount":51234,"blobBytes":412316860416,
	  "blobsCopied":12,"bytesCopied":9123456789,"damaged":1,"retention":{"keepLast":7,"keepWithinSeconds":0},
	  "retained":[{"backupId":"20261107T020000Z-aaaa","snapshotAt":"2026-11-07T02:00:08.913Z","troveVersion":"0.4.0","schemaStep":46,
	               "databaseBytes":183500800,"blobCount":51234,"blobBytes":412316860416,"damaged":true}]}`
)

// TestR_BAK_014_GaugesOverPostgres runs the collector over backup.PGStore on a migrated database and steps its clock over the
// 30 seconds of reuse, so each state of the table is read.
func TestR_BAK_014_GaugesOverPostgres(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "bakgauge"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := postgres.Open(ctx, st.DatabaseConfig(), "it-backup-gauges", meta.RoleWorker, testenv.Quiet())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)

	reg := observability.NewMetrics("worker")
	clock := time.Now()
	col := gauges.New(gauges.Options{
		Reader: backup.NewPGStore(db), Backup: backup.NewMetrics(reg), Journal: journal.NewMetrics(reg),
		ExpectedInterval: time.Hour, Clock: func() time.Time { return clock },
	})
	value := func(series string) (float64, bool) {
		fams, err := reg.Registry().Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fams {
			for _, m := range f.GetMetric() {
				name := f.GetName()
				if len(m.GetLabel()) > 0 {
					var ls []string
					for _, l := range m.GetLabel() {
						ls = append(ls, l.GetName()+"="+l.GetValue())
					}
					name += "{" + strings.Join(ls, ",") + "}"
				}
				if name == series {
					return m.GetGauge().GetValue(), true
				}
			}
		}
		return 0, false
	}
	refresh := func() {
		t.Helper()
		clock = clock.Add(gauges.Reuse)
		if err := col.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("R-BAK-014 an empty table: no last-success and no last-failure series", func(t *testing.T) {
		refresh()
		for _, s := range []string{backup.MetricLastSuccess, backup.MetricLastFailure} {
			if _, ok := value(s); ok {
				t.Errorf("%s has a series", s)
			}
		}
		if v, _ := value(backup.MetricExpectedInterval); v != 3600 {
			t.Errorf("expected interval = %v", v)
		}
	})

	t.Run("R-BAK-014 the success, the sizes and the failure of ended runs; an interrupted run is not a failure", func(t *testing.T) {
		runRow{ID: rid(1), Op: "create", State: "completed", StartedAgo: 7 * hour, LeaseIn: -6 * hour, FinishedAgo: ago(6.5 * hour), Result: listed}.insert(st)
		runRow{ID: rid(2), Op: "create", State: "interrupted", StartedAgo: 2 * hour, LeaseIn: -hour, FinishedAgo: ago(hour), Error: `{"code":"interrupted"}`}.insert(st)
		refresh()
		if v, ok := value(backup.MetricLastSuccess); !ok || math.Abs(v-epoch(st, rid(1), "finished_at")) > 1e-3 {
			t.Errorf("last success = %v, %v", v, ok)
		}
		if v, _ := value(backup.MetricLastDuration); math.Abs(v-1800) > 1 {
			t.Errorf("duration = %v", v)
		}
		for series, want := range map[string]float64{
			backup.MetricLastDatabase: 183500800, backup.MetricLastBlob: 412316860416, backup.MetricLastCopied: 9123456789, backup.MetricSourceDamaged: 1,
		} {
			if v, _ := value(series); v != want {
				t.Errorf("%s = %v, want %v", series, v, want)
			}
		}
		if _, ok := value(backup.MetricLastFailure); ok {
			t.Error("an interrupted run set the last failure")
		}

		runRow{ID: rid(3), Op: "create", State: "failed", StartedAgo: 3 * hour, LeaseIn: -2 * hour, FinishedAgo: ago(2.5 * hour), Error: `{"code":"pool_damaged"}`}.insert(st)
		refresh()
		if v, ok := value(backup.MetricLastFailure); !ok || math.Abs(v-epoch(st, rid(3), "finished_at")) > 1e-3 {
			t.Errorf("last failure = %v, %v", v, ok)
		}
	})

	t.Run("R-BAK-014 a run with a valid lease is running; its pins come from the heartbeat, not from the pin table", func(t *testing.T) {
		runRow{
			ID: rid(4), Op: "create", State: "copying", Hold: "pinned", StartedAgo: 600, LeaseIn: 300,
			Progress: `{"phase":"copying","pinsLeft":399999,"snapshotOpenSince":"` + time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339) + `"}`,
		}.insert(st)
		st.Exec(`INSERT INTO backup_blob_pin (run_id, sha256, size) SELECT $1::uuid, sha256(g::text::bytea), 1 FROM generate_series(1, 3) g`, rid(4))
		refresh()
		if v, ok := value(backup.MetricRunning + "{operation=create,state=copying}"); !ok || v != 1 {
			t.Errorf("running = %v, %v", v, ok)
		}
		if v, _ := value(backup.MetricPinnedBlobs); v != 399999 {
			t.Errorf("pinned blobs = %v, want the heartbeat's 399999, not the 3 rows", v)
		}
		if v, _ := value(backup.MetricRunningSeconds); v < 590 || v > 700 {
			t.Errorf("running seconds = %v", v)
		}
		if v, _ := value(backup.MetricSnapshotOpen); v < 290 || v > 400 {
			t.Errorf("snapshot open seconds = %v", v)
		}
		if v, _ := value(backup.MetricGCHold + "{mode=pinned}"); v != 1 {
			t.Errorf("pinned hold = %v", v)
		}
		if v, _ := value(backup.MetricGCHold + "{mode=all}"); v != 0 {
			t.Errorf("global hold = %v", v)
		}
	})

	t.Run("R-BAK-014 a run whose lease expired counts as a failure at its lease expiry and is no longer running", func(t *testing.T) {
		st.Exec(`UPDATE backup_run SET lease_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, rid(4))
		refresh()
		if v, ok := value(backup.MetricLastFailure); !ok || math.Abs(v-epoch(st, rid(4), "lease_expires_at")) > 1e-3 {
			t.Errorf("last failure = %v, %v, want the lease expiry", v, ok)
		}
		if _, ok := value(backup.MetricRunning + "{operation=create,state=copying}"); ok {
			t.Error("a run with an expired lease is running")
		}
		if v, _ := value(backup.MetricPinnedBlobs); v != 0 {
			t.Errorf("pinned blobs = %v", v)
		}
		if v, _ := value(backup.MetricGCHold + "{mode=pinned}"); v != 0 {
			t.Errorf("an expired hold is valid: %v", v)
		}
	})

	t.Run("R-BAK-014 verify runs feed the verify gauges; the journal backlog is counted from the outbox", func(t *testing.T) {
		st.Exec(`UPDATE backup_run SET state = 'abandoned', hold = 'none', finished_at = now() WHERE id = $1::uuid`, rid(4))
		runRow{
			ID: rid(5), Op: "verify", State: "failed", StartedAgo: 100, LeaseIn: -50, FinishedAgo: ago(60),
			Result: `{"verified":["20261107T020000Z-aaaa"],"findings":[{"kind":"missing","digest":"ab"}]}`, Error: `{"code":"findings"}`,
		}.insert(st)
		refresh()
		if v, _ := value(backup.MetricLastVerifyFinds); v != 1 {
			t.Errorf("verify findings = %v", v)
		}
		if v, _ := value(backup.MetricLastVerify); math.Abs(v-epoch(st, rid(5), "finished_at")) > 1e-3 {
			t.Errorf("last verify = %v", v)
		}
		if v, ok := value(journal.MetricPending); !ok || v != 0 {
			t.Errorf("journal pending = %v, %v", v, ok)
		}
	})

	t.Run("R-BAK-014 a second evaluation inside 30 seconds does not read the table", func(t *testing.T) {
		st.Exec(`DELETE FROM backup_run WHERE operation = 'create' AND state = 'completed'`)
		before, _ := value(backup.MetricLastSuccess)
		clock = clock.Add(gauges.Reuse - time.Second)
		if err := col.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if v, ok := value(backup.MetricLastSuccess); !ok || v != before {
			t.Errorf("the gauge changed inside the reuse window: %v, %v", v, ok)
		}
		clock = clock.Add(2 * time.Second)
		if err := col.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := value(backup.MetricLastSuccess); ok {
			t.Error("the last success stayed after its row was deleted")
		}
	})
}

// TestR_API_050_BackupAPIOverPostgres serves the three operations from a replica over real rows: the filters, the keyset
// paging, leaseExpired, the summary, the info block, the permission and the gauges of the replica's own registry.
func TestR_API_050_BackupAPIOverPostgres(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "bakapi"})
	rita, alice := st.User("rita"), st.User("alice")
	st.Bind("admin", rita)
	admin := st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	nobody := st.Token(alice, scope.Default())

	// 1h interval: the success below, 2h old, is overdue (1.5h).
	runRow{ID: rid(1), Op: "create", State: "completed", StartedAgo: 2.5 * hour, LeaseIn: -2 * hour, FinishedAgo: ago(2 * hour), BackupID: "20261107T000000Z-aaaa", Result: listed}.insert(st)
	runRow{ID: rid(2), Op: "create", State: "failed", StartedAgo: 1.5 * hour, LeaseIn: -hour, FinishedAgo: ago(hour), Error: `{"code":"pool_damaged","title":"The pool is damaged"}`}.insert(st)
	runRow{ID: rid(3), Op: "create", State: "interrupted", StartedAgo: 0.9 * hour, LeaseIn: -0.5 * hour, FinishedAgo: ago(0.5 * hour), Trigger: "manual", Error: `{"code":"interrupted"}`}.insert(st)
	runRow{ID: rid(4), Op: "verify", State: "completed", StartedAgo: 0.4 * hour, LeaseIn: -0.3 * hour, FinishedAgo: ago(0.3 * hour), Result: `{"verified":["x"],"findings":[]}`}.insert(st)
	runRow{ID: rid(5), Op: "create", State: "snapshot", Hold: "all", StartedAgo: 0.2 * hour, LeaseIn: -60, Trigger: "manual", Progress: `{"phase":"snapshot","tablesDone":3,"tablesTotal":58}`}.insert(st)
	st.Exec(`INSERT INTO system_state (key, value) VALUES ('restored_from', '{"installationId":"i-1","incarnation":"c-1","backupId":"20261001T000000Z-bbbb","snapshotAt":"2026-10-01T00:00:00Z"}'::jsonb)`)

	r := st.Start(t, testenv.ReplicaOptions{Name: "web1", Env: []string{
		"TROVE_BACKUP_EXPECTED_INTERVAL=1h", "TROVE_BACKUP_MANUAL_RUN_NAMESPACE=trove", "TROVE_BACKUP_MANUAL_RUN_CRONJOB=trove-backup",
		"TROVE_METRICS_DB_GAUGE_INTERVAL=1s",
	}})
	get := func(path, tok string) testenv.Answer { t.Helper(); return st.Do(r, "GET", path, tok, nil) }

	t.Run("R-API-050 a caller without settings:read gets 403 on all three operations", func(t *testing.T) {
		for _, p := range []string{"/api/v1/system/backups", "/api/v1/system/backups/summary", "/api/v1/system/backups/" + rid(1)} {
			if a := get(p, nobody); a.Status != 403 {
				t.Errorf("%s: %d %s", p, a.Status, a.Body)
			}
		}
		if a := get("/api/v1/system/backups", ""); a.Status != 401 {
			t.Errorf("anonymous: %d", a.Status)
		}
	})

	ids := func(path string) ([]string, map[string]any) {
		t.Helper()
		a := get(path, admin)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		body := a.JSON(t)
		var out []string
		for _, it := range body["items"].([]any) {
			out = append(out, it.(map[string]any)["id"].(string)[len(rid(1))-2:])
		}
		return out, body
	}
	want := func(path string, w ...string) {
		t.Helper()
		got, _ := ids(path)
		if strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%s: got %v, want %v", path, got, w)
		}
	}

	t.Run("R-API-050 the list is newest first and the filters select in SQL", func(t *testing.T) {
		want("/api/v1/system/backups", "05", "04", "03", "02", "01")
		want("/api/v1/system/backups?operation=verify", "04")
		want("/api/v1/system/backups?operation=create&state=failed", "02")
		want("/api/v1/system/backups?trigger=manual", "05", "03")
		from := url.QueryEscape(time.Now().Add(-100 * time.Minute).UTC().Format(time.RFC3339))
		to := url.QueryEscape(time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339))
		want("/api/v1/system/backups?startedFrom="+from, "05", "04", "03", "02")
		want("/api/v1/system/backups?startedFrom="+from+"&startedTo="+to, "03", "02")
		if a := get("/api/v1/system/backups?state=nonsense", admin); a.Status != 400 {
			t.Errorf("unknown state: %d", a.Status)
		}
	})

	t.Run("R-API-050 keyset paging walks every run once, with the total and a Link header", func(t *testing.T) {
		var seen []string
		cursor := ""
		for range 4 {
			path := "/api/v1/system/backups?limit=2&includeTotal=true"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			a := get(path, admin)
			if a.Status != 200 {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
			body := a.JSON(t)
			if body["total"].(float64) != 5 {
				t.Fatalf("total = %v", body["total"])
			}
			for _, it := range body["items"].([]any) {
				seen = append(seen, it.(map[string]any)["id"].(string)[len(rid(1))-2:])
			}
			next, _ := body["nextCursor"].(string)
			if next == "" {
				break
			}
			if !strings.Contains(a.Header.Get("Link"), `rel="next"`) {
				t.Errorf("Link = %q", a.Header.Get("Link"))
			}
			cursor = next
		}
		if strings.Join(seen, ",") != "05,04,03,02,01" {
			t.Errorf("walk = %v", seen)
		}
	})

	t.Run("R-API-050 one run: leaseExpired from the database's clock, null fields, the stored problem object", func(t *testing.T) {
		a := get("/api/v1/system/backups/"+rid(5), admin)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		run := a.JSON(t)
		if run["leaseExpired"] != true || run["finishedAt"] != nil || run["result"] != nil || run["error"] != nil || run["backupId"] != nil || run["state"] != "snapshot" {
			t.Errorf("active run = %v", run)
		}
		if p := run["progress"].(map[string]any); p["phase"] != "snapshot" || p["tablesTotal"].(float64) != 58 {
			t.Errorf("progress = %v", p)
		}
		failed := get("/api/v1/system/backups/"+rid(2), admin).JSON(t)
		if failed["leaseExpired"] != false || failed["error"].(map[string]any)["code"] != "pool_damaged" || failed["finishedAt"] == nil {
			t.Errorf("failed run = %v", failed)
		}
		done := get("/api/v1/system/backups/"+rid(1), admin).JSON(t)
		if done["backupId"] != "20261107T000000Z-aaaa" || done["result"].(map[string]any)["outcome"] != "completed" {
			t.Errorf("completed run = %v", done)
		}
		if a := get("/api/v1/system/backups/"+rid(9), admin); a.Status != 404 {
			t.Errorf("unknown: %d", a.Status)
		}
	})

	t.Run("R-API-050 the summary: overdue, the failure of the dead run, the active run, the stored backups, the origin", func(t *testing.T) {
		a := get("/api/v1/system/backups/summary", admin)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		s := a.JSON(t)
		if s["expectedIntervalSeconds"].(float64) != 3600 || s["overdue"] != true {
			t.Errorf("interval/overdue = %v / %v", s["expectedIntervalSeconds"], s["overdue"])
		}
		if s["lastSuccessAt"] == nil || s["target"] != "s3://trove-backups/trove" {
			t.Errorf("lastSuccessAt/target = %v / %v", s["lastSuccessAt"], s["target"])
		}
		f := s["lastFailure"].(map[string]any)
		if f["runId"] != rid(5) || f["code"] != "lease_expired" {
			t.Errorf("lastFailure = %v (the active create run with an expired lease is the newest failure)", f)
		}
		act := s["active"].(map[string]any)
		if act["id"] != rid(5) || act["leaseExpired"] != true || act["state"] != "snapshot" {
			t.Errorf("active = %v", act)
		}
		if ret := s["retention"].(map[string]any); ret["keepLast"].(float64) != 7 {
			t.Errorf("retention = %v", ret)
		}
		sb := s["storedBackups"].(map[string]any)
		items := sb["items"].([]any)
		if sb["runId"] != rid(1) || len(items) != 1 || items[0].(map[string]any)["backupId"] != "20261107T020000Z-aaaa" || items[0].(map[string]any)["damaged"] != true {
			t.Errorf("storedBackups = %v", sb)
		}
		if rf := s["restoredFrom"].(map[string]any); rf["backupId"] != "20261001T000000Z-bbbb" || rf["incarnation"] != "c-1" {
			t.Errorf("restoredFrom = %v", rf)
		}
		if m := s["manualRun"].(map[string]any); m["namespace"] != "trove" || m["cronJob"] != "trove-backup" {
			t.Errorf("manualRun = %v", m)
		}
	})

	t.Run("R-OPS-004 the info block repeats the success, the overdue mark and the state of the newest run", func(t *testing.T) {
		a := get("/api/v1/system/info", admin)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		b := a.JSON(t)["backup"].(map[string]any)
		if b["expectedIntervalSeconds"].(float64) != 3600 || b["overdue"] != true || b["lastRunState"] != "snapshot" || b["lastSuccessAt"] == nil {
			t.Errorf("backup = %v", b)
		}
	})

	t.Run("R-OPS-007 the replica serves the gauges from its registry: the success, the failure of the dead run, the interval", func(t *testing.T) {
		var text string
		testenv.WaitFor(t, "the backup gauges", 30*time.Second, func() bool {
			text = r.Metrics(t)
			return testenv.MetricValue(text, backup.MetricLastSuccess) > 0
		})
		if v := testenv.MetricValue(text, backup.MetricLastSuccess); math.Abs(v-epoch(st, rid(1), "finished_at")) > 1e-3 {
			t.Errorf("last success = %v", v)
		}
		if v := testenv.MetricValue(text, backup.MetricLastFailure); math.Abs(v-epoch(st, rid(5), "lease_expires_at")) > 1e-3 {
			t.Errorf("last failure = %v, want the lease expiry of the dead run", v)
		}
		if v := testenv.MetricValue(text, backup.MetricExpectedInterval); v != 3600 {
			t.Errorf("expected interval = %v", v)
		}
		if v := testenv.MetricValue(text, journal.MetricPending); v != 0 {
			t.Errorf("journal pending = %v", v)
		}
		if strings.Contains(text, backup.MetricRunning+`{`) {
			t.Error("a run with an expired lease is running")
		}
	})
}
