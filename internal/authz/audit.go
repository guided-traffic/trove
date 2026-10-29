package authz

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// The event names of authz (internal/audit/events_authz.go).
const (
	EventDenied        = "authz.denied"
	EventReadAllowed   = "authz.read.allowed"
	EventWriteAllowed  = "authz.write.allowed"
	EventPolicyCreated = "authz.policy.created"
	EventPolicyUpdated = "authz.policy.updated"
	EventPolicyDeleted = "authz.policy.deleted"
	EventRoleBound     = "authz.role.bound"
	EventRoleUnbound   = "authz.role.unbound"
)

// actorOf converts the subject to the actor of an audit event.
func actorOf(sub Subject) audit.Actor {
	switch {
	case sub.Anonymous:
		return audit.Actor{Kind: "anonymous"}
	case sub.Kind == principal.KindBreakGlass:
		return audit.Actor{Kind: "break_glass", ID: sub.ID, Name: sub.Name}
	case sub.Kind == principal.KindServiceAccount:
		return audit.Actor{Kind: "service_account", ID: sub.ID, Name: sub.Name}
	case sub.Kind == principal.KindCI:
		return audit.Actor{Kind: "ci", ID: sub.ID, Name: sub.Name}
	}
	return audit.Actor{Kind: "user", ID: sub.ID, Name: sub.Name}
}

func credentialKind(sub Subject) string {
	switch {
	case sub.SessionID != "":
		return "session"
	case sub.TokenID != "":
		return "token"
	}
	return "none"
}

func actionStrings(c Coordinate) []string {
	if len(c.Actions) == 0 {
		return []string{string(Read)}
	}
	out := make([]string, len(c.Actions))
	for i, a := range c.Actions {
		out[i] = string(a)
	}
	return out
}

// audit records the decision as R-AUTHZ-029 says. Refusals are recorded
// asynchronously: refused writes always, refused reads with authz.audit.reads
// denied or all, anonymous refusals only with audit.record_anonymous_denials.
// Allowed reads are recorded only with reads: all. An allowed write is recorded
// by the writer in the transaction of the change (see [Engine.RecordAllowed]).
func (e *Engine) audit(ctx context.Context, sub Subject, c Coordinate, d Decision) {
	read := readType(c)
	reads := e.o.Config.Audit.Reads
	switch d.Outcome {
	case Allow:
		if read && reads == config.AuthzReadsAll {
			e.rec.Record(ctx, e.decisionEvent(EventReadAllowed, sub, c, d))
		}
		return
	case DenyEpochUnknown:
		return
	}
	if read && reads == config.AuthzReadsNone {
		return
	}
	if sub.Anonymous && !e.o.Audit.RecordAnonymousDenials {
		return
	}
	ev := e.decisionEvent(EventDenied, sub, c, d)
	e.dedup.record(ctx, e.denialKey(sub, c), ev)
}

// denialKey is the deduplication key of R-OPS-015: principal, action,
// repository and path. An anonymous caller has no principal; the address that
// R-AUTHZ-029 keys it by is not part of the subject, so anonymous denials are
// keyed by repository and path.
func (e *Engine) denialKey(sub Subject, c Coordinate) string {
	who := sub.ID
	if sub.Anonymous {
		who = "anonymous"
	}
	return strings.Join([]string{who, strings.Join(actionStrings(c), ","), c.Repository, c.Package, c.Path}, "\x00")
}

func (e *Engine) decisionEvent(name string, sub Subject, c Coordinate, d Decision) audit.Event {
	details := map[string]any{
		"reason": d.Reason, "status": d.Status(), "actions": actionStrings(c), "repository": c.Repository,
		"credential": credentialKind(sub), "replica": e.o.Replica, "epoch": d.Epoch,
	}
	if c.Class != "" {
		details["class"] = string(c.Class)
	}
	if c.Format != "" {
		details["format"] = c.Format
	}
	if c.Package != "" {
		details["package"] = c.Package
	}
	if c.Path != "" {
		details["path"] = c.Path
	}
	if c.Endpoint != "" {
		details["endpoint"] = c.Endpoint
	}
	if len(d.Sources) > 0 {
		details["sources"] = d.Sources
	}
	raw, _ := json.Marshal(details)
	out := audit.Event{
		Type: name, Actor: actorOf(sub), Outcome: audit.Failure, Reason: d.Reason,
		Subject: audit.Subject{Kind: "repository", Name: c.Repository}, Details: raw,
	}
	if d.Outcome == Allow {
		out.Outcome = audit.Success
	}
	return out
}

// auditCapability records a refused management operation (R-AUTHZ-029).
func (e *Engine) auditCapability(ctx context.Context, sub Subject, permission string, d Decision) {
	if sub.Anonymous && !e.o.Audit.RecordAnonymousDenials {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"reason": d.Reason, "status": d.Status(), "permission": permission,
		"credential": credentialKind(sub), "replica": e.o.Replica, "epoch": d.Epoch,
	})
	ev := audit.Event{
		Type: EventDenied, Actor: actorOf(sub), Outcome: audit.Failure, Reason: d.Reason,
		Subject: audit.Subject{Kind: "permission", Name: permission}, Details: raw,
	}
	e.dedup.record(ctx, strings.Join([]string{subjectKey(sub), "permission", permission}, "\x00"), ev)
}

func subjectKey(sub Subject) string {
	if sub.Anonymous {
		return "anonymous"
	}
	return sub.ID
}

// RecordAllowed records an allowed write or populate in the transaction of the
// change (R-AUTHZ-029): if the audit row cannot be written, the change fails.
// The writer (files, formats) calls it with the decision it acted on.
func (e *Engine) RecordAllowed(ctx context.Context, tx meta.Tx, sub Subject, c Coordinate, d Decision) error {
	return e.rec.RecordTx(ctx, tx, e.decisionEvent(EventWriteAllowed, sub, c, d))
}

// denialDedup collapses repeated refusals (R-OPS-015 "Deduplication without
// updates"): the first occurrence of a key is written at once; repeats within
// the window are counted in memory and written as one summary when the window
// closes. The state is the replica's memory; it never lives in Valkey.
type denialDedup struct {
	window time.Duration
	rec    audit.Recorder

	mu   sync.Mutex
	open map[string]*dedupEntry
}

type dedupEntry struct {
	event   audit.Event
	repeats int
	timer   *time.Timer
}

// maxOpenDenials bounds the memory of the deduplication; past it a refusal is
// written without being collapsed.
const maxOpenDenials = 10000

func newDenialDedup(window time.Duration, rec audit.Recorder) *denialDedup {
	return &denialDedup{window: window, rec: rec, open: map[string]*dedupEntry{}}
}

func (d *denialDedup) record(ctx context.Context, key string, ev audit.Event) {
	if d.window <= 0 {
		d.rec.Record(ctx, withCount(ev, 1))
		return
	}
	d.mu.Lock()
	if en, ok := d.open[key]; ok {
		en.repeats++
		d.mu.Unlock()
		return
	}
	if len(d.open) >= maxOpenDenials {
		d.mu.Unlock()
		d.rec.Record(ctx, withCount(ev, 1))
		return
	}
	en := &dedupEntry{event: ev}
	en.timer = time.AfterFunc(d.window, func() { d.close(key) })
	d.open[key] = en
	d.mu.Unlock()
	d.rec.Record(ctx, withCount(ev, 1))
}

// close ends the window of a key and writes the summary of its repeats.
func (d *denialDedup) close(key string) {
	d.mu.Lock()
	en, ok := d.open[key]
	delete(d.open, key)
	d.mu.Unlock()
	if !ok || en.repeats == 0 {
		return
	}
	d.rec.Record(context.Background(), withCount(en.event, en.repeats))
}

// flushAll writes every open summary; a replica that drains calls it before it
// exits (R-OPS-015).
func (d *denialDedup) flushAll(ctx context.Context) {
	d.mu.Lock()
	open := d.open
	d.open = map[string]*dedupEntry{}
	d.mu.Unlock()
	for _, en := range open {
		if en.timer != nil {
			en.timer.Stop()
		}
		if en.repeats > 0 {
			d.rec.Record(ctx, withCount(en.event, en.repeats))
		}
	}
}

// withCount sets details.count of an event.
func withCount(ev audit.Event, n int) audit.Event {
	var m map[string]any
	if err := json.Unmarshal(ev.Details, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["count"] = n
	ev.Details, _ = json.Marshal(m)
	return ev
}
