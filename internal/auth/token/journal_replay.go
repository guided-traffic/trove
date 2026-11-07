package token

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the token events of the revocation journal (R-HA-054, R-AUTH-029, R-AUTH-030, R-AUTH-037, R-CI-017). Every
// handler removes or narrows and nothing else: a token is revoked, a predecessor's expiry is brought forward, the anonymous
// switch is turned off. An entry is untrusted input; a malformed one is skipped, a token the restored database does not hold
// is "subject unknown".
func init() {
	journal.Register(journal.Handler{
		Domain: "identity",
		Events: []string{"token.revoked", "ci.token.revoked", "token.rotated", "settings.auth.changed"},
		Apply:  replayToken,
	})
}

// validReplayReason reports whether the column revocation_reason accepts the reason. A journaled reason outside the list (a
// forged entry, or a reason of a later release) is stored as "admin": the token is revoked either way.
func validReplayReason(s string) bool {
	switch s {
	case ReasonOwner, ReasonAdmin, ReasonRotated, ReasonNPMLogout, ReasonLeaked, ReasonPrincipalDisabled, "scim", "mapping_deleted", ReasonParentRevoked:
		return true
	}
	return false
}

func replayToken(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	switch o.Event {
	case "token.revoked":
		return replayRevoked(ctx, tx, env, o)
	case "ci.token.revoked":
		return replayCIRevoked(ctx, tx, env, o)
	case "token.rotated":
		return replayRotated(ctx, tx, env, o)
	default:
		return replayAnonymous(ctx, tx, env, o)
	}
}

// revokeTokenTree revokes a token with its derived tokens, as the live revocation does (the cascade of R-AUTH-029), and tells
// the replicas. It reports whether the token exists and whether it ended anything.
func revokeTokenTree(ctx context.Context, tx meta.Tx, env journal.Env, id, reason string) (exists, ended bool, err error) {
	qs := principal.Queries(tx)
	rows, err := qs.JournalTokenState(ctx, id)
	if err != nil || len(rows) == 0 {
		return false, false, err
	}
	if _, err := qs.IdentityLockTokenTree(ctx, q.IdentityLockTokenTreeParams{ID: id}); err != nil {
		return true, false, err
	}
	ended2, err := qs.IdentityRevokeCascade(ctx, q.IdentityRevokeCascadeParams{ID: id, Reason: reason})
	if err != nil || len(ended2) == 0 {
		return true, false, err
	}
	refs := make([]principal.TokenRef, 0, len(ended2))
	for _, r := range ended2 {
		refs = append(refs, principal.TokenRef{ID: r.ID, Hash: r.SecretHex})
	}
	return true, true, principal.NotifyTokens(env, tx, refs...)
}

func replayRevoked(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		TokenID string `json:"tokenId"`
		Reason  string `json:"reason"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.TokenID) {
		return o.Malformed("tokenId is not an ID"), nil
	}
	reason := e.Reason
	if !validReplayReason(reason) {
		reason = ReasonAdmin
	}
	exists, ended, err := revokeTokenTree(ctx, tx, env, e.TokenID, reason)
	return tokenOutcome(o, e.TokenID, exists, ended), err
}

// replayCIRevoked revokes the CI tokens of one event. The outcome is applied when any of them ended, already in effect when
// all are revoked, and subject unknown when the restored database holds none.
func replayCIRevoked(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		TokenIDs []string `json:"tokenIds"`
	}
	if !o.Into(&e) || len(e.TokenIDs) == 0 || len(e.TokenIDs) > MaxCIRevokedIDs {
		return o.Malformed("tokenIds is not a list of IDs"), nil
	}
	known, any := 0, false
	for _, id := range e.TokenIDs {
		if !journal.IsUUID(id) {
			return o.Malformed("tokenIds is not a list of IDs"), nil
		}
		exists, ended, err := revokeTokenTree(ctx, tx, env, id, ReasonAdmin)
		if err != nil {
			return journal.Result{}, err
		}
		if exists {
			known++
		}
		any = any || ended
	}
	return tokenOutcome(o, "", known > 0, any), nil
}

func tokenOutcome(o journal.Object, subject string, exists, ended bool) journal.Result {
	switch {
	case !exists:
		return o.Result(journal.SubjectUnknown, subject)
	case ended:
		return o.Result(journal.Applied, subject)
	}
	return o.Result(journal.AlreadyInEffect, subject)
}

// replayRotated brings the predecessor's expiry forward to the journaled one, and revokes it when the overlap was zero
// (R-AUTH-030). It never extends: a stored expiry that is earlier stays.
func replayRotated(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		Predecessor string  `json:"predecessor"`
		ExpiresAt   *string `json:"expiresAt"`
		Overlap     int64   `json:"overlapSeconds"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.Predecessor) || e.Overlap < 0 {
		return o.Malformed("predecessor is not an ID"), nil
	}
	if e.Overlap == 0 {
		exists, ended, err := revokeTokenTree(ctx, tx, env, e.Predecessor, ReasonRotated)
		return tokenOutcome(o, e.Predecessor, exists, ended), err
	}
	if e.ExpiresAt == nil {
		return o.Malformed("expiresAt is missing"), nil
	}
	at, perr := time.Parse(time.RFC3339, *e.ExpiresAt)
	if perr != nil {
		return o.Malformed("expiresAt is no time"), nil //nolint:nilerr // an unreadable time in an untrusted entry is a skipped entry, not a failed replay
	}
	qs := principal.Queries(tx)
	state, err := qs.JournalTokenState(ctx, e.Predecessor)
	if err != nil {
		return journal.Result{}, err
	}
	if len(state) == 0 {
		return o.Result(journal.SubjectUnknown, e.Predecessor), nil
	}
	rows, err := qs.JournalLimitTokenExpiry(ctx, q.JournalLimitTokenExpiryParams{ID: e.Predecessor, ExpiresUs: at.UnixMicro()})
	if err != nil {
		return journal.Result{}, err
	}
	if len(rows) == 0 {
		return o.Result(journal.AlreadyInEffect, e.Predecessor), nil
	}
	refs := []principal.TokenRef{{ID: e.Predecessor, Hash: rows[0]}}
	return o.Result(journal.Applied, e.Predecessor), principal.NotifyTokens(env, tx, refs...)
}

// replayAnonymous replays settings.auth.changed (R-AUTH-037, Q-AUTH-18): the entry carries the anonymous switch before and
// after. The comparator is the order false < true: a value narrows when it is false or equal. Applied turns the switch off;
// the update rule can never suspend, because with two values the only change that is not a narrowing and not a broadening of
// the journaled direction is an equal one.
func replayAnonymous(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		Anonymous *struct {
			Before *bool `json:"before"`
			After  *bool `json:"after"`
		} `json:"anonymousAccess"`
	}
	if !o.Into(&e) || e.Anonymous == nil || e.Anonymous.Before == nil || e.Anonymous.After == nil {
		return o.Malformed("anonymousAccess has no before and after"), nil
	}
	qs := principal.Queries(tx)
	current, err := qs.JournalAnonymousAccess(ctx)
	if err != nil {
		return journal.Result{}, err
	}
	act := journal.UpdateActions{
		Apply: func() error { return switchOff(ctx, qs, env, tx) },
		Suspend: func() (json.RawMessage, error) {
			return json.RawMessage(`{"anonymousAccess":false}`), switchOff(ctx, qs, env, tx)
		},
	}
	if current == *e.Anonymous.After {
		return o.Result(journal.AlreadyInEffect, "anonymousAccess"), nil
	}
	out, detail, err := journal.ApplyUpdate[bool](switchOrder{}, current, *e.Anonymous.Before, *e.Anonymous.After, act)
	r := o.Result(out, "anonymousAccess")
	r.Detail = detail
	return r, err
}

func switchOff(ctx context.Context, qs *q.Queries, env journal.Env, tx meta.Tx) error {
	rows, err := qs.JournalDisableAnonymousAccess(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	return principal.NotifySettings(env, tx)
}

// switchOrder is the comparator of the anonymous switch: off grants nothing that on does not.
type switchOrder struct{}

// Narrows reports whether after grants nothing that current does not.
func (switchOrder) Narrows(current, after bool) bool { return !after || current }
