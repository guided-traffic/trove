// Package gauges reads the state of the backup job from PostgreSQL and turns it into the gauges of R-BAK-014 and into the
// answers of the summary and the system information (R-API-050, R-OPS-004). Every replica evaluates it the same way over
// backup.RunReader, so the metrics, the API, the UI and the alert rules agree on what "last success", "last failure" and
// "overdue" mean.
package gauges

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
)

// OverdueFactor is the multiple of backup.expected_interval after which the last success counts as overdue: the condition of
// TroveBackupStale, of the API's overdue mark and of the UI's (R-BAK-014, R-API-050). A mark at 1 would flap on every
// schedule whose runs vary in length.
const OverdueFactor = 1.5

// Failure is the newest failure of a create run (R-BAK-014): a run that ended failed or abandoned, or one that is still in an
// active state and whose lease has expired (its process died). An interrupted run never counts, because a drain is not a
// failure.
type Failure struct {
	RunID string
	// At is finished_at of an ended run, or lease_expires_at of an active run with an expired lease.
	At time.Time
	// Code is the stable code of the stored problem object; lease_expired for an active run with an expired lease.
	Code string
}

// Jobs is what the summary, the system information and the gauges share about the create and delete runs, read at one
// moment of the database's clock.
type Jobs struct {
	// Now is the database's clock; every comparison with a lease or a finish time uses it.
	Now time.Time
	// LastSuccess is the newest completed or completed_with_warnings create run.
	LastSuccess *backup.Run
	// Failure is the newest failure of a create run, whether or not a success followed it (the gauge). The summary shows it
	// only when LastFailure says so.
	Failure *Failure
	// Active are the runs in an active state of any operation, newest first, with expired leases included.
	Active []backup.Run
}

// ReadJobs reads the three facts with the reader's cheap single-row statements; none of them reads the pins table.
func ReadJobs(ctx context.Context, r backup.RunReader) (Jobs, error) {
	var j Jobs
	run, ok, err := r.Last(ctx, backup.OpCreate, backup.StateCompleted, backup.StateCompletedWithWarning)
	if err != nil {
		return Jobs{}, err
	}
	if ok {
		j.LastSuccess = &run
	}
	ended, endedOK, err := r.Last(ctx, backup.OpCreate, backup.StateFailed, backup.StateAbandoned)
	if err != nil {
		return Jobs{}, err
	}
	j.Active, j.Now, err = r.ActiveRuns(ctx)
	if err != nil {
		return Jobs{}, err
	}
	if endedOK && ended.FinishedAt != nil {
		j.Failure = &Failure{RunID: ended.ID, At: *ended.FinishedAt, Code: ProblemCode(ended.Error)}
	}
	for _, a := range j.Active {
		if a.Operation == backup.OpCreate && a.LeaseExpired(j.Now) && (j.Failure == nil || a.LeaseExpiresAt.After(j.Failure.At)) {
			j.Failure = &Failure{RunID: a.ID, At: a.LeaseExpiresAt, Code: backup.CodeLeaseExpired}
		}
	}
	return j, nil
}

// LastSuccessAt is finished_at of the last success, nil before one exists.
func (j Jobs) LastSuccessAt() *time.Time {
	if j.LastSuccess == nil {
		return nil
	}
	return j.LastSuccess.FinishedAt
}

// Overdue is true when the last success is older than OverdueFactor times the expected interval, and nil (unknown) while no
// run has succeeded: the rule of R-API-050.
func (j Jobs) Overdue(expected time.Duration) *bool {
	at := j.LastSuccessAt()
	if at == nil {
		return nil
	}
	v := j.Now.Sub(*at) > time.Duration(float64(expected)*OverdueFactor)
	return &v
}

// LastFailure is the failure the summary shows: the newest one, when it is newer than the last success. With no success at
// all, any failure is shown (R-API-050).
func (j Jobs) LastFailure() *Failure {
	if j.Failure == nil {
		return nil
	}
	if at := j.LastSuccessAt(); at != nil && !j.Failure.At.After(*at) {
		return nil
	}
	return j.Failure
}

// ActiveRun is the run the summary shows as active: the newest run in an active state, whether its lease is valid or not.
func (j Jobs) ActiveRun() *backup.Run {
	if len(j.Active) == 0 {
		return nil
	}
	return &j.Active[0]
}

// ProblemCode returns the code of a stored problem object, "" when the object is absent or has none.
func ProblemCode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var p struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return p.Code
}
