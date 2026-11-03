package ci

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// R-CI-018, Q-CI-51: the replay set stores the subject token's exp rounded up, never down to the second, so the
// cleanup never deletes a jti before the token's own exp (plus skew and retention) has passed.
func TestR_CI_018_TheStoredSubjectExpiryRoundsUp(t *testing.T) {
	exp := time.Unix(1_800_000_000, 400_000_500) // a NumericDate with a fraction
	got := time.Unix(ceilUnix(exp), 0)
	if got.Before(exp) || got.Sub(exp) >= time.Second {
		t.Fatalf("stored %s for %s", got.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
	}
	if whole := time.Unix(1_800_000_000, 0); ceilUnix(whole) != whole.Unix() {
		t.Fatal("a whole second moved")
	}
}

// heldOnly is an index in which no grant of the bundle holds npm-hidden, which exists, or npm-nowhere, which does
// not: the index of a job asking for keys it is not granted. The exchange asks it nothing about existence.
type heldOnly struct{ noIndex }

// H-418, Q-CI-51: a scope item whose key names no repository is answered exactly as one whose repository exists and
// is not granted, so a matched job learns nothing of which repositories exist.
func TestQ_CI_51_AnUnknownScopeKeyAnswersAsANotGrantedOne(t *testing.T) {
	f := claims.Frozen{Version: claims.FrozenVersion, Issuer: "gitlab-test", IssuerID: "i-1", ProjectID: "7"}
	f.Add(claims.Freeze("ci:gitlab-test/read", "m-1", TierSystem, nil,
		claims.Rendered{Inline: []claims.InlineGrant{{Repositories: []string{"npm-internal"}, Actions: []claims.Action{claims.Read}}}}, nil))
	answer := func(item string) (Reason, string) {
		items, err := claims.ParseScope(item)
		if err != nil {
			t.Fatal(err)
		}
		_, ee := narrow(f, items, heldOnly{})
		if ee == nil {
			t.Fatalf("%s was granted", item)
		}
		return ee.Reason, strings.ReplaceAll(ee.Description, item, "<item>")
	}
	unknownReason, unknownDetail := answer("read:npm-nowhere")
	hiddenReason, hiddenDetail := answer("read:npm-hidden")
	if unknownReason != reason.ScopeNotGranted || unknownReason != hiddenReason || unknownDetail != hiddenDetail {
		t.Fatalf("an unknown key answers %s %q, an existing one %s %q", unknownReason, unknownDetail, hiddenReason, hiddenDetail)
	}
}
