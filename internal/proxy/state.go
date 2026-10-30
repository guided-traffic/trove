package proxy

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// CacheState is the name of the registered cache of the per-repository state: the
// circuit of the upstream and the gone packages (R-REPO-029, R-REPO-049).
const CacheState = "proxy_upstream_state"

// statePayloadVersion is the payload version of the change area upstream_state.
const statePayloadVersion = 1

// statePayload names the repository whose state changed; a replica reloads it.
type statePayload struct {
	Repo int64 `json:"r"`
}

// repoState is the immutable state of one repository's upstream as a replica holds
// it: how the circuit stands and which documents carry a gone mark.
type repoState struct {
	failures int
	// hasOpen is open_until being set: the circuit is open, or half open once
	// openUntil has passed.
	hasOpen bool
	// openUntil is the end of the open period on this replica's clock. The database
	// reports the time that is left, so the two clocks never have to agree.
	openUntil time.Time
	// gone maps the path of each document marked gone to its variant.
	gone map[string]string
}

// open reports an open circuit.
func (s *repoState) open(now time.Time) bool { return s.hasOpen && now.Before(s.openUntil) }

// halfOpen reports a circuit whose period has passed without a success: the next
// request that gets the probe slot asks the upstream (R-REPO-029).
func (s *repoState) halfOpen(now time.Time) bool { return s.hasOpen && !now.Before(s.openUntil) }

// stateOf returns the state of a repository, loading it on a miss under the fill
// rule of R-ARCH-018.
func (e *Engine) stateOf(ctx context.Context, rem Remote) (*repoState, error) {
	e.keys.Store(rem.id, rem.Repo.Key)
	st, err := e.state.Load(ctx, rem.id, func(ctx context.Context) (*repoState, error) { return e.loadState(ctx, rem.id) })
	if err != nil {
		return nil, err
	}
	e.publishUp(rem.id, st)
	return st, nil
}

// loadState reads the circuit row and the gone documents of one repository.
func (e *Engine) loadState(ctx context.Context, repoID int64) (*repoState, error) {
	st := &repoState{gone: map[string]string{}}
	var rows []q.ProxyGoneListRow
	var circuit []q.ProxyCircuitLoadRow
	err := e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if circuit, err = qs.ProxyCircuitLoad(ctx, repoID); err != nil {
			return err
		}
		rows, err = qs.ProxyGoneList(ctx, repoID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(circuit) > 0 {
		c := circuit[0]
		st.failures, st.hasOpen = int(c.ConsecutiveFailures), c.HasOpenUntil
		st.openUntil = e.now().Add(time.Duration(c.OpenRemainingUs) * time.Microsecond)
	}
	for _, r := range rows {
		st.gone[r.Path] = r.Variant
	}
	return st, nil
}

// publishUp sets trove_remote_upstream_up from the state.
func (e *Engine) publishUp(repoID int64, st *repoState) {
	key, ok := e.keys.Load(repoID)
	if !ok {
		return
	}
	up := 1.0
	if st.open(e.now()) {
		up = 0
	}
	e.m.upstreamUp.WithLabelValues(key.(string)).Set(up)
}

// onStateChange is the OnChange of the state cache: a replica reloads the
// repository's state when the change names it, and everything after a flush. The
// reload runs in the background so that the poll that delivered the change never
// waits for the database, and it sets the gauge of repositories no request has
// touched since (trove_remote_upstream_up must follow the circuit within the
// change-log bound whether or not traffic arrives).
func (e *Engine) onStateChange(c *cache.Cache[int64, *repoState], _ string, _ int, payload json.RawMessage) {
	var p statePayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Repo == 0 {
		c.Flush(cache.ReasonChange)
		return
	}
	c.Invalidate(p.Repo)
	e.reloadAsync(p.Repo)
}

func (e *Engine) reloadAll() {
	e.keys.Range(func(k, _ any) bool {
		e.reloadAsync(k.(int64))
		return true
	})
}

func (e *Engine) reloadAsync(repoID int64) {
	if _, ok := e.keys.Load(repoID); !ok {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		ctx, cancel := context.WithTimeout(e.base, 10*time.Second)
		defer cancel()
		st, err := e.state.Load(ctx, repoID, func(ctx context.Context) (*repoState, error) { return e.loadState(ctx, repoID) })
		if err != nil {
			e.log.Debug("reloading the upstream state failed; the next request loads it", "repo_id", repoID, "error", err)
			return
		}
		e.publishUp(repoID, st)
	}()
}

// notifyState attaches the change of a repository's state to the transaction
// (R-ARCH-018): every replica reloads it within the change-log bound.
func (e *Engine) notifyState(tx meta.Tx, repoID int64) error {
	if e.o.Changes == nil {
		return nil
	}
	return e.o.Changes.Notify(tx, cluster.AreaUpstreamState, statePayloadVersion, statePayload{Repo: repoID})
}

// admit decides step 7 for a request that would ask the upstream. open is true
// while no request may; probe is true when this call took the replica's one probe
// slot of a half-open circuit and the caller must give it back with the returned
// release.
type admission struct {
	open       bool
	retryAfter time.Duration
	// probe: this request is the probe of a half-open circuit.
	probe   bool
	release func()
}

// circuitPeek answers whether the circuit is open without claiming the probe slot:
// step 7 of R-REPO-025 for a request that may still be answered from the negative
// cache or refused for lack of the populate right.
func (e *Engine) circuitPeek(ctx context.Context, rem Remote) (open bool, retryAfter time.Duration, err error) {
	if rem.Settings.Availability.CircuitOpenSeconds == 0 {
		return false, 0, nil
	}
	st, err := e.stateOf(ctx, rem)
	if err != nil {
		return false, 0, err
	}
	now := e.now()
	switch {
	case st.open(now):
		return true, st.openUntil.Sub(now), nil
	case st.halfOpen(now) && e.probing(rem.id).Load():
		return true, time.Second, nil // another request of this replica is probing
	}
	return false, 0, nil
}

// probing returns the flag of the repository's probe slot.
func (e *Engine) probing(repoID int64) *atomic.Bool {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	f := e.probes[repoID]
	if f == nil {
		f = new(atomic.Bool)
		e.probes[repoID] = f
	}
	return f
}

// circuitAdmit is called right before the upstream request. A closed circuit admits
// it; an open one refuses it; a half-open one admits the first request of this
// replica as the probe and refuses the others until that one ends (R-REPO-029: at
// most one probe per replica and period).
func (e *Engine) circuitAdmit(ctx context.Context, rem Remote) (admission, error) {
	if rem.Settings.Availability.CircuitOpenSeconds == 0 {
		return admission{release: func() {}}, nil
	}
	st, err := e.stateOf(ctx, rem)
	if err != nil {
		return admission{}, err
	}
	now := e.now()
	switch {
	case st.open(now):
		return admission{open: true, retryAfter: st.openUntil.Sub(now)}, nil
	case st.halfOpen(now):
		flag := e.probing(rem.id)
		if !flag.CompareAndSwap(false, true) {
			return admission{open: true, retryAfter: time.Second}, nil
		}
		return admission{probe: true, release: func() { flag.Store(false) }}, nil
	}
	return admission{release: func() {}}, nil
}

// circuitSuccess records a 2xx or a 304: it resets the counter and closes the
// circuit for every replica. The statement writes only when there is something to
// reset, so healthy traffic causes no writes (R-REPO-029).
func (e *Engine) circuitSuccess(ctx context.Context, rem Remote) {
	if rem.Settings.Availability.CircuitOpenSeconds == 0 {
		return
	}
	closed := false
	err := e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		closed = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ProxyCircuitSuccess(ctx, rem.id)
		if err != nil {
			return err
		}
		if len(rows) > 0 && rows[0] {
			closed = true
			return e.notifyState(tx, rem.id)
		}
		return nil
	})
	if err != nil {
		e.log.Warn("recording an upstream success failed", "repo", rem.Repo.Key, "error", err)
		return
	}
	if closed {
		e.state.Set(rem.id, &repoState{gone: e.goneOf(ctx, rem)})
		e.publishUp(rem.id, &repoState{})
	}
}

// goneOf keeps the gone documents of the current state through a circuit change.
func (e *Engine) goneOf(ctx context.Context, rem Remote) map[string]string {
	if st, err := e.stateOf(ctx, rem); err == nil {
		return st.gone
	}
	return map[string]string{}
}

// circuitFailure records a failure that counts toward the circuit (R-REPO-029
// table). retryAfter is the Retry-After of a 429, which opens the circuit at once
// for that long, capped at the period.
func (e *Engine) circuitFailure(ctx context.Context, rem Remote, why string, retryAfter time.Duration) {
	a := rem.Settings.Availability
	if a.CircuitOpenSeconds == 0 {
		return
	}
	openFor := int64(a.CircuitOpenSeconds)
	force := retryAfter > 0
	if force {
		openFor = min(openFor, max(1, int64((retryAfter+time.Second-1)/time.Second)))
	}
	var (
		row     q.ProxyCircuitFailureRow
		counted bool
	)
	err := e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		counted = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if !e.rowEnsured(rem.id) {
			if err := qs.ProxyCircuitEnsure(ctx, rem.id); err != nil {
				return err
			}
		}
		rows, err := qs.ProxyCircuitFailure(ctx, q.ProxyCircuitFailureParams{
			RepoID: rem.id, LastFailure: why, OpenSeconds: openFor, ForceOpen: force, Threshold: int32(a.FailureThreshold), //nolint:gosec // 1 to 100
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil // already open: another replica opened it
		}
		counted, row = true, rows[0]
		if row.Opened {
			return e.notifyState(tx, rem.id)
		}
		return nil
	})
	if err != nil {
		e.log.Warn("recording an upstream failure failed", "repo", rem.Repo.Key, "error", err)
		return
	}
	e.markEnsured(rem.id)
	gone := e.goneOf(ctx, rem)
	switch {
	case !counted:
		// An UPDATE that matches no row found the circuit open: read it and hold the
		// open state at once, without waiting for the change log (R-REPO-029).
		e.state.Invalidate(rem.id)
		e.reloadAsync(rem.id)
	case row.Opened:
		st := &repoState{
			failures: int(row.ConsecutiveFailures), hasOpen: true, gone: gone,
			openUntil: e.now().Add(time.Duration(row.OpenRemainingUs) * time.Microsecond),
		}
		e.state.Set(rem.id, st)
		e.publishUp(rem.id, st)
		e.log.Warn("the upstream of a remote repository is assumed offline", "repo", rem.Repo.Key,
			"failures", row.ConsecutiveFailures, "open_for", time.Duration(row.OpenRemainingUs)*time.Microsecond)
	}
}

// ensured remembers the repositories whose circuit row this replica has created or
// seen, so the insert runs once per repository and replica.
func (e *Engine) rowEnsured(repoID int64) bool {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	return e.ensured[repoID]
}

func (e *Engine) markEnsured(repoID int64) {
	e.pmu.Lock()
	e.ensured[repoID] = true
	e.pmu.Unlock()
}

// markGone marks a stored document gone because the upstream answered 404 or 410
// for it, and writes the audit event on the first such answer (R-REPO-029). It
// reports whether this call set the mark.
func (e *Engine) markGone(ctx context.Context, rq Request) (bool, error) {
	var set bool
	rem := rq.Remote
	err := e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		set = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err := qs.ProxyMetadataMarkGone(ctx, q.ProxyMetadataMarkGoneParams{RepoID: rem.id, Path: rq.Path, Variant: rq.Variant})
		if err != nil || n == 0 {
			return err
		}
		set = true
		if err := e.notifyState(tx, rem.id); err != nil {
			return err
		}
		return e.o.Audit.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventProxyUpstreamGone, Actor: auditActor(rq.Caller), Outcome: audit.Success, Source: rq.Caller.Source,
			Subject:    audit.Subject{Kind: "repository", ID: rem.Repo.ID, Name: rem.Repo.Key},
			Repository: rem.Repo.Key, Path: rq.Path, Endpoint: rq.Caller.Endpoint,
			Details: mustJSON(map[string]string{"path": rq.Path, "variant": rq.Variant}),
		})
	})
	if err != nil {
		return false, err
	}
	if set {
		e.state.Invalidate(rem.id)
		e.reloadAsync(rem.id)
	}
	return set, nil
}

func auditActor(c CallerFacts) audit.Actor {
	if c.Actor.Kind != "" {
		return c.Actor
	}
	return audit.Actor{Kind: "system", Name: "proxy"}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil { // cannot happen: the callers pass maps of strings
		return json.RawMessage("{}")
	}
	return b
}
