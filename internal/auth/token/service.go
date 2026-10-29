package token

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// RecentTokens is the throttle-exemption marker store of R-AUTH-038
// (cluster.TokenSeen). A marker lets a throttled address validate a credential;
// it never authenticates anyone.
type RecentTokens interface {
	// Mark records a credential validated from PostgreSQL.
	Mark(ctx context.Context, secretSHA256 []byte) error
	// Seen reports whether a marker exists; any failure is false.
	Seen(ctx context.Context, secretSHA256 []byte) bool
}

// ServiceOptions configure [NewService].
type ServiceOptions struct {
	DB         principal.DB
	Notifier   principal.Notifier
	Recorder   audit.Recorder
	Principals principal.Store
	Settings   *SettingsStore
	Cache      *cache.Registry
	Gate       principal.Gate
	// Tokens is the token section of the configuration: the YAML lifetimes (the
	// ceilings), the limits per principal, the cache TTL and the retention.
	Tokens config.AuthTokens
	// Scope are the cross-area checks of scope validation.
	Scope ScopeOptions
	// Recent is the throttle-exemption marker store; nil disables markers.
	Recent  RecentTokens
	Metrics *principal.Metrics
	Log     *slog.Logger
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Service is the token domain: format, storage, validation with its cache,
// creation, rotation, revocation, listing and the scheduled tasks (R-AUTH-022 to
// R-AUTH-032). *Service implements [Validator].
type Service struct {
	o       ServiceOptions
	tokens  *cache.Cache[[32]byte, cachedToken]
	flights principal.Flights[[32]byte, cachedToken]
	log     *slog.Logger
}

var _ Validator = (*Service)(nil)

// NewService creates the service and registers the cache auth_tokens.
func NewService(o ServiceOptions) *Service {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Service{o: o, log: o.Log}
	s.tokens = cache.Register(o.Cache, tokenCacheName, cache.Options[[32]byte, cachedToken]{
		MaxEntries: maxCachedTokens,
		SafetyTTL:  o.Tokens.CacheTTL,
		Areas:      []string{cluster.AreaAuth},
		OnChange:   s.onChange,
	})
	return s
}

// Record is a token row as listings and inspections return it (R-AUTH-028). It
// carries neither the secret nor its hash.
type Record struct {
	ID               string
	Type             Type
	Hint             string
	Owner            Owner
	Description      string
	Scope            Scope
	SelfRotatable    bool
	CreatedAt        time.Time
	CreatedBy        string
	CreatedVia       string
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	RevokedBy        string
	RevocationReason string
	RotatedFrom      string
	DerivedFrom      string
	LastUsedAt       *time.Time
	LastUsedAddr     string
	CIContext        json.RawMessage
	// State is active, expired or revoked.
	State string
}

// Owner names the principal that owns a token.
type Owner struct {
	ID   string
	Kind principal.Kind
	Name string
}

// Ref renders the owner as the API's reference: the username, or sa:<name>.
func (o Owner) Ref() string {
	switch o.Kind {
	case principal.KindServiceAccount:
		return "sa:" + o.Name
	case principal.KindCI:
		return "ci:" + o.Name
	}
	return o.Name
}

// Created is the result of Create and Rotate: the record and the secret, which
// exists nowhere else and is never shown again (R-AUTH-022).
type Created struct {
	Record Record
	Secret string
}

// CreateRequest is a token creation (R-AUTH-027).
type CreateRequest struct {
	Caller principal.Caller
	// Type is pat or sat. CI tokens are created by the CI exchange.
	Type Type
	// OwnerID is the principal the token belongs to: the caller for a PAT, the
	// service account for a SAT.
	OwnerID     string
	Description string
	// Scope is the JSON document of R-AUTH-024; empty means the default scope.
	Scope         []byte
	TTL           TTLRequest
	SelfRotatable bool
	// Via is created_via: ui, api, npm-web-login, device-login, admin-cli.
	Via string
	// Admin says the caller is an administrator (a session with recent
	// authentication, a token with admin: true, or the admin CLI): only an
	// administrator may create a non-expiring service-account token.
	Admin bool
	// Parent is the presenting token of a token-authenticated create: the
	// derivation rule of R-AUTH-027 applies.
	Parent *Info
}

func forbidden(code, detail string) error { return errs.E(errs.Forbidden, code, detail) }

// Create creates a PAT or SAT (R-AUTH-027). The creating transaction locks the
// owner's principal row before it counts the owner's active tokens, so
// concurrent creates on different replicas cannot exceed the limit.
func (s *Service) Create(ctx context.Context, r CreateRequest) (Created, error) {
	if r.Type != TypePAT && r.Type != TypeSAT {
		return Created{}, errs.E(errs.Invalid, errs.CodeValidation, "Only personal and service-account tokens are created here.")
	}
	if n := utf8.RuneCountInString(r.Description); n < 1 || n > 1024 {
		return Created{}, errs.E(errs.Invalid, errs.CodeDescriptionRequired, "A description of 1 to 1024 characters is required.")
	}
	scope, err := ParseScope(r.Scope, s.o.Scope)
	if err != nil {
		return Created{}, err
	}
	eff, err := s.o.Settings.Effective(ctx)
	if err != nil {
		return Created{}, err
	}
	now := s.o.Now()
	def, maxTTL := eff.PATDefaultTTL, eff.PATMaxTTL
	if r.Type == TypeSAT {
		def, maxTTL = eff.SATDefaultTTL, eff.SATMaxTTL
	}
	var parentExpires *time.Time
	if r.Parent != nil {
		if err := checkDerivation(r, scope); err != nil {
			return Created{}, err
		}
		parentExpires = r.Parent.ExpiresAt
	}
	life, err := resolveLifetime(r.Type, r.TTL, now, def, maxTTL, s.o.Tokens.SAT.AllowNonExpiring, r.Admin, r.Parent != nil, parentExpires)
	if err != nil {
		return Created{}, err
	}
	if r.Parent != nil && parentExpires != nil && !life.nonExpiring && now.Add(life.ttl).After(*parentExpires) {
		return Created{}, forbidden(errs.CodeDerivationExceedsParent, "The new token would outlive the presenting token.")
	}
	if r.Type == TypePAT {
		if err := s.checkPATAllowed(ctx, r.OwnerID); err != nil {
			return Created{}, err
		}
	}
	return s.insert(ctx, insertSpec{
		req: r, scope: scope, life: life, maxActive: s.maxActive(r.Type), via: r.Via,
		derivedFrom: derivedID(r.Parent), capMicros: capMicros(parentExpires), presented: derivedID(r.Parent),
	})
}

func derivedID(p *Info) string {
	if p == nil {
		return ""
	}
	return p.ID
}

func (s *Service) maxActive(t Type) int {
	if t == TypeSAT {
		return s.o.Tokens.MaxActive.SAT
	}
	return s.o.Tokens.MaxActive.PAT
}

// checkDerivation applies the derivation rule of R-AUTH-027 that does not need
// the lifetime: type, owner and scope.
func checkDerivation(r CreateRequest, scope Scope) error {
	p := r.Parent
	switch {
	case p.Type == TypeCIT:
		return forbidden(errs.CodeCITokenRestricted, "A CI token cannot create tokens.")
	case r.Type != p.Type || r.OwnerID != p.Owner.ID:
		return forbidden(errs.CodeForbidden, "A token creates tokens of its own type and owner only.")
	case !scope.Within(p.Scope):
		return forbidden(errs.CodeDerivationExceedsParent, "The scope is broader than the presenting token's.")
	}
	return nil
}

// checkPATAllowed implements auth.tokens.pat.allowed_groups (R-AUTH-017).
func (s *Service) checkPATAllowed(ctx context.Context, ownerID string) error {
	allowed := s.o.Tokens.PAT.AllowedGroups
	if len(allowed) == 0 {
		return nil
	}
	groups, err := s.o.Principals.Groups(ctx, ownerID)
	if err != nil {
		return principal.DBErr("token.create", err)
	}
	for _, g := range groups {
		if slices.Contains(allowed, g.Name) {
			return nil
		}
	}
	return forbidden(errs.CodePATNotAllowed, "Personal tokens are restricted to members of the configured groups.")
}

type insertSpec struct {
	req       CreateRequest
	scope     Scope
	life      lifetime
	maxActive int
	via       string
	// derivedFrom is the token the new one is derived from: the presenting token
	// of a create, or the lineage a rotation carries over (R-AUTH-027, R-AUTH-030).
	derivedFrom string
	// capMicros is the expiry of the presenting token in Unix microseconds, 0 for none.
	capMicros int64
	// presented is the ID of the presenting token, "" for none.
	presented   string
	rotatedFrom string
	// afterInsert runs in the creating transaction (rotation ends the predecessor).
	afterInsert func(ctx context.Context, tx meta.Tx, qs *q.Queries, created Record) error
}

// lineageCheck locks the tokens the new one hangs on, derivedFrom and
// rotatedFrom with their derived_from chains, and decides on what it finds
// after the lock (R-AUTH-027, R-AUTH-029, R-AUTH-030): every one must still be
// live, the rotated token must not have a successor yet, a derived token may
// rotate itself only if its parent may, and the new token expires no later than
// any token it is derived from. It returns the expiry cap in Unix microseconds,
// 0 for none.
func (sp insertSpec) lineageCheck(ctx context.Context, qs *q.Queries) (int64, error) {
	var ids []string
	for _, id := range []string{sp.derivedFrom, sp.rotatedFrom} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return sp.capMicros, nil
	}
	locked, err := qs.IdentityLockTokenLineage(ctx, ids)
	if err != nil {
		return 0, err
	}
	facts, err := qs.IdentityTokenLineageFacts(ctx, locked)
	if err != nil {
		return 0, err
	}
	gone := func(id string) error {
		if id == sp.presented {
			return errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The presenting token is no longer valid.")
		}
		return principal.NotFound("The token")
	}
	found := map[string]bool{}
	limit := sp.capMicros
	for _, f := range facts {
		found[f.ID] = true
		switch {
		case !f.Live && (f.ID == sp.rotatedFrom || f.ID == sp.derivedFrom):
			return 0, gone(f.ID)
		case !f.Live && sp.rotatedFrom != "":
			// An ancestor further up the rotated token's lineage.
			return 0, principal.NotFound("The token")
		case !f.Live:
			return 0, gone(sp.derivedFrom)
		case f.ID == sp.rotatedFrom && f.Rotated:
			return 0, principal.NotFound("The token")
		case f.ID == sp.derivedFrom && sp.presented != "" && sp.rotatedFrom == "" && sp.req.SelfRotatable && !f.SelfRotatable:
			return 0, forbidden(errs.CodeDerivationExceedsParent, "Only a token that may rotate itself creates one that may.")
		}
		if f.ID != sp.rotatedFrom && f.ExpiresUs > 0 && (limit == 0 || f.ExpiresUs < limit) {
			limit = f.ExpiresUs
		}
	}
	for _, id := range ids {
		if !found[id] {
			return 0, gone(id)
		}
	}
	return limit, nil
}

func (s *Service) insert(ctx context.Context, sp insertSpec) (Created, error) {
	r := sp.req
	secret, err := Generate(r.Type)
	if err != nil {
		return Created{}, principal.DBErr("token.generate", err)
	}
	hash := Hash(secret)
	id := principal.NewID()
	ttl := sp.life.ttl.Seconds()
	if sp.life.nonExpiring {
		ttl = -1
	}
	var rec Record
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		owner, err := lockOwner(ctx, qs, r.OwnerID, r.Type)
		if err != nil {
			return err
		}
		capAt, err := sp.lineageCheck(ctx, qs)
		if err != nil {
			return err
		}
		n, err := qs.IdentityCountActiveTokens(ctx, q.IdentityCountActiveTokensParams{PrincipalID: r.OwnerID, Type: string(r.Type)})
		if err != nil {
			return err
		}
		if sp.rotatedFrom != "" {
			// The predecessor is live (lineageCheck) and counted; its successor
			// replaces it, so a rotation goes over the limit by its own overlap
			// at most, never by a chain of overlapping rotations.
			n--
		}
		if int(n) >= sp.maxActive {
			return errs.E(errs.Conflict, errs.CodeTokenLimitReached, "The principal holds the maximum number of active tokens.")
		}
		row, err := qs.IdentityInsertToken(ctx, q.IdentityInsertTokenParams{
			ID: id, Type: string(r.Type), SecretSha256: hash[:], Hint: Hint(secret), PrincipalID: r.OwnerID,
			Description: r.Description, Scope: string(sp.scope.Marshal()), SelfRotatable: r.SelfRotatable,
			CreatedBy: r.Caller.Actor.ID, CreatedVia: sp.via, TtlSecs: ttl, CapMicros: capAt, RotatedFrom: sp.rotatedFrom, DerivedFrom: sp.derivedFrom,
		})
		if err != nil {
			return err
		}
		rec = Record{
			ID: id, Type: r.Type, Hint: Hint(secret), Owner: owner, Description: r.Description, Scope: sp.scope,
			SelfRotatable: r.SelfRotatable, CreatedAt: row.CreatedAt.Time, CreatedBy: r.Caller.Actor.ID, CreatedVia: sp.via,
			RotatedFrom: sp.rotatedFrom, DerivedFrom: sp.derivedFrom, State: "active",
		}
		if row.ExpiresAt.Valid {
			t := row.ExpiresAt.Time
			rec.ExpiresAt = &t
		}
		if sp.afterInsert != nil {
			if err := sp.afterInsert(ctx, tx, qs, rec); err != nil {
				return err
			}
		}
		var exp any
		if rec.ExpiresAt != nil {
			exp = rec.ExpiresAt.UTC().Format(time.RFC3339)
		}
		d := map[string]any{
			"type": string(r.Type), "owner": owner.Ref(), "scope": json.RawMessage(sp.scope.Marshal()),
			"expiresAt": exp, "createdVia": sp.via, "description": r.Description,
		}
		if sp.derivedFrom != "" {
			d["derivedFrom"] = sp.derivedFrom
		}
		if sp.rotatedFrom != "" {
			d["rotatedFrom"] = sp.rotatedFrom
		}
		return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventTokenCreated, Outcome: audit.Success, Actor: r.Caller.Actor, Source: r.Caller.Source,
			Subject: audit.Subject{Kind: "token", ID: id, Name: rec.Hint}, Details: mustJSON(d),
		})
	})
	if err != nil {
		return Created{}, principal.DBErr("token.create", err)
	}
	return Created{Record: rec, Secret: secret}, nil
}

// lockOwner locks the owner's principal row and checks it can hold a token of
// the type (R-AUTH-027): a PAT belongs to a user, a SAT to a service account;
// the break-glass account holds none.
func lockOwner(ctx context.Context, qs *q.Queries, ownerID string, t Type) (Owner, error) {
	if !principal.IsID(ownerID) {
		return Owner{}, principal.NotFound("The owner")
	}
	if rows, err := qs.IdentityLockPrincipal(ctx, ownerID); err != nil {
		return Owner{}, err
	} else if len(rows) == 0 {
		return Owner{}, principal.NotFound("The owner")
	}
	rows, err := qs.IdentityPrincipalByID(ctx, ownerID)
	if err != nil {
		return Owner{}, err
	}
	p := rows[0]
	want := principal.KindUser
	if t == TypeSAT {
		want = principal.KindServiceAccount
	}
	switch {
	case p.IsBreakGlass:
		return Owner{}, principal.ErrBreakGlass
	case principal.Kind(p.Kind) != want:
		return Owner{}, errs.E(errs.Invalid, errs.CodeValidation, "A token of this type cannot belong to this principal.")
	case p.Status != string(principal.StatusActive):
		return Owner{}, forbidden(errs.CodeForbidden, "The principal is disabled.")
	}
	return Owner{ID: p.ID, Kind: want, Name: p.Name}, nil
}

func recordFrom(id, typ, hint, ownerID, ownerKind, ownerName, desc string, scope []byte, selfRot bool, created time.Time,
	createdBy, via string, expires, revoked time.Time, expiresValid, revokedValid bool, revokedBy, reason, rotatedFrom, derivedFrom string,
	lastUsed time.Time, lastUsedValid bool, addr string, expired bool, ci []byte,
) Record {
	sc, err := ParseStoredScope(scope)
	if err != nil {
		sc = Scope{}
	}
	r := Record{
		ID: id, Type: Type(typ), Hint: hint, Owner: Owner{ID: ownerID, Kind: principal.Kind(ownerKind), Name: ownerName},
		Description: desc, Scope: sc, SelfRotatable: selfRot, CreatedAt: created, CreatedBy: createdBy, CreatedVia: via,
		RevokedBy: revokedBy, RevocationReason: reason, RotatedFrom: rotatedFrom, DerivedFrom: derivedFrom, LastUsedAddr: addr,
		CIContext: ci, State: "active",
	}
	if ownerKind == "ci_mapping" {
		r.Owner.Kind = principal.KindCI
	}
	if expiresValid {
		r.ExpiresAt = &expires
	}
	if revokedValid {
		r.RevokedAt = &revoked
		r.State = "revoked"
	} else if expired {
		r.State = "expired"
	}
	if lastUsedValid {
		r.LastUsedAt = &lastUsed
	}
	return r
}

// Get returns one token, or a 404 (R-AUTH-028). Whether the caller may see it is
// the caller's decision; a token the caller may not see is answered with the
// same 404.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	if !principal.IsID(id) {
		return Record{}, principal.NotFound("The token")
	}
	var rec Record
	found := false
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityTokenRow(ctx, id)
		if err != nil || len(rows) == 0 {
			return err
		}
		r := rows[0]
		rec = recordFrom(r.ID, r.Type, r.Hint, r.OwnerID, r.OwnerKind, r.OwnerName, r.Description, r.Scope, r.SelfRotatable,
			r.CreatedAt.Time, r.CreatedBy, r.CreatedVia, r.ExpiresAt.Time, r.RevokedAt.Time, r.ExpiresAt.Valid, r.RevokedAt.Valid,
			r.RevokedBy, r.RevocationReason, r.RotatedFrom, r.DerivedFrom, r.LastUsedAt.Time, r.LastUsedAt.Valid, r.LastUsedAddr, r.Expired, r.CiContext)
		found = true
		return nil
	})
	if err != nil {
		return Record{}, principal.DBErr("token.get", err)
	}
	if !found {
		return Record{}, principal.NotFound("The token")
	}
	return rec, nil
}

// ListFilter filters List (R-AUTH-028).
type ListFilter struct {
	// OwnerID restricts the list to one principal; a non-administrator's list is
	// forced to the caller by the caller of List.
	OwnerID string
	Type    Type
	// State is active (default), expired, revoked or all.
	State          string
	ExpiresBefore  time.Time
	ExpiresAfter   time.Time
	LastUsedBefore time.Time
	// After is the ID of the last token of the previous page; the order is the
	// ID descending, which is newest first (UUIDv7).
	After string
	// Limit is the maximum number of rows; ask for one more than the page size.
	Limit int
}

// List lists tokens, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Record, error) {
	state := f.State
	if state == "" {
		state = "active"
	}
	unix := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	limit := f.Limit
	if limit <= 0 || limit > 1001 {
		limit = 101
	}
	var out []Record
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityListTokens(ctx, q.IdentityListTokensParams{
			After: f.After, OwnerID: f.OwnerID, Type: string(f.Type), State: state,
			ExpiresBeforeUnix: unix(f.ExpiresBefore), ExpiresAfterUnix: unix(f.ExpiresAfter), LastUsedBeforeUnix: unix(f.LastUsedBefore),
			MaxRows: int32(limit), //nolint:gosec // bounded above
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			out = append(out, recordFrom(r.ID, r.Type, r.Hint, r.OwnerID, r.OwnerKind, r.OwnerName, r.Description, r.Scope, r.SelfRotatable,
				r.CreatedAt.Time, r.CreatedBy, r.CreatedVia, r.ExpiresAt.Time, r.RevokedAt.Time, r.ExpiresAt.Valid, r.RevokedAt.Valid,
				r.RevokedBy, r.RevocationReason, r.RotatedFrom, r.DerivedFrom, r.LastUsedAt.Time, r.LastUsedAt.Valid, r.LastUsedAddr, r.Expired, r.CiContext))
		}
		return nil
	})
	return out, principal.DBErr("token.list", err)
}

func capMicros(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMicro()
}
