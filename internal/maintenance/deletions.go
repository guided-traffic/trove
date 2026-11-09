// Package maintenance is the logic of `trove admin maintenance pause-deletions` and `resume-deletions`: the deletion pause of
// R-HA-025, the `system_state` flag that stops garbage collection, the orphan sweep, retention and cap enforcement from deleting
// anything. A post-restore sets it; an operator clears it after verifying the restored installation (step 7), or sets it by hand
// before a planned restore drill.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Pause is the state of the deletion pause.
type Pause struct {
	// Set is true while the pause is set.
	Set bool `json:"set"`
	// At is the time it was set (RFC 3339, UTC) and Reason why; both empty while it is not set.
	At     string `json:"at,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Changed is true when the call changed the state: a pause that was already set, or a resume of a pause that was not set,
	// changes nothing and writes no audit event.
	Changed bool `json:"changed"`
}

// Options configure [New].
type Options struct {
	DB meta.Runner
	// Recorder writes system.deletions_paused and system.deletions_resumed in the transaction of the change.
	Recorder audit.Recorder
}

// Deletions sets and clears the deletion pause.
type Deletions struct{ o Options }

// New builds a Deletions.
func New(o Options) *Deletions { return &Deletions{o: o} }

func actor() audit.Actor { return audit.Actor{Kind: "system", Name: "admin"} }

// Pause sets the pause with the reason. A pause that is already set keeps its time and its reason.
func (d *Deletions) Pause(ctx context.Context, reason string) (Pause, error) {
	if reason == "" {
		return Pause{}, errors.New("a reason is required")
	}
	var out Pause
	err := d.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		n, err := qs.MaintenancePauseDeletions(ctx, reason)
		if err != nil {
			return fmt.Errorf("setting the deletion pause: %w", err)
		}
		st, err := qs.MaintenanceDeletionsState(ctx)
		if err != nil {
			return fmt.Errorf("reading the deletion pause: %w", err)
		}
		out = Pause{Set: st.PausedAt != "", At: st.PausedAt, Reason: st.Reason, Changed: n > 0}
		if n == 0 || d.o.Recorder == nil {
			return nil
		}
		return d.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventDeletionsPaused, Outcome: audit.Success, Actor: actor(), Details: mustJSON(map[string]string{"reason": reason}),
		})
	})
	return out, err
}

// Resume clears the pause and returns the state it had. A pause that is not set stays unset and is not an error: resuming twice
// must be safe in a runbook.
func (d *Deletions) Resume(ctx context.Context) (Pause, error) {
	var out Pause
	err := d.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		st, err := qs.MaintenanceDeletionsState(ctx)
		if err != nil {
			return fmt.Errorf("reading the deletion pause: %w", err)
		}
		n, err := qs.MaintenanceResumeDeletions(ctx)
		if err != nil {
			return fmt.Errorf("clearing the deletion pause: %w", err)
		}
		out = Pause{Set: false, At: st.PausedAt, Reason: st.Reason, Changed: n > 0}
		if n == 0 || d.o.Recorder == nil {
			return nil
		}
		return d.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventDeletionsResumed, Outcome: audit.Success, Actor: actor(),
			Details: mustJSON(map[string]string{"pausedAt": st.PausedAt, "reason": st.Reason}),
		})
	})
	return out, err
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
