package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// Outcome is what a replay did with one entry (R-HA-054 "Replay run").
type Outcome string

// The outcomes.
const (
	// Applied: the recorded removal or reservation was applied.
	Applied Outcome = "applied"
	// AlreadyInEffect: the subject was already in the recorded state.
	AlreadyInEffect Outcome = "already in effect"
	// SubjectUnknown: the subject is missing from the restored database, or differs from what the entry names.
	SubjectUnknown Outcome = "subject unknown"
	// Skipped: the entry is not replayed: access granted after the point in time is not restored, an unknown event, a
	// format without a handler, an unreadable object.
	Skipped Outcome = "skipped"
	// Suspended: a narrowing on top of a broadening that the restored database lacks; replay failed closed.
	Suspended Outcome = "suspended"
	// Revived: a version the journal recorded as removed is present in the restored database; replay leaves it, because it
	// never deletes (R-NUGET-033).
	Revived Outcome = "revived"
)

// Result is the line printed for one entry.
type Result struct {
	ID      string  `json:"id"`
	Event   string  `json:"event"`
	Outcome Outcome `json:"outcome"`
	// Subject says what the entry was about, for display.
	Subject string `json:"subject,omitempty"`
	// Detail adds what an administrator needs: the journaled after-state of a suspended object, the name of a version
	// withheld or revived.
	Detail json.RawMessage `json:"detail,omitempty"`
	// Also are the results of further objects the entry's replay changed, each printed as a line of its own with the
	// entry's ID and event: the later mappings of a tier that a replayed mapping disable suspends (R-CI-020, Q-CI-11).
	// [Summary.Add] records them as results of their own.
	Also []Result `json:"also,omitempty"`
}

// Notifier records a change of a change area in the transaction that makes it. *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Env is what a handler may depend on.
type Env struct {
	// Now is the replay's clock.
	Now time.Time
	// RestoredTo is the point in time of the restore (the backup's snapshotAt, or t of a point-in-time restore).
	RestoredTo time.Time
	// Notifier writes the change-log rows of the areas a handler changed, so replicas that start afterwards (or a
	// replay run beside a running installation) see them. It may be nil: a restore holds no Valkey credentials and no
	// replica serves, so nothing is lost by leaving it out, and Notify then does nothing (R-AUTHZ-031).
	Notifier Notifier
}

// Notify forwards to the notifier; without one it does nothing. Env itself therefore satisfies the Notifier interfaces
// of the domain packages.
func (e Env) Notify(tx meta.Tx, area string, version int, payload any) error {
	if e.Notifier == nil {
		return nil
	}
	return e.Notifier.Notify(tx, area, version, payload)
}

// Handler replays the entries of one or more events. A domain registers one with [Register] in a file of its own.
//
// Apply runs in its own transaction on the restored database, as the owner credential, while no web or worker replica
// serves. It must be idempotent: a resumed restore replays again and expects AlreadyInEffect. It never grants access and
// never deletes content (R-HA-054). The writes a domain's own services make (change areas, audit events) are made through
// the same stores the running system uses, but it publishes no Valkey message: the restore holds no Valkey credentials
// (R-AUTHZ-031).
type Handler struct {
	// Events are the registry names of the events the handler replays.
	Events []string
	// Domain names the owner for the log: identity, authz, ci, exposure, npm, nuget, vuln.
	Domain string
	Apply  func(ctx context.Context, tx meta.Tx, env Env, o Object) (Result, error)
}

var reg = struct {
	mu     sync.RWMutex
	by     map[string]Handler
	frozen bool
}{by: map[string]Handler{}}

// Register adds a handler. It panics on an event registered twice, a handler without events or function, or a registration
// after [Freeze]: all are programming errors found at start-up.
func Register(h Handler) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if len(h.Events) == 0 || h.Apply == nil || h.Domain == "" {
		panic("journal: Register needs a domain, events and an Apply function")
	}
	if reg.frozen {
		panic(fmt.Sprintf("journal: Register(%s) after the registry was frozen", h.Domain))
	}
	for _, e := range h.Events {
		if _, dup := reg.by[e]; dup {
			panic(fmt.Sprintf("journal: event %q has two replay handlers", e))
		}
		reg.by[e] = h
	}
}

// Freeze ends registration; the composition root calls it before a command runs.
func Freeze() {
	reg.mu.Lock()
	reg.frozen = true
	reg.mu.Unlock()
}

// HandlerFor returns the handler of an event.
func HandlerFor(event string) (Handler, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	h, ok := reg.by[event]
	return h, ok
}

// Events lists the events that have a handler, sorted.
func Events() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]string, 0, len(reg.by))
	for e := range reg.by {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// Narrower is the comparator narrows(current, after) of R-HA-054's update rule, written by the page that owns the object
// (R-AUTHZ-031 for policies, memberships and repository settings; R-EP-020 for exposure rules; R-CI-020 for issuers and
// mappings). Narrows answers true only when after grants nothing that current does not, and false whenever it cannot tell.
// An emptied include list, for example, is unrestricted and never narrows.
type Narrower[T any] interface {
	Narrows(current, after T) bool
}

// UpdateActions are the writes of an update entry: Apply sets the access-bearing fields to the journaled after-state
// (never identifying fields), Suspend takes the object to its most restrictive state and returns what to print so that an
// administrator can review and re-apply the after-state.
type UpdateActions struct {
	Apply   func() error
	Suspend func() (json.RawMessage, error)
}

// ApplyUpdate is the update rule of R-HA-054, shared by every domain that journals updates:
//
//  1. if cmp.Narrows(current, after), the access-bearing fields are set to the after-state: Applied;
//  2. otherwise, if the journaled before-state is contained in the after-state (cmp.Narrows(after, before): the change only
//     added access), the entry is Skipped, because access granted after the point in time is not restored;
//  3. otherwise the object is suspended: Suspended, with the after-state in the detail.
//
// Rule 3 is a narrowing on top of a broadening that the restored database lacks; applying it to the older state could keep
// access the narrowing removed, so replay fails closed. A forged before-state can only choose between rules 2 and 3.
func ApplyUpdate[T any](cmp Narrower[T], current, before, after T, act UpdateActions) (Outcome, json.RawMessage, error) {
	switch {
	case cmp.Narrows(current, after):
		return Applied, nil, act.Apply()
	case cmp.Narrows(after, before):
		return Skipped, nil, nil
	}
	detail, err := act.Suspend()
	return Suspended, detail, err
}

// Summary is the outcome of a replay.
type Summary struct {
	Counts  map[Outcome]int `json:"counts"`
	Results []Result        `json:"results"`
	// Unreadable lists the objects that could not be decoded.
	Unreadable []Unreadable `json:"unreadable,omitempty"`
	// NoAdminBinding is true when the replay left no admin role binding: the break-glass account is the way back in.
	NoAdminBinding bool `json:"noAdminBinding,omitempty"`
}

// Add counts and records a result.
func (s *Summary) Add(r Result) {
	if s.Counts == nil {
		s.Counts = map[Outcome]int{}
	}
	also := r.Also
	r.Also = nil
	s.Counts[r.Outcome]++
	s.Results = append(s.Results, r)
	for _, a := range also {
		s.Add(a)
	}
}

// ReplayOptions are the parameters of a replay.
type ReplayOptions struct {
	// RestoredTo is snapshotAt of a backup restore, or t of a point-in-time restore.
	RestoredTo time.Time
	// Margin is backup.revocation_journal.replay_margin: entries with at > RestoredTo - Margin are replayed.
	Margin time.Duration
	// Out receives one line per entry; nil prints nothing.
	Out io.Writer
}

// Replayer reads the journal and applies the handlers in order. The restore (R-BAK-024) and `trove admin post-restore`
// (R-HA-025) both run it; the implementation is internal/journal/replay's work package.
type Replayer interface {
	Replay(ctx context.Context, opts ReplayOptions) (*Summary, error)
}

// Line formats a result as the line a replay prints.
func (r Result) Line() string {
	s := fmt.Sprintf("%s %s %s", r.Event, r.ID, r.Outcome)
	if r.Subject != "" {
		s = fmt.Sprintf("%s %s %s %s", r.Event, r.Subject, r.ID, r.Outcome)
	}
	return s
}

// Flat is the result followed by its further results (Also), each without further results: the results a replay prints
// for one entry, in order.
func (r Result) Flat() []Result {
	also := r.Also
	r.Also = nil
	out := []Result{r}
	for _, a := range also {
		out = append(out, a.Flat()...)
	}
	return out
}
