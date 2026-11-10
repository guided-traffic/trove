package merge

import (
	"reflect"
	"testing"
	"time"
)

func entries(vs ...string) []VersionEntry {
	out := make([]VersionEntry, len(vs))
	for i, v := range vs {
		out[i] = VersionEntry{Version: v}
	}
	return out
}

func versions(es []VersionEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Version
	}
	return out
}

func sum(b byte) [32]byte { return [32]byte{b} }

func TestR_ANS_022_TheMergedListIsTheUnionNewestFirstAndALocalVersionNeverLosesToARemoteOne(t *testing.T) {
	local := Part{Tier: 0, Position: 0, Entries: entries("1.0.0"), SHA256: map[string][32]byte{"1.0.0": sum(1)}}
	remote := Part{Tier: 2, Position: 0, Entries: entries("0.9.0", "1.0.0"), SHA256: map[string][32]byte{"0.9.0": sum(9), "1.0.0": sum(2)}}
	// The parts are handed in the opposite order of their rank: the rank decides, not the slice position.
	m := MergeLists([]Part{remote, local})
	if got, want := versions(m.Entries), []string{"1.0.0", "0.9.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if m.Source[0] != 1 || m.Source[1] != 0 {
		t.Errorf("the local version must come from the local part, sources %v", m.Source)
	}
	if want := []Conflict{{Version: "1.0.0", Winner: 1, Loser: 0}}; !reflect.DeepEqual(m.Conflicts, want) {
		t.Errorf("conflicts %v, want %v", m.Conflicts, want)
	}
}

func TestR_ANS_022_TheSameDigestOrNoDigestIsNoConflict(t *testing.T) {
	a := Part{Entries: entries("1.0.0", "2.0.0"), SHA256: map[string][32]byte{"1.0.0": sum(1)}}
	b := Part{Tier: 1, Entries: entries("1.0.0", "2.0.0"), SHA256: map[string][32]byte{"1.0.0": sum(1), "2.0.0": sum(5)}}
	if c := MergeLists([]Part{a, b}).Conflicts; len(c) != 0 {
		t.Errorf("conflicts %v", c)
	}
}

func TestR_ANS_022_OrderIsSemverPrecedenceNotTextAndBuildMetadataIsTheSameVersion(t *testing.T) {
	p := Part{Entries: entries("1.10.0", "1.2.0", "1.0.0-rc.1", "1.0.0", "1.0.0-rc.10", "1.0.0-rc.2", "1.0.0-alpha", "2.0.0+build.5", "weird")}
	q := Part{Tier: 1, Entries: entries("2.0.0+build.9", "1.0.0")}
	m := MergeLists([]Part{p, q})
	want := []string{"2.0.0+build.5", "1.10.0", "1.2.0", "1.0.0", "1.0.0-rc.10", "1.0.0-rc.2", "1.0.0-rc.1", "1.0.0-alpha", "weird"}
	if got := versions(m.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if len(m.Conflicts) != 0 {
		t.Errorf("no digests announced, conflicts %v", m.Conflicts)
	}
}

func TestR_ANS_022_AMemberListingAVersionTwiceCountsItOnce(t *testing.T) {
	m := MergeLists([]Part{{Entries: entries("1.0.0", "1.0.0")}})
	if len(m.Entries) != 1 {
		t.Errorf("entries %v", m.Entries)
	}
}

func TestR_ANS_022_AnEmptyMergeIsEmpty(t *testing.T) {
	m := MergeLists(nil)
	if len(m.Entries) != 0 || len(m.Conflicts) != 0 {
		t.Errorf("%+v", m)
	}
	if c := MergeCollection(nil, nil); c.HighestVersion != "" || !c.CreatedAt.IsZero() {
		t.Errorf("%+v", c)
	}
}

func TestR_ANS_022_APageIsCutAfterTheMergeAndAPageBeyondTheListIsEmpty(t *testing.T) {
	all := MergeLists([]Part{{Entries: entries("1.0.0", "3.0.0")}, {Tier: 1, Entries: entries("2.0.0", "4.0.0")}}).Entries
	cases := []struct {
		limit, offset int
		want          []string
	}{
		{2, 0, []string{"4.0.0", "3.0.0"}},
		{2, 2, []string{"2.0.0", "1.0.0"}},
		{3, 3, []string{"1.0.0"}},
		{2, 4, nil},
		{0, 0, nil},
		{2, -1, nil},
	}
	for _, c := range cases {
		got := versions(Page(all, c.limit, c.offset))
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("limit %d offset %d: %v, want %v", c.limit, c.offset, got, c.want)
		}
	}
}

func TestR_ANS_022_TheCollectionTakesTheHighestVersionTheEarliestCreationAndTheLatestUpdate(t *testing.T) {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Hour), t0.Add(2*time.Hour)
	c := MergeCollection(entries("2.0.0", "1.0.0"), []Times{
		{CreatedAt: t1, UpdatedAt: t1},
		{},
		{CreatedAt: t0, UpdatedAt: t2},
	})
	if c.HighestVersion != "2.0.0" || !c.CreatedAt.Equal(t0) || !c.UpdatedAt.Equal(t2) {
		t.Errorf("%+v", c)
	}
}

func TestR_ANS_022_TwoPartsOfOneMemberNeverConflictWithEachOther(t *testing.T) {
	// A remote member's cached and uncached versions are two parts; the same member is no conflict with itself.
	a := Part{Member: "remote", Tier: 2, Entries: entries("1.0.0"), SHA256: map[string][32]byte{"1.0.0": sum(1)}}
	b := Part{Member: "remote", Tier: 3, Entries: entries("1.0.0"), SHA256: map[string][32]byte{"1.0.0": sum(2)}}
	if c := MergeLists([]Part{a, b}).Conflicts; len(c) != 0 {
		t.Errorf("conflicts %v", c)
	}
}

func TestCompareIsAnOrder(t *testing.T) {
	vs := []string{"0.0.1", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range vs {
		for j := range vs {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(vs[i], vs[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", vs[i], vs[j], got, want)
			}
		}
	}
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-", "1.0.0-a..b", "x", ""} {
		if Compare(bad, "0.0.1") != -1 || Compare("0.0.1", bad) != 1 {
			t.Errorf("%q must sort below a valid version", bad)
		}
	}
}

func FuzzCompare(f *testing.F) {
	f.Add("1.0.0", "1.0.0-rc.1")
	f.Add("x", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		if Compare(a, b) != -Compare(b, a) {
			t.Fatalf("not antisymmetric: %q %q", a, b)
		}
	})
}
