package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// RevokedToken is one token a revocation ended, as its token.revoked event names it.
type RevokedToken struct {
	ID      string
	OwnerID string
	// Reason is the revocation reason of this token: the one of the request, or parent_revoked for a token the cascade
	// of R-AUTH-029 ended.
	Reason string
	// Bastion marks a Bastion token: its event has the resource type bastion and names the registration in its data
	// (R-BAS-004, Q-BAS-61).
	Bastion bool
}

// RecordTokensRevoked records token.revoked for every token, in order, each with the journal entry of R-HA-054
// ({tokenId, reason}), in tx (R-AUTH-029, R-AUTH-041). Each owner is read once, and the events go through
// audit.RecordAllTx: one statement per batch of audit rows and one per batch of their journal rows. Ending the
// thousands of tokens a principal may hold therefore costs a handful of round trips, not three per token (Q-AUTH-198).
func RecordTokensRevoked(ctx context.Context, tx meta.Tx, rec audit.Recorder, c Caller, toks []RevokedToken) error {
	if len(toks) == 0 {
		return nil
	}
	qs := Queries(tx)
	owners := map[string]tokenOwner{}
	evs := make([]audit.Event, 0, len(toks))
	for _, t := range toks {
		o, ok := owners[t.OwnerID]
		if !ok {
			var err error
			if o, err = readTokenOwner(ctx, qs, t.OwnerID); err != nil {
				return err
			}
			owners[t.OwnerID] = o
		}
		evs = append(evs, tokenRevokedEvent(c, t, o))
	}
	return audit.RecordAllTx(ctx, rec, tx, evs)
}

// RevokeAllTokensTx revokes every unrevoked token of a principal in tx with the reason and records a token.revoked
// with its journal entry for each (RecordTokensRevoked). bastion says the principal is a Bastion registration. It
// returns the IDs; the caller records the change of the principal for the replicas' caches.
func RevokeAllTokensTx(ctx context.Context, tx meta.Tx, rec audit.Recorder, c Caller, principalID, reason string, bastion bool) ([]string, error) {
	ids, err := Queries(tx).IdentityRevokeAllOfPrincipal(ctx, q.IdentityRevokeAllOfPrincipalParams{PrincipalID: principalID, RevokedBy: c.Actor.ID, Reason: reason})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	toks := make([]RevokedToken, len(ids))
	for i, id := range ids {
		toks[i] = RevokedToken{ID: id, OwnerID: principalID, Reason: reason, Bastion: bastion}
	}
	return ids, RecordTokensRevoked(ctx, tx, rec, c, toks)
}

// tokenOwner is what the token.revoked event of a token reads of its owner: the name (for a Bastion token) and the
// project of a project service account (Q-AUTHZ-169).
type tokenOwner struct {
	name    string
	project string
}

func readTokenOwner(ctx context.Context, qs *q.Queries, id string) (tokenOwner, error) {
	var o tokenOwner
	rows, err := qs.IdentityPrincipalByID(ctx, id)
	if err != nil {
		return o, err
	}
	if len(rows) > 0 {
		o.name = rows[0].Name
	}
	projects, err := qs.IdentityIsProjectServiceAccount(ctx, id)
	if err != nil {
		return o, err
	}
	if len(projects) > 0 {
		o.project = projects[0]
	}
	return o, nil
}

func tokenRevokedEvent(c Caller, t RevokedToken, o tokenOwner) audit.Event {
	d := map[string]any{"reason": t.Reason, "cascaded": []string{}}
	subject := audit.Subject{Kind: "token", ID: t.ID}
	if t.Bastion {
		// A Bastion token's event has the resource type bastion and the token as the resource; the registration is in
		// the data.
		if o.name != "" {
			d["owner"] = "bastion:" + o.name
			d["bastion"] = o.name
		}
		d["tokenId"] = t.ID
		subject.Kind = "bastion"
	}
	if o.project != "" {
		d["project"] = o.project
	}
	return audit.Event{
		Type: audit.EventTokenRevoked, Outcome: audit.Success, Reason: t.Reason, Actor: c.Actor, Source: c.Source,
		Subject: subject, Details: details(d),
		Journal: details(map[string]string{"tokenId": t.ID, "reason": t.Reason}),
	}
}
