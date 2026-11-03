package store

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Database is what the operations reads need of the PostgreSQL store: a retrying read. *postgres.DB implements it.
type Database interface {
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// SourceState is the row of one source and ecosystem of vuln_source_state as the gauges and the system information read it
// (R-VULN-024). The KEV source has the ecosystem "".
type SourceState struct {
	Source, Ecosystem string
	// State is "empty" or "complete".
	State string
	// SyncedAt is the end of the last run that ended changed or unchanged; the zero time when there was none.
	SyncedAt time.Time
	// Records is the stored record count; Failures the consecutive failed runs.
	Records, Failures int64
}

// SourceStates reads every row of vuln_source_state, ordered by source and ecosystem.
func SourceStates(ctx context.Context, db Database) ([]SourceState, error) {
	var out []SourceState
	err := db.Read(ctx, func(c q.DBTX) error {
		rows, err := q.New(c).VulnOpsSourceStates(ctx)
		out = make([]SourceState, 0, len(rows))
		for _, r := range rows {
			s := SourceState{Source: r.Source, Ecosystem: r.Ecosystem, State: r.State, Records: r.Records, Failures: r.Failures}
			if r.HasSynced {
				s.SyncedAt = time.UnixMicro(int64(r.SyncedEpoch * 1e6)).UTC()
			}
			out = append(out, s)
		}
		return err
	})
	return out, err
}

// FindingCount is the number of findings of one severity, status and kind.
type FindingCount struct {
	Severity, Status, Kind string
	N                      int64
}

// FindingCounts counts the findings by severity, status and kind (every status, resolved included).
func FindingCounts(ctx context.Context, db Database) ([]FindingCount, error) {
	var out []FindingCount
	err := db.Read(ctx, func(c q.DBTX) error {
		rows, err := q.New(c).VulnOpsFindingCounts(ctx)
		out = make([]FindingCount, 0, len(rows))
		for _, r := range rows {
			out = append(out, FindingCount{Severity: r.Severity, Status: r.Status, Kind: r.Kind, N: r.N})
		}
		return err
	})
	return out, err
}

// ClaimedMatchCount is the number of claimed-name matches of one ecosystem and kind.
type ClaimedMatchCount struct {
	Ecosystem, Kind string
	N               int64
}

// ClaimedMatchCounts counts the rows of vuln_claimed_match by ecosystem and kind.
func ClaimedMatchCounts(ctx context.Context, db Database) ([]ClaimedMatchCount, error) {
	var out []ClaimedMatchCount
	err := db.Read(ctx, func(c q.DBTX) error {
		rows, err := q.New(c).VulnOpsClaimedMatchCounts(ctx)
		out = make([]ClaimedMatchCount, 0, len(rows))
		for _, r := range rows {
			out = append(out, ClaimedMatchCount{Ecosystem: r.Ecosystem, Kind: r.Kind, N: r.N})
		}
		return err
	})
	return out, err
}
