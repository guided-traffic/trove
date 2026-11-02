//go:build integration

package nuget

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The recomputation of is_semver2 for the rows stored before the classification counted the
// dependency ranges (F-30, Q-NUGET-93, R-NUGET-004): a background data migration a worker queues
// when it starts (R-HA-022). The rows are put into the state release 0.9.0 left: the push of
// this build classifies them right, and the test sets the flag back to false as the old writer
// stored it.

const semver2Kind = "data_migration_nuget_semver2"

// semver2Push pushes the packages of the recomputation tests: Acme.Widgets 1.0.0 without
// dependencies and 1.1.0 with a dependency whose minimum is a dotted pre-release, and Acme.Plain
// 1.0.0 whose range has SemVer 1 bounds. Then 1.1.0 says SemVer 1, as release 0.9.0 stored it.
func semver2Push(t *testing.T, e *wenv, r *testenv.Replica) {
	t.Helper()
	e.mustPush(r, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	e.mustPush(r, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.1.0", depRange: "[2.0.0-rc.1, )"}))
	e.mustPush(r, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Plain", version: "1.0.0", depRange: "[1.0.0, 2.0.0)"}))
	if got := e.semver2("acme.widgets", "1.1.0"); got != "true" {
		t.Fatalf("this build stored is_semver2 = %s for a SemVer 2 dependency bound", got)
	}
	e.st.Exec(`UPDATE nuget_version SET is_semver2 = false WHERE lower_id = 'acme.widgets' AND original_version = '1.1.0'`)
}

// semver2 is nuget_version.is_semver2 of a version of nuget-local, "none" without a row.
func (e *wenv) semver2(id, original string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE((SELECT nv.is_semver2::text FROM nuget_version nv JOIN repositories r ON r.id = nv.repository_id
		WHERE r.key = 'nuget-local' AND nv.lower_id = $1 AND nv.original_version = $2), 'none')`, id, original)
}

// semver2Jobs counts the runs of the recomputation in a state, with a result state when one is
// given.
func (e *wenv) semver2Jobs(state, result string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM job WHERE kind = $1 AND state::text = $2 AND ($3 = '' OR result->>'state' = $3)`,
		semver2Kind, state, result)
}

// hive lists the versions the registration index of acme.widgets in a hive shows to a reader,
// with the index's validator.
func (e *wenv) hive(r *testenv.Replica, hive string) ([]string, string) {
	e.t.Helper()
	a := e.st.Do(r, "GET", "/api/nuget/nuget-local/v3/"+hive+"/acme.widgets/index.json", e.tokReader, nil)
	if a.Status != 200 {
		e.t.Fatalf("%s: %d %s", hive, a.Status, a.Body)
	}
	var out []string
	for _, l := range leaves(e.t, a.JSON(e.t)) {
		out = append(out, l["version"].(string))
	}
	return out, a.Header.Get("ETag")
}

// latest is the version the search shows for acme.widgets with the query's semVerLevel.
func (e *wenv) latest(r *testenv.Replica, query string) string {
	e.t.Helper()
	a := e.st.Do(r, "GET", "/api/nuget/nuget-local/v3/search?q=acme.widgets"+query, e.tokReader, nil)
	if a.Status != 200 {
		e.t.Fatalf("search%s: %d %s", query, a.Status, a.Body)
	}
	var f found
	if err := json.Unmarshal(a.Body, &f); err != nil {
		e.t.Fatalf("search%s: %v %s", query, err, a.Body)
	}
	for _, r := range f.Data {
		if strings.EqualFold(r.ID, "acme.widgets") {
			return r.Version
		}
	}
	e.t.Fatalf("search%s does not find acme.widgets: %s", query, a.Body)
	return ""
}

// semver2Result is the result of the newest finished run.
func (e *wenv) semver2Result() map[string]any {
	e.t.Helper()
	var m map[string]any
	raw := e.st.Text(`SELECT result::text FROM job WHERE kind = $1 AND state = 'succeeded' ORDER BY finished_at DESC LIMIT 1`, semver2Kind)
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		e.t.Fatalf("result %q: %v", raw, err)
	}
	return m
}

// TestR_NUGET_004_AWorkerRecomputesTheSemVer2FlagOfVersionsStoredBeforeTheRule: a version stored
// with is_semver2 = false although a dependency range has a SemVer 2 bound shows in the SemVer 1
// hive and in a search without semVerLevel. The first worker queues the recomputation; after it
// the row says true, the package's revision rose once, so the SemVer 1 hive renders anew without
// the version and its validator moved, and the search shows the SemVer 1 version. A version
// without such a bound keeps false and its package its revision. The completion is recorded: a
// worker that starts later queues nothing, and a run over the rows again changes nothing. A run
// resumes from the cursor an attempt before it saved (R-HA-022).
func TestR_NUGET_004_AWorkerRecomputesTheSemVer2FlagOfVersionsStoredBeforeTheRule(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	semver2Push(t, e, a)
	rev, plainRev := e.rev("nuget-local", "acme.widgets"), e.rev("nuget-local", "acme.plain")

	shown, etag := e.hive(a, "registration")
	if !slices.Equal(shown, []string{"1.0.0", "1.1.0"}) || e.latest(a, "") != "1.1.0" {
		t.Fatalf("before the recomputation: the SemVer 1 hive shows %v and the search %q; the defect is not in place", shown, e.latest(a, ""))
	}
	if n := e.st.Number(`SELECT count(*) FROM system_state WHERE key = 'nuget_semver2_recomputed'`); n != 0 {
		t.Fatalf("the completion is recorded before a worker ran: %d", n)
	}

	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-a", Role: "worker"})
	testenv.WaitFor(t, "the recomputation", 30*time.Second, func() bool { return e.semver2Jobs("succeeded", "recomputed") == 1 })

	if got := e.semver2("acme.widgets", "1.1.0"); got != "true" {
		t.Errorf("1.1.0 says is_semver2 = %s after the recomputation", got)
	}
	if got, plain := e.semver2("acme.widgets", "1.0.0"), e.semver2("acme.plain", "1.0.0"); got != "false" || plain != "false" {
		t.Errorf("versions without a SemVer 2 bound: acme.widgets 1.0.0 %s, acme.plain 1.0.0 %s; want false", got, plain)
	}
	if got := e.rev("nuget-local", "acme.widgets"); got != rev+1 {
		t.Errorf("the revision of acme.widgets is %d, want %d", got, rev+1)
	}
	if got := e.rev("nuget-local", "acme.plain"); got != plainRev {
		t.Errorf("the revision of acme.plain moved from %d to %d", plainRev, got)
	}
	res := e.semver2Result()
	if res["changed"] != 1.0 || res["packages"] != 1.0 || res["examined"] != 3.0 || res["unreadable"] != 0.0 {
		t.Errorf("result = %v, want 3 rows examined, 1 version of 1 package changed", res)
	}
	if got := e.st.Text(`SELECT value->>'changed' FROM system_state WHERE key = 'nuget_semver2_recomputed'`); got != "1" {
		t.Errorf("the recorded completion says changed %q", got)
	}

	shown, after := e.hive(a, "registration")
	if !slices.Equal(shown, []string{"1.0.0"}) || after == etag {
		t.Errorf("the SemVer 1 hive shows %v with the validator %q (before %q); want 1.0.0 alone and a new validator", shown, after, etag)
	}
	if shown, _ := e.hive(a, "registration-semver2"); !slices.Equal(shown, []string{"1.0.0", "1.1.0"}) {
		t.Errorf("the SemVer 2 hive shows %v", shown)
	}
	if got, sv2 := e.latest(a, ""), e.latest(a, "&semVerLevel=2.0.0"); got != "1.0.0" || sv2 != "1.1.0" {
		t.Errorf("the search shows %q without semVerLevel and %q with 2.0.0; want 1.0.0 and 1.1.0", got, sv2)
	}

	t.Run("a worker that starts after the completion queues nothing", func(t *testing.T) {
		e.st.Start(t, testenv.ReplicaOptions{Name: "worker-b", Role: "worker"})
		if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = $1`, semver2Kind); n != 1 {
			t.Errorf("%d runs after a second worker started, want the first alone", n)
		}
	})

	t.Run("a second run over the rows changes nothing", func(t *testing.T) {
		e.st.Exec(`DELETE FROM system_state WHERE key = 'nuget_semver2_recomputed'`)
		e.st.Start(t, testenv.ReplicaOptions{Name: "worker-c", Role: "worker"})
		testenv.WaitFor(t, "the second run", 30*time.Second, func() bool { return e.semver2Jobs("succeeded", "recomputed") == 2 })
		res := e.semver2Result()
		if res["changed"] != 0.0 || res["packages"] != 0.0 || res["examined"] != 2.0 {
			t.Errorf("result = %v, want the two SemVer 1 rows examined and nothing changed", res)
		}
		if got := e.rev("nuget-local", "acme.widgets"); got != rev+1 {
			t.Errorf("the second run moved the revision of acme.widgets to %d", got)
		}
		if got := e.semver2("acme.widgets", "1.1.0"); got != "true" {
			t.Errorf("1.1.0 says %s after the second run", got)
		}
	})

	t.Run("a run resumes from the cursor the attempt before it saved", func(t *testing.T) {
		// The attempt before read every row and saved its cursor and counts; the row behind the
		// cursor is not read again, so it keeps what it says, and the counts go on.
		e.st.Exec(`DELETE FROM system_state WHERE key = 'nuget_semver2_recomputed'`)
		e.st.Exec(`UPDATE nuget_version SET is_semver2 = false WHERE lower_id = 'acme.widgets' AND original_version = '1.1.0'`)
		after := e.st.Number(`SELECT max(version_id) FROM nuget_version`)
		e.st.Exec(`INSERT INTO job (id, kind, queue, dedup_key, dedup_mode, checkpoint)
			VALUES (gen_random_uuid(), $1, 'maintenance', $1, 'coalesce', jsonb_build_object('after', $2::bigint, 'examined', 7, 'changed', 2, 'packages', 1))`,
			semver2Kind, after)
		testenv.WaitFor(t, "the resumed run", 30*time.Second, func() bool { return e.semver2Jobs("succeeded", "recomputed") == 3 })
		res := e.semver2Result()
		if res["examined"] != 7.0 || res["changed"] != 2.0 || res["packages"] != 1.0 {
			t.Errorf("result = %v, want the counts of the checkpoint and nothing read after it", res)
		}
		if got := e.semver2("acme.widgets", "1.1.0"); got != "false" {
			t.Errorf("a row behind the cursor was read again: %s", got)
		}
	})
}

// TestR_HA_022_TheSemVer2RecomputationWaitsForAReplicaOfTheOldRelease: while a replica of release
// 0.9.0 is live it could write a row the old way behind the recomputation, so the run changes
// nothing, says whom it waits for and queues the run after it; that run recomputes once the
// replica is gone.
func TestR_HA_022_TheSemVer2RecomputationWaitsForAReplicaOfTheOldRelease(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	semver2Push(t, e, a)
	// A heartbeat ahead of the clock keeps the row live however long the worker takes to start.
	e.st.Exec(`INSERT INTO replica (instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, started_at, heartbeat_at)
		VALUES (gen_random_uuid(), 'web-old', '0.9.0', 'f6037ef', '{web}', 'ready', '{}', '', now(), now() + interval '10 minutes')`)

	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-a", Role: "worker"})
	testenv.WaitFor(t, "the waiting run", 30*time.Second, func() bool { return e.semver2Jobs("succeeded", "waiting") == 1 })
	if got := e.semver2Result()["waitingFor"]; !slices.Equal(strsOf(got), []string{"web-old (0.9.0)"}) {
		t.Errorf("waitingFor = %v", got)
	}
	if got := e.semver2("acme.widgets", "1.1.0"); got != "false" {
		t.Errorf("a waiting run changed the row: %s", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = $1 AND state = 'queued' AND run_at > now()`, semver2Kind); n != 1 {
		t.Fatalf("%d later runs queued, want 1", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM system_state WHERE key = 'nuget_semver2_recomputed'`); n != 0 {
		t.Errorf("a waiting run recorded the completion")
	}

	// The replica of the old release is gone; the queued run need not wait out its delay.
	e.st.Exec(`DELETE FROM replica WHERE name = 'web-old'`)
	e.st.Exec(`UPDATE job SET run_at = now() WHERE kind = $1 AND state = 'queued'`, semver2Kind)
	testenv.WaitFor(t, "the run after it", 30*time.Second, func() bool { return e.semver2Jobs("succeeded", "recomputed") == 1 })
	if got := e.semver2("acme.widgets", "1.1.0"); got != "true" {
		t.Errorf("1.1.0 says %s after the run that followed the wait", got)
	}
}

func strsOf(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
