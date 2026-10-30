package repository

import (
	"context"
	"log/slog"
	"slices"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The virtual repository (R-REPO-017): its settings, how the relational part of
// the document (members, deploy target, pin rules) is read and rendered, and the
// view of it that the snapshot carries for routing.
//
// The members, the deploy target and the pin rules are rows (Q-REPO-104): the
// foreign keys carry the rule of R-REPO-013 that a repository which is a member or a
// target cannot be deleted. repositories.config keeps only the part of the
// `virtual` section that is not a reference, mergedMetadataTtlSeconds; the
// document of a read is rendered from both, and a write splits it again.

// Limits and defaults of the virtual section (R-REPO-017, R-REPO-023).
const (
	// DefaultMergedMetadataTTLSeconds is the default of mergedMetadataTtlSeconds.
	DefaultMergedMetadataTTLSeconds = 7200
	// MaxMergedMetadataTTLSeconds is the upper bound of mergedMetadataTtlSeconds.
	MaxMergedMetadataTTLSeconds = 86400
	// DefaultMaxVirtualMembers is the default of repositories.max_virtual_members.
	DefaultMaxVirtualMembers = 100
	// MaxPinRules is the number of pin rules a virtual repository holds.
	MaxPinRules = 200
	// MaxPinPatterns is the number of patterns of one pin rule (the CHECK of
	// virtual_pin_rules).
	MaxPinPatterns = 50
)

// VirtualSettings is the `virtual` section of a document (R-REPO-017).
type VirtualSettings struct {
	// Members are the keys of the member repositories in resolution order.
	Members []string `json:"members"`
	// DeployTarget is the key of the local member that takes writes (R-REPO-021);
	// null for a read-only virtual repository.
	DeployTarget *string `json:"deployTarget"`
	// MergedMetadataTTLSeconds is the period a merged document is cached
	// (R-REPO-020); 0 to 86400.
	MergedMetadataTTLSeconds *int    `json:"mergedMetadataTtlSeconds"`
	Pinning                  Pinning `json:"pinning"`
}

// Pinning is the `pinning` member of the virtual section (R-REPO-023).
type Pinning struct {
	Rules []PinRule `json:"rules"`
}

// PinRule says which members are consulted for the names that its patterns
// select (R-REPO-023). A rule without members answers 404 for its names.
type PinRule struct {
	Packages []string `json:"packages"`
	Members  []string `json:"members"`
}

// normalize fills the defaults of omitted members.
func (v *VirtualSettings) normalize() {
	if v.Members == nil {
		v.Members = []string{}
	}
	if v.MergedMetadataTTLSeconds == nil {
		ttl := DefaultMergedMetadataTTLSeconds
		v.MergedMetadataTTLSeconds = &ttl
	}
	if v.Pinning.Rules == nil {
		v.Pinning.Rules = []PinRule{}
	}
	for i := range v.Pinning.Rules {
		if v.Pinning.Rules[i].Packages == nil {
			v.Pinning.Rules[i].Packages = []string{}
		}
		if v.Pinning.Rules[i].Members == nil {
			v.Pinning.Rules[i].Members = []string{}
		}
	}
}

func (v VirtualSettings) clone() VirtualSettings {
	out := v
	out.Members = slices.Clone(v.Members)
	if v.DeployTarget != nil {
		t := *v.DeployTarget
		out.DeployTarget = &t
	}
	if v.MergedMetadataTTLSeconds != nil {
		ttl := *v.MergedMetadataTTLSeconds
		out.MergedMetadataTTLSeconds = &ttl
	}
	out.Pinning.Rules = make([]PinRule, len(v.Pinning.Rules))
	for i, r := range v.Pinning.Rules {
		out.Pinning.Rules[i] = PinRule{Packages: slices.Clone(r.Packages), Members: slices.Clone(r.Members)}
	}
	return out
}

// storedVirtual is what repositories.config keeps of the virtual section: the
// part that is not a reference to another repository.
type storedVirtual struct {
	MergedMetadataTTLSeconds int `json:"mergedMetadataTtlSeconds"`
}

// virtualState is the routing view of a virtual repository that a snapshot
// carries. It is immutable once the snapshot is built.
type virtualState struct {
	settings VirtualSettings
	// members are the member repositories in list order; nil in a repository that
	// did not come from the snapshot.
	members   []Repository
	memberIDs map[int64]bool
	target    *Repository
	pins      *pinIndex
}

// Virtual returns the virtual section of a virtual repository as the snapshot
// holds it: the members in order, the deploy target, the merged-metadata period
// and the pin rules. ok is false for a repository of another class.
func (r Repository) Virtual() (VirtualSettings, bool) {
	if r.virt == nil {
		return VirtualSettings{}, false
	}
	return r.virt.settings.clone(), true
}

// Members returns the member repositories of a virtual repository in resolution
// order, as the snapshot holds them (the list is shared: do not modify it). It is
// empty for a repository of another class and for one that did not come from the
// snapshot.
func (r Repository) Members() []Repository {
	if r.virt == nil {
		return nil
	}
	return r.virt.members
}

// DeployTarget returns the deployment target of a virtual repository
// (R-REPO-021); ok is false when there is none.
func (r Repository) DeployTarget() (Repository, bool) {
	if r.virt == nil || r.virt.target == nil {
		return Repository{}, false
	}
	return *r.virt.target, true
}

// relRef is a repository named by a row of the virtual tables.
type relRef struct {
	id  int64
	key string
}

type relPin struct {
	patterns []string
	members  []relRef
}

// virtualRel is the relational part of one virtual repository's document.
type virtualRel struct {
	members []relRef
	target  *relRef
	pins    []relPin
}

func keysOf(refs []relRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.key
	}
	return out
}

// settings renders the relational part into the section the document shows.
func (rel *virtualRel) settings(mergedTTL int) VirtualSettings {
	v := VirtualSettings{Members: []string{}, MergedMetadataTTLSeconds: &mergedTTL, Pinning: Pinning{Rules: []PinRule{}}}
	if rel == nil {
		return v
	}
	v.Members = keysOf(rel.members)
	if rel.target != nil {
		t := rel.target.key
		v.DeployTarget = &t
	}
	for _, p := range rel.pins {
		v.Pinning.Rules = append(v.Pinning.Rules, PinRule{Packages: slices.Clone(p.patterns), Members: keysOf(p.members)})
	}
	return v
}

// loadVirtualRels reads the relational part of the given virtual repositories.
func loadVirtualRels(ctx context.Context, qs *q.Queries, ids []int64) (map[int64]*virtualRel, error) {
	out := make(map[int64]*virtualRel, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		out[id] = &virtualRel{}
	}
	members, err := qs.ListVirtualMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		rel := out[m.VirtualID]
		rel.members = append(rel.members, relRef{id: m.MemberID, key: m.MemberKey})
	}
	targets, err := qs.ListVirtualDeployTargets(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		out[t.VirtualID].target = &relRef{id: t.TargetID, key: t.TargetKey}
	}
	rules, err := qs.ListVirtualPinRules(ctx, ids)
	if err != nil {
		return nil, err
	}
	type ruleKey struct {
		virtual  int64
		position int32
	}
	at := map[ruleKey]int{}
	for _, p := range rules {
		rel := out[p.VirtualID]
		at[ruleKey{p.VirtualID, p.Position}] = len(rel.pins)
		rel.pins = append(rel.pins, relPin{patterns: p.PackagePatterns})
	}
	pm, err := qs.ListVirtualPinMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, m := range pm {
		rel := out[m.VirtualID]
		i := at[ruleKey{m.VirtualID, m.Position}]
		rel.pins[i].members = append(rel.pins[i].members, relRef{id: m.MemberID, key: m.MemberKey})
	}
	return out, nil
}

// virtualIDs lists the ids of the virtual repositories among rows.
func virtualIDs(rows []q.Repository) []int64 {
	var ids []int64
	for _, r := range rows {
		if Class(r.Class) == Virtual {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// mergedTTLOf reads mergedMetadataTtlSeconds from a stored configuration; the
// default when the document does not hold one.
func mergedTTLOf(s Settings) int {
	if s.Virtual != nil && s.Virtual.MergedMetadataTTLSeconds != nil {
		return *s.Virtual.MergedMetadataTTLSeconds
	}
	return DefaultMergedMetadataTTLSeconds
}

// render puts the relational part into a document read from a row.
func (d *Document) renderVirtual(rel *virtualRel) {
	if d.Class != Virtual {
		d.Virtual = nil
		return
	}
	v := rel.settings(mergedTTLOf(d.Settings))
	d.Virtual = &v
}

// readSnapshot reads the repository table and then the relational part of the
// virtual repositories. The two reads are not one snapshot: a member created
// between them is missing from the list and is left out of its virtual
// repository (fail closed). That creation is a repo_config change, for which the
// fill rule of the cache (R-ARCH-018) discards the load or flushes the stored
// snapshot, so the next load sees both.
func readSnapshot(ctx context.Context, run meta.Runner) ([]q.Repository, map[int64]*virtualRel, error) {
	var rows []q.Repository
	var rels map[int64]*virtualRel
	err := run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if rows, err = qs.ListRepositories(ctx); err != nil {
			return err
		}
		rels, err = loadVirtualRels(ctx, qs, virtualIDs(rows))
		return err
	})
	return rows, rels, err
}

// linkVirtual gives every virtual repository of repos its routing view: the
// resolved members in order, the deploy target and the pin index. A reference to
// a repository that is not in the list is left out and logged, which only
// removes a member (fail closed).
func linkVirtual(repos []Repository, rels map[int64]*virtualRel, cat *catalog.Registry, log *slog.Logger) {
	byID := make(map[int64]int, len(repos))
	for i, r := range repos {
		byID[r.rid] = i
	}
	for i := range repos {
		v := &repos[i]
		if v.Class != Virtual || v.virt == nil {
			continue
		}
		rel := rels[v.rid]
		st := &virtualState{settings: rel.settings(mergedTTLOfState(v.virt)), memberIDs: map[int64]bool{}}
		for _, m := range rel.members {
			j, ok := byID[m.id]
			if !ok {
				log.Warn("repository: a virtual repository names a member that is not in the snapshot yet", "virtual", v.Key, "member", m.key)
				continue
			}
			st.members = append(st.members, repos[j])
			st.memberIDs[m.id] = true
			if rel.target != nil && rel.target.id == m.id {
				t := repos[j]
				st.target = &t
			}
		}
		st.pins = newPinIndex(cat, v.Format, st.settings.Pinning.Rules, log, v.Key)
		v.virt = st
	}
}

func mergedTTLOfState(s *virtualState) int {
	if s != nil && s.settings.MergedMetadataTTLSeconds != nil {
		return *s.settings.MergedMetadataTTLSeconds
	}
	return DefaultMergedMetadataTTLSeconds
}
