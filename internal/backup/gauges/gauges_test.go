package gauges_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/gauges"
	"github.com/guided-traffic/trove/internal/backup/gauges/gaugestest"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/observability"
)

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := t0.Add(d); return &t }

// run builds a finished run that started started before t0 and ended after d.
func finished(id string, op backup.Operation, st backup.State, started, ended time.Duration, result, runErr string) backup.Run {
	r := backup.Run{
		ID: id, Operation: op, State: st, StartedAt: t0.Add(started), FinishedAt: at(ended), HeartbeatAt: t0.Add(ended),
		LeaseExpiresAt: t0.Add(ended),
	}
	if result != "" {
		r.Result = json.RawMessage(result)
	}
	if runErr != "" {
		r.Error = json.RawMessage(runErr)
	}
	return r
}

func active(id string, op backup.Operation, st backup.State, started, lease time.Duration, p backup.Progress) backup.Run {
	return backup.Run{
		ID: id, Operation: op, State: st, StartedAt: t0.Add(started), HeartbeatAt: t0.Add(started),
		LeaseExpiresAt: t0.Add(lease), Progress: p,
	}
}

type env struct {
	mem   *gaugestest.Mem
	reg   *observability.Metrics
	col   *gauges.Collector
	clock time.Time
}

func newEnv(t *testing.T, runs ...backup.Run) *env {
	t.Helper()
	e := &env{mem: &gaugestest.Mem{Now: t0, Runs: runs}, reg: observability.NewMetrics("worker"), clock: t0}
	e.col = gauges.New(gauges.Options{
		Reader: e.mem, Backup: backup.NewMetrics(e.reg), Journal: journal.NewMetrics(e.reg),
		ExpectedInterval: 24 * time.Hour, Clock: func() time.Time { return e.clock },
	})
	return e
}

// values gathers the registry as "name" or "name{label=value,…}" to value.
func (e *env) values(t *testing.T) map[string]float64 {
	t.Helper()
	fams, err := e.reg.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			var ls []string
			for _, l := range m.GetLabel() {
				ls = append(ls, l.GetName()+"="+l.GetValue())
			}
			sort.Strings(ls)
			name := f.GetName()
			if len(ls) > 0 {
				name += "{" + strings.Join(ls, ",") + "}"
			}
			out[name] = m.GetGauge().GetValue()
		}
	}
	return out
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func TestR_BAK_014_Gauges(t *testing.T) {
	t.Run("R-BAK-014 no run at all: no last-success and no last-failure series, zeros elsewhere, the configured interval", func(t *testing.T) {
		e := newEnv(t)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v := e.values(t)
		for _, name := range []string{backup.MetricLastSuccess, backup.MetricLastFailure} {
			if _, ok := v[name]; ok {
				t.Errorf("%s has a series before the event happened", name)
			}
		}
		if v[backup.MetricExpectedInterval] != 86400 {
			t.Errorf("expected interval = %v", v[backup.MetricExpectedInterval])
		}
		if v[backup.MetricRunningSeconds] != 0 || v[backup.MetricPinnedBlobs] != 0 || v[backup.MetricLastVerify] != 0 {
			t.Errorf("gauges = %v", v)
		}
		if v[backup.MetricGCHold+"{mode=all}"] != 0 || v[backup.MetricGCHold+"{mode=pinned}"] != 0 {
			t.Errorf("hold = %v", v)
		}
	})

	t.Run("R-BAK-014 the last success carries finished_at, the duration and the sizes of the newest successful create run", func(t *testing.T) {
		old := finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -50*time.Hour, -49*time.Hour, `{"databaseBytes":1,"blobBytes":2,"bytesCopied":3,"damaged":9}`, "")
		newest := finished("00000000-0000-0000-0000-000000000002", backup.OpCreate, backup.StateCompletedWithWarning, -26*time.Hour-30*time.Minute, -26*time.Hour,
			`{"databaseBytes":183500800,"blobBytes":412316860416,"bytesCopied":9123456789,"damaged":2}`, "")
		e := newEnv(t, old, newest)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v := e.values(t)
		want := map[string]float64{
			backup.MetricLastSuccess: unix(*newest.FinishedAt), backup.MetricLastDuration: 1800,
			backup.MetricLastDatabase: 183500800, backup.MetricLastBlob: 412316860416, backup.MetricLastCopied: 9123456789,
			backup.MetricSourceDamaged: 2,
		}
		for k, w := range want {
			if v[k] != w {
				t.Errorf("%s = %v, want %v", k, v[k], w)
			}
		}
	})

	t.Run("R-BAK-014 failed and abandoned create runs set the last failure; interrupted ones do not", func(t *testing.T) {
		ok := finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -30*time.Hour, -29*time.Hour, "", "")
		interrupted := finished("00000000-0000-0000-0000-000000000002", backup.OpCreate, backup.StateInterrupted, -2*time.Hour, -time.Hour, "", `{"code":"interrupted"}`)
		e := newEnv(t, ok, interrupted)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, has := e.values(t)[backup.MetricLastFailure]; has {
			t.Fatal("an interrupted run counts as a failure")
		}

		failed := finished("00000000-0000-0000-0000-000000000003", backup.OpCreate, backup.StateFailed, -3*time.Hour, -2*time.Hour-30*time.Minute, "", `{"code":"pool_damaged"}`)
		abandoned := finished("00000000-0000-0000-0000-000000000004", backup.OpCreate, backup.StateAbandoned, -90*time.Minute, -30*time.Minute, "", `{"code":"lease_expired"}`)
		e = newEnv(t, ok, interrupted, failed, abandoned)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := e.values(t)[backup.MetricLastFailure]; got != unix(*abandoned.FinishedAt) {
			t.Errorf("last failure = %v, want the newest of failed and abandoned = %v", got, unix(*abandoned.FinishedAt))
		}
	})

	t.Run("R-BAK-014 a failed delete run is not a failed backup", func(t *testing.T) {
		e := newEnv(t, finished("00000000-0000-0000-0000-000000000005", backup.OpDelete, backup.StateFailed, -2*time.Hour, -time.Hour, "", `{"code":"x"}`))
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, has := e.values(t)[backup.MetricLastFailure]; has {
			t.Fatal("a delete run set the last failure")
		}
	})

	t.Run("R-BAK-014 an active create run with an expired lease counts as a failure at its lease expiry and is not running", func(t *testing.T) {
		dead := active("00000000-0000-0000-0000-000000000006", backup.OpCreate, backup.StateCopying, -3*time.Hour, -time.Hour, backup.Progress{PinsLeft: 40})
		e := newEnv(t, dead)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v := e.values(t)
		if v[backup.MetricLastFailure] != unix(dead.LeaseExpiresAt) {
			t.Errorf("last failure = %v, want lease expiry %v", v[backup.MetricLastFailure], unix(dead.LeaseExpiresAt))
		}
		if _, ok := v[backup.MetricRunning+"{operation=create,state=copying}"]; ok {
			t.Error("a dead run is reported as running")
		}
		if v[backup.MetricRunningSeconds] != 0 || v[backup.MetricPinnedBlobs] != 0 {
			t.Errorf("a dead run feeds the running gauges: %v", v)
		}
	})

	t.Run("R-BAK-014 the active run with a valid lease feeds running, running seconds, pins and the open snapshot", func(t *testing.T) {
		since := t0.Add(-10 * time.Minute)
		live := active("00000000-0000-0000-0000-000000000007", backup.OpCreate, backup.StateCopying, -42*time.Minute, 5*time.Minute,
			backup.Progress{PinsLeft: 399000, SnapshotOpenSince: &since})
		e := newEnv(t, live)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v := e.values(t)
		if v[backup.MetricRunning+"{operation=create,state=copying}"] != 1 {
			t.Errorf("running = %v", v)
		}
		if v[backup.MetricRunningSeconds] != 2520 || v[backup.MetricSnapshotOpen] != 600 || v[backup.MetricPinnedBlobs] != 399000 {
			t.Errorf("running seconds %v, snapshot %v, pins %v", v[backup.MetricRunningSeconds], v[backup.MetricSnapshotOpen], v[backup.MetricPinnedBlobs])
		}
		if _, has := v[backup.MetricLastFailure]; has {
			t.Error("a live run is a failure")
		}

		// The run ends: the labelled series disappears, it does not stay at 1.
		e.mem.Runs = []backup.Run{finished(live.ID, backup.OpCreate, backup.StateCompleted, -42*time.Minute, -time.Minute, "", "")}
		e.clock = e.clock.Add(gauges.Reuse)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v = e.values(t)
		if _, ok := v[backup.MetricRunning+"{operation=create,state=copying}"]; ok {
			t.Error("the running series stayed after the run ended")
		}
		if v[backup.MetricRunningSeconds] != 0 || v[backup.MetricPinnedBlobs] != 0 {
			t.Errorf("gauges after the run: %v", v)
		}
	})

	t.Run("R-BAK-014 the holds and the verify gauges", func(t *testing.T) {
		verify := finished("00000000-0000-0000-0000-000000000008", backup.OpVerify, backup.StateFailed, -time.Hour, -50*time.Minute,
			`{"verified":["a"],"findings":[{"kind":"missing"},{"kind":"missing"}]}`, `{"code":"findings"}`)
		e := newEnv(t, verify)
		e.mem.HoldAll = true
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		v := e.values(t)
		if v[backup.MetricGCHold+"{mode=all}"] != 1 || v[backup.MetricGCHold+"{mode=pinned}"] != 0 {
			t.Errorf("hold = %v", v)
		}
		if v[backup.MetricLastVerify] != unix(*verify.FinishedAt) || v[backup.MetricLastVerifyFinds] != 2 {
			t.Errorf("verify = %v / %v", v[backup.MetricLastVerify], v[backup.MetricLastVerifyFinds])
		}
	})

	t.Run("R-HA-054 the journal backlog is read from PostgreSQL", func(t *testing.T) {
		e := newEnv(t)
		e.mem.JournalRows = 17
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := e.values(t)[journal.MetricPending]; got != 17 {
			t.Errorf("pending = %v", got)
		}
	})
}

func TestR_BAK_014_Reuse(t *testing.T) {
	t.Run("R-BAK-014 a second evaluation within 30 seconds reads nothing and keeps the values", func(t *testing.T) {
		e := newEnv(t, finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -2*time.Hour, -time.Hour, "", ""))
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		first := e.mem.Total()
		if first == 0 {
			t.Fatal("the first evaluation read nothing")
		}
		e.mem.Runs = nil
		e.clock = e.clock.Add(gauges.Reuse - time.Second)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if e.mem.Total() != first {
			t.Errorf("the second evaluation read the database again: %d reads, then %d", first, e.mem.Total())
		}
		if _, ok := e.values(t)[backup.MetricLastSuccess]; !ok {
			t.Error("the values were dropped within the reuse window")
		}
		e.clock = e.clock.Add(time.Second)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if e.mem.Total() == first {
			t.Error("the evaluation after 30 seconds did not read")
		}
		if _, ok := e.values(t)[backup.MetricLastSuccess]; ok {
			t.Error("the last success stayed after its run left the table")
		}
	})

	t.Run("R-BAK-014 a failed read keeps the previous values and is tried again at the next call", func(t *testing.T) {
		e := newEnv(t, finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -2*time.Hour, -time.Hour, "", ""))
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.mem.Err = errors.New("database down")
		e.clock = e.clock.Add(time.Minute)
		if err := e.col.Refresh(context.Background()); err == nil {
			t.Fatal("the failure was swallowed")
		}
		if _, ok := e.values(t)[backup.MetricLastSuccess]; !ok {
			t.Error("a failed read dropped the values")
		}
		e.mem.Err = nil
		before := e.mem.Total()
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if e.mem.Total() == before {
			t.Error("the retry waited for the reuse window")
		}
	})

	t.Run("R-BAK-014 an evaluation reads single rows and the runs, never the pins", func(t *testing.T) {
		// The reader interface has no method that counts backup_blob_pin; the pins left come from the progress of the
		// heartbeat. The evaluation is a fixed handful of reads, whatever the number of runs or pins.
		var runs []backup.Run
		for i := range 200 {
			runs = append(runs, finished("00000000-0000-0000-0000-00000000"+pad(i), backup.OpCreate, backup.StateCompleted, -time.Duration(i+2)*time.Hour, -time.Duration(i+1)*time.Hour, "", ""))
		}
		e := newEnv(t, runs...)
		if err := e.col.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if n := e.mem.Total(); n > 8 {
			t.Errorf("an evaluation made %d reads", n)
		}
		if e.mem.Calls["List"] != 0 || e.mem.Calls["Count"] != 0 {
			t.Errorf("the gauges list or count runs: %v", e.mem.Calls)
		}
	})
}

func pad(i int) string {
	s := "0000" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
	return s[len(s)-4:]
}

func TestR_API_050_Rules(t *testing.T) {
	jobs := func(runs ...backup.Run) gauges.Jobs {
		j, err := gauges.ReadJobs(t.Context(), &gaugestest.Mem{Now: t0, Runs: runs})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	day := 24 * time.Hour
	t.Run("R-API-050 overdue is null without a success, false up to 1.5 times the interval, true beyond", func(t *testing.T) {
		if jobs().Overdue(day) != nil {
			t.Error("overdue is not null without a success")
		}
		cases := []struct {
			age  time.Duration
			want bool
		}{{day, false}, {day + 12*time.Hour, false}, {day + 12*time.Hour + time.Second, true}, {3 * day, true}}
		for _, c := range cases {
			ov := jobs(finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -c.age-time.Hour, -c.age, "", "")).Overdue(day)
			if ov == nil || *ov != c.want {
				t.Errorf("age %v: overdue = %v, want %v", c.age, ov, c.want)
			}
		}
	})
	t.Run("R-API-050 the last failure shows only when it is newer than the last success", func(t *testing.T) {
		okRun := finished("00000000-0000-0000-0000-000000000001", backup.OpCreate, backup.StateCompleted, -5*time.Hour, -4*time.Hour, "", "")
		bad := finished("00000000-0000-0000-0000-000000000002", backup.OpCreate, backup.StateFailed, -3*time.Hour, -2*time.Hour, "", `{"code":"stalled"}`)
		if f := jobs(okRun, bad).LastFailure(); f == nil || f.Code != "stalled" || f.RunID != bad.ID {
			t.Errorf("failure after the success = %+v", f)
		}
		if f := jobs(finished(bad.ID, backup.OpCreate, backup.StateFailed, -7*time.Hour, -6*time.Hour, "", `{"code":"stalled"}`), okRun).LastFailure(); f != nil {
			t.Errorf("a failure older than the last success is shown: %+v", f)
		}
		if f := jobs(bad).LastFailure(); f == nil {
			t.Error("with no success at all a failure is shown")
		}
	})
	t.Run("R-API-050 the newest active run is the active one, a dead one included", func(t *testing.T) {
		dead := active("00000000-0000-0000-0000-000000000009", backup.OpCreate, backup.StateSnapshot, -time.Hour, -time.Minute, backup.Progress{})
		j := jobs(dead)
		if a := j.ActiveRun(); a == nil || a.ID != dead.ID || !a.LeaseExpired(j.Now) {
			t.Errorf("active = %+v", a)
		}
		if f := j.LastFailure(); f == nil || f.Code != backup.CodeLeaseExpired {
			t.Errorf("failure of a dead run = %+v", f)
		}
	})
}

func TestR_BAK_014_NoPinCount(t *testing.T) {
	t.Run("R-BAK-014 no gauge counts backup_blob_pin: the evaluation never names the pins table or its count query", func(t *testing.T) {
		files, err := filepath.Glob("*.go")
		if err != nil || len(files) == 0 {
			t.Fatalf("glob: %v %v", files, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"backup_blob_pin", "CountBackupPins"} {
				if strings.Contains(string(src), bad) {
					t.Errorf("%s mentions %s", f, bad)
				}
			}
		}
	})
}
