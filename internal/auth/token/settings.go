package token

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const settingsCacheName = "auth_settings"

// Settings is the row of auth_settings (R-AUTH-040). A nil duration means the
// YAML value applies.
type Settings struct {
	AnonymousAccess bool
	PATDefaultTTL   *time.Duration
	PATMaxTTL       *time.Duration
	SATDefaultTTL   *time.Duration
	SATMaxTTL       *time.Duration
	ExpiryNotice    *time.Duration
	// Revision is the ETag number of the row: the microseconds of its last change,
	// which every update raises by at least one (R-API-012; Q-AUTH-40).
	Revision int64
}

// Effective is what the runtime applies: the row's values within the YAML
// ceilings, and the anonymous switch combined with the operator's ceiling.
type Effective struct {
	AnonymousAccess bool
	PATDefaultTTL   time.Duration
	PATMaxTTL       time.Duration
	SATDefaultTTL   time.Duration
	SATMaxTTL       time.Duration
	ExpiryNotice    time.Duration
}

// SettingsPatch is the request of Update; a nil field is unchanged.
type SettingsPatch struct {
	AnonymousAccess *bool
	PATDefaultTTL   *time.Duration
	PATMaxTTL       *time.Duration
	SATDefaultTTL   *time.Duration
	SATMaxTTL       *time.Duration
	ExpiryNotice    *time.Duration
	// Expected is the revision the caller loaded (If-Match), or errs.AnyRevision
	// for the deliberate blind write; the update is conditional on it in the same
	// statement and fails with errs.Stale when the row has moved on (R-API-012).
	// The zero value matches no revision.
	Expected int64
}

// SettingsOptions configure [NewSettingsStore].
type SettingsOptions struct {
	DB       principal.DB
	Notifier principal.Notifier
	Recorder audit.Recorder
	Cache    *cache.Registry
	Gate     principal.Gate
	// Tokens and Anonymous are the YAML ceilings.
	Tokens    config.AuthTokens
	Anonymous config.AuthAnonymous
}

// SettingsStore reads and changes the runtime authentication settings
// (R-AUTH-040): a cached row invalidated through the change area auth.
type SettingsStore struct {
	o       SettingsOptions
	cache   *cache.Cache[struct{}, Settings]
	flights principal.Flights[struct{}, Settings]
}

// NewSettingsStore creates the store and registers the cache auth_settings.
func NewSettingsStore(o SettingsOptions) *SettingsStore {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	s := &SettingsStore{o: o}
	s.cache = cache.Register(o.Cache, settingsCacheName, cache.Options[struct{}, Settings]{
		MaxEntries: 1,
		SafetyTTL:  o.Tokens.CacheTTL,
		Areas:      []string{cluster.AreaAuth},
		OnChange: func(c *cache.Cache[struct{}, Settings], _ string, _ int, payload json.RawMessage) {
			ch, ok := principal.Decode(payload)
			if !ok || ch.Kind == principal.ChangeSettings || ch.Kind == principal.ChangeAll {
				c.Flush(cache.ReasonChange)
			}
		},
	})
	return s
}

// Get returns the settings row. While the freshness gate is closed it reads
// PostgreSQL and does not cache (R-AUTH-023).
func (s *SettingsStore) Get(ctx context.Context) (Settings, error) {
	if s.o.Gate.Open() {
		return s.cache.Load(ctx, struct{}{}, s.load)
	}
	return s.flights.Do(ctx, struct{}{}, s.o.Gate.Bound, s.load)
}

// Read returns the settings row from PostgreSQL, never from the cache: the
// management API answers a read that shows a write of another replica at once
// (R-API-052) and derives the ETag from it.
func (s *SettingsStore) Read(ctx context.Context) (Settings, error) { return s.load(ctx) }

func (s *SettingsStore) load(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		out, err = readSettings(ctx, q.New(db))
		return err
	})
	return out, principal.DBErr("token.settings", err)
}

func readSettings(ctx context.Context, qs *q.Queries) (Settings, error) {
	rows, err := qs.IdentityGetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if len(rows) == 0 {
		return Settings{}, nil // the row is seeded by the migration; its absence reads as all defaults
	}
	r := rows[0]
	return Settings{
		AnonymousAccess: r.AnonymousAccess,
		PATDefaultTTL:   secs(r.PatDefaultSecs), PATMaxTTL: secs(r.PatMaxSecs),
		SATDefaultTTL: secs(r.SatDefaultSecs), SATMaxTTL: secs(r.SatMaxSecs),
		ExpiryNotice: secs(r.ExpiryNoticeSecs), Revision: r.Revision,
	}, nil
}

func secs(v float64) *time.Duration {
	if v < 0 {
		return nil
	}
	d := time.Duration(v * float64(time.Second))
	return &d
}

func fromSecs(d *time.Duration) float64 {
	if d == nil {
		return -1
	}
	return d.Seconds()
}

// Effective combines the row with the YAML values. A runtime value above its
// ceiling cannot be written, but a configuration that was lowered after the
// write can leave one behind: the YAML ceiling wins.
func (s *SettingsStore) Effective(ctx context.Context) (Effective, error) {
	row, err := s.Get(ctx)
	if err != nil {
		return Effective{}, err
	}
	return s.effective(row), nil
}

// Resolve combines a settings row with the YAML values, as Effective does for the
// row it reads.
func (s *SettingsStore) Resolve(row Settings) Effective { return s.effective(row) }

func (s *SettingsStore) effective(row Settings) Effective {
	t := s.o.Tokens
	pick := func(v *time.Duration, ceiling time.Duration) time.Duration {
		if v == nil || *v > ceiling {
			return ceiling
		}
		return *v
	}
	e := Effective{
		AnonymousAccess: row.AnonymousAccess && s.o.Anonymous.Allowed,
		PATMaxTTL:       pick(row.PATMaxTTL, t.PAT.MaxTTL),
		SATMaxTTL:       pick(row.SATMaxTTL, t.SAT.MaxTTL),
		ExpiryNotice:    pick(row.ExpiryNotice, t.ExpiryNotice),
	}
	e.PATDefaultTTL = min(pick(row.PATDefaultTTL, t.PAT.DefaultTTL), e.PATMaxTTL)
	e.SATDefaultTTL = min(pick(row.SATDefaultTTL, t.SAT.DefaultTTL), e.SATMaxTTL)
	return e
}

// Update applies a patch (R-AUTH-040): each value at most its YAML ceiling
// (400 exceeds_ceiling), each default at most its maximum (400
// default_above_maximum), anonymous access only while the ceiling allows it
// (409 anonymous_not_allowed). It records settings.auth.changed with the before
// and after values and the change in the area auth in the same transaction.
func (s *SettingsStore) Update(ctx context.Context, c principal.Caller, p SettingsPatch) (Settings, error) {
	var after Settings
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		before, err := readSettings(ctx, qs)
		if err != nil {
			return err
		}
		after = before
		if p.AnonymousAccess != nil {
			after.AnonymousAccess = *p.AnonymousAccess
		}
		for _, f := range []struct {
			patch *time.Duration
			dst   **time.Duration
		}{
			{p.PATDefaultTTL, &after.PATDefaultTTL},
			{p.PATMaxTTL, &after.PATMaxTTL},
			{p.SATDefaultTTL, &after.SATDefaultTTL},
			{p.SATMaxTTL, &after.SATMaxTTL},
			{p.ExpiryNotice, &after.ExpiryNotice},
		} {
			if f.patch != nil {
				*f.dst = f.patch
			}
		}
		if err := s.validate(after, p); err != nil {
			return err
		}
		written, err := qs.IdentityUpdateSettings(ctx, q.IdentityUpdateSettingsParams{
			AnonymousAccess: after.AnonymousAccess,
			PatDefaultSecs:  fromSecs(after.PATDefaultTTL), PatMaxSecs: fromSecs(after.PATMaxTTL),
			SatDefaultSecs: fromSecs(after.SATDefaultTTL), SatMaxSecs: fromSecs(after.SATMaxTTL),
			ExpiryNoticeSecs: fromSecs(after.ExpiryNotice), UpdatedBy: c.Actor.ID, Expected: p.Expected,
		})
		if err != nil {
			return err
		}
		if len(written) == 0 {
			cur, err := readSettings(ctx, qs)
			if err != nil {
				return err
			}
			return errs.Stale("The authentication settings", cur.Revision)
		}
		if err := principal.NotifySettings(s.o.Notifier, tx); err != nil {
			return err
		}
		if after, err = readSettings(ctx, qs); err != nil {
			return err
		}
		// The journal entry carries anonymousAccess before and after on every
		// change, so a producer cannot drop a narrowing (Q-AUTH-18).
		entry := map[string]any{"anonymousAccess": map[string]bool{"before": before.AnonymousAccess, "after": after.AnonymousAccess}}
		return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventSettingsAuthChanged, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "settings", Name: "auth"},
			Details: mustJSON(map[string]any{"before": settingsJSON(before), "after": settingsJSON(after)}),
			Journal: mustJSON(entry),
		})
	})
	if _, stale := errs.CurrentRevision(err); stale {
		return after, err
	}
	return after, principal.DBErr("token.settings.update", err)
}

func (s *SettingsStore) validate(after Settings, p SettingsPatch) error {
	t := s.o.Tokens
	if after.AnonymousAccess && !s.o.Anonymous.Allowed && p.AnonymousAccess != nil {
		return errs.E(errs.Conflict, errs.CodeAnonymousNotAllowed, "auth.anonymous.allowed is false: anonymous access cannot be enabled.")
	}
	for _, c := range []struct {
		key     string
		v       *time.Duration
		ceiling time.Duration
	}{
		{"/tokens/pat/defaultTTLSeconds", p.PATDefaultTTL, t.PAT.DefaultTTL},
		{"/tokens/pat/maxTTLSeconds", p.PATMaxTTL, t.PAT.MaxTTL},
		{"/tokens/sat/defaultTTLSeconds", p.SATDefaultTTL, t.SAT.DefaultTTL},
		{"/tokens/sat/maxTTLSeconds", p.SATMaxTTL, t.SAT.MaxTTL},
		{"/tokens/expiryNoticeSeconds", p.ExpiryNotice, t.ExpiryNotice},
	} {
		switch {
		case c.v == nil:
		case *c.v < time.Minute:
			return errs.E(errs.Invalid, errs.CodeInvalidTTL, "A lifetime must be at least 60 seconds.")
		case *c.v > c.ceiling:
			return errs.E(errs.Invalid, errs.CodeExceedsCeiling, "The value exceeds the ceiling in the configuration ("+c.ceiling.String()+").")
		}
	}
	e := s.effective(after)
	for _, pr := range []struct {
		def  *time.Duration
		max  time.Duration
		kind string
	}{{after.PATDefaultTTL, e.PATMaxTTL, "pat"}, {after.SATDefaultTTL, e.SATMaxTTL, "sat"}} {
		if pr.def != nil && *pr.def > pr.max {
			return errs.E(errs.Invalid, errs.CodeDefaultAboveMaximum, "The default lifetime exceeds the maximum ("+pr.max.String()+").")
		}
	}
	return nil
}

func settingsJSON(st Settings) map[string]any {
	n := func(d *time.Duration) any {
		if d == nil {
			return nil
		}
		return int64(d.Seconds())
	}
	return map[string]any{
		"anonymousAccess": st.AnonymousAccess, "patDefaultTTLSeconds": n(st.PATDefaultTTL), "patMaxTTLSeconds": n(st.PATMaxTTL),
		"satDefaultTTLSeconds": n(st.SATDefaultTTL), "satMaxTTLSeconds": n(st.SATMaxTTL), "expiryNoticeSeconds": n(st.ExpiryNotice),
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
