package token

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the token events of the revocation journal (R-HA-054, R-AUTH-029, R-AUTH-030, R-AUTH-037, R-CI-017). Every
// handler removes or narrows and nothing else: a token is revoked, a predecessor's expiry is brought forward, the anonymous
// switch is turned off, a lifetime maximum is lowered. An entry is untrusted input; a malformed one is skipped, a token the restored database does not hold
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
		return replaySettings(ctx, tx, env, o)
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

// settingsEntry is the journal entry of settings.auth.changed (R-AUTH-041, Q-AUTH-18, Q-AUTH-207): the anonymous switch and
// the two runtime maxima, before and after, on every change. The parts are read independently: a part the entry lacks or
// holds out of range is nil and narrows nothing, and it never takes another part with it.
type settingsEntry struct {
	Anonymous *boolChange
	// PAT and SAT are the maxima after the change, in seconds; nil in the pointer is the YAML value, the longest.
	PAT, SAT *maxAfter
}

type boolChange struct{ Before, After bool }

// maxAfter is the journaled after-value of a maximum: Seconds is nil for the YAML value.
type maxAfter struct{ Seconds *int64 }

// Bounds a journaled maximum must keep: the live validation refuses less than a minute, the configuration refuses a ceiling
// above 876000h (100 years), so the producer never writes a value the reader rejects, and the upper bound keeps the interval
// arithmetic of PostgreSQL far from an overflow.
const (
	minReplayTTLSeconds = 60
	maxReplayTTLSeconds = int64(config.MaxTokenLifetime / time.Second)
)

// readSettingsEntry reads a settings.auth.changed entry part by part. It reports false when no part is readable.
func readSettingsEntry(o journal.Object) (settingsEntry, bool) {
	var e struct {
		Anonymous json.RawMessage `json:"anonymousAccess"`
		Tokens    struct {
			PAT json.RawMessage `json:"pat"`
			SAT json.RawMessage `json:"sat"`
		} `json:"tokens"`
	}
	if !o.Into(&e) {
		return settingsEntry{}, false
	}
	var out settingsEntry
	var a struct {
		Before *bool `json:"before"`
		After  *bool `json:"after"`
	}
	if json.Unmarshal(e.Anonymous, &a) == nil && a.Before != nil && a.After != nil {
		out.Anonymous = &boolChange{*a.Before, *a.After}
	}
	out.PAT, out.SAT = readMax(e.Tokens.PAT), readMax(e.Tokens.SAT)
	return out, out.Anonymous != nil || out.PAT != nil || out.SAT != nil
}

// readMax reads {"maxTTLSeconds": {"before": n|null, "after": n|null}}: the after-value is null or a whole number of seconds
// within the bounds, and its key is present; nil for anything else.
func readMax(raw json.RawMessage) *maxAfter {
	var m struct {
		MaxTTL map[string]json.RawMessage `json:"maxTTLSeconds"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	after, ok := m.MaxTTL["after"]
	if !ok {
		return nil
	}
	if string(after) == "null" {
		return &maxAfter{}
	}
	var n int64
	if err := json.Unmarshal(after, &n); err != nil || n < minReplayTTLSeconds || n > maxReplayTTLSeconds {
		return nil
	}
	return &maxAfter{Seconds: &n}
}

// maxOrder is the comparator of a lifetime maximum (R-AUTH-026, R-AUTH-040): a value is narrower the shorter it is, and nil,
// the YAML value, is the longest. The order is total, so the update rule of R-HA-054 never reaches its third case: a
// journaled value that is not at most the current one is longer than it, which a restore to an earlier point never
// produced and a replay never writes.
type maxOrder struct{}

// Narrows reports whether after allows nothing that current does not: after is a number at most current, or current is nil.
func (maxOrder) Narrows(current, after *int64) bool {
	return after != nil && (current == nil || *after <= *current)
}

// replaySettings replays settings.auth.changed (R-AUTH-037, R-AUTH-040, Q-AUTH-18, Q-AUTH-207) part by part: the anonymous
// switch and the two maxima. The outcome is applied when any part changed something, skipped when none did and a part
// would have widened, and already in effect otherwise.
func replaySettings(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	e, ok := readSettingsEntry(o)
	if !ok {
		return o.Malformed("no part of the entry is readable"), nil
	}
	anon := journal.Result{Outcome: journal.AlreadyInEffect}
	if e.Anonymous != nil {
		var err error
		if anon, err = replayAnonymous(ctx, tx, env, o, *e.Anonymous); err != nil {
			return journal.Result{}, err
		}
	}
	qs := principal.Queries(tx)
	cur, err := readSettings(ctx, qs)
	if err != nil {
		return journal.Result{}, err
	}
	var parts []string
	if anon.Outcome == journal.Applied || anon.Outcome == journal.Suspended {
		parts = append(parts, "anonymousAccess")
	}
	widened := anon.Outcome == journal.Skipped
	lowered := false
	for _, m := range []struct {
		name    string
		current *time.Duration
		after   *maxAfter
		lower   func(context.Context, float64) ([]int64, error)
	}{
		{"tokens.pat.maxTTLSeconds", cur.PATMaxTTL, e.PAT, func(c context.Context, s float64) ([]int64, error) { return qs.IdentityLowerPATMax(c, s) }},
		{"tokens.sat.maxTTLSeconds", cur.SATMaxTTL, e.SAT, func(c context.Context, s float64) ([]int64, error) { return qs.IdentityLowerSATMax(c, s) }},
	} {
		if m.after == nil {
			continue
		}
		var current *int64
		if m.current != nil {
			n := int64(m.current.Seconds())
			current = &n
		}
		switch {
		case sameMax(current, m.after.Seconds):
		case !(maxOrder{}).Narrows(current, m.after.Seconds):
			widened = true
		default:
			rows, err := m.lower(ctx, float64(*m.after.Seconds))
			if err != nil {
				return journal.Result{}, err
			}
			if len(rows) > 0 {
				lowered = true
				parts = append(parts, m.name)
			}
		}
	}
	if lowered {
		if err := principal.NotifySettings(env, tx); err != nil {
			return journal.Result{}, err
		}
	}
	r := o.Result(journal.AlreadyInEffect, "settings")
	switch {
	case anon.Outcome == journal.Suspended:
		r.Outcome, r.Detail = journal.Suspended, anon.Detail
	case len(parts) > 0:
		r.Outcome = journal.Applied
		r.Detail = mustJSON(map[string]any{"parts": parts})
	case widened:
		r.Outcome = journal.Skipped
	}
	return r, nil
}

// sameMax reports whether two maxima are equal, nil being equal to nil.
func sameMax(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// replayAnonymous is the anonymous-switch part of settings.auth.changed: the order false < true, a value narrows when it is
// false or equal. Applied turns the switch off; the update rule can never suspend, because with two values the only change
// that is not a narrowing and not a broadening of the journaled direction is an equal one.
func replayAnonymous(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object, c boolChange) (journal.Result, error) {
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
	if current == c.After {
		return o.Result(journal.AlreadyInEffect, "anonymousAccess"), nil
	}
	out, detail, err := journal.ApplyUpdate[bool](switchOrder{}, current, c.Before, c.After, act)
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
