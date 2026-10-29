package principal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The name of the cache of principals (R-AUTH-023).
const cacheName = "auth_principals"

// maxCachedPrincipals bounds the entries of a replica's principal cache.
const maxCachedPrincipals = 20000

// entry is one cached principal with its groups. Only positive results are
// cached (R-AUTH-023).
type entry struct {
	p      Principal
	groups []GroupRef
}

// StoreOptions configure [NewStore].
type StoreOptions struct {
	DB DB
	// Cache is the replica's cache registry.
	Cache *cache.Registry
	// Gate is the freshness gate of R-AUTH-023.
	Gate Gate
	// SafetyTTL is auth.tokens.cache_ttl.
	SafetyTTL time.Duration
}

// PGStore is the cache-backed read side of the principal table. It implements
// [Store]: lookups are answered from the replica's cache auth_principals while
// the freshness gate is open, invalidated through the change area auth, and
// never go to Valkey (Q-AUTH-9).
type PGStore struct {
	db      DB
	gate    Gate
	cache   *cache.Cache[string, entry]
	flights Flights[string, entry]
}

var _ Store = (*PGStore)(nil)

// NewStore creates the store and registers its cache.
func NewStore(o StoreOptions) *PGStore {
	s := &PGStore{db: o.DB, gate: o.Gate}
	s.cache = cache.Register(o.Cache, cacheName, cache.Options[string, entry]{
		MaxEntries: maxCachedPrincipals,
		SafetyTTL:  o.SafetyTTL,
		Areas:      []string{cluster.AreaAuth},
		OnChange:   s.onChange,
	})
	return s
}

// onChange evicts what a change of the area auth can have altered. A payload of
// a kind this build does not know flushes the cache.
func (s *PGStore) onChange(c *cache.Cache[string, entry], _ string, _ int, payload json.RawMessage) {
	s.gate.Metrics.Invalidated()
	ch, ok := Decode(payload)
	if !ok {
		c.Flush(cache.ReasonChange)
		return
	}
	switch ch.Kind {
	case ChangeSession, ChangeToken, ChangeSettings:
		// Neither status, name nor memberships of a principal change.
	case ChangePrincipal, ChangeMembership:
		for _, id := range ch.IDs {
			c.Invalidate(idKey(id))
		}
		// A name key points at the entry of a renamed or deleted principal; names
		// are looked up rarely (administration, authorization setup), so all of
		// them go.
		c.InvalidateFunc(isNameKey)
	default:
		c.Flush(cache.ReasonChange)
	}
}

func idKey(id string) string                { return "i:" + id }
func nameKey(kind Kind, name string) string { return "n:" + string(kind) + ":" + name }
func isNameKey(k string) bool               { return len(k) > 1 && k[0] == 'n' }

// KindFromRow maps the kind column of the principal table, and the break-glass
// flag of its user row, to a [Kind].
func KindFromRow(kind string, bg bool) Kind { //nolint:revive // flag argument mirrors the row
	if bg {
		return KindBreakGlass
	}
	if kind == "ci_mapping" {
		return KindCI
	}
	return Kind(kind)
}

// ByID implements [Store].
func (s *PGStore) ByID(ctx context.Context, id string) (Principal, error) {
	if !IsID(id) {
		return Principal{}, ErrNotFound
	}
	e, err := s.lookup(ctx, idKey(id), func(ctx context.Context) (entry, error) { return s.loadByID(ctx, id) })
	return e.p, err
}

// ByName implements [Store].
func (s *PGStore) ByName(ctx context.Context, kind Kind, name string) (Principal, error) {
	dbKind := string(kind)
	switch kind {
	case KindCI:
		dbKind = "ci_mapping"
	case KindBreakGlass:
		dbKind = string(KindUser)
	}
	e, err := s.lookup(ctx, nameKey(kind, name), func(ctx context.Context) (entry, error) {
		return s.loadByName(ctx, dbKind, name, kind == KindBreakGlass)
	})
	return e.p, err
}

// Groups implements [Store]. The result is a copy.
func (s *PGStore) Groups(ctx context.Context, id string) ([]GroupRef, error) {
	if !IsID(id) {
		return nil, ErrNotFound
	}
	e, err := s.lookup(ctx, idKey(id), func(ctx context.Context) (entry, error) { return s.loadByID(ctx, id) })
	if err != nil {
		return nil, err
	}
	return append([]GroupRef(nil), e.groups...), nil
}

// lookup answers from the cache while the gate is open; otherwise the lookup
// goes to PostgreSQL, shared with concurrent callers of the same key, and is not
// cached.
func (s *PGStore) lookup(ctx context.Context, key string, load func(context.Context) (entry, error)) (entry, error) {
	if s.gate.Open() {
		return s.cache.Load(ctx, key, load)
	}
	return s.flights.Do(ctx, key, s.gate.Bound, load)
}

func (s *PGStore) loadByID(ctx context.Context, id string) (entry, error) {
	var e entry
	err := s.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.IdentityPrincipalByID(ctx, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		e.p = Principal{ID: r.ID, Kind: KindFromRow(r.Kind, r.IsBreakGlass), Name: r.Name, Status: Status(r.Status)}
		e.groups, err = groupsOf(ctx, qs, e.p)
		return err
	})
	return e, mapLoadErr("principal.load", err)
}

func (s *PGStore) loadByName(ctx context.Context, dbKind, name string, wantBreakGlass bool) (entry, error) {
	var e entry
	err := s.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: dbKind, Name: name})
		if err != nil {
			return err
		}
		if len(rows) == 0 || (wantBreakGlass && !rows[0].IsBreakGlass) {
			return ErrNotFound
		}
		r := rows[0]
		e.p = Principal{ID: r.ID, Kind: KindFromRow(r.Kind, r.IsBreakGlass), Name: r.Name, Status: Status(r.Status)}
		e.groups, err = groupsOf(ctx, qs, e.p)
		return err
	})
	return e, mapLoadErr("principal.load", err)
}

func groupsOf(ctx context.Context, qs *q.Queries, p Principal) ([]GroupRef, error) {
	if p.Kind == KindGroup || p.Kind == KindAnonymous || p.Kind == KindAuthenticated {
		return nil, nil
	}
	rows, err := qs.IdentityGroupsOfMember(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]GroupRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupRef{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func mapLoadErr(op string, err error) error {
	if err == ErrNotFound { //nolint:errorlint // the sentinel is returned unwrapped by the loaders above
		return err
	}
	return DBErr(op, err)
}
