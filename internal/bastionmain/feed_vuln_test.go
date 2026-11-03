package bastionmain

import (
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// TestR_BAS_015_FindingEventsThatSetABlock: the two finding events yield a content entry for their repository when they set a
// download block, and none when they lift one or touch none (R-VULN-013, Q-VULN-48).
func TestR_BAS_015_FindingEventsThatSetABlock(t *testing.T) {
	for _, action := range []string{audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened} {
		set := mapEvent(FeedEvent{Action: action, Repository: "npm-remote", Details: []byte(`{"block":"set"}`)}, "default")
		if want := []rawEntry{{kind: iface.FeedContent, repository: "npm-remote"}}; !reflect.DeepEqual(set, want) {
			t.Errorf("%s with block set = %+v", action, set)
		}
		for _, d := range []string{`{"block":"lifted"}`, `{}`, ``} {
			if got := mapEvent(FeedEvent{Action: action, Repository: "npm-remote", Details: []byte(d)}, "default"); got != nil {
				t.Errorf("%s with %q = %+v", action, d, got)
			}
		}
		if got := mapEvent(FeedEvent{Action: action, Details: []byte(`{"block":"set"}`)}, "default"); got != nil {
			t.Errorf("%s without a repository = %+v", action, got)
		}
		typ, ok := audit.Lookup(action)
		if !ok || typ.Journal != audit.JournalRevocation || typ.Entry != audit.EntryOptional {
			t.Errorf("%s is registered as %+v", action, typ)
		}
	}
}
