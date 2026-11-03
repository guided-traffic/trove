package sync

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The alias groups of R-VULN-006. Records of one ecosystem that name each other in aliases, directly or through a chain, are one
// vulnerability; its canonical identifier is chosen among the stored records of the group in the order MAL-, GHSA-, then the
// lexically smallest. The grouping is recomputed from the stored rows at the end of a run for every group that has a record
// the run wrote, so that the order in which records arrive never matters.

const (
	// groupChunk is the number of names one closure query starts from.
	groupChunk = 2000
	// maxClosureRounds bounds the walk along alias chains; a chain that long is no vulnerability's alias list.
	maxClosureRounds = 16
	kevChunk         = 2000
)

// touchedNames are the names a written record puts into the run's touched set: its identifier and its aliases.
func touchedNames(a advisory.Advisory) []string {
	return append([]string{a.ID}, a.Aliases...)
}

// touchedPage is the number of touched names one recomputation pass reads.
const touchedPage = 20000

// canonicalOfRows chooses the canonical identifier of a group among its stored records (R-VULN-006, Q-VULN-61): a withdrawn
// record is never chosen while the group has another, so that a withdrawn MAL- record cannot make an alias group malicious. A
// group of withdrawn records only chooses among them by the same order.
func canonicalOfRows(members []store.GroupRow) string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		if !m.Withdrawn {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		for _, m := range members {
			ids = append(ids, m.ID)
		}
	}
	return canonicalOf(ids)
}

// canonicalOf chooses the canonical identifier among identifiers (R-VULN-006): the lexically smallest MAL-, else the lexically
// smallest GHSA-, else the lexically smallest.
func canonicalOf(ids []string) string {
	best := ""
	rank := func(id string) int {
		switch {
		case strings.HasPrefix(id, "MAL-"):
			return 0
		case strings.HasPrefix(id, "GHSA-"):
			return 1
		}
		return 2
	}
	for _, id := range ids {
		if best == "" || rank(id) < rank(best) || (rank(id) == rank(best) && id < best) {
			best = id
		}
	}
	return best
}

// dsu is a union-find over names.
type dsu struct{ parent map[string]string }

func (d *dsu) find(x string) string {
	if _, ok := d.parent[x]; !ok {
		d.parent[x] = x
	}
	for d.parent[x] != x {
		d.parent[x] = d.parent[d.parent[x]]
		x = d.parent[x]
	}
	return x
}

func (d *dsu) union(a, b string) {
	ra, rb := d.find(a), d.find(b)
	if ra != rb {
		d.parent[ra] = rb
	}
}

// groupStats is what a recomputation looked at.
type groupStats struct {
	// canonical are the canonical identifiers of every group looked at, for the known-exploited flags.
	canonical []string
	// announce is true when a change of a group's kind is an audit event.
	announce bool
}

// recomputeGroups finds the alias group of every name, assigns the canonical identifier and decides the severity of each
// canonical record with its aliases (R-VULN-006, R-VULN-007 rule 3). Names that no stored record is or aliases are ignored.
// The writes of one chunk, the queueing of what they changed for the evaluation and the audit events of a kind change run in one
// transaction.
func (s *Service) recomputeGroups(ctx context.Context, ecosystem string, names []string, announce bool) (groupStats, error) {
	stats := groupStats{announce: announce}
	done := map[string]bool{}
	for len(names) > 0 {
		chunk := names[:min(groupChunk, len(names))]
		names = names[len(chunk):]
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		rows, err := s.closure(ctx, ecosystem, chunk)
		if err != nil {
			return stats, err
		}
		if len(rows) == 0 {
			continue
		}
		groups := groupRows(rows)
		var pending []group
		for _, g := range groups {
			if done[g.members[0].ID] {
				continue
			}
			for _, m := range g.members {
				done[m.ID] = true
			}
			pending = append(pending, g)
		}
		if len(pending) == 0 {
			continue
		}
		err = s.o.DB.InTx(ctx, func(tx metaTx) error {
			db, err := store.DBOf(tx)
			if err != nil {
				return err
			}
			for _, g := range pending {
				if err := s.writeGroup(ctx, tx, db, ecosystem, g, &stats); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// closure loads the stored records that are or alias the names, then those that are or alias their names in turn, until no new
// name appears.
func (s *Service) closure(ctx context.Context, ecosystem string, names []string) ([]store.GroupRow, error) {
	known := map[string]store.GroupRow{}
	asked := map[string]bool{}
	frontier := slices.Clone(names)
	for round := 0; round < maxClosureRounds && len(frontier) > 0; round++ {
		for _, n := range frontier {
			asked[n] = true
		}
		var rows []store.GroupRow
		err := s.o.DB.Read(ctx, func(db dbtx) (err error) {
			rows, err = store.Sync{}.GroupRows(ctx, db, ecosystem, frontier)
			return err
		})
		if err != nil {
			return nil, err
		}
		frontier = nil
		for _, r := range rows {
			if _, ok := known[r.ID]; ok {
				continue
			}
			known[r.ID] = r
			for _, n := range append([]string{r.ID}, r.Aliases...) {
				if !asked[n] {
					asked[n] = true
					frontier = append(frontier, n)
				}
			}
		}
	}
	out := make([]store.GroupRow, 0, len(known))
	for _, r := range known {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b store.GroupRow) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// group is one alias group: the stored records that belong to it.
type group struct {
	members []store.GroupRow
}

// groupRows splits rows into alias groups by union-find over the identifiers and aliases they name.
func groupRows(rows []store.GroupRow) []group {
	d := &dsu{parent: map[string]string{}}
	for _, r := range rows {
		d.find(r.ID)
		for _, a := range r.Aliases {
			d.union(r.ID, a)
		}
	}
	by := map[string][]store.GroupRow{}
	for _, r := range rows {
		root := d.find(r.ID)
		by[root] = append(by[root], r)
	}
	out := make([]group, 0, len(by))
	for _, ms := range by {
		slices.SortFunc(ms, func(a, b store.GroupRow) int { return strings.Compare(a.ID, b.ID) })
		out = append(out, group{members: ms})
	}
	slices.SortFunc(out, func(a, b group) int { return strings.Compare(a.members[0].ID, b.members[0].ID) })
	return out
}

func ownOf(r store.GroupOwn) osv.Own {
	return osv.Own{Level: r.Severity, Source: r.SeveritySource, CVSS: r.CVSS}
}

func decidedOf(r store.GroupRow) osv.Own {
	return osv.Own{Level: r.Severity, Source: r.SeveritySource, CVSS: r.CVSS}
}

// maxEventMembers is the number of group members an audit event of a kind change names.
const maxEventMembers = 50

// writeGroup assigns the canonical identifier of one group, decides the severity of each of its records and queues what changed
// for the evaluation, in the transaction of the caller (R-VULN-006, R-VULN-007). The canonical record's severity is decided from
// the own severities of the group, so a changed alias vector is adopted and a rewrite of a record, which leaves the decided
// columns alone, is not undone (Q-VULN-62). When the kind of the canonical record differs from the kind of the one it replaces,
// an audit event records the change (Q-VULN-61).
func (s *Service) writeGroup(ctx context.Context, tx metaTx, db dbtx, ecosystem string, g group, stats *groupStats) error {
	canonical := canonicalOfRows(g.members)
	stats.canonical = append(stats.canonical, canonical)

	var change, requeue []string
	var canon store.GroupRow
	var aliasOwns []osv.Own
	byID := make(map[string]store.GroupRow, len(g.members))
	for _, m := range g.members {
		byID[m.ID] = m
		if m.ID == canonical {
			canon = m
		} else {
			aliasOwns = append(aliasOwns, ownOf(m.Own))
		}
		if m.CanonicalID != canonical {
			change = append(change, m.ID)
			requeue = append(requeue, m.ID, m.CanonicalID)
		}
	}
	if len(change) > 0 {
		if err := (store.Sync{}).SetCanonical(ctx, db, ecosystem, change, canonical); err != nil {
			return err
		}
		requeue = append(requeue, canonical)
		if stats.announce {
			if err := s.recordKindChange(ctx, tx, ecosystem, g, canon, byID); err != nil {
				return err
			}
		}
	}
	for _, m := range g.members {
		want := ownOf(m.Own)
		if m.ID == canonical {
			want = osv.Decide(want, aliasOwns)
		}
		if want == decidedOf(m) {
			continue
		}
		if err := (store.Sync{}).SetSeverity(ctx, db, ecosystem, m.ID, want.Level, want.Source, want.CVSS); err != nil {
			return err
		}
		if m.ID == canonical {
			requeue = append(requeue, canonical)
		}
	}
	slices.Sort(requeue)
	return (store.Sync{}).AddPending(ctx, db, ecosystem, slices.Compact(requeue))
}

// recordKindChange writes the audit event of an alias group whose canonical record changed to one of another kind. The group
// the members formed before are the classes of their stored canonical identifiers; the kind of a class is the kind of its
// canonical record. A class of one withdrawn record, a record that joins the group already withdrawn, is not a group that
// existed, and a class whose canonical record is no longer stored has no kind to compare. Of several classes of another kind the
// smallest identifier is named.
func (s *Service) recordKindChange(ctx context.Context, tx metaTx, ecosystem string, g group, canon store.GroupRow, byID map[string]store.GroupRow) error {
	size := map[string]int{}
	for _, m := range g.members {
		size[m.CanonicalID]++
	}
	prev := ""
	var from advisory.Kind
	for id, n := range size {
		old, ok := byID[id]
		if !ok || old.Kind == canon.Kind || (old.Withdrawn && n == 1) {
			continue
		}
		if prev == "" || old.ID < prev {
			prev, from = old.ID, old.Kind
		}
	}
	if prev == "" {
		return nil
	}
	members := make([]string, 0, min(len(g.members), maxEventMembers))
	for _, m := range g.members {
		if len(members) == maxEventMembers {
			break
		}
		members = append(members, m.ID)
	}
	body, err := json.Marshal(map[string]any{
		"ecosystem": ecosystem, "advisory": canon.ID, "previous": prev, "from": string(from), "to": string(canon.Kind), "members": members,
	})
	if err != nil {
		return err
	}
	s.log.Warn("the kind of an advisory alias group changed", "ecosystem", ecosystem, "advisory", canon.ID, "previous", prev, "from", from, "to", canon.Kind)
	return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventVulnAdvisoryKindChanged, Actor: audit.Actor{Kind: "system", Name: "vulnerability-sync"}, Outcome: audit.Success,
		Subject: audit.Subject{Kind: "vulnerability_advisory", Name: canon.ID}, Details: body,
	})
}

// applyKEV recomputes the known-exploited flag of the canonical records and queues those whose flag changed for the
// evaluation (R-VULN-008). It returns the number that changed.
func (s *Service) applyKEV(ctx context.Context, ecosystem string, canonical []string) (int, error) {
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	changed := 0
	for len(canonical) > 0 {
		chunk := canonical[:min(kevChunk, len(canonical))]
		canonical = canonical[len(chunk):]
		err := s.o.DB.InTx(ctx, func(tx metaTx) error {
			db, err := store.DBOf(tx)
			if err != nil {
				return err
			}
			ids, err := (store.Sync{}).ApplyKEV(ctx, db, ecosystem, chunk)
			if err != nil {
				return err
			}
			changed += len(ids)
			return (store.Sync{}).AddPending(ctx, db, ecosystem, ids)
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
