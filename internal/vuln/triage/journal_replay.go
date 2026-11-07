package triage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
)

// The replay of the download blocks of the revocation journal (R-HA-054, R-VULN-013, Q-VULN-48, H-489). A change that sets a
// block carries the entry {finding, repository, ecosystem, package, version, advisory, block: "set"}: a triage that blocks a
// confirmed finding (record in triage.go), and an evaluation that reopens a malicious finding whose policy blocks it
// (internal/vuln/evaluate). A restore to t brings the finding back in its status at t; the replay sets the block again.
//
//   - The entry names the finding by its ID and is acted on only when the restored finding has the entry's ecosystem, package
//     and version, which never change for a finding; the repository's name and the advisory are shown only (a move renames
//     the one, a group's canonical record can move the other) (Q-VULN-115).
//   - A finding that is blocked is "already in effect". In every other status the block is set again: a status without a block
//     is the finding before the change the entry records, or a lift after it, which is not journaled because it widens access,
//     so the replay cannot tell the two apart and keeps the block (Q-VULN-114). A missing finding is "subject unknown".
//   - The write is the finding's own: status blocked, the revision raised, a history row with no principal and the entry's ID.
//     It writes no audit event (Q-AUTHZ-232); a download reads the status at its next request on every replica (R-VULN-027).

func init() {
	journal.Register(journal.Handler{
		Domain: "vuln", Events: []string{audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened}, Apply: replayBlock,
	})
}

// blockEntry is the journal entry of a change that set a block.
type blockEntry struct {
	Finding    string `json:"finding"`
	Repository string `json:"repository"`
	Ecosystem  string `json:"ecosystem"`
	Package    string `json:"package"`
	Version    string `json:"version"`
	Advisory   string `json:"advisory"`
	Block      string `json:"block"`
}

// readBlockEntry decodes an entry; ok is false for one that names no finding or records no block that was set.
func readBlockEntry(o journal.Object) (blockEntry, bool) {
	var e blockEntry
	if !o.Into(&e) {
		return e, false
	}
	return e, journal.IsUUID(e.Finding) && e.Block == BlockSet && e.Ecosystem != "" && e.Package != "" && e.Version != ""
}

// blockOutcome is what replaying a block does to a restored finding of the status.
func blockOutcome(status vuln.Status) journal.Outcome {
	if status == vuln.StatusBlocked {
		return journal.AlreadyInEffect
	}
	return journal.Applied
}

var errNotPostgres = errors.New("triage: the transaction does not come from the PostgreSQL store")

func replayBlock(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
	e, ok := readBlockEntry(o)
	if !ok {
		return o.Malformed("the entry names no finding and no block it set"), nil
	}
	subject := e.Package + "@" + e.Version
	if e.Repository != "" {
		subject = e.Repository + " " + subject
	}
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return journal.Result{}, errNotPostgres
	}
	qs := q.New(ptx)
	rows, err := qs.JournalVulnFindingLock(ctx, e.Finding)
	if err != nil {
		return journal.Result{}, err
	}
	if len(rows) == 0 {
		return o.Result(journal.SubjectUnknown, subject), nil
	}
	f := rows[0]
	if f.Ecosystem != e.Ecosystem || f.PackageName != e.Package || f.Version != e.Version {
		res := o.Result(journal.SubjectUnknown, subject)
		res.Detail, _ = json.Marshal(map[string]string{"reason": "the restored finding of this ID is another package version"})
		return res, nil
	}
	outcome := blockOutcome(vuln.Status(f.Status))
	if outcome != journal.Applied {
		return o.Result(outcome, subject), nil
	}
	if _, err := qs.JournalVulnFindingBlock(ctx, e.Finding); err != nil {
		return journal.Result{}, err
	}
	details, _ := json.Marshal(map[string]string{"journal": o.ID})
	if err := qs.VulnFindingEventInsert(ctx, q.VulnFindingEventInsertParams{
		FindingID: e.Finding, FromStatus: f.Status, ToStatus: string(vuln.StatusBlocked), Details: string(details),
	}); err != nil {
		return journal.Result{}, err
	}
	res := o.Result(journal.Applied, subject)
	res.Detail, _ = json.Marshal(map[string]string{"from": f.Status})
	return res, nil
}
