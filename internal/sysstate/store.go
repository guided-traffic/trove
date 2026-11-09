package sysstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The system_state keys of this package (R-OPS-024, R-OPS-025).
const (
	KeyBanner      = "banner"
	KeyMaintenance = "maintenance_mode"
)

// CacheName is the registered name of the in-process copy; ChangeVersion the payload version of the change area cache that this
// build writes and understands.
const (
	CacheName     = "system_state"
	ChangeVersion = 1
)

// ChangeKind is the payload kind of a change of the area cache that concerns these settings; any other kind of that area is none of
// this package's business.
const ChangeKind = "system_state"

// Banner severities (R-OPS-025).
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Maintenance modes (R-OPS-024).
const (
	ModeNormal   = "normal"
	ModeReadOnly = "read_only"
)

// The bounds of the texts and of the retry hint.
const (
	MaxTitleRunes   = 120
	MaxMessageRunes = 2000
	// MaxMaintenanceMessageRunes bounds the maintenance message, which is also the body of every refused write.
	MaxMaintenanceMessageRunes = 500
	// DefaultRetryAfterSeconds is the Retry-After of a refused write when the operator named none; MaxRetryAfterSeconds is its ceiling.
	DefaultRetryAfterSeconds = 300
	MaxRetryAfterSeconds     = 86400
)

// DefaultRefusal is the detail of a refused write when the maintenance message is empty.
const DefaultRefusal = "The installation is in read-only maintenance mode; writes are refused until it ends."

// Banner is the system banner (R-OPS-025). A banner that was never set is the zero value: not enabled.
type Banner struct {
	Enabled  bool   `json:"enabled"`
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Maintenance is the maintenance mode (R-OPS-024). A mode that was never set reads as normal.
type Maintenance struct {
	Mode              string    `json:"mode"`
	CacheFill         bool      `json:"cacheFill"`
	Message           string    `json:"message,omitempty"`
	RetryAfterSeconds int       `json:"retryAfterSeconds,omitempty"`
	Since             time.Time `json:"since,omitzero"`
	By                string    `json:"by,omitempty"`
}

// ReadOnly reports whether writes are refused.
func (m Maintenance) ReadOnly() bool { return m.Mode == ModeReadOnly }

func (m Maintenance) same(o Maintenance) bool {
	return m.Mode == o.Mode && m.CacheFill == o.CacheFill && m.Message == o.Message && m.RetryAfterSeconds == o.RetryAfterSeconds &&
		m.Since.Equal(o.Since) && m.By == o.By
}

// DB is the database the store uses: transactions with the change-log and audit hooks, and reads with the retry of R-ARCH-019.
// *postgres.DB implements it.
type DB interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Notifier attaches a change of a change area to the writing transaction (R-ARCH-018). *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Freshness says whether the replica's view of the change log is younger than d; *cluster.Changes implements it.
type Freshness interface {
	FreshWithin(d time.Duration) bool
}

// Options configure [New].
type Options struct {
	DB       DB
	Notifier Notifier
	Recorder audit.Recorder
	Cache    *cache.Registry
	// Fresh and Bound are the freshness gate: while the change log's last poll is older than Bound, the copy is not used and every
	// question goes to PostgreSQL. A nil Fresh is a replica without a change log (a unit test), whose copy is always usable.
	Fresh Freshness
	Bound time.Duration
	// SafetyTTL bounds the age of the copy if a note is ever missed (R-OPS-024: 30 s when zero).
	SafetyTTL time.Duration
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Caller is who changes a setting.
type Caller struct {
	Actor  audit.Actor
	Source audit.Source
}

// State is both settings as read together, the unit of the cached copy.
type State struct {
	Banner      Banner
	Maintenance Maintenance
}

// Store reads and changes the banner and the maintenance mode.
type Store struct {
	o     Options
	cache *cache.Cache[string, State]
}

// New creates the store and registers the cache system_state.
func New(o Options) *Store {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.SafetyTTL <= 0 {
		o.SafetyTTL = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Store{o: o}
	s.cache = cache.Register(o.Cache, CacheName, cache.Options[string, State]{
		MaxEntries: 1,
		SafetyTTL:  o.SafetyTTL,
		Areas:      []string{cluster.AreaCache},
		OnChange: func(c *cache.Cache[string, State], _ string, _ int, payload json.RawMessage) {
			var p struct {
				Kind string `json:"kind"`
			}
			// The area cache carries the flush requests of other caches too; a payload this package cannot read is flushed as well,
			// because the coarse reaction is always safe.
			if err := json.Unmarshal(payload, &p); err != nil || p.Kind == ChangeKind || p.Kind == "" {
				c.Flush(cache.ReasonChange)
			}
		},
	})
	return s
}

const stateKey = "state"

// Read returns both settings from PostgreSQL, never from the copy: the management API answers a read that shows a write of another
// replica at once (R-API-052).
func (s *Store) Read(ctx context.Context) (State, error) { return s.load(ctx) }

// Get returns both settings from the copy while it is usable, from PostgreSQL otherwise.
func (s *Store) Get(ctx context.Context) (State, error) {
	if s.o.Fresh == nil || s.o.Fresh.FreshWithin(s.o.Bound) {
		return s.cache.Load(ctx, stateKey, s.load)
	}
	return s.load(ctx)
}

// Maintenance is the maintenance mode as the replica applies it.
func (s *Store) Maintenance(ctx context.Context) (Maintenance, error) {
	st, err := s.Get(ctx)
	return st.Maintenance, err
}

// RefuseWrite returns nil while writes are allowed and the 503 of R-OPS-024 (code unavailable, Retry-After, the operator's message)
// in read-only mode. An error reading the mode is returned as it is: a write that cannot learn whether the installation is in
// maintenance is not started. A nil store (a replica without the settings) refuses nothing.
func (s *Store) RefuseWrite(ctx context.Context) error {
	if s == nil {
		return nil
	}
	m, err := s.Maintenance(ctx)
	if err != nil {
		return err
	}
	return Refusal(m)
}

// Refusal is the error of a refused write, or nil when m allows writes.
func Refusal(m Maintenance) error {
	if !m.ReadOnly() {
		return nil
	}
	detail := m.Message
	if detail == "" {
		detail = DefaultRefusal
	}
	after := m.RetryAfterSeconds
	if after <= 0 {
		after = DefaultRetryAfterSeconds
	}
	return errs.E(errs.Unavailable, errs.CodeUnavailable, detail, errs.WithRetryAfter(time.Duration(after)*time.Second))
}

func (s *Store) load(ctx context.Context) (State, error) {
	var st State
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		st, err = readState(ctx, q.New(db))
		return err
	})
	if err != nil {
		return State{}, fmt.Errorf("reading the system state: %w", err)
	}
	return st, nil
}

func readState(ctx context.Context, qs *q.Queries) (State, error) {
	var st State
	rows, err := qs.SystemStateRead(ctx, KeyBanner)
	if err != nil {
		return st, err
	}
	if len(rows) == 1 {
		if err := json.Unmarshal([]byte(rows[0]), &st.Banner); err != nil {
			return st, fmt.Errorf("system_state %s is not a banner: %w", KeyBanner, err)
		}
	}
	rows, err = qs.SystemStateRead(ctx, KeyMaintenance)
	if err != nil {
		return st, err
	}
	if len(rows) == 1 {
		if err := json.Unmarshal([]byte(rows[0]), &st.Maintenance); err != nil {
			return st, fmt.Errorf("system_state %s is not a maintenance mode: %w", KeyMaintenance, err)
		}
	}
	if st.Maintenance.Mode == "" {
		st.Maintenance = Maintenance{Mode: ModeNormal, CacheFill: true}
	}
	return st, nil
}

// BannerInput is the request of [Store.SetBanner].
type BannerInput struct {
	Enabled  bool
	Severity string
	Title    string
	Message  string
}

// SetBanner validates and stores the banner, writes system.banner_changed and notes the change in one transaction. A disabled banner
// keeps its text, so that an operator can switch it off and on again; the severity defaults to info.
func (s *Store) SetBanner(ctx context.Context, c Caller, in BannerInput) (Banner, error) {
	b := Banner{Enabled: in.Enabled, Severity: in.Severity, Title: strings.TrimSpace(in.Title), Message: strings.TrimSpace(in.Message)}
	if b.Severity == "" {
		b.Severity = SeverityInfo
	}
	if err := validateBanner(b); err != nil {
		return Banner{}, err
	}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		cur, err := readState(ctx, qs)
		if err != nil {
			return err
		}
		if cur.Banner == b {
			return nil
		}
		if err := write(ctx, qs, KeyBanner, b, c.Actor.ID); err != nil {
			return err
		}
		if err := s.o.Notifier.Notify(tx, cluster.AreaCache, ChangeVersion, map[string]string{"kind": ChangeKind}); err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventBannerChanged, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "system", Name: "banner"},
			Details: mustJSON(map[string]any{"changes": bannerChanges(cur.Banner, b)}),
		})
	})
	if err != nil {
		return Banner{}, err
	}
	return b, nil
}

func validateBanner(b Banner) error {
	var fields []errs.FieldError
	switch b.Severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		fields = append(fields, errs.FieldError{Pointer: "/severity", Code: "invalid_value", Detail: "The severity is info, warning or critical."})
	}
	if utf8.RuneCountInString(b.Title) > MaxTitleRunes {
		fields = append(fields, errs.FieldError{Pointer: "/title", Code: "too_long", Detail: fmt.Sprintf("The title has at most %d characters.", MaxTitleRunes)})
	}
	if utf8.RuneCountInString(b.Message) > MaxMessageRunes {
		fields = append(fields, errs.FieldError{Pointer: "/message", Code: "too_long", Detail: fmt.Sprintf("The message has at most %d characters.", MaxMessageRunes)})
	}
	if b.Enabled && b.Message == "" {
		fields = append(fields, errs.FieldError{Pointer: "/message", Code: "required", Detail: "An enabled banner needs a message."})
	}
	return fieldErrors(fields)
}

// MaintenanceInput is the request of [Store.SetMaintenance]. A nil CacheFill is true, a zero RetryAfterSeconds is the default.
type MaintenanceInput struct {
	Mode              string
	CacheFill         *bool
	Message           string
	RetryAfterSeconds int
}

// SetMaintenance validates and stores the mode, writes system.maintenance_mode_changed and notes the change in one transaction.
// Setting what is stored already changes nothing, writes no event and still answers with the mode; the time and the name of the
// administrator who switched it on stay those of the switch.
func (s *Store) SetMaintenance(ctx context.Context, c Caller, in MaintenanceInput) (Maintenance, error) {
	m := Maintenance{
		Mode: in.Mode, CacheFill: in.CacheFill == nil || *in.CacheFill, Message: strings.TrimSpace(in.Message),
		RetryAfterSeconds: in.RetryAfterSeconds,
	}
	if m.RetryAfterSeconds == 0 {
		m.RetryAfterSeconds = DefaultRetryAfterSeconds
	}
	if err := validateMaintenance(m); err != nil {
		return Maintenance{}, err
	}
	var out Maintenance
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		cur, err := readState(ctx, qs)
		if err != nil {
			return err
		}
		before := cur.Maintenance
		next := m
		switch {
		case m.Mode == ModeNormal:
			next = Maintenance{Mode: ModeNormal, CacheFill: true}
		case before.Mode == ModeReadOnly:
			next.Since, next.By = before.Since, before.By
		default:
			next.Since, next.By = s.o.Now().UTC().Truncate(time.Microsecond), c.Actor.Name
		}
		out = next
		if before.same(next) {
			return nil
		}
		if err := write(ctx, qs, KeyMaintenance, next, c.Actor.ID); err != nil {
			return err
		}
		if err := s.o.Notifier.Notify(tx, cluster.AreaCache, ChangeVersion, map[string]string{"kind": ChangeKind}); err != nil {
			return err
		}
		return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventMaintenanceModeChanged, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "system", Name: "maintenance"},
			Details: mustJSON(map[string]any{"from": before.Mode, "to": next.Mode, "message": next.Message}),
		})
	})
	if err != nil {
		return Maintenance{}, err
	}
	return out, nil
}

func validateMaintenance(m Maintenance) error {
	var fields []errs.FieldError
	switch m.Mode {
	case ModeNormal, ModeReadOnly:
	default:
		fields = append(fields, errs.FieldError{Pointer: "/mode", Code: "invalid_value", Detail: "The mode is normal or read_only."})
	}
	if utf8.RuneCountInString(m.Message) > MaxMaintenanceMessageRunes {
		fields = append(fields, errs.FieldError{Pointer: "/message", Code: "too_long", Detail: fmt.Sprintf("The message has at most %d characters.", MaxMaintenanceMessageRunes)})
	}
	if m.RetryAfterSeconds < 1 || m.RetryAfterSeconds > MaxRetryAfterSeconds {
		fields = append(fields, errs.FieldError{Pointer: "/retryAfterSeconds", Code: "out_of_range", Detail: fmt.Sprintf("The retry hint is between 1 and %d seconds.", MaxRetryAfterSeconds)})
	}
	return fieldErrors(fields)
}

func fieldErrors(f []errs.FieldError) error {
	if len(f) == 0 {
		return nil
	}
	return errs.E(errs.Invalid, errs.CodeValidation, "The request is invalid.", errs.WithFields(f...))
}

// bannerChanges lists the members that differ, by name: the event names what changed, the text itself is public in the banner.
func bannerChanges(before, after Banner) []string {
	out := []string{}
	if before.Enabled != after.Enabled {
		out = append(out, "enabled")
	}
	if before.Severity != after.Severity {
		out = append(out, "severity")
	}
	if before.Title != after.Title {
		out = append(out, "title")
	}
	if before.Message != after.Message {
		out = append(out, "message")
	}
	return out
}

func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("sysstate: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

func write(ctx context.Context, qs *q.Queries, key string, v any, actorID string) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return qs.SystemStateWrite(ctx, q.SystemStateWriteParams{Key: key, Value: string(raw), UpdatedBy: actorID})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
