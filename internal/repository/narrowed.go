package repository

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
)

// The narrowings of a repository update in the revocation journal (R-REPO-061, Q-REPO-245): what the update found to narrow,
// as the entry of repository.narrowed that the replay reads (journal_replay.go).

// EventNarrowed is the audit event of an update that narrowed what the repository serves, recorded beside repository.updated.
const EventNarrowed = audit.EventRepositoryNarrowed

// The parts of an entry, as the entry's members and the event's parts name them.
const (
	partInclude           = "include"
	partExcludeAdded      = "excludeAdded"
	partAutoClaim         = "autoClaim"
	partMembersRemoved    = "membersRemoved"
	partPinsAdded         = "pinsAdded"
	partPinMembersRemoved = "pinMembersRemoved"
	partVulnerabilities   = "vulnerabilities"
)

// repoRef names a repository of an entry: its ID as decimal text, which the replay acts on, and its qualified name, for
// display.
type repoRef struct {
	RepositoryID string `json:"repositoryId"`
	Key          string `json:"key"`
}

// pinPart is a pin-rule pattern with members: the members of the rule that holds it (pinsAdded) or the members it lost
// (pinMembersRemoved).
type pinPart struct {
	Pattern string    `json:"pattern"`
	Members []repoRef `json:"members"`
}

// vulnChange is the member `vulnerabilities` before and after a write that made it more blocking; an absent member is the
// defaults, so both values are complete.
type vulnChange struct {
	Before vuln.Settings `json:"before"`
	After  vuln.Settings `json:"after"`
}

// narrowedEntry is the journal entry of repository.narrowed: the repository, and each part that narrowed, only when it did.
type narrowedEntry struct {
	RepositoryID      string      `json:"repositoryId"`
	Key               string      `json:"key"`
	Include           []string    `json:"include,omitempty"`
	ExcludeAdded      []string    `json:"excludeAdded,omitempty"`
	AutoClaim         bool        `json:"autoClaim,omitempty"`
	MembersRemoved    []repoRef   `json:"membersRemoved,omitempty"`
	PinsAdded         []pinPart   `json:"pinsAdded,omitempty"`
	PinMembersRemoved []pinPart   `json:"pinMembersRemoved,omitempty"`
	Vulnerabilities   *vulnChange `json:"vulnerabilities,omitempty"`
}

// parts lists the parts the entry carries, in the order of the table of R-REPO-061.
func (e narrowedEntry) parts() []string {
	out := []string{}
	add := func(has bool, name string) {
		if has {
			out = append(out, name)
		}
	}
	add(len(e.Include) > 0, partInclude)
	add(len(e.ExcludeAdded) > 0, partExcludeAdded)
	add(e.AutoClaim, partAutoClaim)
	add(len(e.MembersRemoved) > 0, partMembersRemoved)
	add(len(e.PinsAdded) > 0, partPinsAdded)
	add(len(e.PinMembersRemoved) > 0, partPinMembersRemoved)
	add(e.Vulnerabilities != nil, partVulnerabilities)
	return out
}

// narrowedDetails is the data of repository.narrowed: the repository's class, format and project, and the parts.
type narrowedDetails struct {
	Class   Class    `json:"class"`
	Format  string   `json:"format"`
	Project string   `json:"project,omitempty"`
	Parts   []string `json:"parts"`
}

// narrowing compares the stored document and rows of an update with the new ones and returns what narrows (the table of
// R-REPO-061). The rows are nil for a repository that is not virtual. A part that widens is not part of the result, so the
// widening half of a mixed update is never replayed.
func (s *Service) narrowing(r Repository, cur, nd *Document, curRel, rel *virtualRel) narrowedEntry {
	e := narrowedEntry{RepositoryID: r.ID, Key: r.Name.String()}
	e.Include = includeNarrowed(s.cat, cur.Format, cur.IncludePatterns, nd.IncludePatterns)
	e.ExcludeAdded = addedPatterns(cur.ExcludePatterns, nd.ExcludePatterns)
	if r.Class == Local {
		info, known := s.cat.Lookup(cur.Format)
		e.AutoClaim = !effectiveAutoClaim(info, known, &cur.Settings) && effectiveAutoClaim(info, known, &nd.Settings)
	}
	if curRel != nil && rel != nil {
		e.MembersRemoved = lostRefs(r.Name.Project, curRel.members, rel.members)
		e.PinsAdded, e.PinMembersRemoved = pinNarrowings(r.Name.Project, curRel.pins, rel.pins)
	}
	before, after := vuln.DefaultSettings(), vuln.DefaultSettings()
	if cur.Vulnerabilities != nil {
		before = *cur.Vulnerabilities
	}
	if nd.Vulnerabilities != nil {
		after = *nd.Vulnerabilities
	}
	// The write strengthens the member when the old value does less than the new one in some field.
	if len(weakensVulnerabilities(after, before)) > 0 {
		e.Vulnerabilities = &vulnChange{Before: before, After: after}
	}
	return e
}

// includeNarrowed is the new include list when the update narrows it: the list is not empty (an emptied list admits
// everything) and the old one was empty or held a pattern that no pattern of the new one covers. Patterns are compared in the
// folded form of the format's name rule, so a change of case only is no narrowing, and a pattern covers another when it is
// the same or a trailing wildcard that takes in all the other can match ([patternCovers]); an edit this cannot prove to
// widen counts as a narrowing, which the replay applies as an intersection and so cannot make wider than either list.
func includeNarrowed(cat *catalog.Registry, format string, old, next []string) []string {
	if len(next) == 0 {
		return nil
	}
	if len(old) == 0 {
		return slices.Clone(next)
	}
	fold := func(list []string) []string {
		if info, ok := cat.Lookup(format); ok && info.PatternSubject == SubjectName {
			return foldAll(cat, format, list)
		}
		return list
	}
	folded := fold(next)
	for _, p := range fold(old) {
		if !slices.ContainsFunc(folded, func(q string) bool { return patternCovers(q, p) }) {
			return slices.Clone(next)
		}
	}
	return nil
}

// patternCovers reports whether pattern q matches every name pattern p matches. It proves two cases and refuses the rest: the
// patterns are equal, or q is a literal followed by one trailing `*` (or `**`) and p starts with that literal, with a rest that
// cannot cross a `/` (for `*`) and holds no wildcard that could.
func patternCovers(q, p string) bool {
	if q == p {
		return true
	}
	lit, deep := strings.CutSuffix(q, "**")
	if !deep {
		var star bool
		if lit, star = strings.CutSuffix(q, "*"); !star {
			return false
		}
	}
	if strings.ContainsAny(lit, `*?[]{}\`) {
		return false
	}
	rest, ok := strings.CutPrefix(p, lit)
	if !ok {
		return false
	}
	return deep || !strings.ContainsAny(rest, "/[]{}\\") && !strings.Contains(rest, "**")
}

// addedPatterns lists the patterns of next that old lacks.
func addedPatterns(old, next []string) []string {
	var out []string
	for _, p := range next {
		if !slices.Contains(old, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func qualified(project, key string) string {
	if project == "" {
		return key
	}
	return project + "/" + key
}

func refOf(project string, m relRef) repoRef {
	return repoRef{RepositoryID: strconv.FormatInt(m.id, 10), Key: qualified(project, m.key)}
}

// lostRefs lists the members of old that next lacks, compared by repository ID.
func lostRefs(project string, old, next []relRef) []repoRef {
	var out []repoRef
	for _, m := range old {
		if !slices.ContainsFunc(next, func(o relRef) bool { return o.id == m.id }) {
			out = append(out, refOf(project, m))
		}
	}
	return out
}

// pinsByPattern maps each pattern of the rules to the members of the rule that holds it, and lists the patterns in order.
func pinsByPattern(rules []relPin) (order []string, byPattern map[string][]relRef) {
	byPattern = map[string][]relRef{}
	for _, rule := range rules {
		for _, p := range rule.patterns {
			if _, dup := byPattern[p]; !dup {
				order = append(order, p)
				byPattern[p] = rule.members
			}
		}
	}
	return order, byPattern
}

// pinNarrowings finds the two narrowings of the pin rules: a pattern that no rule held and a rule holds now (with the
// members of that rule), and a pattern that a rule held before and after and lost members of (with those members). A pattern
// that no rule holds any more and a member added widen and are not parts; a pattern added to a rule that exists is a pattern
// that no rule held, so it is a part.
func pinNarrowings(project string, old, next []relPin) (added, lost []pinPart) {
	_, before := pinsByPattern(old)
	order, after := pinsByPattern(next)
	for _, p := range order {
		members, had := before[p]
		if !had {
			part := pinPart{Pattern: p, Members: []repoRef{}}
			for _, m := range after[p] {
				part.Members = append(part.Members, refOf(project, m))
			}
			added = append(added, part)
			continue
		}
		if gone := lostRefs(project, members, after[p]); len(gone) > 0 {
			lost = append(lost, pinPart{Pattern: p, Members: gone})
		}
	}
	return added, lost
}

// recordNarrowed records repository.narrowed beside the update's repository.updated, in the same transaction, when the update
// narrowed something. The audit event's journal entry is the narrowing; its details name the parts.
func (s *Service) recordNarrowed(ctx context.Context, tx meta.Tx, c Caller, r Repository, e narrowedEntry) error {
	parts := e.parts()
	if len(parts) == 0 {
		return nil
	}
	details, err := json.Marshal(narrowedDetails{Class: r.Class, Format: r.Format, Project: r.Name.Project, Parts: parts})
	if err != nil {
		return err
	}
	entry, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: EventNarrowed, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject: audit.Subject{Kind: "repository", ID: r.ID, Name: r.Name.String()}, Repository: r.Name.String(),
		Project: r.Name.Project, Details: details, Journal: entry,
	})
}
