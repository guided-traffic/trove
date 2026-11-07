//go:build integration

package bastionreg

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

const bastions = "/api/v1/bastions"

func tokensOf(t *testing.T, rp reply) []any {
	t.Helper()
	toks, _ := rp.json(t)["tokens"].([]any)
	return toks
}

// TestR_BAS_004_RegisterIssueAndLimit is the first acceptance criterion of R-BAS-004: a registration, then a
// token of one year that is 57 characters long with the prefix trv_bst_ and passes the checksum of R-AUTH-022;
// ttlSeconds above max_ttl answers 400 invalid_ttl, and a fourth unrevoked token 409 token_limit_reached.
func TestR_BAS_004_RegisterIssueAndLimit(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})

	rp := r.call("POST", bastions, c.admin, map[string]any{"name": "partners", "description": "The partner network"})
	want(t, rp, http.StatusCreated, "")
	reg := rp.json(t)
	if reg["name"] != "partners" || reg["enabled"] != true || reg["description"] != "The partner network" ||
		len(tokensOf(t, rp)) != 0 || rp.Header.Get("ETag") == "" || rp.Header.Get("Location") != baseURL+bastions+"/partners" {
		t.Fatalf("registration = %v, headers %v", reg, rp.Header)
	}

	t.Run("a duplicate name is 409 already_exists", func(t *testing.T) {
		want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusConflict, errs.CodeAlreadyExists)
	})
	t.Run("a name outside the rule is 422", func(t *testing.T) {
		for _, name := range []string{"Partners", "1partners", "a_b", "abcdefghijklmnopqrstu", "-a", ""} {
			rp := r.call("POST", bastions, c.admin, map[string]any{"name": name})
			if rp.Status != http.StatusUnprocessableEntity && rp.Status != http.StatusBadRequest {
				t.Errorf("name %q: %d %s", name, rp.Status, rp.Body)
			}
		}
	})

	t.Run("a one-year token is 57 characters with the prefix and a valid checksum", func(t *testing.T) {
		rp := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{"ttlSeconds": 31536000})
		want(t, rp, http.StatusCreated, "")
		if rp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
		}
		body := rp.json(t)
		secret, _ := body["token"].(string)
		typ, err := token.Check(secret)
		if err != nil || typ != token.TypeBST || len(secret) != 57 || !strings.HasPrefix(secret, "trv_bst_") {
			t.Fatalf("token %q: type %v, err %v", secret, typ, err)
		}
		if body["hint"] != secret[:12] {
			t.Errorf("hint = %v, want %s", body["hint"], secret[:12])
		}
		exp, err := time.Parse(time.RFC3339, body["expiresAt"].(string))
		if err != nil || time.Until(exp) < 364*24*time.Hour || time.Until(exp) > 366*24*time.Hour {
			t.Errorf("expiresAt = %v (%v), want one year ahead", body["expiresAt"], err)
		}
		// The secret is in this answer only: neither the registration nor the list shows it.
		for _, p := range []string{bastions + "/partners", bastions} {
			if got := r.call("GET", p, c.admin, nil); strings.Contains(string(got.Body), secret) || got.Status != http.StatusOK {
				t.Fatalf("GET %s: %d, the body holds the secret: %v", p, got.Status, strings.Contains(string(got.Body), secret))
			}
		}
	})

	t.Run("ttlSeconds above max_ttl is 400 invalid_ttl, as are 0, a negative value and 59 seconds", func(t *testing.T) {
		maxSecs := int64(9480 * 3600)
		for _, secs := range []int64{maxSecs + 1, 0, -5, 59} {
			want(t, r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{"ttlSeconds": secs}), http.StatusBadRequest, errs.CodeInvalidTTL)
		}
		// The ceiling itself is allowed.
		want(t, r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{"ttlSeconds": maxSecs}), http.StatusCreated, "")
	})

	t.Run("a fourth unrevoked token is 409 token_limit_reached, and a revocation makes room", func(t *testing.T) {
		// Two exist already (the one-year token and the one at the ceiling).
		want(t, r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}), http.StatusCreated, "")
		want(t, r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}), http.StatusConflict, errs.CodeTokenLimitReached)
		toks := tokensOf(t, r.call("GET", bastions+"/partners", c.admin, nil))
		if len(toks) != 3 {
			t.Fatalf("tokens = %d, want 3", len(toks))
		}
		id := toks[0].(map[string]any)["id"].(string)
		want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.admin, nil), http.StatusNoContent, "")
		want(t, r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}), http.StatusCreated, "")
	})

	t.Run("the default lifetime applies without ttlSeconds, the description without a description", func(t *testing.T) {
		e2 := newEnv(t)
		r2, c2 := e2.newCast("web-1", replicaOpts{})
		want(t, r2.call("POST", bastions, c2.admin, map[string]any{"name": "dmz"}), http.StatusCreated, "")
		rp := r2.call("POST", bastions+"/dmz/tokens", c2.admin, map[string]any{})
		want(t, rp, http.StatusCreated, "")
		exp, _ := time.Parse(time.RFC3339, rp.json(t)["expiresAt"].(string))
		if d := time.Until(exp); d < 8759*time.Hour || d > 8761*time.Hour {
			t.Errorf("default lifetime = %v, want 8760h", d)
		}
		if rp.json(t)["description"] != "Bastion dmz" {
			t.Errorf("description = %v", rp.json(t)["description"])
		}
	})

	t.Run("a token of an unknown registration is 404", func(t *testing.T) {
		want(t, r.call("POST", bastions+"/ghost/tokens", c.admin, map[string]any{}), http.StatusNotFound, "")
	})

	t.Run("an idempotent replay of an issuance is 409 token_already_issued: the value is not stored", func(t *testing.T) {
		e2 := newEnv(t)
		r2, c2 := e2.newCast("web-1", replicaOpts{})
		want(t, r2.call("POST", bastions, c2.admin, map[string]any{"name": "dmz"}), http.StatusCreated, "")
		first := r2.call("POST", bastions+"/dmz/tokens", c2.admin, map[string]any{}, "Idempotency-Key", "k-1")
		want(t, first, http.StatusCreated, "")
		again := r2.call("POST", bastions+"/dmz/tokens", c2.admin, map[string]any{}, "Idempotency-Key", "k-1")
		want(t, again, http.StatusConflict, errs.CodeTokenAlreadyIssued)
		if strings.Contains(string(again.Body), first.json(t)["token"].(string)) {
			t.Fatal("the replay shows the secret")
		}
		if n := len(tokensOf(t, r2.call("GET", bastions+"/dmz", c2.admin, nil))); n != 1 {
			t.Fatalf("tokens = %d after a replay, want 1", n)
		}
	})
}

// TestR_BAS_004_TheLimitOfRegistrations: at most bastions.max_registrations exist, also when creates race.
func TestR_BAS_004_TheLimitOfRegistrations(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{maxRegistrations: 3})
	for _, n := range []string{"one", "two", "three"} {
		want(t, r.call("POST", bastions, c.admin, map[string]any{"name": n}), http.StatusCreated, "")
	}
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "four"}), http.StatusConflict, errs.CodeConflict)
	want(t, r.call("DELETE", bastions+"/one", c.admin, nil), http.StatusNoContent, "")
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "four"}), http.StatusCreated, "")

	t.Run("concurrent creates cannot pass the limit", func(t *testing.T) {
		e2 := newEnv(t)
		r2, c2 := e2.newCast("web-1", replicaOpts{maxRegistrations: 2})
		results := make(chan int, 6)
		for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
			go func() { results <- r2.call("POST", bastions, c2.admin, map[string]any{"name": n}).Status }()
		}
		created := 0
		for range 6 {
			if <-results == http.StatusCreated {
				created++
			}
		}
		if created != 2 || e2.number(`SELECT count(*) FROM principal WHERE kind = 'bastion'`) != 2 {
			t.Fatalf("created %d registrations with a limit of 2", created)
		}
	})
}

// TestR_BAS_004_TheListShowsTheTokensAndTheLastContacts: the list and the registration carry the tokens (ID,
// hint, expiresAt, createdAt, lastUsedAt) and the last contact of each replica; revoked tokens are not shown.
func TestR_BAS_004_TheListShowsTheTokensAndTheLastContacts(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusCreated, "")
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "dmz"}), http.StatusCreated, "")
	tok := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{"description": "partners 2026"}).json(t)
	revoked := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}).json(t)
	want(t, r.call("DELETE", bastions+"/partners/tokens/"+revoked["id"].(string), c.admin, nil), http.StatusNoContent, "")

	id := e.text(`SELECT id::text FROM principal WHERE kind = 'bastion' AND name = 'partners'`)
	e.exec(`INSERT INTO bastion_contact (bastion_id, replica, contact_at, client_address, release, interface_version, base_url)
	        VALUES ($1::uuid, 'trove-bastion-a', now() - interval '5 minutes', '198.51.100.20', '0.15.0', 1, 'https://packages.example.com'),
	               ($1::uuid, 'trove-bastion-b', now(), NULL, '0.15.0', 1, '')`, id)

	check := func(t *testing.T, reg map[string]any) {
		t.Helper()
		toks, _ := reg["tokens"].([]any)
		if len(toks) != 1 {
			t.Fatalf("tokens = %v, want the one unrevoked token", toks)
		}
		got := toks[0].(map[string]any)
		for _, k := range []string{"id", "hint", "description", "createdAt", "expiresAt", "lastUsedAt"} {
			if _, ok := got[k]; !ok {
				t.Errorf("token lacks %s: %v", k, got)
			}
		}
		if got["id"] != tok["id"] || got["hint"] != tok["hint"] || got["lastUsedAt"] != nil || got["description"] != "partners 2026" {
			t.Errorf("token = %v, want the issued one", got)
		}
		if _, ok := got["token"]; ok {
			t.Error("the token value is part of the registration")
		}
		contacts, _ := reg["contacts"].([]any)
		if len(contacts) != 2 {
			t.Fatalf("contacts = %v", contacts)
		}
		newest, older := contacts[0].(map[string]any), contacts[1].(map[string]any)
		if newest["replica"] != "trove-bastion-b" || newest["clientAddress"] != nil || older["replica"] != "trove-bastion-a" ||
			older["clientAddress"] != "198.51.100.20" || older["release"] != "0.15.0" || older["interfaceVersion"] != float64(1) ||
			older["baseUrl"] != "https://packages.example.com" {
			t.Errorf("contacts = %v", contacts)
		}
	}
	t.Run("GET /bastions/{name}", func(t *testing.T) {
		rp := r.call("GET", bastions+"/partners", c.admin, nil)
		want(t, rp, http.StatusOK, "")
		check(t, rp.json(t))
		// The ETag is the revision: a second read with it is 304.
		want(t, r.call("GET", bastions+"/partners", c.admin, nil, "If-None-Match", rp.Header.Get("ETag")), http.StatusNotModified, "")
	})
	t.Run("GET /bastions orders by name and shows the same", func(t *testing.T) {
		rp := r.call("GET", bastions, c.admin, nil)
		want(t, rp, http.StatusOK, "")
		items, _ := rp.json(t)["items"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["name"] != "dmz" || items[1].(map[string]any)["name"] != "partners" {
			t.Fatalf("items = %v", items)
		}
		if n := len(items[0].(map[string]any)["tokens"].([]any)) + len(items[0].(map[string]any)["contacts"].([]any)); n != 0 {
			t.Errorf("dmz shows %d tokens and contacts", n)
		}
		check(t, items[1].(map[string]any))
	})
	t.Run("an unknown registration is 404", func(t *testing.T) {
		want(t, r.call("GET", bastions+"/ghost", c.admin, nil), http.StatusNotFound, "")
	})
}

// TestR_BAS_004_PatchDisableAndTheRevision: the description and enabled change with If-Match; a stale tag is
// 412, none is 428, a patch that changes nothing records no event, and null never switches a registration off.
func TestR_BAS_004_PatchDisableAndTheRevision(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	created := r.call("POST", bastions, c.admin, map[string]any{"name": "partners", "description": "old"})
	etag := created.Header.Get("ETag")

	want(t, r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"description": "new"}), http.StatusPreconditionRequired, "")
	rp := r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"description": "new", "enabled": false}, "If-Match", etag)
	want(t, rp, http.StatusOK, "")
	if got := rp.json(t); got["description"] != "new" || got["enabled"] != false || rp.Header.Get("ETag") == etag {
		t.Fatalf("after the patch: %v, ETag %q", got, rp.Header.Get("ETag"))
	}
	want(t, r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"description": "again"}, "If-Match", etag), http.StatusPreconditionFailed, "")
	next := rp.Header.Get("ETag")

	t.Run("a patch that changes nothing records no event", func(t *testing.T) {
		before := len(e.events(audit.EventBastionUpdated))
		want(t, r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"description": "new", "enabled": false}, "If-Match", next), http.StatusOK, "")
		if after := len(e.events(audit.EventBastionUpdated)); after != before {
			t.Fatalf("bastion.updated: %d -> %d", before, after)
		}
	})
	t.Run("a string is refused for enabled, and a null changes nothing", func(t *testing.T) {
		rp := r.call("PATCH", bastions+"/partners", c.admin, `{"enabled": "yes"}`, "If-Match", next)
		if rp.Status != http.StatusUnprocessableEntity && rp.Status != http.StatusBadRequest {
			t.Errorf("a string: %d %s", rp.Status, rp.Body)
		}
		want(t, r.call("PATCH", bastions+"/partners", c.admin, `{"enabled": null}`, "If-Match", next), http.StatusOK, "")
		if got := r.call("GET", bastions+"/partners", c.admin, nil).json(t); got["enabled"] != false {
			t.Fatalf("enabled = %v", got["enabled"])
		}
	})
	t.Run("the read-only members are accepted and ignored", func(t *testing.T) {
		rp := r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"name": "other", "tokens": []any{}, "enabled": true}, "If-Match", next)
		want(t, rp, http.StatusOK, "")
		if got := rp.json(t); got["name"] != "partners" || got["enabled"] != true {
			t.Fatalf("after: %v", got)
		}
	})
	t.Run("the description, the disable and the enable are events of their own; the disable is journaled", func(t *testing.T) {
		upd, off, on := e.events(audit.EventBastionUpdated), e.events(audit.EventBastionDisabled), e.events(audit.EventBastionEnabled)
		if len(upd) != 1 || upd[0].ResourceType != "bastion" || fmt.Sprint(upd[0].Details["fields"]) != "[description]" {
			t.Fatalf("bastion.updated = %+v", upd)
		}
		if len(off) != 1 || off[0].ResourceType != "bastion" || len(on) != 1 || on[0].ResourceType != "bastion" {
			t.Fatalf("bastion.disabled = %+v, bastion.enabled = %+v", off, on)
		}
		bid := e.text(`SELECT id::text FROM principal WHERE kind = 'bastion' AND name = 'partners'`)
		if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event = 'bastion.disabled' AND entry ->> 'principalId' = $1 AND entry ->> 'name' = 'partners'`, bid); n != 1 {
			t.Fatalf("journal entries of the disable = %d, want 1 (R-HA-054, Q-BAS-145)", n)
		}
		if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event IN ('bastion.updated', 'bastion.enabled')`); n != 0 {
			t.Fatalf("%d journal entries of the description or the enable, want none", n)
		}
	})
}

// TestR_BAS_004_DeleteRevokesTheTokensAndJournalsThem: deleting a registration revokes its tokens
// (token.revoked with resource_type bastion and the journal entry of R-HA-054, one per token), removes the
// registration and its contacts, and records bastion.deleted.
func TestR_BAS_004_DeleteRevokesTheTokensAndJournalsThem(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusCreated, "")
	var secrets, ids []string
	for range 2 {
		b := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}).json(t)
		secrets, ids = append(secrets, b["token"].(string)), append(ids, b["id"].(string))
	}
	bid := e.text(`SELECT id::text FROM principal WHERE kind = 'bastion' AND name = 'partners'`)
	e.exec(`INSERT INTO bastion_contact (bastion_id, replica, contact_at, interface_version) VALUES ($1::uuid, 'a', now(), 1)`, bid)
	if _, err := r.auth.Authenticate(context.Background(), secrets[0]); err != nil {
		t.Fatalf("the token does not authenticate before the delete: %v", err)
	}

	want(t, r.call("DELETE", bastions+"/partners", c.admin, nil), http.StatusNoContent, "")
	want(t, r.call("GET", bastions+"/partners", c.admin, nil), http.StatusNotFound, "")
	want(t, r.call("DELETE", bastions+"/partners", c.admin, nil), http.StatusNotFound, "")
	if n := e.number(`SELECT count(*) FROM bastion_contact`) + e.number(`SELECT count(*) FROM bastion`) + e.number(`SELECT count(*) FROM api_token WHERE type = 'bst'`); n != 0 {
		t.Fatalf("rows left after the delete: %d", n)
	}

	revoked := e.events(audit.EventTokenRevoked)
	if len(revoked) != 2 {
		t.Fatalf("token.revoked events = %+v, want 2", revoked)
	}
	for _, ev := range revoked {
		if ev.ResourceType != "bastion" || ev.Details["bastion"] != "partners" || ev.Details["owner"] != "bastion:partners" || ev.Details["reason"] != "admin" {
			t.Errorf("token.revoked = %+v", ev)
		}
		if ev.ResourceID != ev.Details["tokenId"] || (ev.ResourceID != ids[0] && ev.ResourceID != ids[1]) {
			t.Errorf("token.revoked names %q, the tokens are %v", ev.ResourceID, ids)
		}
	}
	if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event = 'token.revoked' AND entry ->> 'tokenId' = ANY($1)`, ids); n != 2 {
		t.Fatalf("journal entries of the revoked Bastion tokens = %d, want 2", n)
	}
	if evs := e.events(audit.EventBastionDeleted); len(evs) != 1 || evs[0].ResourceType != "bastion" || evs[0].Details["tokensRevoked"] != float64(2) {
		t.Fatalf("bastion.deleted = %+v", evs)
	}
	if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event = 'bastion.deleted' AND entry ->> 'principalId' = $1`, bid); n != 1 {
		t.Fatalf("journal entries of the deletion = %d, want 1: the replay disables the registration a restore brings back (Q-BAS-145)", n)
	}
	waitFor(t, "the deleted registration's token is refused on the replica that cached it", revocationBound+2*time.Second, func() bool {
		_, err := r.auth.Authenticate(context.Background(), secrets[0])
		return err != nil
	})
}

// TestR_BAS_004_RevokeOneToken: DELETE …/tokens/{id} revokes one token (204, also for one revoked already),
// journaled; a token of another registration or an unknown one is 404.
func TestR_BAS_004_RevokeOneToken(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusCreated, "")
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "dmz"}), http.StatusCreated, "")
	b := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}).json(t)
	id := b["id"].(string)

	want(t, r.call("DELETE", bastions+"/dmz/tokens/"+id, c.admin, nil), http.StatusNotFound, "")
	want(t, r.call("DELETE", bastions+"/partners/tokens/00000000-0000-4000-8000-000000000001", c.admin, nil), http.StatusNotFound, "")
	want(t, r.call("DELETE", bastions+"/partners/tokens/not-a-uuid", c.admin, nil), http.StatusBadRequest, "")
	want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.admin, nil), http.StatusNoContent, "")
	want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.admin, nil), http.StatusNoContent, "")

	evs := e.events(audit.EventTokenRevoked)
	if len(evs) != 1 || evs[0].ResourceType != "bastion" || evs[0].ResourceID != id || evs[0].Details["bastion"] != "partners" {
		t.Fatalf("token.revoked = %+v, want one event of the token", evs)
	}
	if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event = 'token.revoked' AND entry ->> 'tokenId' = $1`, id); n != 1 {
		t.Fatalf("journal entries = %d, want 1 (a repeated revocation writes none)", n)
	}
	if got := r.call("GET", bastions+"/partners", c.admin, nil); len(tokensOf(t, got)) != 0 {
		t.Fatalf("the revoked token is still listed: %s", got.Body)
	}
	// The token API does not see a Bastion token (Q-BAS-48): it is not listed and answers 404 there.
	want(t, r.call("DELETE", "/api/v1/tokens/"+id, c.admin, nil), http.StatusNotFound, "")
}

// TestR_BAS_004_CapabilitiesGateTheOperations: endpoints:read reads, endpoints:write writes, and tokens:revoke
// alone may revoke a Bastion token and nothing else (Q-BAS-24).
func TestR_BAS_004_CapabilitiesGateTheOperations(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusCreated, "")
	issue := func() string {
		return r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{}).json(t)["id"].(string)
	}
	id := issue()

	t.Run("a caller with endpoints:read only reads and gets 403 on every write", func(t *testing.T) {
		want(t, r.call("GET", bastions, c.auditor, nil), http.StatusOK, "")
		want(t, r.call("GET", bastions+"/partners", c.auditor, nil), http.StatusOK, "")
		want(t, r.call("POST", bastions, c.auditor, map[string]any{"name": "x"}), http.StatusForbidden, "")
		want(t, r.call("POST", bastions+"/partners/tokens", c.auditor, map[string]any{}), http.StatusForbidden, "")
		want(t, r.call("PATCH", bastions+"/partners", c.auditor, map[string]any{"enabled": false}, "If-Match", "*"), http.StatusForbidden, "")
		want(t, r.call("DELETE", bastions+"/partners", c.auditor, nil), http.StatusForbidden, "")
		want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.auditor, nil), http.StatusForbidden, "")
	})
	t.Run("a token cut down to tokens:revoke revokes a Bastion token and does nothing else", func(t *testing.T) {
		want(t, r.call("GET", bastions, c.revokeOnly, nil), http.StatusForbidden, "")
		want(t, r.call("POST", bastions+"/partners/tokens", c.revokeOnly, map[string]any{}), http.StatusForbidden, "")
		want(t, r.call("DELETE", bastions+"/partners", c.revokeOnly, nil), http.StatusForbidden, "")
		want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.revokeOnly, nil), http.StatusNoContent, "")
	})
	t.Run("the security administrator holds endpoints:write and manages Bastions", func(t *testing.T) {
		want(t, r.call("POST", bastions, c.security, map[string]any{"name": "dmz"}), http.StatusCreated, "")
		want(t, r.call("POST", bastions+"/dmz/tokens", c.security, map[string]any{}), http.StatusCreated, "")
		want(t, r.call("DELETE", bastions+"/dmz", c.security, nil), http.StatusNoContent, "")
	})
	t.Run("an anonymous caller is 401", func(t *testing.T) {
		want(t, r.call("GET", bastions, "", nil), http.StatusUnauthorized, "")
		want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, "", nil), http.StatusUnauthorized, "")
	})
}

// TestR_BAS_004_ABastionHoldsNoGrant is the second acceptance criterion of R-BAS-004: a policy entry, a role
// binding and a group membership naming bastion:<name> answer 422 principal_not_allowed, and the database
// refuses the rows when they are written around the API (the guards of the migrations).
func TestR_BAS_004_ABastionHoldsNoGrant(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners"}), http.StatusCreated, "")
	want(t, r.call("POST", "/api/v1/groups", c.admin, map[string]any{"name": "readers"}), http.StatusCreated, "")

	t.Run("a policy entry", func(t *testing.T) {
		policy := map[string]any{
			"name": "p", "resources": map[string]any{"repositories": []string{"*/npm-*"}},
			"entries": []any{map[string]any{"principal": "bastion:partners", "actions": []string{"read"}}},
		}
		want(t, r.call("POST", "/api/v1/permission-policies", c.admin, policy), http.StatusUnprocessableEntity, errs.CodePrincipalNotAllowed)
		want(t, r.call("POST", "/api/v1/authz/validate-policy", c.admin, policy), http.StatusUnprocessableEntity, errs.CodePrincipalNotAllowed)
		if n := e.number(`SELECT count(*) FROM authz_policy`); n != 0 {
			t.Fatalf("a policy was stored: %d", n)
		}
	})
	t.Run("a role binding of admin", func(t *testing.T) {
		want(t, r.call("PUT", "/api/v1/role-bindings/admin/bastion%3Apartners", c.admin, nil), http.StatusUnprocessableEntity, errs.CodePrincipalNotAllowed)
		if n := e.number(`SELECT count(*) FROM authz_role_binding b JOIN principal p ON p.id = b.principal_id WHERE p.kind = 'bastion'`); n != 0 {
			t.Fatalf("a role was bound: %d", n)
		}
		// Asking for the bindings of a registration is an empty answer, not an error.
		rp := r.call("GET", "/api/v1/role-bindings?principal=bastion%3Apartners", c.admin, nil)
		want(t, rp, http.StatusOK, "")
	})
	t.Run("a group member", func(t *testing.T) {
		want(t, r.call("PATCH", "/api/v1/groups/readers/members", c.admin, map[string]any{"add": []string{"bastion:partners"}}, "Content-Type", "application/json"), http.StatusUnprocessableEntity, errs.CodePrincipalNotAllowed)
		if n := e.number(`SELECT count(*) FROM group_membership m JOIN principal p ON p.id = m.member_id WHERE p.kind = 'bastion'`); n != 0 {
			t.Fatalf("a membership was written: %d", n)
		}
	})
	t.Run("the database refuses the rows written around the API", func(t *testing.T) {
		bid := e.text(`SELECT id::text FROM principal WHERE kind = 'bastion' AND name = 'partners'`)
		gid := e.text(`SELECT id::text FROM principal WHERE kind = 'group' AND name = 'readers'`)
		for _, sql := range []string{
			`INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid)`,
			`INSERT INTO group_membership (group_id, member_id, source) VALUES ('` + gid + `'::uuid, $1::uuid, 'manual')`,
		} {
			if err := e.execErr(sql, bid); err == nil {
				t.Errorf("%s: the database accepted a Bastion", sql)
			}
		}
	})
}

// TestR_BAS_004_TheEventsCarryNoSecretAndTheResourceTypeBastion: bastion.created, bastion.updated,
// bastion.deleted and token.created / token.revoked with resource_type bastion; no event holds a token value
// or a hash.
func TestR_BAS_004_TheEventsCarryNoSecretAndTheResourceTypeBastion(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	want(t, r.call("POST", bastions, c.admin, map[string]any{"name": "partners", "description": "d"}), http.StatusCreated, "")
	b := r.call("POST", bastions+"/partners/tokens", c.admin, map[string]any{"description": "t"}).json(t)
	secret, id := b["token"].(string), b["id"].(string)
	want(t, r.call("PATCH", bastions+"/partners", c.admin, map[string]any{"description": "e"}, "If-Match", "*"), http.StatusOK, "")
	want(t, r.call("DELETE", bastions+"/partners/tokens/"+id, c.admin, nil), http.StatusNoContent, "")
	want(t, r.call("DELETE", bastions+"/partners", c.admin, nil), http.StatusNoContent, "")

	created := e.events(audit.EventBastionCreated)
	if len(created) != 1 || created[0].ResourceType != "bastion" || created[0].Details["source"] != "api" {
		t.Fatalf("bastion.created = %+v", created)
	}
	var tokCreated []auditRow
	for _, ev := range e.events(audit.EventTokenCreated) {
		if ev.ResourceType == "bastion" {
			tokCreated = append(tokCreated, ev)
		}
	}
	if len(tokCreated) != 1 || tokCreated[0].ResourceType != "bastion" || tokCreated[0].ResourceID != id ||
		tokCreated[0].Details["type"] != "bst" || tokCreated[0].Details["bastion"] != "partners" || tokCreated[0].Details["owner"] != "bastion:partners" {
		t.Fatalf("token.created = %+v", tokCreated)
	}
	if len(e.events(audit.EventBastionUpdated)) != 1 || len(e.events(audit.EventBastionDeleted)) != 1 || len(e.events(audit.EventTokenRevoked)) != 1 {
		t.Fatal("bastion.updated, bastion.deleted or token.revoked missing")
	}
	if n := e.number(`SELECT count(*) FROM audit_event WHERE details::text LIKE '%' || $1 || '%' OR details::text LIKE '%' || $2 || '%'`, secret, secret[8:]); n != 0 {
		t.Fatalf("%d audit events hold the token value", n)
	}
	// The registration's disable and deletion end what its tokens may do and are journaled (Q-BAS-145); the others narrow
	// nothing.
	for name, journaled := range map[string]audit.Journal{
		audit.EventBastionCreated: audit.NoJournal, audit.EventBastionUpdated: audit.NoJournal, audit.EventBastionEnabled: audit.NoJournal,
		audit.EventBastionDisabled: audit.JournalRevocation, audit.EventBastionDeleted: audit.JournalRevocation,
	} {
		et, ok := audit.Lookup(name)
		if !ok || et.Journal != journaled || et.Mode != audit.Transactional {
			t.Errorf("%s registered as %+v (%v), want journal %q", name, et, ok, journaled)
		}
	}
	if et, _ := audit.Lookup(audit.EventTokenRevoked); et.Journal != audit.JournalRevocation {
		t.Errorf("token.revoked is not journaled: %+v", et)
	}
}

var _ = token.TypeBST
