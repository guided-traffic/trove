package repository

import (
	"context"
	"slices"
	"strings"
)

// Withheld search terms (R-REPO-023, R-NUGET-016): a format that forwards a free-text search to a
// remote repository must not leak, through the query, a name the configuration keeps away from
// that repository. KeepsAway answers it for one term from the replica's in-memory indices alone.

// stemSeparators are the characters a name prefix usually ends in: `Acme.*` protects the term
// `acme` as well as the names below it.
const stemSeparators = ".-_"

// KeepsAway reports whether the configuration in effect keeps a search term away from a member.
//
// The term is a package name as the format folds names. It is kept away when
//   - an exclude pattern of the member matches it (for a format whose patterns match names; an
//     include pattern restricts what a repository serves and protects nothing),
//   - through a virtual repository (via), its pin rule for the name does not list the member, or,
//     without a pin rule, a claim in effect in that repository covers the name and the member is
//     not one of the claimants (R-REPO-019 steps 2 and 3),
//   - on a remote repository addressed directly (via is nil), a global claim covers the name and
//     the remote refusal is on (R-REPO-023),
//   - or the term is the literal stem of a prefix pattern of one of those sources: the literal
//     part of the pattern, with or without one trailing `.`, `-` or `_`.
//
// A term that is no name of the format keeps nothing away: no package has it. The call reads the
// claim index of the replica and the compiled patterns and runs no database query.
func (rt *Routing) KeepsAway(ctx context.Context, via *Repository, member Repository, term string) (bool, error) {
	idx, err := rt.index(ctx)
	if err != nil {
		return false, err
	}
	return keepsAway(rt, idx, via, member, term), nil
}

func keepsAway(rt *Routing, idx *claimIndex, via *Repository, member Repository, term string) bool {
	name, ok := rt.cat.NormalizeName(member.Format, term)
	if !ok || name == "" {
		return false
	}
	info, known := rt.cat.Lookup(member.Format)
	if !known {
		return false
	}
	stems := stemCandidates(name)
	if info.PatternSubject == SubjectName && (member.adm.excludes(name) || member.adm.hasStem(stems)) {
		return true
	}
	if via != nil && via.virt != nil {
		return keptFromMember(rt, idx, via, member, name, stems)
	}
	if member.Class == Remote && rt.refusal() {
		if remoteRefused(rt.cat, idx, member, name, nil) {
			return true
		}
		for _, h := range idx.stems(member.Format, stems) {
			if h.scope == ScopeGlobal {
				return true
			}
		}
	}
	return false
}

// keptFromMember is the part of KeepsAway that depends on the virtual repository's pin rules and
// the claims in effect in it.
func keptFromMember(rt *Routing, idx *claimIndex, via *Repository, member Repository, name string, stems []string) bool {
	r := routeOf(rt.cat, idx, func(int64) string { return "" }, *via, RouteRequest{Name: name})
	switch r.Dropped[member.Name] {
	case DropPinnedOut, DropClaimedElsewhere:
		return true
	}
	for _, h := range inEffect(idx.stems(member.Format, stems), via.virt) {
		if h.repoID != member.rid {
			return true
		}
	}
	for _, rule := range via.virt.pins.rules {
		if slices.Contains(rule.Members, member.Name) {
			continue
		}
		for _, raw := range rule.Packages {
			if folded, ok := rt.cat.NormalizeName(via.Format, raw); ok && patternHasStem(folded, stems) {
				return true
			}
		}
	}
	return false
}

// stemCandidates are the literals a prefix pattern has when a term is its stem: the term itself,
// and the term with one separator.
func stemCandidates(name string) []string {
	out := make([]string, 0, 1+len(stemSeparators))
	out = append(out, name)
	for _, s := range stemSeparators {
		out = append(out, name+string(s))
	}
	return out
}

// excludes reports whether an exclude pattern matches s.
func (a *Admission) excludes(s string) bool {
	if a == nil {
		return false
	}
	for _, p := range a.exclude {
		if p.Match(s) {
			return true
		}
	}
	return false
}

// hasStem reports whether an exclude pattern is a prefix pattern whose literal is one of the
// candidates.
func (a *Admission) hasStem(stems []string) bool {
	if a == nil {
		return false
	}
	for _, p := range a.exclude {
		if lit, ok := p.prefixLiteral(); ok && slices.Contains(stems, strings.ToLower(lit)) {
			return true
		}
	}
	return false
}

// prefixLiteral returns the literal of a name pattern that is a literal followed by one trailing
// `*` or `**`.
func (p *Pattern) prefixLiteral() (string, bool) {
	if p.subject != SubjectName || len(p.segs) != 1 || p.segs[0].globstar {
		return "", false
	}
	toks := p.segs[0].toks
	if len(toks) == 0 {
		return "", false
	}
	if last := toks[len(toks)-1]; last.kind != tokStar && last.kind != tokAny {
		return "", false
	}
	var b strings.Builder
	for _, t := range toks[:len(toks)-1] {
		if t.kind != tokLit {
			return "", false
		}
		b.WriteRune(t.r)
	}
	return b.String(), true
}

// patternHasStem reports whether a folded claim or pin-rule pattern is a prefix pattern whose
// literal is one of the candidates.
func patternHasStem(folded string, stems []string) bool {
	cp, err := ParseClaimPattern(folded)
	return err == nil && cp.kind != claimExact && slices.Contains(stems, cp.literal)
}

// stems returns the prefix claims of the format whose literal is one of the candidates.
func (x *claimIndex) stems(format string, stems []string) []claimHit {
	t := x.byFormat[format]
	if t == nil {
		return nil
	}
	var out []claimHit
	for _, s := range stems {
		for _, e := range t.at(s) {
			if e.pat.kind != claimExact {
				out = append(out, claimHit{claimRef: e.val, pattern: e.pat})
			}
		}
	}
	return out
}

// at returns the entries whose literal is exactly lit.
func (t *patTrie[T]) at(lit string) []patEntry[T] {
	n := &t.root
	for i := 0; i < len(lit); i++ {
		if n = n.next[lit[i]]; n == nil {
			return nil
		}
	}
	return n.entries
}
