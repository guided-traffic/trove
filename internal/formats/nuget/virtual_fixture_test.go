package nuget

import (
	"encoding/json"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
)

// mergeFixture is the fixture of check 5 (R-ARCH-010): member documents of the registration
// entries, a local member in the form of the merge and a remote one as an upstream's inlined
// index, and the reading back of the member each merged entry came from.
func mergeFixture() *formattest.MergeFixture {
	const path = "acme.widgets/.registration/index.json"
	local := func(key string, vs ...string) []byte {
		entries := make([]merge.Entry, len(vs))
		for i, v := range vs {
			entries[i] = entryOf(registrationVersion("Acme.Widgets", v, true, `{"description":"`+key+`"}`))
		}
		b, err := merge.Encode(entries)
		if err != nil {
			panic(err)
		}
		return b
	}
	return &formattest.MergeFixture{
		Cases: []formattest.MergeCase{
			{
				Name: "a local member and a remote member that share a version", Path: path,
				Parts: []formats.MemberDocument{
					{Member: formats.RepoRef{Name: "local"}, Tier: formats.TierLocal, Position: 0, Doc: local("local", "1.0.0")},
					remotePart("remote", 1, "", "1.0.0", "2.0.0"),
				},
				Sources: []string{"local", "remote"},
			},
			{
				Name: "a remote member with a cached package before one without, local last", Path: path,
				Parts: []formats.MemberDocument{
					remotePart("first", 0, "", "1.0.0", "2.0.0"),
					{Member: formats.RepoRef{Name: "second"}, Tier: formats.TierRemoteCached, Position: 1, Doc: remotePart("second", 1, "", "1.0.0").Doc},
					{Member: formats.RepoRef{Name: "third"}, Tier: formats.TierLocal, Position: 2, Doc: local("third", "2.0.0")},
				},
				Sources: []string{"first", "second", "third"},
			},
		},
		Versions: func(doc []byte) []string {
			var d struct {
				Entries []struct {
					V string `json:"v"`
				} `json:"entries"`
				Items []struct {
					Items []struct {
						CatalogEntry struct {
							Version string `json:"version"`
						} `json:"catalogEntry"`
					} `json:"items"`
				} `json:"items"`
			}
			if err := json.Unmarshal(doc, &d); err != nil {
				return nil
			}
			var out []string
			for _, e := range d.Entries {
				out = append(out, e.V)
			}
			for _, p := range d.Items {
				for _, l := range p.Items {
					out = append(out, l.CatalogEntry.Version)
				}
			}
			return out
		},
		VersionSource: func(merged []byte, version string) string {
			es, err := merge.Decode(merged)
			if err != nil {
				return ""
			}
			for _, e := range es {
				if e.Version == version {
					return e.Source
				}
			}
			return ""
		},
	}
}
