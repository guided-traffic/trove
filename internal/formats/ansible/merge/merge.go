package merge

import (
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// VersionEntry is one entry of a collection's version list.
type VersionEntry = galaxydoc.VersionEntry

// Part is the version list of one member.
type Part struct {
	// Member names the member the part comes from; the merged list records it per entry. Two parts of one member (its cached and its
	// uncached versions) never conflict with each other.
	Member string
	// Tier and Position order the members as the repository's resolution order does (R-REPO-019): the lowest (Tier,
	// Position) wins a version that several members hold. A local member has the lowest tier, so its version never loses
	// to a remote member's.
	Tier, Position int
	Entries        []VersionEntry
	// SHA256 is the archive digest of a version (by the entry's Version string), where the member announces it. A version
	// without a digest on either side is never a conflict.
	SHA256 map[string][32]byte
}

// Conflict is a version that two members hold with different archive digests (R-ANS-022). The winner's entry stays in
// the merged list.
type Conflict struct {
	Version string
	// Winner and Loser are indexes into the parts handed to MergeLists.
	Winner, Loser int
}

// Merged is the result of MergeLists.
type Merged struct {
	// Entries are the union of the members' versions, one entry per version (build metadata ignored), newest first.
	Entries []VersionEntry
	// Source is, per index of Entries, the index of the part whose entry won.
	Source []int
	// Conflicts has one element per losing member and version whose digest differs from the winner's.
	Conflicts []Conflict
}

type winner struct {
	entry VersionEntry
	part  int
	sum   [32]byte
	known bool
}

// MergeLists unions the members' version lists. For a version in several members the entry of the lowest (Tier,
// Position) wins; the part that comes first in parts wins a tie of rank. The order is semantic-version precedence
// descending; a string that is not a semantic version sorts after every valid one.
func MergeLists(parts []Part) Merged { //nolint:revive // the names are the ones of the work-package plan, which the virtual adapter is written against
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := parts[order[a]], parts[order[b]]
		if pa.Tier != pb.Tier {
			return pa.Tier < pb.Tier
		}
		return pa.Position < pb.Position
	})
	byKey := map[string]*winner{}
	var out Merged
	for _, pi := range order {
		p := parts[pi]
		seen := map[string]bool{}
		for _, e := range p.Entries {
			key := Key(e.Version)
			if seen[key] {
				continue
			}
			seen[key] = true
			sum, known := p.SHA256[e.Version]
			w, ok := byKey[key]
			if !ok {
				byKey[key] = &winner{entry: e, part: pi, sum: sum, known: known}
				continue
			}
			if w.known && known && w.sum != sum && (p.Member == "" || parts[w.part].Member != p.Member) {
				out.Conflicts = append(out.Conflicts, Conflict{Version: w.entry.Version, Winner: w.part, Loser: pi})
			}
		}
	}
	wins := make([]*winner, 0, len(byKey))
	for _, w := range byKey {
		wins = append(wins, w)
	}
	sort.Slice(wins, func(i, j int) bool { return Compare(wins[i].entry.Version, wins[j].entry.Version) > 0 })
	out.Entries = make([]VersionEntry, len(wins))
	out.Source = make([]int, len(wins))
	for i, w := range wins {
		out.Entries[i], out.Source[i] = w.entry, w.part
	}
	return out
}

// Page cuts one page out of a merged list, after the merge, so that limit and offset are consistent across members. An
// offset beyond the list, and a limit of 0 or less, give an empty page.
func Page(entries []VersionEntry, limit, offset int) []VersionEntry {
	if limit <= 0 || offset < 0 || offset >= len(entries) {
		return nil
	}
	return entries[offset:min(offset+limit, len(entries))]
}

// Collection is what the members' collection documents contribute to the merged collection document.
type Collection struct {
	// HighestVersion is the highest of the merged list, "" for an empty list.
	HighestVersion string
	// CreatedAt is the earliest and UpdatedAt the latest of the members'; a zero time is no information.
	CreatedAt, UpdatedAt time.Time
}

// Times are the creation and update time of a collection in one member.
type Times struct{ CreatedAt, UpdatedAt time.Time }

// MergeCollection derives the merged collection document from the merged list (newest first) and the members' times.
func MergeCollection(merged []VersionEntry, members []Times) Collection { //nolint:revive // see MergeLists
	var c Collection
	if len(merged) > 0 {
		c.HighestVersion = merged[0].Version
	}
	for _, m := range members {
		if !m.CreatedAt.IsZero() && (c.CreatedAt.IsZero() || m.CreatedAt.Before(c.CreatedAt)) {
			c.CreatedAt = m.CreatedAt
		}
		if m.UpdatedAt.After(c.UpdatedAt) {
			c.UpdatedAt = m.UpdatedAt
		}
	}
	return c
}

// Key identifies a version for the union: two versions that differ only in build metadata are one version
// (R-ANS-002). A string that is no semantic version is its own key.
func Key(v string) string {
	if s, err := version.Parse(v); err == nil {
		return "v" + string(version.SortKey(s))
	}
	return "x" + v
}

// Compare orders two versions by semantic-version precedence (build metadata ignored); a string that is not a semantic
// version is lower than every one that is, and two of them compare as bytes.
func Compare(a, b string) int {
	sa, aerr := version.Parse(a)
	sb, berr := version.Parse(b)
	switch {
	case aerr != nil && berr != nil:
		return strings.Compare(a, b)
	case aerr != nil:
		return -1
	case berr != nil:
		return 1
	}
	return version.Compare(sa, sb)
}
