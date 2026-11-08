package vuln

import (
	"strconv"

	"github.com/guided-traffic/trove/internal/jobs"
)

// The job kinds of the vulnerability scanning (R-VULN-004, R-VULN-011, R-VULN-014), and the queue and dedup mode each
// registers with. The contract wave names them; the work package that owns a kind registers it, with its handler, in
// internal/app/wire_vuln.go (a kind is never registered without a handler).
const (
	// KindSyncOSV is the incremental OSV sync, KindSyncOSVFull the full one (work package A). Both: queue
	// vuln (Q-ARCH-296), dedup key DedupSyncOSV, exclusive, MaxAttempts 1, so one OSV sync runs at a time and a colliding slot
	// is postponed, never lost.
	KindSyncOSV     = "vuln-sync-osv"
	KindSyncOSVFull = "vuln-sync-osv-full"
	// KindSyncKEV imports the CISA catalog (work package A): queue vuln, exclusive, MaxAttempts 1.
	KindSyncKEV = "vuln-sync-kev"
	// KindEvaluate works off the changed advisories and the requested full evaluations (work package B): queue
	// default, dedup key DedupEvaluate, coalesce (work is read from tables, Q-VULN-18).
	KindEvaluate = "vuln-evaluate"
	// KindRescan evaluates one repository (work package B): queue default, dedup key RescanKey(id), exclusive.
	KindRescan = "vuln-rescan"
	// KindFindingsRetention deletes resolved findings past their retention (work package B): queue maintenance.
	KindFindingsRetention = "vuln-findings-retention"
)

// The queues of the kinds.
const (
	QueueMaintenance = "maintenance"
	QueueVuln        = "vuln"
	QueueDefault     = "default"
)

// The dedup keys.
const (
	DedupSyncOSV  = "vuln-sync-osv"
	DedupEvaluate = "vuln-evaluate"
)

// RescanKey is the dedup key of the rescan of one repository, "vuln-rescan:<repository id>" (R-VULN-011).
func RescanKey(repoID int64) string { return KindRescan + ":" + strconv.FormatInt(repoID, 10) }

// RescanKindSpec is the spec of the kind KindRescan without its handler: queue default, exclusive. The application registers
// the kind with it plus the handler (internal/app/wire_vuln_eval.go), and a replay handler that starts a rescan in its own
// transaction passes it to [jobs.EnqueueTx], so the two rows are one (R-HA-054, Q-HA-180).
func RescanKindSpec() jobs.KindSpec {
	return jobs.KindSpec{Queue: QueueDefault, DedupMode: jobs.Exclusive}
}

// The tasks (R-OPS-019) with their default schedules (R-VULN-004, R-VULN-014, R-VULN-025; cron, UTC). A task has the name
// of its kind. maintenance.tasks.<name> overrides the schedule; the configuration's defaults (internal/config) hold
// the same values, and a test holds the two equal.
const (
	TaskSyncOSV           = KindSyncOSV
	TaskSyncOSVFull       = KindSyncOSVFull
	TaskSyncKEV           = KindSyncKEV
	TaskFindingsRetention = KindFindingsRetention

	DefaultSyncOSVSchedule           = "*/15 * * * *"
	DefaultSyncOSVFullSchedule       = "17 3 * * 0"
	DefaultSyncKEVSchedule           = "41 */6 * * *"
	DefaultFindingsRetentionSchedule = "50 3 * * *"
)

// TaskDefault is the default of one scheduled task.
type TaskDefault struct {
	Name     string
	Schedule string
	// DedupKey is the key the task's job is enqueued under; empty for the task's own name.
	DedupKey string
}

// TaskDefaults lists the four tasks of R-VULN-025 in the order of the configuration.
func TaskDefaults() []TaskDefault {
	return []TaskDefault{
		{TaskSyncOSV, DefaultSyncOSVSchedule, DedupSyncOSV},
		{TaskSyncOSVFull, DefaultSyncOSVFullSchedule, DedupSyncOSV},
		{TaskSyncKEV, DefaultSyncKEVSchedule, ""},
		{TaskFindingsRetention, DefaultFindingsRetentionSchedule, ""},
	}
}

// SyncArgs are the parameters of a manual run of vuln-sync-osv and vuln-sync-osv-full, the body of
// POST /api/v1/system/tasks/{name}/run (R-VULN-004): {"acceptShrink": true} overrides the shrink guard once, and the
// override is recorded in the run's audit event.
type SyncArgs struct {
	AcceptShrink bool `json:"acceptShrink"`
}

// RescanArgs are the arguments of a vuln-rescan job (R-VULN-011): the repository by key and numeric ID. The arguments only
// name the repository; the inputs of the rescan are read from the repository snapshot when the job starts and again when it
// ends, because a coalescing enqueue that finds a running job drops the arguments of its own (Q-VULN-18). The API's rescan
// operation enqueues it with this type, under the dedup key RescanKey.
type RescanArgs struct {
	Repository   string `json:"repository"`
	RepositoryID int64  `json:"repositoryId"`
}
