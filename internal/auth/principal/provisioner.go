package principal

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ProvisionerOptions configure [NewProvisioner].
type ProvisionerOptions struct {
	DB       DB
	Notifier Notifier
	Recorder audit.Recorder
	// BreakGlassName is auth.break_glass.username, a reserved username.
	BreakGlassName string
	Metrics        *Metrics
	// Authz is told of every renamed user; nil tells nobody.
	Authz AuthzHook
}

// PGProvisioner implements [Provisioner] on PostgreSQL (R-AUTH-007, R-AUTH-008).
type PGProvisioner struct {
	db       DB
	notify   Notifier
	rec      audit.Recorder
	reserved []string
	metrics  *Metrics
	authz    AuthzHook
}

var _ Provisioner = (*PGProvisioner)(nil)

// NewProvisioner creates the provisioner.
func NewProvisioner(o ProvisionerOptions) *PGProvisioner {
	p := &PGProvisioner{db: o.DB, notify: o.Notifier, rec: o.Recorder, metrics: o.Metrics, authz: o.Authz}
	if p.rec == nil {
		p.rec = audit.Nop{}
	}
	if o.BreakGlassName != "" {
		p.reserved = []string{o.BreakGlassName}
	}
	return p
}

// errRetry ends a sign-in attempt that lost the race for the identity link: the
// retry finds the identity linked and continues as a returning login
// (R-AUTH-007).
var errRetry = errors.New("principal: identity link raced")

// SignIn implements [Provisioner]. One transaction finds or creates the user,
// completes a pending link or links the new identity, applies the gates of the
// principal tables, updates the profile, renames the user and synchronizes the
// groups; the change area auth and the audit events are part of it.
func (p *PGProvisioner) SignIn(ctx context.Context, c LoginClaims) (Principal, error) {
	if !ValidUserName(c.Username, p.reserved...) {
		return Principal{}, ErrUsernameInvalid
	}
	for attempt := 0; ; attempt++ {
		out, err := p.signInOnce(ctx, c)
		if errors.Is(err, errRetry) && attempt == 0 {
			continue
		}
		if errors.Is(err, errRetry) {
			err = ErrUsernameTaken
		}
		return out, mapSignInErr(err)
	}
}

func mapSignInErr(err error) error {
	switch {
	case err == nil, errors.Is(err, ErrUserDisabled), errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrNotProvisioned), errors.Is(err, ErrUsernameInvalid):
		return err
	}
	return DBErr("principal.SignIn", err)
}

func (p *PGProvisioner) signInOnce(ctx context.Context, c LoginClaims) (Principal, error) {
	var out Principal
	err := p.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		userID, created, err := p.resolveUser(ctx, tx, qs, c)
		if err != nil {
			return err
		}
		rows, err := qs.IdentityPrincipalByID(ctx, userID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errRetry
		}
		pr := rows[0]
		name := pr.Name
		actor := func() audit.Actor { return audit.Actor{Kind: "user", ID: userID, Name: name} }
		subject := func() audit.Subject { return audit.Subject{Kind: "user", ID: userID, Name: name} }

		if pr.Status != string(StatusActive) {
			reasons, err := qs.IdentityUserDisabledReason(ctx, userID)
			if err != nil {
				return err
			}
			if len(reasons) == 0 || reasons[0].Reason != "dormant" {
				return ErrUserDisabled
			}
			// A user disabled only for dormancy is re-enabled by a login the IdP vouched for (R-AUTH-021).
			if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: userID, Status: string(StatusActive)}); err != nil {
				return err
			}
			if err := qs.IdentitySetUserDisabled(ctx, q.IdentitySetUserDisabledParams{ID: userID, Reason: ""}); err != nil {
				return err
			}
			if err := NotifyPrincipals(p.notify, tx, ChangePrincipal, userID); err != nil {
				return err
			}
			if err := p.rec.RecordTx(ctx, tx, audit.Event{
				Type: audit.EventUserEnabled, Outcome: audit.Success, Actor: actor(), Source: c.Source, Subject: subject(),
				Details: details(map[string]string{"source": "dormancy"}),
			}); err != nil {
				return err
			}
		}

		if !created && c.Username != name {
			if renamed, err := p.rename(ctx, tx, qs, c, userID, name); err != nil {
				return err
			} else if renamed {
				name = c.Username
			}
		}
		if err := qs.IdentityRecordLogin(ctx, q.IdentityRecordLoginParams{
			ID: userID, Email: c.Email, EmailVerified: c.EmailVerified, DisplayName: c.DisplayName,
		}); err != nil {
			return err
		}
		claims := map[string]any{"email": c.Email, "displayName": c.DisplayName}
		if c.GroupSync {
			if err := p.syncGroups(ctx, tx, qs, c, userID, name, claims); err != nil {
				return err
			}
		}
		if err := qs.IdentitySetLastClaims(ctx, q.IdentitySetLastClaimsParams{Issuer: c.Issuer, Subject: c.Subject, Claims: string(details(claims))}); err != nil {
			return err
		}
		out = Principal{ID: userID, Kind: KindFromRow(pr.Kind, pr.IsBreakGlass), Name: name, Status: StatusActive}
		return nil
	})
	return out, err
}

// resolveUser finds the user of the identity, completes a pending link, or
// creates the user (R-AUTH-007 "First login of an identity").
func (p *PGProvisioner) resolveUser(ctx context.Context, tx meta.Tx, qs *q.Queries, c LoginClaims) (id string, created bool, err error) {
	rows, err := qs.IdentityUserByIdentity(ctx, q.IdentityUserByIdentityParams{Issuer: c.Issuer, Subject: c.Subject})
	if err != nil {
		return "", false, err
	}
	if len(rows) > 0 {
		return rows[0], false, nil
	}
	claimed, err := qs.IdentityClaimPendingLink(ctx, q.IdentityClaimPendingLinkParams{
		Subject: c.Subject, Issuer: c.Issuer, ProviderID: c.Provider, Username: c.Username,
	})
	if err != nil {
		return "", false, err
	}
	if len(claimed) > 0 {
		return claimed[0], false, nil
	}
	taken, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: c.Username})
	if err != nil {
		return "", false, err
	}
	if len(taken) > 0 {
		// Under READ COMMITTED every statement has its own snapshot: a concurrent first
		// login of this very identity may have committed between the identity lookup above
		// and this one, which then sees its user without having seen its link. Looking the
		// identity up again tells that login from a genuinely taken name (R-AUTH-007).
		again, err := qs.IdentityUserByIdentity(ctx, q.IdentityUserByIdentityParams{Issuer: c.Issuer, Subject: c.Subject})
		if err != nil {
			return "", false, err
		}
		if len(again) > 0 {
			return again[0], false, nil
		}
		return "", false, ErrUsernameTaken
	}
	if !c.JIT {
		return "", false, ErrNotProvisioned
	}
	id = NewID()
	ins, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindUser), Name: c.Username})
	if err != nil {
		return "", false, err
	}
	if len(ins) == 0 {
		// A concurrent transaction took the name between the check and the insert:
		// the retry finds out whether it was the same identity (R-AUTH-007).
		return "", false, errRetry
	}
	if err := qs.IdentityInsertUser(ctx, q.IdentityInsertUserParams{
		ID: id, Email: c.Email, EmailVerified: c.EmailVerified, DisplayName: c.DisplayName, Origin: "jit", LoggedIn: true,
	}); err != nil {
		return "", false, err
	}
	link, err := qs.IdentityInsertIdentity(ctx, q.IdentityInsertIdentityParams{UserID: id, ProviderID: c.Provider, Issuer: c.Issuer, Subject: c.Subject})
	if err != nil {
		return "", false, err
	}
	if len(link) == 0 {
		return "", false, errRetry // another replica linked this identity first
	}
	groups, err := qs.IdentityAutoJoinGroups(ctx)
	if err != nil {
		return "", false, err
	}
	for _, g := range groups {
		if _, err := qs.IdentityAddMembership(ctx, q.IdentityAddMembershipParams{GroupID: g.ID, MemberID: id, Source: "auto"}); err != nil {
			return "", false, err
		}
	}
	err = p.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventUserCreated, Outcome: audit.Success,
		Actor: audit.Actor{Kind: "user", ID: id, Name: c.Username}, Source: c.Source,
		Subject: audit.Subject{Kind: "user", ID: id, Name: c.Username},
		Details: details(map[string]string{"source": "jit", "provider": c.Provider}),
	})
	return id, true, err
}

// rename applies the mapped username of a returning login (R-AUTH-007): with
// sync_username and a free name the user is renamed, otherwise the old name stays
// and user.rename_conflict is recorded.
func (p *PGProvisioner) rename(ctx context.Context, tx meta.Tx, qs *q.Queries, c LoginClaims, id, old string) (bool, error) {
	actor := audit.Actor{Kind: "user", ID: id, Name: old}
	subject := audit.Subject{Kind: "user", ID: id, Name: old}
	if c.SyncUsername {
		rows, err := qs.IdentityRenamePrincipal(ctx, q.IdentityRenamePrincipalParams{ID: id, Name: c.Username})
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			if err := notifyAuthz(ctx, p.authz, tx, id); err != nil {
				return false, err
			}
			if err := NotifyPrincipals(p.notify, tx, ChangePrincipal, id); err != nil {
				return false, err
			}
			subject.Name = c.Username
			return true, p.rec.RecordTx(ctx, tx, audit.Event{
				Type: audit.EventUserRenamed, Outcome: audit.Success, Actor: actor, Source: c.Source, Subject: subject,
				Details: details(map[string]string{"source": "jit", "from": old, "to": c.Username}),
			})
		}
	}
	return false, p.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventUserRenameConflict, Outcome: audit.Failure, Reason: "username_taken", Actor: actor, Source: c.Source, Subject: subject,
		Details: details(map[string]string{"source": "jit", "wanted": c.Username}),
	})
}

// syncGroups makes the user's oidc:<provider> memberships exactly the target set
// of the login (R-AUTH-008). Memberships of the other sources are never touched.
func (p *PGProvisioner) syncGroups(ctx context.Context, tx meta.Tx, qs *q.Queries, c LoginClaims, userID, name string, claims map[string]any) error {
	actor := audit.Actor{Kind: "user", ID: userID, Name: name}
	subject := audit.Subject{Kind: "user", ID: userID, Name: name}
	source := "oidc:" + c.Provider
	missing := c.ClaimState != ClaimPresent
	if missing {
		claims["groupClaimMissing"] = ClaimMissing{Provider: c.Provider, Reason: string(c.ClaimState)}
		p.metrics.claimMissing(c.Provider, string(c.ClaimState))
		if err := p.rec.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventUserGroupsClaimMiss, Outcome: audit.Failure, Reason: string(c.ClaimState), Actor: actor, Source: c.Source, Subject: subject,
			Details: details(map[string]string{"provider": c.Provider, "reason": string(c.ClaimState)}),
		}); err != nil {
			return err
		}
		if c.KeepOnMissing {
			return nil
		}
	} else {
		claims["groups"] = c.Groups
	}

	current, err := qs.IdentityOIDCMemberships(ctx, q.IdentityOIDCMembershipsParams{MemberID: userID, Source: source})
	if err != nil {
		return err
	}
	target := map[string]string{} // name -> id
	if !missing {
		names := validGroupNames(c.Groups)
		found, err := qs.IdentityGroupsByName(ctx, names)
		if err != nil {
			return err
		}
		for _, g := range found {
			target[g.Name] = g.ID
		}
		if c.CreateGroups {
			for _, n := range names {
				if _, ok := target[n]; ok {
					continue
				}
				id, err := p.createOIDCGroup(ctx, qs, n)
				if err != nil {
					return err
				}
				target[n] = id
			}
		}
	}

	have := map[string]string{}
	for _, g := range current {
		have[g.GroupName] = g.GroupID
	}
	var added, removed []string
	for n, id := range target {
		if _, ok := have[n]; ok {
			continue
		}
		if _, err := qs.IdentityAddMembership(ctx, q.IdentityAddMembershipParams{GroupID: id, MemberID: userID, Source: source}); err != nil {
			return err
		}
		added = append(added, n)
	}
	for n, id := range have {
		if _, ok := target[n]; ok {
			continue
		}
		if _, err := qs.IdentityRemoveMembership(ctx, q.IdentityRemoveMembershipParams{GroupID: id, MemberID: userID, Source: source}); err != nil {
			return err
		}
		removed = append(removed, n)
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	sort.Strings(added)
	sort.Strings(removed)
	if err := NotifyPrincipals(p.notify, tx, ChangeMembership, userID); err != nil {
		return err
	}
	type removedRef struct {
		Group  string `json:"group"`
		Source string `json:"source"`
	}
	var entry struct {
		User    string       `json:"user"`
		Removed []removedRef `json:"removed"`
	}
	entry.User, entry.Removed = name, []removedRef{}
	for _, n := range removed {
		entry.Removed = append(entry.Removed, removedRef{Group: n, Source: source})
	}
	return p.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventUserGroupsSynced, Outcome: audit.Success, Actor: actor, Source: c.Source, Subject: subject,
		Details: details(map[string]any{"provider": c.Provider, "added": orEmpty(added), "removed": orEmpty(removed)}),
		Journal: details(entry),
	})
}

// createOIDCGroup creates a missing target group with origin oidc, or finds the
// one a concurrent login created at the same moment (ON CONFLICT DO NOTHING,
// then a read).
func (p *PGProvisioner) createOIDCGroup(ctx context.Context, qs *q.Queries, name string) (string, error) {
	id := NewID()
	ins, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindGroup), Name: name})
	if err != nil {
		return "", err
	}
	if len(ins) > 0 {
		if err := qs.IdentityInsertGroup(ctx, q.IdentityInsertGroupParams{ID: id, Origin: "oidc"}); err != nil {
			return "", err
		}
		return id, nil
	}
	found, err := qs.IdentityGroupsByName(ctx, []string{name})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errRetry
	}
	return found[0].ID, nil
}

// validGroupNames drops values that violate the group-name rule and duplicates.
func validGroupNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, n := range in {
		if ValidGroupName(n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
