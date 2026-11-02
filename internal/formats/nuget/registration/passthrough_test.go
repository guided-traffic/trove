package registration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// TestR_NUGET_015_AnEntryKeepsTheMembersOfAnUpstreamAfterItsOwn: the pass-through members of a
// remote entry are written after the members trove computes, in the order given, compact and
// without the escaping of HTML characters; a local entry has none and its bytes do not change.
func TestR_NUGET_015_AnEntryKeepsTheMembersOfAnUpstreamAfterItsOwn(t *testing.T) {
	v := entryOf(t, "1.2.3", true, `{"description":"d"}`)
	plain, _, err := Doc{Feed: feed, Hive: SemVer2, LowerID: "acme.widgets"}.Page([]rows.RegistrationVersion{v}, "1.2.3", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	extras := map[string]Passthrough{"1.2.3": {
		Deprecation: json.RawMessage(`{"message":"a < b"}`),
		Other:       []Member{{Key: "copyright", Value: json.RawMessage(`"(c) <Acme>"`)}, {Key: "packageSize", Value: json.RawMessage(`12`)}},
	}}
	kept, _, err := Doc{Feed: feed, Hive: SemVer2, LowerID: "acme.widgets", Extras: extras}.Page([]rows.RegistrationVersion{v}, "1.2.3", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	s := string(kept)
	if !strings.Contains(s, `"deprecation":{"message":"a < b"},"copyright":"(c) <Acme>","packageSize":12}`) {
		t.Errorf("the members are not after the entry's own: %s", s)
	}
	if !json.Valid(kept) {
		t.Errorf("not valid JSON: %s", s)
	}
	if strings.Contains(string(plain), "copyright") {
		t.Error("a local entry carries pass-through members")
	}
}

// TestR_NUGET_003_AnUpstreamDocumentLinksTheRegistrationOfADependencyOutsideASCII: the rule for
// the ID of a dependency is the class's.
func TestR_NUGET_003_AnUpstreamDocumentLinksTheRegistrationOfADependencyOutsideASCII(t *testing.T) {
	v := entryOf(t, "1.0.0", true, `{"dependencyGroups":[{"dependencies":[{"id":"Ünï.Dep","version":"1.0.0"}]}]}`)
	for _, upstream := range []bool{false, true} {
		body, _, err := Doc{Feed: feed, Hive: SemVer2, LowerID: "a", UpstreamIDs: upstream}.Page([]rows.RegistrationVersion{v}, "1.0.0", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(body), `"registration":"`+string(feed)+`registration-semver2/%C3%9Cn%C3%AF.dep/index.json"`); got != upstream {
			t.Errorf("upstream rule %v: link present %v: %s", upstream, got, body)
		}
	}
}
