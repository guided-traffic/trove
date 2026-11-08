package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// AdminBindingCheck reports whether any admin role binding exists. The replay prints a warning when none is left
// (R-HA-054): the break-glass account is the way back in.
type AdminBindingCheck func(ctx context.Context, tx meta.Tx) (bool, error)

// Options configure [New].
type Options struct {
	// DB runs one transaction per entry and the one that records the summary.
	DB     meta.Runner
	Reader journal.Reader
	// Recorder writes system.restore.journal_replayed. Nil writes none (a test of the handlers alone).
	Recorder audit.Recorder
	// Notifier is handed to the handlers (journal.Env.Notifier); nil is the restore without Valkey and change log.
	Notifier journal.Notifier
	// AdminBinding fills Summary.NoAdminBinding; nil leaves it false.
	AdminBinding AdminBindingCheck
	// Newest returns the time of the newest audit event of the restored database; ok is false when there is none or the
	// database cannot say. The replay starts at the earlier of that time and the restore point given by the operator, so a
	// restore point that lies later than the database does not skip the revocations in between (audit finding L2). Nil
	// reads audit_event.
	Newest func(ctx context.Context) (t time.Time, ok bool, err error)
	Log    *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Replayer is the [journal.Replayer] over the registry of [journal.Handler]s.
type Replayer struct{ o Options }

var _ journal.Replayer = (*Replayer)(nil)

// New builds a replayer.
func New(o Options) *Replayer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Replayer{o: o}
}

// Replay lists the entries with at > RestoredTo - Margin, sorts them by at and id and applies each to the state its
// predecessors left, every entry in a transaction of its own (R-HA-054 "Replay run"). It fails closed:
//
//   - an entry whose event has no handler, an object that cannot be read and a handler that panics are reported
//     [journal.Skipped] and change nothing;
//   - an entry whose value the database refuses (SQLSTATE class 22 or 54, or not_null or check_violation) is rolled back and reported [journal.Skipped]
//     with its subject, and the replay goes on;
//   - a handler that returns an error (the database failed) ends the replay with that error; the entries applied so far
//     stay, and running the replay again reports them as already in effect;
//   - a journal that cannot be listed ends the replay with an error, because an incomplete journal is not a journal.
//
// It prints one line per entry to opts.Out and writes system.restore.journal_replayed with the counts.
func (r *Replayer) Replay(ctx context.Context, opts journal.ReplayOptions) (*journal.Summary, error) {
	from := opts.RestoredTo
	if newest, ok, err := r.newest(ctx); err != nil {
		return nil, err
	} else if ok && newest.Before(from) {
		from = newest
		line(opts, fmt.Sprintf("the newest audit event of the database (%s) is older than the restore point (%s): replaying from it",
			newest.UTC().Format(time.RFC3339), opts.RestoredTo.UTC().Format(time.RFC3339)))
	}
	entries, unreadable, err := r.o.Reader.Entries(ctx, from.Add(-opts.Margin))
	if err != nil {
		return nil, err
	}
	sum := &journal.Summary{Counts: map[journal.Outcome]int{}}
	for _, u := range unreadable {
		sum.Unreadable = append(sum.Unreadable, u)
		sum.Counts[journal.Skipped]++
		line(opts, fmt.Sprintf("unreadable %q skipped: %s", u.Key, u.Reason))
	}
	env := journal.Env{Now: r.o.Now(), RestoredTo: opts.RestoredTo, Notifier: r.o.Notifier}
	for _, o := range entries {
		if o.Warning != "" {
			sum.Warnings = append(sum.Warnings, o.ID+": "+o.Warning)
			line(opts, "warning: "+strconv.Quote(o.Event)+" "+o.ID+": "+o.Warning)
		}
		res, err := r.apply(ctx, env, o)
		if err != nil {
			return sum, fmt.Errorf("journal: replaying %s %s: %w", strconv.Quote(o.Event), o.ID, err)
		}
		sum.Add(res)
		for _, r := range res.Flat() {
			line(opts, r.Line())
			if r.Note != "" {
				sum.Warnings = append(sum.Warnings, r.ID+": "+r.Note)
				line(opts, "  warning: "+r.Note)
			}
			if len(r.Detail) > 0 && (r.Outcome == journal.Suspended || r.Outcome == journal.Revived) {
				line(opts, "  "+string(r.Detail))
			}
		}
	}
	if err := r.finish(ctx, sum); err != nil {
		return sum, err
	}
	if sum.NoAdminBinding {
		line(opts, "no enabled administrator is left (a service account that holds the role counts as one, though nobody can sign in as it): sign in with the break-glass account (R-AUTH-014), then bind the admin role again or re-attach or fill the group that holds it")
	}
	return sum, nil
}

// newest returns the time of the newest audit event of the restored database.
func (r *Replayer) newest(ctx context.Context) (time.Time, bool, error) {
	if r.o.Newest != nil {
		return r.o.Newest(ctx)
	}
	var us int64
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return nil // not a PostgreSQL transaction (a test of the handlers): nothing to read
		}
		var err error
		us, err = q.New(ptx).JournalNewestAuditEvent(ctx)
		return err
	})
	if err != nil || us <= 0 {
		return time.Time{}, false, err
	}
	return time.UnixMicro(us).UTC(), true, nil
}

// apply runs the handler of one entry in its own transaction. A panic in a handler is an entry that cannot be replayed,
// not a restore that cannot finish: it becomes a skipped result and its transaction is rolled back.
func (r *Replayer) apply(ctx context.Context, env journal.Env, o journal.Object) (res journal.Result, err error) {
	h, ok := journal.HandlerFor(o.Event)
	if !ok {
		out := o.Result(journal.Skipped, "")
		out.Detail, _ = json.Marshal(map[string]string{"reason": "no replay handler for this event"})
		return out, nil
	}
	err = r.o.DB.InTx(ctx, func(tx meta.Tx) (txErr error) {
		defer func() {
			if p := recover(); p != nil {
				r.o.Log.Error("journal handler panicked", "event", o.Event, "id", o.ID, "domain", h.Domain, "panic", fmt.Sprint(p))
				res = o.Malformed(fmt.Sprintf("handler failed: %v", p))
				txErr = errRollback
			}
		}()
		res, txErr = h.Apply(ctx, tx, env, o)
		return txErr
	})
	if errors.Is(err, errRollback) {
		return res, nil
	}
	if code, bad := postgres.ForgedValueErrorCode(err); bad {
		// The entry holds a value the database refuses (a NUL byte, a text too long, a constraint): a forged or damaged
		// entry, not a database that failed. It rolled back with its transaction and must not stop the entries after it.
		r.o.Log.Error("journal entry refused by the database", "event", o.Event, "id", o.ID, "domain", h.Domain, "sqlstate", code)
		out := o.Malformed("the database refused a value of the entry (SQLSTATE " + code + ")")
		out.Subject = entrySubject(o)
		return out, nil
	}
	if err != nil {
		return journal.Result{}, err
	}
	res.ID, res.Event = o.ID, o.Event
	return res, nil
}

// entrySubject names what an entry is about for the line a skipped entry prints: the display member `key` or `name` of the
// entry, which the producers write. It is the entry's own claim and is printed quoted.
func entrySubject(o journal.Object) string {
	var e struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if !o.Into(&e) {
		return ""
	}
	if e.Key != "" {
		return e.Key
	}
	return e.Name
}

var errRollback = fmt.Errorf("journal: entry rolled back")

// finish checks the admin binding and writes the summary event in one transaction.
func (r *Replayer) finish(ctx context.Context, sum *journal.Summary) error {
	if r.o.AdminBinding == nil && r.o.Recorder == nil {
		return nil
	}
	return r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		if r.o.AdminBinding != nil {
			has, err := r.o.AdminBinding(ctx, tx)
			if err != nil {
				return err
			}
			sum.NoAdminBinding = !has
		}
		if r.o.Recorder == nil {
			return nil
		}
		c := sum.Counts
		return r.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventJournalReplayed, Outcome: audit.Success,
			Actor: audit.Actor{Kind: "system", Name: "restore"},
			Details: mustJSON(map[string]int{
				"applied": c[journal.Applied], "alreadyInEffect": c[journal.AlreadyInEffect], "subjectUnknown": c[journal.SubjectUnknown],
				"skipped": c[journal.Skipped], "suspended": c[journal.Suspended], "revived": c[journal.Revived],
				"unreadable": len(sum.Unreadable),
			}),
		})
	})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func line(opts journal.ReplayOptions, s string) {
	if opts.Out != nil {
		_, _ = fmt.Fprintln(opts.Out, s)
	}
}
