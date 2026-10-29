package authz

import (
	"context"
	"encoding/json"
	"strings"

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

// actorOf converts the subject to the actor of an audit event. The kind of a
// principal is the string of its principal.Kind, which is also the kind of the
// edge type (v1.ActorFrom), so both build the same actor.
func actorOf(sub Subject) audit.Actor {
	if sub.Anonymous {
		return audit.Actor{Kind: string(principal.KindAnonymous)}
	}
	kind := string(sub.Kind)
	if kind == "" { // a subject a test built by name only
		kind = string(principal.KindUser)
	}
	return audit.Actor{Kind: kind, ID: sub.ID, Name: sub.Name, TokenID: sub.TokenID, AuthMethod: sub.AuthMethod}
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
	e.rec.Record(ctx, e.decisionEvent(EventDenied, sub, c, d))
}

func (e *Engine) decisionEvent(name string, sub Subject, c Coordinate, d Decision) audit.Event {
	details := map[string]any{
		"reason": d.Reason, "status": d.Status(), "actions": actionStrings(c), "repository": c.Repository,
		"required_action": strings.Join(actionStrings(c), ","),
		"credential":      credentialKind(sub), "replica": e.o.Replica, "epoch": d.Epoch,
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
	// Repository and Path are the columns the writer's deduplication keys on
	// (R-OPS-015); a package operation has a package name and no path.
	out := audit.Event{
		Type: name, Actor: actorOf(sub), Outcome: audit.Denied, Reason: d.Reason,
		Subject:    audit.Subject{Kind: "repository", Name: c.Repository},
		Repository: c.Repository, Path: firstNonEmpty(c.Path, c.Package), Endpoint: c.Endpoint,
		Details: raw,
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
		"reason": d.Reason, "status": d.Status(), "permission": permission, "required_action": permission,
		"credential": credentialKind(sub), "replica": e.o.Replica, "epoch": d.Epoch,
	})
	e.rec.Record(ctx, audit.Event{
		Type: EventDenied, Actor: actorOf(sub), Outcome: audit.Denied, Reason: d.Reason,
		Subject: audit.Subject{Kind: "permission", Name: permission}, Details: raw,
	})
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// RecordAllowed records an allowed write or populate in the transaction of the
// change (R-AUTHZ-029): if the audit row cannot be written, the change fails.
// The writer (files, formats) calls it with the decision it acted on.
func (e *Engine) RecordAllowed(ctx context.Context, tx meta.Tx, sub Subject, c Coordinate, d Decision) error {
	return e.rec.RecordTx(ctx, tx, e.decisionEvent(EventWriteAllowed, sub, c, d))
}
