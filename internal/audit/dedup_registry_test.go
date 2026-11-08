package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The token service's refusals collapse per client address, endpoint, reason and form of the credential (R-OCI-034,
// Q-OCI-53), so a summary never folds one reason under another.
func TestR_OPS_015_RegistryTokenRefusalsCollapsePerAddressEndpointReasonAndCredential(t *testing.T) {
	s := &fakeSink{}
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	w, _, _ := newTestWriter(t, s, baseCfg(), c)
	w.Start()
	refusal := func(addr, endpoint, reason, credential string) Event {
		d, _ := json.Marshal(map[string]string{"reason": reason, "credential": credential})
		return Event{
			Type: EventRegistryTokenDenied, Actor: Actor{Kind: "anonymous"}, Outcome: Failure, Reason: reason,
			Source: Source{ClientAddr: addr}, Endpoint: endpoint, Details: d,
		}
	}
	ctx := context.Background()
	for range 5 { // one first event and one summary of four
		w.Record(ctx, refusal("203.0.113.7", "default", "invalid_token", "basic"))
	}
	w.Record(ctx, refusal("203.0.113.7", "default", "credentials_required", "none")) // another reason: its own first
	w.Record(ctx, refusal("203.0.113.7", "default", "principal_disabled", "basic"))  // another reason: its own first
	w.Record(ctx, refusal("203.0.113.7", "default", "invalid_token", "form"))        // another form: its own first
	w.Record(ctx, refusal("203.0.113.7", "customers", "invalid_token", "basic"))     // another endpoint: its own first
	w.Record(ctx, refusal("198.51.100.1", "default", "invalid_token", "basic"))      // another address: its own first
	flush(t, w)
	rows := s.stored()
	if len(rows) != 7 {
		t.Fatalf("rows = %d, want six first events and one summary", len(rows))
	}
	summaries := 0
	for _, r := range rows {
		var d map[string]any
		_ = json.Unmarshal(r.Details, &d)
		if d["repeat_of"] != nil {
			summaries++
			if d["count"] != float64(4) || d["reason"] != "invalid_token" || d["credential"] != "basic" {
				t.Errorf("summary %s", r.Details)
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("%d summaries", summaries)
	}
}
