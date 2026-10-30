//go:build integration

package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/meta"
)

// The ...Tx forms of the identity writes (Q-AUTH-46, R-API-014): the caller's
// transaction carries the write, its change, its audit event and the read of the
// record it returns, so a later write of the same transaction sees the rows of
// the earlier one and a rollback takes all of them away.

var errRollback = errors.New("roll the transaction back")

func TestR_API_014_IdentityTxForms(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true})
	ctx := context.Background()

	t.Run("R-API-014 a write inside one transaction sees the principal rows of that transaction", func(t *testing.T) {
		var group principal.Group
		var created token.Created
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			u, err := a.manager.CreateUserTx(ctx, tx, admin, principal.NewUser{Name: "tx-alice", Email: "tx-alice@example.com"})
			if err != nil {
				return err
			}
			// A member that exists only in this transaction resolves.
			group, err = a.manager.CreateGroupTx(ctx, tx, admin, principal.NewGroup{Name: "tx-team", Members: []string{u.Name}})
			if err != nil {
				return err
			}
			sa, err := a.manager.CreateServiceAccountTx(ctx, tx, admin, principal.NewServiceAccount{Name: "tx-bot"})
			if err != nil {
				return err
			}
			// The owner lock and the active count of the token read the service
			// account the transaction created.
			created, err = a.tokens.CreateTx(ctx, tx, token.CreateRequest{
				Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "tx", Via: "api", Admin: true,
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if group.MemberCount != 1 || group.Name != "tx-team" {
			t.Fatalf("the group as the transaction read it: %+v", group)
		}
		if created.Secret == "" || created.Record.Owner.Name != "tx-bot" {
			t.Fatalf("the token as the transaction read it: %+v", created.Record)
		}
		// After the commit every reader sees what the transaction wrote.
		if _, err := a.manager.GetUser(ctx, "tx-alice"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.tokens.Validate(ctx, created.Secret); err != nil {
			t.Fatalf("the committed token does not validate: %v", err)
		}
		for _, ev := range []string{audit.EventUserCreated, audit.EventGroupCreated, audit.EventSACreated, audit.EventTokenCreated} {
			if n := len(a.rec.named(ev)); n != 1 {
				t.Errorf("%s recorded %d times, want once", ev, n)
			}
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action IN ('user.created', 'group.created', 'serviceaccount.created', 'token.created')`); n != 4 {
			t.Errorf("audit rows %d, want 4 written in the transaction", n)
		}
	})

	t.Run("R-API-014 a rolled back transaction leaves no principal, no token and no event", func(t *testing.T) {
		events := e.scalar(`SELECT count(*) FROM audit_event`)
		users := e.scalar(`SELECT count(*) FROM principal`)
		tokens := e.scalar(`SELECT count(*) FROM api_token`)
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			if _, err := a.manager.CreateUserTx(ctx, tx, admin, principal.NewUser{Name: "tx-gone"}); err != nil {
				return err
			}
			sa, err := a.manager.CreateServiceAccountTx(ctx, tx, admin, principal.NewServiceAccount{Name: "tx-gone-bot"})
			if err != nil {
				return err
			}
			if _, err := a.manager.CreateGroupTx(ctx, tx, admin, principal.NewGroup{Name: "tx-gone-team", Members: []string{"tx-gone"}}); err != nil {
				return err
			}
			if _, err := a.tokens.CreateTx(ctx, tx, token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "gone", Via: "api", Admin: true}); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("InTx: %v", err)
		}
		if n := e.scalar(`SELECT count(*) FROM principal`); n != users {
			t.Fatalf("principal rows %d, want %d", n, users)
		}
		if n := e.scalar(`SELECT count(*) FROM api_token`); n != tokens {
			t.Fatalf("token rows %d, want %d", n, tokens)
		}
		if _, err := a.manager.GetUser(ctx, "tx-gone"); err == nil {
			t.Fatal("the rolled back user exists")
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event`); n != events {
			t.Fatalf("audit rows %d, want %d: an event of a rolled back transaction was kept", n, events)
		}
	})

	t.Run("R-API-014 the failures of a Tx form are the domain's: a taken name is a conflict", func(t *testing.T) {
		a.user("tx-taken")
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			_, err := a.manager.CreateUserTx(ctx, tx, admin, principal.NewUser{Name: "tx-taken"})
			return err
		})
		if err == nil {
			t.Fatal("a second user of the same name was created")
		}
		if _, err := a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "Not Valid"}); err == nil {
			t.Fatal("an invalid username was accepted")
		}
		err = a.db.InTx(ctx, func(tx meta.Tx) error {
			_, err := a.manager.CreateUserTx(ctx, tx, admin, principal.NewUser{Name: "Not Valid"})
			return err
		})
		if err == nil {
			t.Fatal("an invalid username was accepted by the Tx form")
		}
	})
}

func TestR_API_014_TokenRotateTxEndsThePredecessorInTheTransaction(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	old := a.pat(alice)

	t.Run("R-API-014 a rotation inside a rolled back transaction leaves the predecessor valid", func(t *testing.T) {
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			if _, err := a.tokens.RotateTx(ctx, tx, token.RotateRequest{Caller: admin, ID: old.Record.ID}); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("InTx: %v", err)
		}
		if _, err := a.tokens.Validate(ctx, old.Secret); err != nil {
			t.Fatalf("the predecessor was ended by a transaction that rolled back: %v", err)
		}
	})

	t.Run("R-API-014 a committed rotation returns the successor and ends the predecessor", func(t *testing.T) {
		var next token.Created
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			var err error
			next, err = a.tokens.RotateTx(ctx, tx, token.RotateRequest{Caller: admin, ID: old.Record.ID})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if next.Record.RotatedFrom != old.Record.ID {
			t.Fatalf("rotatedFrom %q, want %s", next.Record.RotatedFrom, old.Record.ID)
		}
		if _, err := a.tokens.Validate(ctx, next.Secret); err != nil {
			t.Fatalf("the successor does not validate: %v", err)
		}
		if _, err := a.tokens.Validate(ctx, old.Secret); err == nil {
			t.Fatal("the predecessor still validates after an overlap of 0")
		}
	})
}
