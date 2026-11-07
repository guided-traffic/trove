package gauges

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/journal"
)

// Reuse is how long a replica keeps the values of an evaluation before it reads PostgreSQL again (R-BAK-014): the database
// load of the gauges grows with the replica count only by one evaluation per replica and interval.
const Reuse = 30 * time.Second

// Collector sets the gauges of the backup job and of the revocation journal from PostgreSQL (R-BAK-014, R-HA-054, R-OPS-007).
// It is the Collect function of a database gauge source: the values live in the families of backup.Metrics and
// journal.Metrics, which are registered on the registry, so a scrape reads memory and never the database.
type Collector struct {
	reader   backup.RunReader
	bak      *backup.Metrics
	journal  *journal.Metrics
	expected time.Duration
	clock    func() time.Time

	mu   sync.Mutex
	last time.Time
}

// Options configure a Collector.
type Options struct {
	Reader backup.RunReader
	// Backup and Journal are the families to set; a nil member is not set.
	Backup  *backup.Metrics
	Journal *journal.Metrics
	// ExpectedInterval is backup.expected_interval of this replica's configuration.
	ExpectedInterval time.Duration
	// Clock is the replica's clock for the reuse window; time.Now when nil.
	Clock func() time.Time
}

// New builds a Collector and sets the gauge that comes from the configuration, so it is present before the first evaluation.
func New(o Options) *Collector {
	c := &Collector{reader: o.Reader, bak: o.Backup, journal: o.Journal, expected: o.ExpectedInterval, clock: o.Clock}
	if c.clock == nil {
		c.clock = time.Now
	}
	if c.bak != nil {
		c.bak.ExpectedInterval.Set(c.expected.Seconds())
	}
	return c
}

// Refresh evaluates the gauges unless the previous successful evaluation is younger than [Reuse]. A failed evaluation keeps
// the previous values and is tried again at the next call.
func (c *Collector) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.clock(); !c.last.IsZero() && now.Sub(c.last) < Reuse {
		return nil
	}
	if err := c.evaluate(ctx); err != nil {
		return err
	}
	c.last = c.clock()
	return nil
}

func (c *Collector) evaluate(ctx context.Context) error {
	jobs, err := ReadJobs(ctx, c.reader)
	if err != nil {
		return err
	}
	verify, hasVerify, err := c.reader.Last(ctx, backup.OpVerify, backup.StateCompleted, backup.StateCompletedWithWarning, backup.StateFailed)
	if err != nil {
		return err
	}
	allHeld, err := c.reader.HoldValid(ctx, backup.HoldAll)
	if err != nil {
		return err
	}
	pinnedHeld, err := c.reader.HoldValid(ctx, backup.HoldPinned)
	if err != nil {
		return err
	}
	var pending int64
	if c.journal != nil {
		if pending, _, err = c.reader.JournalPending(ctx); err != nil {
			return err
		}
		c.journal.Pending.Set(float64(pending))
	}
	if c.bak != nil {
		c.setBackup(jobs, verify, hasVerify, allHeld, pinnedHeld)
	}
	return nil
}

func (c *Collector) setBackup(jobs Jobs, verify backup.Run, hasVerify, allHeld, pinnedHeld bool) {
	m := c.bak
	m.ExpectedInterval.Set(c.expected.Seconds())

	// The last-success and last-failure families have no series until the event happened (Q-OPS-180).
	m.LastSuccess.Reset()
	m.LastFailure.Reset()
	var res backup.Result
	if s := jobs.LastSuccess; s != nil && s.FinishedAt != nil {
		m.LastSuccess.WithLabelValues().Set(unix(*s.FinishedAt))
		m.LastDuration.Set(s.FinishedAt.Sub(s.StartedAt).Seconds())
		_ = json.Unmarshal(s.Result, &res)
	} else {
		m.LastDuration.Set(0)
	}
	m.LastDatabase.Set(float64(res.DatabaseBytes))
	m.LastBlob.Set(float64(res.BlobBytes))
	m.LastCopied.Set(float64(res.BytesCopied))
	m.SourceDamaged.Set(float64(res.DamagedBlobs))
	if f := jobs.Failure; f != nil {
		m.LastFailure.WithLabelValues().Set(unix(f.At))
	}

	// The run that holds the slot with a valid lease; a run with an expired lease is dead and counted as a failure above.
	m.Running.Reset()
	var running *backup.Run
	for i := range jobs.Active {
		a := &jobs.Active[i]
		if a.Operation != backup.OpVerify && !a.LeaseExpired(jobs.Now) {
			running = a
			break
		}
	}
	var runningSeconds, snapshotOpen, pins float64
	if running != nil {
		m.Running.WithLabelValues(string(running.Operation), string(running.State)).Set(1)
		runningSeconds = max(jobs.Now.Sub(running.StartedAt).Seconds(), 0)
		if since := running.Progress.SnapshotOpenSince; since != nil {
			snapshotOpen = max(jobs.Now.Sub(*since).Seconds(), 0)
		}
		pins = float64(running.Progress.PinsLeft)
	}
	m.RunningSeconds.Set(runningSeconds)
	m.SnapshotOpen.Set(snapshotOpen)
	m.PinnedBlobs.Set(pins)
	m.GCHold.WithLabelValues(string(backup.HoldAll)).Set(boolValue(allHeld))
	m.GCHold.WithLabelValues(string(backup.HoldPinned)).Set(boolValue(pinnedHeld))

	var verified, findings float64
	if hasVerify && verify.FinishedAt != nil {
		verified = unix(*verify.FinishedAt)
		findings = float64(verifyFindings(verify.Result))
	}
	m.LastVerify.Set(verified)
	m.LastVerifyFindings.Set(findings)
}

// verifyFindings counts the findings a verify run stored: the findings of its backup.VerifyReport.
func verifyFindings(raw json.RawMessage) int {
	var r struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil {
		return 0
	}
	return len(r.Findings)
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
