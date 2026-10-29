package token

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The scheduled authentication tasks that belong to the tokens (R-AUTH-043); they
// run on worker replicas, one job per slot in the cluster.
const (
	// TaskCleanup is the kind and task name of the retention task.
	TaskCleanup = "token-cleanup"
	// TaskExpiryNotice is the kind and task name of the expiry notices.
	TaskExpiryNotice = "token-expiry-notice"
)

// DefaultCleanupSchedule and DefaultExpiryNoticeSchedule are the cron schedules
// (UTC) used when maintenance.tasks does not name the task.
const (
	DefaultCleanupSchedule      = "40 3 * * *"
	DefaultExpiryNoticeSchedule = "0 6 * * *"
)

// taskBatch is the number of rows a task changes per transaction (R-AUTH-043).
const taskBatch = 1000

// Cleanup deletes token rows auth.tokens.retention_after_end after their
// expiry or revocation, in batches of 1,000 (R-AUTH-023 "Retention"). A deleted
// row is an unknown token; the audit trail keeps the history. It is idempotent
// and stops between batches when ctx ends.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	var total int64
	for ctx.Err() == nil {
		var n int64
		err := s.o.DB.Read(ctx, func(db q.DBTX) error {
			var err error
			n, err = q.New(db).IdentityDeleteExpiredTokens(ctx, q.IdentityDeleteExpiredTokensParams{
				RetentionSecs: s.o.Tokens.RetentionAfterEnd.Seconds(), MaxRows: taskBatch,
			})
			return err
		})
		if err != nil {
			return total, principal.DBErr("token.cleanup", err)
		}
		total += n
		if n < taskBatch {
			break
		}
	}
	return total, ctx.Err()
}

// NoticeExpiring writes token.expiring for the active PATs and SATs whose expiry
// falls within the notice period and that have not been announced (R-AUTH-032).
// Each token is one transaction: the conditional update of expiry_notified_at and
// the event, written only when the update changed the row, so two overlapping
// runs write one event.
func (s *Service) NoticeExpiring(ctx context.Context) (int, error) {
	eff, err := s.o.Settings.Effective(ctx)
	if err != nil {
		return 0, err
	}
	notified := 0
	for ctx.Err() == nil {
		var rows []q.IdentityTokensEnteringNoticeRow
		err := s.o.DB.Read(ctx, func(db q.DBTX) error {
			var err error
			rows, err = q.New(db).IdentityTokensEnteringNotice(ctx, q.IdentityTokensEnteringNoticeParams{NoticeSecs: eff.ExpiryNotice.Seconds(), MaxRows: taskBatch})
			return err
		})
		if err != nil {
			return notified, principal.DBErr("token.expiry_notice", err)
		}
		changed := 0
		for _, r := range rows {
			ok, err := s.noticeOne(ctx, r)
			if err != nil {
				return notified, principal.DBErr("token.expiry_notice", err)
			}
			if ok {
				changed++
			}
		}
		notified += changed
		if len(rows) < taskBatch || changed == 0 {
			break
		}
	}
	return notified, ctx.Err()
}

func (s *Service) noticeOne(ctx context.Context, r q.IdentityTokensEnteringNoticeRow) (bool, error) {
	changed := false
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		changed = false
		rows, err := principal.Queries(tx).IdentityMarkExpiryNotified(ctx, r.ID)
		if err != nil || len(rows) == 0 {
			return err
		}
		changed = true
		owner := Owner{ID: r.OwnerID, Kind: principal.KindFromRow(r.OwnerKind, false), Name: r.OwnerName}
		return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventTokenExpiring, Outcome: audit.Success, Actor: audit.Actor{Kind: "system", Name: TaskExpiryNotice},
			Subject: audit.Subject{Kind: "token", ID: r.ID, Name: r.Hint},
			Details: mustJSON(map[string]string{
				"owner": owner.Ref(), "hint": r.Hint, "description": r.Description, "expiresAt": r.ExpiresAt.Time.UTC().Format(time.RFC3339),
			}),
		})
	})
	return changed, err
}
