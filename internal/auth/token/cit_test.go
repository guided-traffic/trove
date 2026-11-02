package token

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

type eventLog struct{ events []audit.Event }

func (l *eventLog) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	l.events = append(l.events, ev)
	return nil
}

func (l *eventLog) Record(_ context.Context, ev audit.Event) { l.events = append(l.events, ev) }

// R-CI-020, R-OPS-014: ci.token.revoked carries at most 1,000 token IDs; a larger revocation writes several events,
// each with the journal entry of the IDs it names and the cause.
func TestR_CI_020_ARevocationOfMoreThan1000TokensWritesSeveralEvents(t *testing.T) {
	ids := make([]string, 2001)
	for i := range ids {
		ids[i] = "t" + string(rune('a'+i%26))
	}
	var log eventLog
	if err := RecordCIRevoked(t.Context(), &log, nil, audit.Actor{Kind: "user"}, audit.Source{}, "gitlab-main", "acme", "mapping", ids, nil); err != nil {
		t.Fatal(err)
	}
	if len(log.events) != 3 {
		t.Fatalf("%d events, want 3", len(log.events))
	}
	total := 0
	for _, ev := range log.events {
		var d struct {
			TokenIDs []string `json:"tokenIds"`
			Cause    string   `json:"cause"`
			Mapping  string   `json:"mapping"`
		}
		var j struct {
			TokenIDs []string `json:"tokenIds"`
			Cause    string   `json:"cause"`
		}
		if err := json.Unmarshal(ev.Details, &d); err != nil || json.Unmarshal(ev.Journal, &j) != nil {
			t.Fatalf("event %s %s", ev.Details, ev.Journal)
		}
		if ev.Type != audit.EventCITokenRevoked || len(d.TokenIDs) > MaxCIRevokedIDs || len(j.TokenIDs) != len(d.TokenIDs) || d.Cause != "mapping" || j.Cause != "mapping" || d.Mapping != "acme" {
			t.Fatalf("event %s with journal %s", ev.Details, ev.Journal)
		}
		total += len(d.TokenIDs)
	}
	if total != len(ids) {
		t.Fatalf("%d IDs in the events, want %d", total, len(ids))
	}
}

// Q-CI-17: the validator decodes ci_context with claims.DecodeFrozen, so another version and an inconsistent bundle
// are storage faults, never an acceptance without the bundle.
func TestTheCIContextIsDecodedWithItsVersion(t *testing.T) {
	good := claims.Frozen{Version: claims.FrozenVersion, Issuer: "gitlab-main", IssuerID: "i", ProjectID: "1"}
	good.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/acme", ID: "m", Tier: "system"})
	raw, _ := json.Marshal(good)
	if f, err := decodeCIContext(raw); err != nil || f.Issuer != "gitlab-main" {
		t.Fatalf("a good bundle: %v %v", f, err)
	}
	other := good
	other.Version = claims.FrozenVersion + 1
	raw, _ = json.Marshal(other)
	for name, in := range map[string][]byte{"no bundle": nil, "null": []byte("null"), "not JSON": []byte("{"), "another version": raw, "no contribution": []byte(`{"version":1,"issuer":"a","issuerId":"b"}`)} {
		if _, err := decodeCIContext(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// R-CI-016, R-AUTH-026: an issuance outside the lifetime bounds, with a bundle that fails its check or without a
// description is refused before anything is written.
func TestAnIssuanceOutsideItsRulesIsRefused(t *testing.T) {
	s := &Service{o: ServiceOptions{Tokens: config.AuthTokens{CIT: config.AuthTokenCIT{MaxTTL: 6 * time.Hour}}}}
	good := claims.Frozen{Version: claims.FrozenVersion, Issuer: "gitlab-main", IssuerID: "i", ProjectID: "1"}
	good.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/acme", ID: "m", Tier: "system"})
	base := CITRequest{OwnerID: "0190a4c2-0000-7000-8000-000000000001", Description: "CI exchange gitlab-main job 9", Frozen: good, TTL: time.Minute}
	if err := s.checkCIT(base); err != nil {
		t.Fatalf("a good request: %v", err)
	}
	for name, mod := range map[string]func(*CITRequest){
		"no owner":          func(r *CITRequest) { r.OwnerID = "x" },
		"no description":    func(r *CITRequest) { r.Description = "" },
		"below one second":  func(r *CITRequest) { r.TTL = time.Millisecond },
		"above the ceiling": func(r *CITRequest) { r.TTL = 7 * time.Hour },
		"a broken bundle":   func(r *CITRequest) { r.Frozen.Version = 0 },
	} {
		r := base
		mod(&r)
		if err := s.checkCIT(r); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
