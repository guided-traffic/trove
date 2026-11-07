package mainclient_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// TestR_BAS_006_R_BAS_021_ATokenRefusalAnswerToAForwardIsNoRefusalOfTheBastionsToken: the path of a forward is the
// caller's, so a 401 bastion_token_invalid that comes back on it (the caller named a route of the interface)
// says nothing about the Bastion's own token: it is a bad answer, not ErrTokenRefused, and the gauge stays 0
// (Q-BAS-121). The same answer to the handshake is the refusal.
func TestR_BAS_006_R_BAS_021_ATokenRefusalAnswerToAForwardIsNoRefusalOfTheBastionsToken(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil)
	r.main.Revoke(r.token)
	resp, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x"))
	if resp != nil {
		closeBody(t, resp)
	}
	var ae *mainclient.AnswerError
	if errors.Is(err, mainclient.ErrTokenRefused) || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("a forward answered 401 bastion_token_invalid: %v", err)
	}
	if v := maintest.Value(r.m.MainTokenRefused); v != 0 {
		t.Fatalf("trove_bastion_main_token_refused = %v after a forward", v)
	}
	if _, err := r.c.Handshake(ctx); !errors.Is(err, mainclient.ErrTokenRefused) {
		t.Fatalf("the handshake with a revoked token: %v", err)
	}
	if v := maintest.Value(r.m.MainTokenRefused); v != 1 {
		t.Fatalf("trove_bastion_main_token_refused = %v after the handshake", v)
	}
}
