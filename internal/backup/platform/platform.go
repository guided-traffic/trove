// Package platform verifies the platform protection that R-HA-023 declares: the point-in-time recovery of PostgreSQL and the
// versioning of the blob bucket. It reports, and changes nothing. The task backup-check runs it daily and stores the result;
// every replica exports it as the gauge trove_backup_check_ok{check}, which the alert TroveBackupCheckFailed reads. `trove
// admin backup check` runs the same checks on demand.
//
// A check exists only for what the operator declared: with backup.recovery_window and backup.object_versioning at their
// defaults nothing is checked and the gauge has no series, so an installation that relies on trove's own backup alone is not
// alerted for the absence of a protection it never claimed. A setting the credential may not read is unknown and has no
// series either ("otherwise unknown", R-HA-023); it is neither a pass nor an alarm.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The checks, the values of the label check.
const (
	CheckArchiveMode      = "archive_mode"
	CheckObjectVersioning = "object_versioning"
	CheckNoncurrentExpiry = "noncurrent_expiry"
	CheckGracePeriod      = "grace_period"
)

// StateKey is the system_state key that holds the newest result, so that every replica exports it, not only the worker that
// ran the task.
const StateKey = "backup_check"

// Status of a check.
type Status string

// The statuses.
const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusUnknown Status = "unknown"
)

// Result is the outcome of one check.
type Result struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Report is what one run found, and when.
type Report struct {
	At      time.Time `json:"at"`
	Results []Result  `json:"results"`
}

// Settings are the declarations the checks hold the platform to.
type Settings struct {
	// RecoveryWindow is backup.recovery_window; 0 is undeclared.
	RecoveryWindow time.Duration
	// ObjectVersioning is backup.object_versioning.
	ObjectVersioning bool
	// GracePeriod is storage.gc.grace_period.
	GracePeriod time.Duration
}

// Bucket reads the protection settings of the blob bucket. *s3.Client implements it.
type Bucket interface {
	VersioningStatus(ctx context.Context) (string, error)
	NoncurrentExpiryDays(ctx context.Context) (days int, limited bool, err error)
}

// Checker runs the checks.
type Checker struct {
	// ArchiveMode returns SHOW archive_mode; ArchiveModeOf builds it over a connection.
	ArchiveMode func(ctx context.Context) (string, error)
	Bucket      Bucket
	Settings    Settings
	Now         func() time.Time
}

// Run runs the checks of the declared protection. A failure to read a setting that is not "may not read" is an error: the
// run did not complete and stores nothing.
func (c Checker) Run(ctx context.Context) (Report, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	s := c.Settings
	rep := Report{At: now().UTC(), Results: []Result{}}
	add := func(check string, st Status, format string, a ...any) {
		rep.Results = append(rep.Results, Result{Check: check, Status: st, Detail: fmt.Sprintf(format, a...)})
	}

	if s.RecoveryWindow > 0 {
		mode, err := c.ArchiveMode(ctx)
		if err != nil {
			return Report{}, fmt.Errorf("SHOW archive_mode: %w", err)
		}
		if m := strings.ToLower(mode); m == "on" || m == "always" {
			add(CheckArchiveMode, StatusOK, "archive_mode is %s", mode)
		} else {
			add(CheckArchiveMode, StatusFailed, "archive_mode is %s: PostgreSQL archives no WAL, so a restore to a point in time within the recovery window %s is not possible", mode, s.RecoveryWindow)
		}
		if s.ObjectVersioning || s.GracePeriod >= s.RecoveryWindow {
			add(CheckGracePeriod, StatusOK, "storage.gc.grace_period %s, recovery window %s, object_versioning %t", s.GracePeriod, s.RecoveryWindow, s.ObjectVersioning)
		} else {
			add(CheckGracePeriod, StatusFailed, "storage.gc.grace_period %s is shorter than the recovery window %s and object_versioning is off: a restored database could reference a deleted object (R-HA-024)", s.GracePeriod, s.RecoveryWindow)
		}
	}

	if s.ObjectVersioning {
		status, err := c.Bucket.VersioningStatus(ctx)
		switch {
		case errors.Is(err, s3.ErrProtectionUnknown):
			add(CheckObjectVersioning, StatusUnknown, "the credential may not read the bucket's versioning")
		case err != nil:
			return Report{}, err
		case status == "Enabled":
			add(CheckObjectVersioning, StatusOK, "bucket versioning is Enabled")
		default:
			if status == "" {
				status = "not enabled"
			}
			add(CheckObjectVersioning, StatusFailed, "bucket versioning is %s, but backup.object_versioning asserts it", status)
		}
		if s.RecoveryWindow > 0 {
			days, limited, err := c.Bucket.NoncurrentExpiryDays(ctx)
			switch {
			case errors.Is(err, s3.ErrProtectionUnknown):
				add(CheckNoncurrentExpiry, StatusUnknown, "the credential may not read the bucket's lifecycle rules")
			case err != nil:
				return Report{}, err
			case !limited:
				add(CheckNoncurrentExpiry, StatusOK, "no lifecycle rule expires noncurrent versions")
			case time.Duration(days)*24*time.Hour >= s.RecoveryWindow:
				add(CheckNoncurrentExpiry, StatusOK, "noncurrent versions expire after %d days, the recovery window is %s", days, s.RecoveryWindow)
			default:
				add(CheckNoncurrentExpiry, StatusFailed, "noncurrent versions expire after %d days, before the recovery window of %s ends", days, s.RecoveryWindow)
			}
		}
	}
	return rep, nil
}

// ArchiveModeOf reads archive_mode over a database connection.
func ArchiveModeOf(db q.DBTX) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var mode string
		err := db.QueryRow(ctx, "SELECT current_setting('archive_mode')").Scan(&mode)
		return mode, err
	}
}

// Store writes the report to system_state.
func Store(ctx context.Context, db q.DBTX, rep Report) error {
	raw, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return q.New(db).UpsertSystemState(ctx, q.UpsertSystemStateParams{Key: StateKey, Value: raw})
}

// Load reads the stored report; ok is false when no run has stored one.
func Load(ctx context.Context, db q.DBTX) (rep Report, ok bool, err error) {
	row, err := q.New(db).GetSystemState(ctx, StateKey)
	if postgres.IsNoRows(err) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, err
	}
	if err := json.Unmarshal(row.Value, &rep); err != nil {
		return Report{}, false, fmt.Errorf("system_state %s is not a check report: %w", StateKey, err)
	}
	return rep, true, nil
}

// Export sets trove_backup_check_ok from a report: 1 for ok, 0 for failed, and no series for unknown or undeclared. It first
// removes every series, so a check that is no longer declared leaves the gauge.
func Export(g *prometheus.GaugeVec, rep Report) {
	g.Reset()
	for _, r := range rep.Results {
		switch r.Status {
		case StatusOK:
			g.WithLabelValues(r.Check).Set(1)
		case StatusFailed:
			g.WithLabelValues(r.Check).Set(0)
		case StatusUnknown:
		}
	}
}
