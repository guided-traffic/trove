package bastionmain

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The Bastion registrations of the main installation (R-BAS-004): a principal of the kind bastion plus the
// row of the table bastion, the tokens of the type bst that belong to it, and the last contact of each
// replica. The service owns the rules of the page; the management API (internal/api/v1) and the admin
// commands (cmd/trove) call it.

// namePattern is the name rule of R-BAS-004 and of the check principal_bastion_name (migration 00032).
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

// maxDescription is the longest description of a registration or a token, in characters.
const maxDescription = 1024

// ValidName reports whether a string is a registration name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Registration is a Bastion registration as the API shows it.
type Registration struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	CreatedAt   time.Time
	// Revision is the ETag of the registration (principal.revision, R-API-012).
	Revision int64
	// Tokens are the unrevoked tokens, newest first. They hold neither a value nor a hash.
	Tokens []RegistrationToken
	// Contacts are the last contact of each replica, the most recent first (R-BAS-010).
	Contacts []Contact
}

// RegistrationToken is the public record of a Bastion token.
type RegistrationToken struct {
	ID          string
	Hint        string
	Description string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastUsedAt  *time.Time
	// Expired says the expiry has passed although the token is not revoked.
	Expired bool
}

// Contact is the last handshake of one replica of a Bastion (R-BAS-010).
type Contact struct {
	Replica          string
	ContactAt        time.Time
	ClientAddress    string
	Release          string
	InterfaceVersion int
	BaseURL          string
	TokenID          string
}

// NewRegistration is the request of [Registrations.Create].
type NewRegistration struct {
	Name        string
	Description string
	// Source is "api" or "admin-cli", for the audit event.
	Source string
}

// Patch is the request of [Registrations.Update]; a nil member is unchanged.
type Patch struct {
	Description *string
	Enabled     *bool
}

// RegistrationOptions configure [NewRegistrations].
type RegistrationOptions struct {
	DB       principal.DB
	Notifier principal.Notifier
	Recorder audit.Recorder
	// Tokens issues and revokes the Bastion tokens.
	Tokens *token.Service
	// Max is bastions.max_registrations.
	Max int
}

// Registrations is the registration service.
type Registrations struct {
	o RegistrationOptions
}

// NewRegistrations creates the service.
func NewRegistrations(o RegistrationOptions) *Registrations {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &Registrations{o: o}
}

func invalid(pointer, code, detail string) error {
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail}))
}

func checkDescription(pointer, d string) error {
	if utf8.RuneCountInString(d) > maxDescription {
		return invalid(pointer, "too_long", fmt.Sprintf("The description has at most %d characters.", maxDescription))
	}
	return nil
}

// Create registers a Bastion (R-BAS-004): a name by the rule of R-AUTHZ-002, at most bastions.max_registrations
// registrations, event bastion.created.
func (r *Registrations) Create(ctx context.Context, c principal.Caller, in NewRegistration) (Registration, error) {
	if !ValidName(in.Name) {
		return Registration{}, invalid("/name", "invalid_name", "The name must match ^[a-z][a-z0-9-]{0,19}$.")
	}
	if err := checkDescription("/description", in.Description); err != nil {
		return Registration{}, err
	}
	if in.Source == "" {
		in.Source = "api"
	}
	var out Registration
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		// The configuration lock of R-ARCH-017 serializes the creates of all replicas before the count.
		if err := cluster.LockConfigObject(ctx, tx, cluster.ConfigBastionRegistrations); err != nil {
			return err
		}
		qs := principal.Queries(tx)
		n, err := qs.BastionCount(ctx)
		if err != nil {
			return err
		}
		if int(n) >= r.o.Max {
			return errs.E(errs.Conflict, errs.CodeConflict, fmt.Sprintf("%d Bastion registrations exist, the most bastions.max_registrations allows; delete one first.", n))
		}
		id := principal.NewID()
		rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(principal.KindBastion), Name: in.Name, CreatedBy: c.Actor.ID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeAlreadyExists, "A Bastion registration with this name exists.")
		}
		if err := qs.BastionInsert(ctx, q.BastionInsertParams{ID: id, Description: in.Description}); err != nil {
			return err
		}
		if err := r.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventBastionCreated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "bastion", ID: id, Name: in.Name},
			Details: mustJSON(map[string]any{"source": in.Source, "description": in.Description}),
		}); err != nil {
			return err
		}
		out, err = load(ctx, qs, in.Name)
		return err
	})
	if err != nil {
		return Registration{}, principal.DBErr("bastion.create", err)
	}
	return out, nil
}

// load reads a registration with its tokens and contacts; a 404 when there is none.
func load(ctx context.Context, qs *q.Queries, name string) (Registration, error) {
	rows, err := qs.BastionGet(ctx, name)
	if err != nil {
		return Registration{}, err
	}
	if len(rows) == 0 {
		return Registration{}, notFound()
	}
	g := rows[0]
	out := Registration{ID: g.ID, Name: g.Name, Description: g.Description, Enabled: g.Enabled, CreatedAt: g.CreatedAt.Time, Revision: g.Revision}
	toks, err := qs.BastionListTokens(ctx, q.BastionListTokensParams{ID: g.ID})
	if err != nil {
		return Registration{}, err
	}
	out.Tokens = make([]RegistrationToken, 0, len(toks))
	for _, t := range toks {
		out.Tokens = append(out.Tokens, tokenOf(t.ID, t.Hint, t.Description, t.CreatedAt.Time, t.ExpiresAt.Time, t.LastUsedAt.Time, t.LastUsedAt.Valid, t.Expired))
	}
	contacts, err := qs.BastionContactList(ctx, g.ID)
	if err != nil {
		return Registration{}, err
	}
	out.Contacts = make([]Contact, 0, len(contacts))
	for _, ct := range contacts {
		out.Contacts = append(out.Contacts, Contact{
			Replica: ct.Replica, ContactAt: ct.ContactAt.Time, ClientAddress: ct.ClientAddress, Release: ct.Release,
			InterfaceVersion: int(ct.InterfaceVersion), BaseURL: ct.BaseUrl, TokenID: ct.TokenID,
		})
	}
	return out, nil
}

func tokenOf(id, hint, desc string, created, expires, lastUsed time.Time, lastUsedValid, expired bool) RegistrationToken {
	t := RegistrationToken{ID: id, Hint: hint, Description: desc, CreatedAt: created, ExpiresAt: expires, Expired: expired}
	if lastUsedValid {
		t.LastUsedAt = &lastUsed
	}
	return t
}

// notFound is the 404 of a registration that does not exist.
func notFound() error { return principal.NotFound("The Bastion registration") }

// Get returns a registration, or a 404.
func (r *Registrations) Get(ctx context.Context, name string) (Registration, error) {
	var out Registration
	err := r.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		out, err = load(ctx, q.New(db), name)
		return err
	})
	return out, principal.DBErr("bastion.get", err)
}

// List returns every registration ordered by name, with its tokens and contacts: there are at most
// bastions.max_registrations of them, so it is one read.
func (r *Registrations) List(ctx context.Context) ([]Registration, error) {
	var out []Registration
	err := r.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.BastionList(ctx, q.BastionListParams{MaxRows: int32(max(r.o.Max, 1))}) //nolint:gosec // bounded by the configuration (at most 256)
		if err != nil {
			return err
		}
		toks, err := qs.BastionListTokensOfAll(ctx)
		if err != nil {
			return err
		}
		contacts, err := qs.BastionContactListAll(ctx)
		if err != nil {
			return err
		}
		out = make([]Registration, 0, len(rows))
		byID := map[string]int{}
		for _, g := range rows {
			byID[g.ID] = len(out)
			out = append(out, Registration{
				ID: g.ID, Name: g.Name, Description: g.Description, Enabled: g.Enabled, CreatedAt: g.CreatedAt.Time, Revision: g.Revision,
				Tokens: []RegistrationToken{}, Contacts: []Contact{},
			})
		}
		for _, t := range toks {
			if i, ok := byID[t.BastionID]; ok {
				out[i].Tokens = append(out[i].Tokens, tokenOf(t.ID, t.Hint, t.Description, t.CreatedAt.Time, t.ExpiresAt.Time, t.LastUsedAt.Time, t.LastUsedAt.Valid, t.Expired))
			}
		}
		for _, ct := range contacts {
			if i, ok := byID[ct.BastionID]; ok {
				out[i].Contacts = append(out[i].Contacts, Contact{
					Replica: ct.Replica, ContactAt: ct.ContactAt.Time, ClientAddress: ct.ClientAddress, Release: ct.Release,
					InterfaceVersion: int(ct.InterfaceVersion), BaseURL: ct.BaseUrl, TokenID: ct.TokenID,
				})
			}
		}
		return nil
	})
	return out, principal.DBErr("bastion.list", err)
}

// Update changes the description and enables or disables a registration (R-BAS-004). expected is the revision
// the caller loaded (If-Match) or errs.AnyRevision for the blind write. A disabled registration refuses its
// tokens like a disabled service account's (R-AUTH-020) within the revocation bound; enabling it restores them,
// because disabling revokes nothing. A new description records bastion.updated; a disable records
// bastion.disabled with its journal entry, so a restore to a point before it disables the registration again
// (R-HA-054, Q-BAS-145); an enable records bastion.enabled. A patch that changes nothing writes nothing and
// records nothing.
func (r *Registrations) Update(ctx context.Context, c principal.Caller, name string, p Patch, expected int64) (Registration, error) {
	if p.Description != nil {
		if err := checkDescription("/description", *p.Description); err != nil {
			return Registration{}, err
		}
	}
	var out Registration
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		rows, err := qs.BastionLock(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound()
		}
		lock := rows[0]
		if expected != errs.AnyRevision && lock.Revision != expected {
			return errs.Stale("The Bastion registration", lock.Revision)
		}
		cur, err := load(ctx, qs, name)
		if err != nil {
			return err
		}
		subject := audit.Subject{Kind: "bastion", ID: lock.ID, Name: name}
		if p.Description != nil && *p.Description != cur.Description {
			if err := qs.BastionUpdateDescription(ctx, q.BastionUpdateDescriptionParams{ID: lock.ID, Description: *p.Description}); err != nil {
				return err
			}
			if err := r.o.Recorder.RecordTx(ctx, tx, audit.Event{
				Type: audit.EventBastionUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
				Subject: subject, Details: mustJSON(map[string]any{"fields": []string{"description"}}),
			}); err != nil {
				return err
			}
		}
		if p.Enabled != nil && *p.Enabled != cur.Enabled {
			changed, err := qs.BastionSetEnabled(ctx, q.BastionSetEnabledParams{ID: lock.ID, Enabled: *p.Enabled})
			if err != nil {
				return err
			}
			if len(changed) > 0 {
				if err := principal.NotifyPrincipals(r.o.Notifier, tx, principal.ChangePrincipal, lock.ID); err != nil {
					return err
				}
				ev := audit.Event{
					Type: audit.EventBastionEnabled, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
					Subject: subject, Details: mustJSON(map[string]any{}),
				}
				if !*p.Enabled {
					ev.Type, ev.Journal = audit.EventBastionDisabled, registrationEntry(lock.ID, name)
				}
				if err := r.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
					return err
				}
			}
		}
		out, err = load(ctx, qs, name)
		return err
	})
	if err != nil {
		return Registration{}, principal.DBErr("bastion.update", err)
	}
	return out, nil
}

// Delete deletes a registration: its tokens are revoked first (token.revoked with the journal entry of each,
// R-HA-054), then the registration and its contacts go; bastion.deleted is the last event, with the journal entry
// that disables the registration a restore brings back (Q-BAS-145).
func (r *Registrations) Delete(ctx context.Context, c principal.Caller, name string) error {
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		rows, err := qs.BastionLock(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound()
		}
		id := rows[0].ID
		revoked, err := r.o.Tokens.RevokeAllOfBastionTx(ctx, tx, c, id, token.ReasonAdmin)
		if err != nil {
			return err
		}
		if _, err := qs.IdentityDeletePrincipal(ctx, id); err != nil {
			return err
		}
		if err := principal.NotifyPrincipals(r.o.Notifier, tx, principal.ChangePrincipal, id); err != nil {
			return err
		}
		return r.o.Recorder.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventBastionDeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "bastion", ID: id, Name: name}, Details: mustJSON(map[string]any{"tokensRevoked": len(revoked)}),
			Journal: registrationEntry(id, name),
		})
	})
	return principal.DBErr("bastion.delete", err)
}

// registrationEntry is the journal entry of bastion.disabled and bastion.deleted: the registration's principal ID and,
// for display, its name. The replay disables the registration of that ID (R-HA-054, Q-BAS-145).
func registrationEntry(id, name string) json.RawMessage {
	return mustJSON(map[string]string{"principalId": id, "name": name})
}

// TokenRequest is the request of [Registrations.CreateTokenTx].
type TokenRequest struct {
	Description string
	// TTL is the lifetime; zero applies auth.tokens.bst.default_ttl.
	TTL time.Duration
	// Via is created_via: ui, api or admin-cli.
	Via string
}

// CreateTokenTx issues a Bastion token for a registration inside the caller's transaction (R-BAS-004,
// R-AUTH-022): the management API makes the idempotent create one transaction with its idempotency row. The
// secret is in the result only.
func (r *Registrations) CreateTokenTx(ctx context.Context, tx meta.Tx, c principal.Caller, name string, in TokenRequest) (token.Created, error) {
	if utf8.RuneCountInString(in.Description) > maxDescription {
		return token.Created{}, invalid("/description", "too_long", fmt.Sprintf("The description has at most %d characters.", maxDescription))
	}
	qs := principal.Queries(tx)
	rows, err := qs.BastionGet(ctx, name)
	if err != nil {
		return token.Created{}, principal.DBErr("bastion.token", err)
	}
	if len(rows) == 0 {
		return token.Created{}, notFound()
	}
	desc := in.Description
	if desc == "" {
		desc = "Bastion " + name
	}
	return r.o.Tokens.CreateBastionTx(ctx, tx, token.BastionRequest{
		Caller: c, BastionID: rows[0].ID, BastionName: name, Description: desc, TTL: in.TTL, Via: in.Via,
	})
}

// CreateToken is [Registrations.CreateTokenTx] in a transaction of its own (the admin command).
func (r *Registrations) CreateToken(ctx context.Context, c principal.Caller, name string, in TokenRequest) (token.Created, error) {
	var created token.Created
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		var err error
		created, err = r.CreateTokenTx(ctx, tx, c, name, in)
		return err
	})
	if err != nil {
		return token.Created{}, principal.DBErr("bastion.token", err)
	}
	return created, nil
}

// RevokeToken revokes one token of a registration (R-BAS-004); a token that is revoked already is not an error.
func (r *Registrations) RevokeToken(ctx context.Context, c principal.Caller, name, id string) error {
	return r.o.Tokens.RevokeBastionToken(ctx, c, name, id)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the values are maps of strings, numbers and booleans
	}
	return b
}
