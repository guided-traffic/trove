package session

import (
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
)

// ChangeArea is the change area that carries the end of a session to every
// replica (R-AUTH-023): the area auth of the identity work package, whose
// payload kinds are those of Q-AUTH-22 ([principal.Change]). This package writes
// the kind "session" and reads every kind.
const ChangeArea = cluster.AreaAuth

// CacheName is the in-process session cache of R-AUTH-023.
const CacheName = "auth_sessions"

// maxNamed is how many ended sessions travel as one message each; more are one
// coarse message of kind session without a list, which flushes the session
// cache.
const maxNamed = 8

// Notifier attaches a change to the writing transaction: *cluster.Changes.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Freshness is the gate of Q-AUTH-10: *cluster.Changes.
type Freshness interface {
	FreshWithin(d time.Duration) bool
}

// registerCache creates the session cache (R-AUTH-023): positive entries only,
// SafetyTTL auth.tokens.cache_ttl, invalidated through the area auth.
func registerCache(r *cache.Registry, ttl time.Duration, maxEntries int) *cache.Cache[string, *entry] {
	return cache.Register(r, CacheName, cache.Options[string, *entry]{
		MaxEntries: maxEntries, SafetyTTL: ttl, Areas: []string{ChangeArea},
		OnChange: applyChange,
	})
}

// applyChange is the cache's OnChange: idempotent, it runs for early evictions
// from Valkey and for the poll alike. The entries hold the principal's status,
// so a change of principals flushes the cache; the kinds that cannot touch a
// session entry (tokens, memberships, groups, settings) are ignored; an ended
// session is evicted by the hash of its secret; any other kind flushes
// (Q-AUTH-22: the coarse reaction is always safe).
func applyChange(c *cache.Cache[string, *entry], _ string, version int, payload json.RawMessage) {
	ch, ok := principal.Decode(payload)
	if !ok || version != principal.ChangeVersion {
		c.Flush(cache.ReasonChange)
		return
	}
	switch ch.Kind {
	case principal.ChangeToken, principal.ChangeMembership, principal.ChangeGroup, principal.ChangeSettings:
		return
	case principal.ChangeSession:
		if len(ch.Sessions) > 0 {
			named := true
			for _, ref := range ch.Sessions {
				if b, err := hex.DecodeString(ref.Hash); err == nil && len(b) == 32 {
					c.Invalidate(string(b))
				} else {
					named = false
				}
			}
			if named {
				return
			}
		}
	}
	c.Flush(cache.ReasonChange)
}

// notifyEnded records the end of sessions in the area auth on tx. Up to maxNamed
// named sessions travel one by one; more are one coarse entry.
func notifyEnded(n Notifier, tx meta.Tx, ended []endedSession) error {
	if n == nil || len(ended) == 0 {
		return nil
	}
	if len(ended) > maxNamed {
		return n.Notify(tx, ChangeArea, principal.ChangeVersion, principal.Change{Kind: principal.ChangeSession})
	}
	for _, e := range ended {
		c := principal.Change{Kind: principal.ChangeSession, Sessions: []principal.SessionRef{{ID: e.id, Hash: e.sha256Hex}}}
		if err := n.Notify(tx, ChangeArea, principal.ChangeVersion, c); err != nil {
			return err
		}
	}
	return nil
}

// endedSession is a session a statement ended: its ID and the hex SHA-256 of
// its secret, the cache key.
type endedSession struct{ id, sha256Hex string }
