package v1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// R-OPS-019, Q-OPS-186: the error column of a job row is answered as the declared JobError.
func TestR_OPS_019_TheErrorOfAJobRowBecomesAJobError(t *testing.T) {
	row := func(errCol string) q.GetJobViewRow {
		r := q.GetJobViewRow{Kind: "probe", State: "failed", Attempt: 2, MaxAttempts: 3}
		if errCol != "" {
			r.Error = []byte(errCol)
		}
		r.ID = pgtype.UUID{Valid: true}
		return r
	}

	t.Run("all three members", func(t *testing.T) {
		e := toJob(row(`{"code":"job_failed","detail":"x","attempt":2}`)).Error
		if e == nil || string(e.Code) != "job_failed" || e.Detail == nil || *e.Detail != "x" || e.Attempt == nil || *e.Attempt != 2 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("a code alone", func(t *testing.T) {
		e := toJob(row(`{"code":"cancelled"}`)).Error
		if e == nil || string(e.Code) != "cancelled" || e.Detail != nil || e.Attempt != nil {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("the reaper's error has no attempt", func(t *testing.T) {
		e := toJob(row(`{"code":"attempts_exhausted","detail":"the lease expired on the last attempt"}`)).Error
		if e == nil || e.Detail == nil || !strings.Contains(*e.Detail, "lease expired") || e.Attempt != nil {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("SQL NULL, JSON null, a row without a code and a row that is no object are no error", func(t *testing.T) {
		for _, col := range []string{"", "null", `{"detail":"x"}`, `{"code":""}`, `[1]`, `"text"`, `{`} {
			if e := toJob(row(col)).Error; e != nil {
				t.Errorf("%q gave %+v", col, e)
			}
		}
	})
	t.Run("a member the schema does not declare is not answered", func(t *testing.T) {
		body, err := json.Marshal(toJob(row(`{"code":"job_failed","secret":"s3cr3t"}`)))
		if err != nil || strings.Contains(string(body), "s3cr3t") || strings.Contains(string(body), "secret") {
			t.Fatalf("%s %v", body, err)
		}
	})
}

// R-OPS-019, Q-OPS-186: a result that is an object reaches the API, the shape of every job kind.
func TestR_OPS_019_TheResultOfAJobRowIsAnObject(t *testing.T) {
	r := q.GetJobViewRow{Kind: "vuln-sync-osv", State: "succeeded", Result: []byte(`{"ecosystems":[{"ecosystem":"npm","rejected":1}]}`)}
	got := toJob(r).Result
	if got == nil || (*got)["ecosystems"] == nil {
		t.Fatalf("%v", got)
	}
}
