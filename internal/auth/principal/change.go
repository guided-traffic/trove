package principal

import (
	"encoding/json"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
)

// ChangeVersion is the payload version of the change area auth that this build
// writes and understands (cluster.AreaAuth).
const ChangeVersion = 1

// The payload kinds of the change area auth. A kind that a cache does not know
// flushes that cache (the coarse reaction is always safe); KindSession is the
// one the identity caches know to be none of their business.
const (
	// ChangeToken: tokens ended or changed. Tokens lists them.
	ChangeToken = "token"
	// ChangePrincipal: a principal's status, name or existence changed. IDs
	// lists them. Credentials of the principal, its sessions and its cached
	// entry are affected.
	ChangePrincipal = "principal"
	// ChangeMembership: the memberships of the principals in IDs changed. Only
	// the principal cache is affected.
	ChangeMembership = "membership"
	// ChangeGroup: a group was renamed, deleted or its members cannot be listed
	// in a payload. The principal cache is flushed.
	ChangeGroup = "group"
	// ChangeSettings: the row of auth_settings changed.
	ChangeSettings = "settings"
	// ChangeSession: a session ended. Written by the login area; the identity
	// caches ignore it.
	ChangeSession = "session"
	// ChangeAll: everything the area keeps fresh.
	ChangeAll = "all"
)

// maxChangeIDs bounds the list of a payload so that it stays under the limit of
// a change message (1 KiB); more entries are sent as a coarse kind.
const maxChangeIDs = 12

// TokenRef names an ended or changed token: its ID and the hex SHA-256 of its
// secret, which is the key of the replica caches. The hash of a 256-bit random
// secret allows no guess of the secret, and a payload never carries the secret
// itself (R-ARCH-018).
type TokenRef struct {
	ID   string `json:"i"`
	Hash string `json:"h"`
}

// Change is the payload of a change of the area auth.
type Change struct {
	Kind   string     `json:"t"`
	IDs    []string   `json:"ids,omitempty"`
	Tokens []TokenRef `json:"k,omitempty"`
}

// Decode parses a payload; ok is false for a payload that is not a Change.
func Decode(payload json.RawMessage) (c Change, ok bool) {
	if err := json.Unmarshal(payload, &c); err != nil || c.Kind == "" {
		return Change{}, false
	}
	return c, true
}

// NotifyPrincipals records a change of principals in tx. Too many IDs for one
// payload become a flush of the whole area.
func NotifyPrincipals(n Notifier, tx meta.Tx, kind string, ids ...string) error {
	c := Change{Kind: kind, IDs: ids}
	if len(ids) > maxChangeIDs {
		c = Change{Kind: ChangeAll}
	}
	return n.Notify(tx, cluster.AreaAuth, ChangeVersion, c)
}

// NotifyTokens records ended tokens in tx.
func NotifyTokens(n Notifier, tx meta.Tx, refs ...TokenRef) error {
	c := Change{Kind: ChangeToken, Tokens: refs}
	if len(refs) > maxChangeIDs/2 {
		c = Change{Kind: ChangeAll}
	}
	return n.Notify(tx, cluster.AreaAuth, ChangeVersion, c)
}

// NotifySettings records a change of the runtime settings in tx.
func NotifySettings(n Notifier, tx meta.Tx) error {
	return n.Notify(tx, cluster.AreaAuth, ChangeVersion, Change{Kind: ChangeSettings})
}
