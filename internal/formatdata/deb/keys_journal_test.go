package deb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

func TestR_DEB_032_BothKeyEventsHaveAReplayHandler(t *testing.T) {
	for _, ev := range []string{audit.EventDebSigningKeyPromoted, audit.EventDebSigningKeyRetired} {
		h, ok := journal.HandlerFor(ev)
		if !ok || h.Domain != "deb" {
			t.Errorf("%s: handler %+v, %v", ev, h, ok)
		}
		if et, ok := audit.Lookup(ev); !ok || et.Journal != audit.JournalRevocation {
			t.Errorf("%s is not a journaled event", ev)
		}
	}
}

// An entry that names no repository or no fingerprint is skipped before the database is asked: the entry is a claim to check, and a
// malformed one is no claim.
func TestR_DEB_032_AMalformedKeyEntryIsSkipped(t *testing.T) {
	fp := strings.Repeat("A", 40)
	tests := []struct {
		name, event, entry string
	}{
		{"not JSON", audit.EventDebSigningKeyRetired, `[1]`},
		{"no repository", audit.EventDebSigningKeyRetired, `{"fingerprint":"` + fp + `"}`},
		{"retired without a fingerprint", audit.EventDebSigningKeyRetired, `{"repositoryId":1}`},
		{"a short fingerprint", audit.EventDebSigningKeyRetired, `{"repositoryId":1,"fingerprint":"ABC"}`},
		{"a lower-case fingerprint", audit.EventDebSigningKeyRetired, `{"repositoryId":1,"fingerprint":"` + strings.ToLower(fp) + `"}`},
		{"promoted without the new key", audit.EventDebSigningKeyPromoted, `{"repositoryId":1,"oldFingerprint":"` + fp + `"}`},
		{"promoted with a bad old key", audit.EventDebSigningKeyPromoted, `{"repositoryId":1,"oldFingerprint":"zz","newFingerprint":"` + fp + `"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := applyKeyEntry(context.Background(), nil, journal.Env{}, journal.Object{ID: "e1", Event: tc.event, Entry: json.RawMessage(tc.entry)})
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != journal.Skipped || res.ID != "e1" || res.Event != tc.event {
				t.Errorf("result %+v", res)
			}
		})
	}
}

func TestR_DEB_015_FingerprintGrammar(t *testing.T) {
	if !validFingerprint(strings.Repeat("0F", 20)) || validFingerprint(strings.Repeat("0f", 20)) || validFingerprint(strings.Repeat("A", 39)) {
		t.Error("validFingerprint")
	}
}

func TestR_DEB_015_ARepositoryWithoutKeysIsNotApplicable(t *testing.T) {
	for _, r := range []repoRef{{Format: "deb", Class: "remote"}, {Format: "npm", Class: "local"}} {
		if r.hasKeys() {
			t.Errorf("%+v has keys", r)
		}
	}
	for _, r := range []repoRef{{Format: "deb", Class: "local"}, {Format: "deb", Class: "virtual"}} {
		if !r.hasKeys() {
			t.Errorf("%+v has no keys", r)
		}
	}
}
