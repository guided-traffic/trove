package merge_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
)

func local(t *testing.T, entries ...merge.Entry) []byte {
	t.Helper()
	b, err := merge.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func flat(t *testing.T, versions ...string) []byte {
	t.Helper()
	b, err := merge.EncodeFlat(versions)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func entry(v, description string) merge.Entry {
	return merge.Entry{Version: v, Full: v, DeclaredID: "Acme.Widgets", Listed: true, Metadata: json.RawMessage(`{"description":"` + description + `"}`)}
}

// upstream is an inlined registration index of an upstream with the catalog entries given.
func upstream(entries ...string) []byte {
	return []byte(`{"count":1,"items":[{"@id":"x#page/a/b","count":` + string(rune('0'+len(entries))) + `,"items":[` +
		strings.Join(entries, ",") + `]}]}`)
}

func leaf(catalog string) string { return `{"@id":"x","catalogEntry":` + catalog + `}` }

// upstreamEntries are the entries of a remote member whose upstream index inlines the leaves
// given, as the format hands them to the merge: the remote's own conversion (merge.FromRemote).
func upstreamEntries(t *testing.T, leaves ...string) []merge.Entry {
	t.Helper()
	idx, err := remote.ParseIndex(upstream(leaves...), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var ls []remote.Leaf
	for _, p := range idx.Pages {
		ls = append(ls, p.Leaves...)
	}
	return merge.FromRemote(remote.Merge(ls, "acme.widgets"))
}

func decoded(t *testing.T, b []byte) []merge.Entry {
	t.Helper()
	es, err := merge.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func versionsOf(es []merge.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Version)
	}
	return out
}

// TestR_NUGET_017_TheVersionListIsTheUnionAscendingByPrecedence: the members' lists are
// merged without duplicates, in version order and not in text order, and the spellings of a
// version meet.
func TestR_NUGET_017_TheVersionListIsTheUnionAscendingByPrecedence(t *testing.T) {
	out, err := merge.Do(merge.Flat, []merge.Part{
		{Member: "local", Tier: merge.Local, Doc: flat(t, "1.0.0", "10.0.0", "2.0.0-rc.1")},
		{Member: "remote", Tier: merge.Remote, Position: 1, Doc: []byte(`{"versions":["2.0.0","2.0.0-rc.1","1.0.0.0","9.0.0"]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versionsOf(decoded(t, out)), []string{"1.0.0", "2.0.0-rc.1", "2.0.0", "9.0.0", "10.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

// TestR_NUGET_017_AVersionComesFromTheMemberTierResolutionWouldPick: the local member wins
// over a remote one with a cached package, which wins over one that would fetch; within a
// tier the earlier position wins, whatever the order of the parts.
func TestR_NUGET_017_AVersionComesFromTheMemberTierResolutionWouldPick(t *testing.T) {
	parts := []merge.Part{
		{Member: "fetch", Tier: merge.Remote, Position: 0, Entries: upstreamEntries(t, leaf(`{"id":"Acme.Widgets","version":"1.0.0","description":"fetch"}`), leaf(`{"id":"Acme.Widgets","version":"3.0.0","description":"fetch"}`))},
		{
			Member: "cached", Tier: merge.Remote, Position: 1, Cached: map[string]bool{"1.0.0": true},
			Entries: upstreamEntries(t, leaf(`{"id":"Acme.Widgets","version":"1.0.0","description":"cached"}`), leaf(`{"id":"Acme.Widgets","version":"2.0.0","description":"cached"}`)),
		},
		{Member: "second-local", Tier: merge.Local, Position: 3, Doc: local(t, entry("4.0.0", "second"), entry("5.0.0", "second"))},
		{Member: "first-local", Tier: merge.Local, Position: 2, Doc: local(t, entry("4.0.0", "first"))},
	}
	want := map[string]string{"1.0.0": "cached", "2.0.0": "cached", "3.0.0": "fetch", "4.0.0": "first-local", "5.0.0": "second-local"}
	for _, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {1, 3, 0, 2}} {
		var ps []merge.Part
		for _, i := range order {
			ps = append(ps, parts[i])
		}
		out, err := merge.Do(merge.Registration, ps)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, e := range decoded(t, out) {
			got[e.Version] = e.Source
		}
		if len(got) != len(want) {
			t.Fatalf("order %v: %v", order, got)
		}
		for v, src := range want {
			if got[v] != src {
				t.Errorf("order %v: %s came from %q, want %q", order, v, got[v], src)
			}
		}
	}
}

// TestR_AUTHZ_008_ARestrictedMemberListsOnlyWhatItHasCached: a caller who may not fill a
// remote member's cache sees the versions it has cached, so that every listed version can be
// downloaded; a member that is not restricted lists all of its versions.
func TestR_AUTHZ_008_ARestrictedMemberListsOnlyWhatItHasCached(t *testing.T) {
	doc := []byte(`{"versions":["1.0.0","2.0.0","3.0.0"]}`)
	cached := map[string]bool{"2.0.0": true}
	out, err := merge.Do(merge.Flat, []merge.Part{{Member: "remote", Tier: merge.Remote, Doc: doc, Cached: cached, Restricted: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(decoded(t, out)); !slices.Equal(got, []string{"2.0.0"}) {
		t.Errorf("restricted: %v", got)
	}
	out, err = merge.Do(merge.Flat, []merge.Part{{Member: "remote", Tier: merge.Remote, Doc: doc, Cached: cached}})
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(decoded(t, out)); len(got) != 3 {
		t.Errorf("not restricted: %v", got)
	}
}

// TestR_NUGET_017_MergingIsDeterministic: the same parts give the same bytes, which the
// validator and the cache rely on across replicas (R-NUGET-030).
func TestR_NUGET_017_MergingIsDeterministic(t *testing.T) {
	ps := []merge.Part{
		{Member: "a", Tier: merge.Local, Doc: local(t, entry("1.0.0", "a"), entry("1.1.0", "a"), entry("2.0.0", "a"))},
		{Member: "b", Tier: merge.Remote, Position: 1, Entries: upstreamEntries(t, leaf(`{"id":"Acme.Widgets","version":"1.0.0"}`), leaf(`{"id":"Acme.Widgets","version":"3.0.0"}`))},
	}
	first, err := merge.Do(merge.Registration, ps)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := merge.Do(merge.Registration, ps)
		if err != nil || string(again) != string(first) {
			t.Fatalf("not deterministic: %v", err)
		}
	}
}

// TestR_NUGET_017_ARemoteEntryIsTheRemotesOwnConversion: the entries of a remote member are
// the remote's conversion of its leaves (Q-NUGET-82): an entry without an id takes the ID of the
// request, the members the remote keeps travel with the entry, the pass-through members come
// without the upstream's addresses, and the SemVer 2.0.0 classification follows R-NUGET-004. The
// merge decoded the upstream's index itself before and lost the first two.
func TestR_NUGET_017_ARemoteEntryIsTheRemotesOwnConversion(t *testing.T) {
	es := upstreamEntries(t,
		leaf(`{"version":"13.0.3","authors":["James","Newton-King"],"description":"Json.","tags":"json serialization","listed":false,`+
			`"published":"2023-03-08T10:00:00.123+00:00","licenseExpression":"MIT","requireLicenseAcceptance":false,"copyright":"(c) J",`+
			`"dependencyGroups":[{"targetFramework":"net6.0","dependencies":[{"id":"Other","range":"[1.0.0, )"}]}],`+
			`"deprecation":{"@id":"https://up.test/cat#deprecation","message":"old","reasons":["Legacy"]},"vulnerabilities":null}`),
		leaf(`{"id":"Acme.Widgets","version":"14.0.0-beta1"}`),
		leaf(`{"id":"Acme.Widgets","version":"14.0.0","dependencyGroups":[{"dependencies":[{"id":"X","range":"[1.0.0-a.b, )"}]}]}`),
		leaf(`{"id":"Acme.Widgets","version":"not a version"}`),
	)
	if len(es) != 3 {
		t.Fatalf("%d entries, want 3 (a text that is no version is dropped)", len(es))
	}
	e := es[0]
	if e.Version != "13.0.3" || e.DeclaredID != "acme.widgets" || e.Listed || e.Semver2 || e.Prerelease ||
		!e.Published.Equal(time.Date(2023, 3, 8, 10, 0, 0, 123000000, time.UTC)) {
		t.Errorf("first entry = %+v", e)
	}
	var m struct {
		Authors          string
		Tags             []string
		License          struct{ Type, Value string }
		DependencyGroups []struct {
			TargetFramework string
			Dependencies    []struct{ ID, Version string }
		}
	}
	if err := json.Unmarshal(e.Metadata, &m); err != nil {
		t.Fatal(err)
	}
	if m.Authors != "James, Newton-King" || !slices.Equal(m.Tags, []string{"json", "serialization"}) || m.License.Value != "MIT" ||
		len(m.DependencyGroups) != 1 || m.DependencyGroups[0].Dependencies[0].Version != "[1.0.0, )" {
		t.Errorf("metadata = %s", e.Metadata)
	}
	if string(e.Deprecation) != `{"message":"old","reasons":["Legacy"]}` || e.Vulnerabilities != nil {
		t.Errorf("pass-through = %s / %s", e.Deprecation, e.Vulnerabilities)
	}
	if len(e.Other) != 1 || e.Other[0].Key != "copyright" || string(e.Other[0].Value) != `"(c) J"` {
		t.Errorf("kept members = %+v", e.Other)
	}
	if !es[1].Listed || !es[1].Prerelease || es[1].Semver2 {
		t.Errorf("a leaf without listed is listed: %+v", es[1])
	}
	if !es[2].Semver2 {
		t.Errorf("a dependency range with a SemVer 2.0.0 bound makes the version SemVer 2.0.0-specific: %+v", es[2])
	}
	// The kept members survive the merged document, which the merged-metadata cache holds.
	out, err := merge.Do(merge.Registration, []merge.Part{{Member: "r", Tier: merge.Remote, Entries: es}})
	if err != nil {
		t.Fatal(err)
	}
	back := decoded(t, out)
	if len(back) != 3 || len(back[0].Other) != 1 || back[0].DeclaredID != "acme.widgets" || len(back[0].Members()) != 1 {
		t.Errorf("merged = %+v", back)
	}
}

// TestR_NUGET_017_AFlatListOfARemoteMemberIsReadAsTheMergeReceivesIt: the version list of a
// remote member reaches the merge as plain JSON (the adapter inflates a gzip-coded one), and a
// document that is no version list is an upstream failure, not an empty list.
func TestR_NUGET_017_AFlatListOfARemoteMemberIsReadAsTheMergeReceivesIt(t *testing.T) {
	for name, doc := range map[string]string{"not JSON": `<html>`, "no list": `{"versions":"1.0.0"}`} {
		_, err := merge.Do(merge.Flat, []merge.Part{{Member: "r", Tier: merge.Remote, Doc: []byte(doc)}})
		if e, ok := errs.As(err); !ok || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: %v, want an upstream failure", name, err)
		}
	}
	// A remote member without a version contributes nothing and fails nothing.
	out, err := merge.Do(merge.Registration, []merge.Part{{Member: "r", Tier: merge.Remote}})
	if err != nil || len(decoded(t, out)) != 0 {
		t.Errorf("empty member: %s %v", out, err)
	}
}
