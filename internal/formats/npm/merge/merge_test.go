package merge_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/merge"
)

// member is the full document of a member: its versions (each entry names its member and
// carries an integrity of its own, unless same lists a shared one), its dist-tags and its time.
type member struct {
	name     string
	versions []string
	tags     map[string]string
	created  string
	modified string
	times    map[string]string
	// sameIntegrity gives every version one integrity that the other members share.
	sameIntegrity bool
	top           map[string]any
}

func (m member) doc() []byte {
	vs := map[string]any{}
	for _, v := range m.versions {
		integrity := "sha512-" + m.name + "-" + v
		if m.sameIntegrity {
			integrity = "sha512-shared-" + v
		}
		vs[v] = map[string]any{"name": "pkg", "version": v, "x-member": m.name, "dist": map[string]any{"integrity": integrity, "tarball": "https://" + m.name + ".example/pkg-" + v + ".tgz"}}
	}
	times := map[string]string{}
	if m.created != "" {
		times["created"] = m.created
	}
	if m.modified != "" {
		times["modified"] = m.modified
	}
	for v, t := range m.times {
		times[v] = t
	}
	d := map[string]any{"_id": "pkg", "name": "pkg", "dist-tags": m.tags, "versions": vs, "time": times}
	for k, v := range m.top {
		d[k] = v
	}
	b, _ := json.Marshal(d)
	return b
}

type merged struct {
	Name     string            `json:"name"`
	ID       string            `json:"_id"`
	Rev      string            `json:"_rev"`
	Tags     map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Member string `json:"x-member"`
	} `json:"versions"`
	Time map[string]string `json:"time"`
	Desc string            `json:"description"`
}

func run(t *testing.T, parts []merge.Part, o merge.Options) (merged, *merge.Result) {
	t.Helper()
	if o.Name == "" {
		o.Name = "pkg"
	}
	res, err := merge.Do(parts, o)
	if err != nil {
		t.Fatal(err)
	}
	var m merged
	if err := json.Unmarshal(res.Doc, &m); err != nil {
		t.Fatalf("%v\n%s", err, res.Doc)
	}
	return m, res
}

func local(m member, pos int) merge.Part {
	return merge.Part{Member: m.name, Tier: merge.TierLocal, Position: pos, Doc: m.doc()}
}

func remotePart(m member, pos int, cached ...string) merge.Part {
	c := map[string]bool{}
	for _, v := range cached {
		c[v] = true
	}
	return merge.Part{Member: m.name, Tier: merge.TierRemote, Position: pos, Doc: m.doc(), CachedVersions: c}
}

func TestR_NPM_030_EachVersionComesFromTheMemberTierResolutionPicks(t *testing.T) {
	a := member{name: "a", versions: []string{"1.0.0"}, tags: map[string]string{"latest": "1.0.0"}}
	b := member{name: "b", versions: []string{"1.0.0", "1.1.0", "2.0.0"}, tags: map[string]string{"latest": "2.0.0"}}
	c := member{name: "c", versions: []string{"1.1.0", "2.0.0", "3.0.0"}, tags: map[string]string{"latest": "3.0.0"}}
	t.Run("local members first, then cached remote versions, then remote members in order", func(t *testing.T) {
		m, res := run(t, []merge.Part{remotePart(c, 2, "2.0.0"), remotePart(b, 1), local(a, 0)}, merge.Options{})
		want := map[string]string{"1.0.0": "a", "1.1.0": "b", "2.0.0": "c", "3.0.0": "c"}
		for v, member := range want {
			if got := m.Versions[v].Member; got != member {
				t.Errorf("%s came from %q, want %q", v, got, member)
			}
		}
		if len(m.Versions) != len(want) {
			t.Errorf("versions = %v", m.Versions)
		}
		for v, member := range want {
			if res.Sources[v] != member {
				t.Errorf("Sources[%s] = %q", v, res.Sources[v])
			}
		}
	})
	t.Run("a member the caller may not fill supplies the versions it has cached only", func(t *testing.T) {
		// R-AUTHZ-008: every version a document lists can be downloaded.
		restricted := remotePart(b, 0, "1.1.0")
		restricted.Restricted = true
		m, _ := run(t, []merge.Part{local(a, 1), restricted}, merge.Options{})
		if len(m.Versions) != 2 || m.Versions["1.1.0"].Member != "b" || m.Versions["1.0.0"].Member != "a" || m.Versions["2.0.0"].Member != "" {
			t.Errorf("versions = %v", m.Versions)
		}
		// The restricted member's own latest (2.0.0) points to a version the merge lacks, so it
		// is no candidate: latest is the one the local member points to.
		if m.Tags["latest"] != "1.0.0" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
	t.Run("a merge that holds no version is not found", func(t *testing.T) {
		restricted := remotePart(b, 0)
		restricted.Restricted = true
		_, err := merge.Do([]merge.Part{restricted}, merge.Options{Name: "pkg"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the result is a function of the parts, not of the order they come in beyond tier and position", func(t *testing.T) {
		one, _ := merge.Do([]merge.Part{local(a, 0), remotePart(b, 1), remotePart(c, 2)}, merge.Options{Name: "pkg"})
		two, _ := merge.Do([]merge.Part{remotePart(c, 2), local(a, 0), remotePart(b, 1)}, merge.Options{Name: "pkg"})
		if !bytes.Equal(one.Doc, two.Doc) {
			t.Errorf("%s\n%s", one.Doc, two.Doc)
		}
	})
}

func TestR_NPM_030_ConflictsAreFoundWhereMembersHoldOtherBytes(t *testing.T) {
	a := member{name: "a", versions: []string{"1.0.0", "1.1.0"}, tags: map[string]string{"latest": "1.1.0"}}
	b := member{name: "b", versions: []string{"1.0.0", "1.1.0"}, tags: map[string]string{"latest": "1.1.0"}}
	b.sameIntegrity = true
	same := a
	same.sameIntegrity = true
	_, res := run(t, []merge.Part{local(a, 0), remotePart(b, 1)}, merge.Options{})
	if len(res.Conflicts) != 2 || res.Conflicts[0].Winner != "a" || res.Conflicts[0].Other != "b" {
		t.Errorf("conflicts = %+v", res.Conflicts)
	}
	_, res = run(t, []merge.Part{local(same, 0), remotePart(b, 1)}, merge.Options{})
	if len(res.Conflicts) != 0 {
		t.Errorf("members with the same bytes conflict: %+v", res.Conflicts)
	}
}

func TestR_NPM_031_LatestStrategies(t *testing.T) {
	a := member{
		name: "a", versions: []string{"1.3.0", "1.4.0"}, tags: map[string]string{"latest": "1.4.0"},
		times: map[string]string{"1.4.0": "2024-01-01T00:00:00.000Z"},
	}
	b := member{
		name: "b", versions: []string{"2.0.0"}, tags: map[string]string{"latest": "2.0.0"},
		times: map[string]string{"2.0.0": "2023-01-01T00:00:00.000Z"},
	}
	c := member{name: "c", versions: []string{"3.0.0-rc.1"}, tags: map[string]string{"latest": "3.0.0-rc.1"}}
	parts := func() []merge.Part { return []merge.Part{local(a, 0), local(b, 1), local(c, 2)} }

	t.Run("highest-member-latest takes the highest, a release before a pre-release", func(t *testing.T) {
		m, _ := run(t, parts(), merge.Options{LatestStrategy: merge.HighestMemberLatest})
		if m.Tags["latest"] != "2.0.0" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
	t.Run("the default strategy is highest-member-latest", func(t *testing.T) {
		m, _ := run(t, parts(), merge.Options{})
		if m.Tags["latest"] != "2.0.0" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
	t.Run("newest-published takes the member latest published last", func(t *testing.T) {
		m, _ := run(t, parts(), merge.Options{LatestStrategy: merge.NewestPublished})
		if m.Tags["latest"] != "1.4.0" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
	t.Run("a member that moved latest back is respected while no member holds a higher one", func(t *testing.T) {
		old := member{name: "old", versions: []string{"1.0.0", "2.0.0"}, tags: map[string]string{"latest": "1.0.0"}}
		m, _ := run(t, []merge.Part{remotePart(old, 0)}, merge.Options{})
		if m.Tags["latest"] != "1.0.0" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
	t.Run("only pre-releases: the highest of them", func(t *testing.T) {
		pre := member{name: "pre", versions: []string{"1.0.0-beta.1", "1.0.0-beta.2"}, tags: map[string]string{"latest": "1.0.0-beta.1"}}
		m, _ := run(t, []merge.Part{local(pre, 0), local(c, 1)}, merge.Options{})
		if m.Tags["latest"] != "3.0.0-rc.1" {
			t.Errorf("latest = %q", m.Tags["latest"])
		}
	})
}

func TestR_NPM_031_OtherTagStrategies(t *testing.T) {
	a := member{
		name: "a", versions: []string{"1.0.0", "1.2.0"}, tags: map[string]string{"latest": "1.2.0", "beta": "1.0.0", "only-a": "1.2.0"},
		times: map[string]string{"1.0.0": "2020-01-01T00:00:00.000Z", "1.2.0": "2020-06-01T00:00:00.000Z"},
	}
	b := member{
		name: "b", versions: []string{"2.0.0-beta.1", "1.9.0"}, tags: map[string]string{"latest": "1.9.0", "beta": "2.0.0-beta.1", "ghost": "9.9.9"},
		times: map[string]string{"2.0.0-beta.1": "2019-01-01T00:00:00.000Z", "1.9.0": "2019-01-01T00:00:00.000Z"},
	}
	parts := func() []merge.Part { return []merge.Part{local(a, 0), local(b, 1)} }
	for _, c := range []struct{ strategy, beta string }{
		{merge.MemberOrder, "1.0.0"},
		{merge.HighestSemver, "2.0.0-beta.1"},
		{merge.NewestPublished, "1.0.0"},
		{"", "1.0.0"},
	} {
		m, _ := run(t, parts(), merge.Options{DistTagStrategy: c.strategy})
		if m.Tags["beta"] != c.beta {
			t.Errorf("%q: beta = %q, want %q", c.strategy, m.Tags["beta"], c.beta)
		}
		if m.Tags["only-a"] != "1.2.0" {
			t.Errorf("%q: only-a = %q", c.strategy, m.Tags["only-a"])
		}
		if _, ok := m.Tags["ghost"]; ok {
			t.Errorf("%q: a tag pointing to a version the merge lacks is kept", c.strategy)
		}
	}
}

func TestR_NPM_030_TimeAndTopLevelFields(t *testing.T) {
	a := member{
		name: "a", versions: []string{"1.0.0", "2.0.0"}, tags: map[string]string{"latest": "2.0.0"},
		created: "2021-01-01T00:00:00.000Z", modified: "2022-01-01T00:00:00.000Z",
		times: map[string]string{"1.0.0": "2021-02-01T00:00:00.000Z", "2.0.0": "2022-01-01T00:00:00.000Z"},
		top:   map[string]any{"description": "from a", "license": "MIT", "readme": "# a", "maintainers": []any{map[string]string{"name": "ma"}}},
	}
	b := member{
		name: "b", versions: []string{"1.0.0", "3.0.0"}, tags: map[string]string{"latest": "3.0.0"},
		created: "2019-05-05T00:00:00.000Z", modified: "2024-03-03T00:00:00.000Z",
		times: map[string]string{"1.0.0": "2019-06-06T00:00:00.000Z", "3.0.0": "2024-03-03T00:00:00.000Z"},
		top:   map[string]any{"description": "from b", "license": "Apache-2.0", "x-private": "no"},
	}
	m, _ := run(t, []merge.Part{local(a, 0), remotePart(b, 1)}, merge.Options{Rev: "1-abc"})
	if m.Time["created"] != "2019-05-05T00:00:00.000Z" || m.Time["modified"] != "2024-03-03T00:00:00.000Z" {
		t.Errorf("time = %v", m.Time)
	}
	if m.Time["1.0.0"] != "2021-02-01T00:00:00.000Z" || m.Time["3.0.0"] != "2024-03-03T00:00:00.000Z" {
		t.Errorf("the time of an entry is that of the member it came from: %v", m.Time)
	}
	// latest is 3.0.0, from b: the top-level fields are b's.
	if m.Desc != "from b" || m.Name != "pkg" || m.ID != "pkg" || m.Rev != "1-abc" {
		t.Errorf("merged = %+v", m)
	}
	res, _ := merge.Do([]merge.Part{local(a, 0), remotePart(b, 1)}, merge.Options{Name: "pkg"})
	for _, absent := range []string{`"x-private"`, `"_attachments"`, `"users"`, `"_rev"`} {
		if strings.Contains(string(res.Doc), absent) {
			t.Errorf("the merge carries %s", absent)
		}
	}
}

func TestR_NPM_030_AMemberWithoutTimesStillMerges(t *testing.T) {
	a := member{name: "a", versions: []string{"1.0.0"}, tags: map[string]string{"latest": "1.0.0"}}
	m, _ := run(t, []merge.Part{local(a, 0)}, merge.Options{})
	if _, ok := m.Time["created"]; ok {
		t.Errorf("time = %v", m.Time)
	}
}

func TestR_NPM_030_APartThatIsNoDocumentIsAnError(t *testing.T) {
	if _, err := merge.Do([]merge.Part{{Member: "x", Tier: merge.TierLocal, Doc: []byte(`[1]`)}}, merge.Options{Name: "pkg"}); err == nil {
		t.Error("an array was merged")
	}
}

// FuzzMerge: Merge never panics, is deterministic, writes a JSON document, and every version
// of it comes from a member that holds it and may supply it.
func FuzzMerge(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s[0], s[1], s[2], uint8(0), uint8(0))
	}
	f.Fuzz(func(t *testing.T, d0, d1, d2 []byte, restrict, cached uint8) {
		var parts []merge.Part
		for i, d := range [][]byte{d0, d1, d2} {
			p := merge.Part{Member: fmt.Sprint("m", i), Tier: merge.Tier(1 + i%3), Position: i, Doc: d, CachedVersions: map[string]bool{}}
			if restrict&(1<<i) != 0 {
				p.Restricted = true
			}
			if cached&(1<<i) != 0 {
				p.CachedVersions["1.0.0"] = true
			}
			parts = append(parts, p)
		}
		res, err := merge.Do(parts, merge.Options{Name: "x"})
		again, err2 := merge.Do(parts, merge.Options{Name: "x"})
		if (err == nil) != (err2 == nil) {
			t.Fatalf("one run fails: %v / %v", err, err2)
		}
		if err != nil {
			return
		}
		if !bytes.Equal(res.Doc, again.Doc) {
			t.Fatalf("not deterministic:\n%s\n%s", res.Doc, again.Doc)
		}
		var out struct {
			Versions map[string]json.RawMessage `json:"versions"`
			Tags     map[string]string          `json:"dist-tags"`
		}
		if err := json.Unmarshal(res.Doc, &out); err != nil {
			t.Fatalf("the merge is no JSON document: %v\n%s", err, res.Doc)
		}
		var got []string
		for v := range out.Versions {
			got = append(got, v)
			holder := res.Sources[v]
			ok := false
			for _, p := range parts {
				if p.Member == holder {
					ok = true
				}
			}
			if !ok {
				t.Fatalf("%q: no source", v)
			}
		}
		sort.Strings(got)
		if len(got) == 0 {
			t.Fatal("a merge without a version was returned")
		}
		for tag, v := range out.Tags {
			if _, ok := out.Versions[v]; !ok {
				t.Fatalf("tag %q points to %q, which the merge lacks", tag, v)
			}
		}
	})
}

func seeds() [][3][]byte {
	a := member{name: "a", versions: []string{"1.0.0", "1.1.0"}, tags: map[string]string{"latest": "1.1.0"}, created: "2021-01-01T00:00:00.000Z"}
	b := member{name: "b", versions: []string{"1.0.0", "2.0.0-rc.1"}, tags: map[string]string{"latest": "2.0.0-rc.1", "next": "1.0.0"}}
	return [][3][]byte{
		{a.doc(), b.doc(), []byte(`{}`)},
		{[]byte(`{"versions":{"1.0.0":{}}}`), []byte(`{"versions":null}`), []byte(`{"dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"dist":{"integrity":"x"}}}}`)},
	}
}
