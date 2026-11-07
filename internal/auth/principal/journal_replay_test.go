package principal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

const gid = "0192f1c4-0000-7000-8000-0000000000c1"

// TestR_AUTH_019_AutoJoinOrder: off gives the users created afterwards nothing that on does not, so only a flag that ends off
// or stays as it is narrows (R-HA-054 comparator of the auto-join flag, Q-AUTH-198).
func TestR_AUTH_019_AutoJoinOrder(t *testing.T) {
	for _, c := range []struct{ current, after, want bool }{
		{true, false, true}, {false, false, true}, {true, true, true}, {false, true, false},
	} {
		if got := (autoJoinOrder{}).Narrows(c.current, c.after); got != c.want {
			t.Errorf("Narrows(%v, %v) = %v, want %v", c.current, c.after, got, c.want)
		}
	}
}

// TestR_AUTH_019_TheUpdateRuleClearsAndNeverSetsTheFlag: over every combination the replay meets (the current flag differs
// from the journaled after-state), a cleared flag is applied, a set flag is skipped whatever the forged before-state says,
// and nothing is ever suspended.
func TestR_AUTH_019_TheUpdateRuleClearsAndNeverSetsTheFlag(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, before := range []bool{false, true} {
			after := !current
			cleared := false
			act := journal.UpdateActions{
				Apply:   func() error { cleared = true; return nil },
				Suspend: func() (json.RawMessage, error) { t.Fatal("suspended"); return nil, nil },
			}
			out, _, err := journal.ApplyUpdate[bool](autoJoinOrder{}, current, before, after, act)
			if err != nil {
				t.Fatal(err)
			}
			want := journal.Skipped
			if !after {
				want = journal.Applied
			}
			if out != want || cleared != (want == journal.Applied) {
				t.Errorf("current %v, before %v, after %v: %s (cleared %v), want %s", current, before, after, out, cleared, want)
			}
		}
	}
}

// TestR_AUTH_041_GroupUpdatedEntriesAreReadStrictly: the entry the producer writes reads back; an entry without the group's
// ID or without both values of the flag is skipped as malformed before any statement runs.
func TestR_AUTH_041_GroupUpdatedEntriesAreReadStrictly(t *testing.T) {
	written, err := json.Marshal(groupUpdatedEntry{PrincipalID: gid, Name: "everyone", AutoJoin: boolChange{Before: true, After: false}})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := readGroupUpdated(journal.Object{Entry: written}); !ok || e.PrincipalID != gid || !e.AutoJoin.Before || e.AutoJoin.After {
		t.Fatalf("the producer's entry %s reads as %+v, %v", written, e, ok)
	}
	for _, bad := range []string{
		`{"name":"everyone","autoJoin":{"before":true,"after":false}}`,
		`{"principalId":"everyone","autoJoin":{"before":true,"after":false}}`,
		`{"principalId":"` + gid + `"}`,
		`{"principalId":"` + gid + `","autoJoin":{"before":true}}`,
		`{"principalId":"` + gid + `","autoJoin":{"after":false}}`,
		`{"principalId":"` + gid + `","autoJoin":{"before":"yes","after":false}}`,
	} {
		o := journal.Object{Event: audit.EventGroupUpdated, Entry: json.RawMessage(bad)}
		if _, ok := readGroupUpdated(o); ok {
			t.Errorf("%s was read", bad)
		}
		if r, err := replayPrincipal(context.Background(), nil, journal.Env{}, o); err != nil || r.Outcome != journal.Skipped {
			t.Errorf("%s: %+v, %v; want skipped as malformed", bad, r, err)
		}
	}
}

// TestR_BAS_004_ARegistrationEntryWithoutAnIDIsSkipped: bastion.disabled and bastion.deleted name the registration by its
// principal ID; an entry without one is skipped before any statement runs (Q-BAS-145).
func TestR_BAS_004_ARegistrationEntryWithoutAnIDIsSkipped(t *testing.T) {
	for _, event := range []string{audit.EventBastionDisabled, audit.EventBastionDeleted} {
		for _, bad := range []string{`{"name":"partners"}`, `{"principalId":"partners"}`, `[]`} {
			o := journal.Object{Event: event, Entry: json.RawMessage(bad)}
			if r, err := replayPrincipal(context.Background(), nil, journal.Env{}, o); err != nil || r.Outcome != journal.Skipped {
				t.Errorf("%s %s: %+v, %v; want skipped as malformed", event, bad, r, err)
			}
		}
	}
}

// TestR_AUTH_029_TheEventOfARevokedToken: the token.revoked a revoke-all records for each token has the journal entry
// {tokenId, reason}; a Bastion token's names the registration and has the resource type bastion, and the token of a project
// service account names the project (R-AUTH-029, R-BAS-004, Q-AUTHZ-169, Q-AUTH-198).
func TestR_AUTH_029_TheEventOfARevokedToken(t *testing.T) {
	const tok = "0192f1c4-0000-7000-8000-0000000000a1"
	c := Caller{Actor: audit.Actor{Kind: "user", Name: "admin"}}
	read := func(raw json.RawMessage) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	ev := tokenRevokedEvent(c, RevokedToken{ID: tok, OwnerID: gid, Reason: "principal_disabled"}, tokenOwner{name: "alice"})
	if ev.Type != audit.EventTokenRevoked || ev.Subject.Kind != "token" || ev.Subject.ID != tok || ev.Reason != "principal_disabled" {
		t.Fatalf("event %+v", ev)
	}
	if j := read(ev.Journal); j["tokenId"] != tok || j["reason"] != "principal_disabled" || len(j) != 2 {
		t.Errorf("journal entry %s", ev.Journal)
	}
	if d := read(ev.Details); d["reason"] != "principal_disabled" || d["owner"] != nil || d["project"] != nil {
		t.Errorf("details %s", ev.Details)
	}
	bst := tokenRevokedEvent(c, RevokedToken{ID: tok, OwnerID: gid, Reason: "admin", Bastion: true}, tokenOwner{name: "partners"})
	if d := read(bst.Details); bst.Subject.Kind != "bastion" || d["owner"] != "bastion:partners" || d["bastion"] != "partners" || d["tokenId"] != tok {
		t.Errorf("Bastion token: %+v %s", bst.Subject, bst.Details)
	}
	psa := tokenRevokedEvent(c, RevokedToken{ID: tok, OwnerID: gid, Reason: "principal_disabled"}, tokenOwner{name: "deploy", project: "web"})
	if d := read(psa.Details); d["project"] != "web" {
		t.Errorf("project service account: %s", psa.Details)
	}
}
