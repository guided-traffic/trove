package repository

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Namespace claims (R-REPO-023). A claim is a name pattern on a local repository;
// the claims decide where a name resolves from in the virtual repositories that
// the claiming repository takes part in. This file holds the pattern form, the
// per-replica claim index, and the API service (read and replace the manual set
// of a repository under If-Match on claims_version). The routing that uses the
// index and the auto claim of a first publish are in routing.go.

// Limits of R-REPO-023.
const (
	// MaxClaims is the number of claims of one repository, manual and automatic.
	MaxClaims = 1000
	// MaxClaimPatternLength is the length of a claim or pin-rule pattern in
	// characters.
	MaxClaimPatternLength = 256
)

// The claim scopes and sources (R-REPO-023).
const (
	ScopeMembers = "members"
	ScopeGlobal  = "global"

	SourceManual = "manual"
	SourceAuto   = "auto"
)

// EventClaimsChanged is the audit event of a change of a repository's claims,
// made through the API or by the auto claim of a first publish (R-OPS-016); it is
// registered in internal/audit/events_repository.go.
const EventClaimsChanged = "repository.claims_changed"

const (
	codeClaimOverlaps  = "claim_overlaps"
	codeDuplicateValue = "invalid_value"
)

type claimKind uint8

const (
	claimExact claimKind = iota
	claimStar
	claimGlobstar
)

// ClaimPattern is a claim or pin-rule pattern: an exact name, or a literal
// (possibly empty) followed by one trailing `*` or `**`. These are the forms for
// which "most specific wins" is a total order (R-REPO-023). It is matched by the
// matcher of R-AUTHZ-005, so `*` stops at `/`; the trie only narrows the
// candidates.
type ClaimPattern struct {
	literal string
	kind    claimKind
	pat     *Pattern
}

// ParseClaimPattern parses a name pattern that is already folded with the name
// rule of its format. A pattern outside the two forms is refused.
func ParseClaimPattern(p string) (ClaimPattern, error) {
	if utf8.RuneCountInString(p) > MaxClaimPatternLength {
		return ClaimPattern{}, errors.New("a claim or pin-rule pattern is at most 256 characters")
	}
	pat, err := CompilePattern(SubjectName, p)
	if err != nil {
		return ClaimPattern{}, err
	}
	rs := []rune(p)
	var lit strings.Builder
	kind := claimExact
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++ // CompilePattern refused a lone trailing backslash
			lit.WriteRune(rs[i])
		case '?':
			return ClaimPattern{}, errors.New(`a claim or pin-rule pattern is an exact name or a literal followed by one trailing "*" or "**"; "?" is not allowed`)
		case '*':
			for _, r := range rs[i:] {
				if r != '*' {
					return ClaimPattern{}, errors.New(`a claim or pin-rule pattern is an exact name or a literal followed by one trailing "*" or "**"; a wildcard is allowed only at the end`)
				}
			}
			kind = claimStar
			if len(rs)-i > 1 {
				kind = claimGlobstar
			}
			i = len(rs)
		default:
			lit.WriteRune(rs[i])
		}
	}
	return ClaimPattern{literal: lit.String(), kind: kind, pat: pat}, nil
}

// parseFolded folds a pattern with the name rule of the format and parses it.
func parseFolded(cat *catalog.Registry, format, raw string) (ClaimPattern, error) {
	folded, ok := cat.NormalizeName(format, raw)
	if !ok {
		return ClaimPattern{}, errors.New("not a package name or name prefix of this format")
	}
	return ParseClaimPattern(folded)
}

// String is the folded form that is stored and shown.
func (c ClaimPattern) String() string {
	var b strings.Builder
	for _, r := range c.literal {
		if r == '\\' || r == '*' || r == '?' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	switch c.kind {
	case claimStar:
		b.WriteByte('*')
	case claimGlobstar:
		b.WriteString("**")
	}
	return b.String()
}

// Match reports whether the pattern covers a normalized name.
func (c ClaimPattern) Match(name string) bool { return c.pat.Match(name) }

// rank orders patterns that match one name: an exact name beats any wildcard
// pattern, a longer literal beats a shorter one, and with equal literals a
// trailing `*` beats `**` (R-REPO-023 "most specific wins").
func (c ClaimPattern) rank() int {
	switch c.kind {
	case claimExact:
		return 1 << 30
	case claimStar:
		return len(c.literal)*4 + 2
	}
	return len(c.literal)*4 + 1
}

func (c ClaimPattern) moreSpecific(o ClaimPattern) bool { return c.rank() > o.rank() }
func (c ClaimPattern) sameRank(o ClaimPattern) bool     { return c.rank() == o.rank() }

// Overlaps reports whether some name is covered by both patterns.
func (c ClaimPattern) Overlaps(o ClaimPattern) bool {
	switch {
	case c.kind == claimExact && o.kind == claimExact:
		return c.literal == o.literal
	case c.kind == claimExact:
		return o.pat.Match(c.literal)
	case o.kind == claimExact:
		return c.pat.Match(o.literal)
	}
	a, b := c, o
	if len(a.literal) > len(b.literal) {
		a, b = b, a
	}
	if !strings.HasPrefix(b.literal, a.literal) {
		return false
	}
	return a.kind == claimGlobstar || !strings.Contains(b.literal[len(a.literal):], "/")
}

// claimRef is the value a claim carries in the index.
type claimRef struct {
	repoID int64
	scope  string
	source string
}

// claimHit is a claim that covers a name.
type claimHit struct {
	claimRef
	pattern ClaimPattern
}

// claimIndex is every claim of the installation, one prefix trie per format
// (R-REPO-023). A lookup costs no query.
type claimIndex struct {
	byFormat map[string]*patTrie[claimRef]
}

func buildClaimIndex(rows []q.ListNamespaceClaimsRow, log *slog.Logger) *claimIndex {
	x := &claimIndex{byFormat: map[string]*patTrie[claimRef]{}}
	for _, r := range rows {
		pat, err := ParseClaimPattern(r.Pattern) // stored folded
		if err != nil {
			log.Error("repository: a stored claim does not parse and protects nothing", "repository", r.RepoID, "pattern", r.Pattern, "error", err)
			continue
		}
		t := x.byFormat[r.Format]
		if t == nil {
			t = &patTrie[claimRef]{}
			x.byFormat[r.Format] = t
		}
		t.add(pat, claimRef{repoID: r.RepoID, scope: r.Scope, source: r.Source})
	}
	return x
}

// lookup returns every claim of the format that covers the normalized name, the
// most specific first; claims of equal specificity are in the order of their
// repository ids.
func (x *claimIndex) lookup(format, name string) []claimHit {
	t := x.byFormat[format]
	if t == nil || name == "" {
		return nil
	}
	var out []claimHit
	for _, e := range t.matches(name) {
		out = append(out, claimHit{claimRef: e.val, pattern: e.pat})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].pattern.sameRank(out[j].pattern) {
			return out[i].pattern.moreSpecific(out[j].pattern)
		}
		return out[i].repoID < out[j].repoID
	})
	return out
}

// mostSpecific keeps the hits that share the highest rank. hits are sorted.
func mostSpecific(hits []claimHit) []claimHit {
	n := 0
	for n < len(hits) && hits[n].pattern.sameRank(hits[0].pattern) {
		n++
	}
	return hits[:n]
}

// Claim is one claim of a repository as the API shows it.
type Claim struct {
	Pattern   string
	Source    string
	Scope     string
	CreatedAt time.Time
}

// ClaimSet is the claims of a repository with the revision they were read at
// (the ETag of the resource) and, after a write, the warnings of the write.
type ClaimSet struct {
	Claims   []Claim
	Version  int64
	Warnings []Warning
}

// ClaimEntry is one manual claim of a write.
type ClaimEntry struct {
	Pattern string
	// Scope is "members" or "global"; empty means "members".
	Scope string
}

// ClaimInput is the body of PUT .../claims: the complete set of manual claims,
// and the automatic claims to remove (R-REPO-023). Automatic claims that are not
// listed in RemoveAuto stay.
type ClaimInput struct {
	Claims     []ClaimEntry
	RemoveAuto []string
}

// GlobalGuard decides whether the caller may write a global claim, which needs
// repositories:write-config where a members claim needs only manage on the
// repository (R-REPO-023). The service calls it, inside the write transaction,
// when the request adds, keeps, narrows or removes a global claim; its error is
// returned as it is.
type GlobalGuard func(ctx context.Context) error

// claimsPayload is the payload of a change of the area claims.
type claimsPayload struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	Op  string `json:"op"`
}

// claimChange is one entry of the added list of the event.
type claimChange struct {
	Pattern string `json:"pattern"`
	Scope   string `json:"scope"`
}

// claimsEventDetails is the data of repository.claims_changed.
type claimsEventDetails struct {
	Source        string        `json:"source"`
	Added         []claimChange `json:"added"`
	Removed       []string      `json:"removed"`
	ClaimsVersion int64         `json:"claimsVersion"`
}

// Claims implements GET .../claims: the claims of a repository from PostgreSQL
// and their revision. A repository that cannot hold claims (not local, or of a
// format without claims) has none.
func (s *Service) Claims(ctx context.Context, key string) (ClaimSet, error) {
	var out ClaimSet
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.GetRepositoryByKey(ctx, key)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound(key)
		}
		claims, err := qs.ListRepositoryClaims(ctx, rows[0].ID)
		if err != nil {
			return err
		}
		out = ClaimSet{Claims: toClaims(claims), Version: rows[0].ClaimsVersion}
		return nil
	})
	if err != nil {
		return ClaimSet{}, err
	}
	return out, nil
}

func toClaims(rows []q.ListRepositoryClaimsRow) []Claim {
	out := make([]Claim, len(rows))
	for i, r := range rows {
		out[i] = Claim{Pattern: r.Pattern, Source: r.Source, Scope: r.Scope, CreatedAt: r.CreatedAt.Time}
	}
	return out
}

// wantClaims is a validated PUT: the manual set by pattern, and the automatic
// claims to drop.
type wantClaims struct {
	scope      map[string]string
	order      []string
	removeAuto []string
}

// parseClaimInput checks what needs no state: the shape of every pattern and
// scope, duplicates and the count.
func parseClaimInput(cat *catalog.Registry, format string, in ClaimInput, v *validation) wantClaims {
	w := wantClaims{scope: map[string]string{}}
	for i, e := range in.Claims {
		ptr := "/claims/" + strconv.Itoa(i)
		pat, err := parseFolded(cat, format, e.Pattern)
		if err != nil {
			v.add(ptr+"/pattern", codeInvalidPattern, err.Error())
			continue
		}
		scope := e.Scope
		switch scope {
		case "":
			scope = ScopeMembers
		case ScopeMembers, ScopeGlobal:
		default:
			v.add(ptr+"/scope", codeInvalidValue, `must be "members" or "global"`)
			continue
		}
		p := pat.String()
		if _, dup := w.scope[p]; dup {
			v.add(ptr+"/pattern", codeDuplicateValue, "the pattern is listed twice")
			continue
		}
		w.scope[p] = scope
		w.order = append(w.order, p)
	}
	for i, raw := range in.RemoveAuto {
		pat, err := parseFolded(cat, format, raw)
		if err != nil {
			v.add("/removeAuto/"+strconv.Itoa(i), codeInvalidPattern, err.Error())
			continue
		}
		w.removeAuto = append(w.removeAuto, pat.String())
	}
	return w
}

// PutClaims implements PUT .../claims: it replaces the manual claims of the
// repository and removes the named automatic claims, under If-Match on
// claims_version (R-REPO-023). guard is called, in the transaction, when a global
// claim is added, kept, narrowed or removed. A write that changes nothing writes
// nothing and keeps the revision.
func (s *Service) PutClaims(ctx context.Context, c Caller, key string, pre Precondition, in ClaimInput, guard GlobalGuard) (ClaimSet, error) {
	if !pre.Any && pre.Version < 1 {
		return ClaimSet{}, errors.New("repository: a claims write needs a precondition")
	}
	var out ClaimSet
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.LockRepositoryByKey(ctx, key)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound(key)
		}
		row := rows[0]
		if Lifecycle(row.Lifecycle) == Deleting {
			return errs.E(errs.Conflict, errs.CodeConflict, "the repository is being deleted")
		}
		if !pre.Any && pre.Version != row.ClaimsVersion {
			return errs.Stale("The claims", row.ClaimsVersion)
		}
		out, err = s.putClaims(ctx, tx, qs, c, row, in, guard)
		return err
	})
	if err != nil {
		return ClaimSet{}, err
	}
	return out, nil
}

func (s *Service) putClaims(ctx context.Context, tx meta.Tx, qs *q.Queries, c Caller, row q.Repository,
	in ClaimInput, guard GlobalGuard,
) (ClaimSet, error) {
	v := &validation{}
	info, known := s.cat.Lookup(row.Format)
	if Class(row.Class) != Local || !known || !info.PinsAndClaims {
		v.add("/claims", errs.CodeNotApplicable,
			"claims belong to local repositories of a format with package names that supports them")
		return ClaimSet{}, v.err()
	}
	want := parseClaimInput(s.cat, row.Format, in, v)
	existing, err := qs.ListRepositoryClaims(ctx, row.ID)
	if err != nil {
		return ClaimSet{}, err
	}
	manual, auto := map[string]string{}, map[string]bool{}
	for _, e := range existing {
		if e.Source == SourceManual {
			manual[e.Pattern] = e.Scope
		} else {
			auto[e.Pattern] = true
		}
	}
	for i, p := range want.removeAuto {
		switch {
		case !auto[p]:
			v.add("/removeAuto/"+strconv.Itoa(i), codeInvalidValue, "the repository has no automatic claim with this pattern")
		case want.scope[p] != "":
			v.add("/removeAuto/"+strconv.Itoa(i), codeInvalidValue, "the pattern is also listed under claims")
		}
	}
	remaining := len(want.scope)
	for p := range auto {
		if want.scope[p] == "" && !slices.Contains(want.removeAuto, p) {
			remaining++
		}
	}
	if remaining > MaxClaims {
		v.add("/claims", codeOutOfRange, "a repository holds at most "+strconv.Itoa(MaxClaims)+" claims, automatic ones included")
	}
	if err := v.err(); err != nil {
		return ClaimSet{}, err
	}

	// What changes. A global claim that is added, kept, narrowed or removed needs
	// repositories:write-config: the claim set is one resource, and whoever may
	// drop a global claim by omitting it from a replace could reopen a
	// dependency-confusion path (Q-REPO-105).
	var added []claimChange
	var removed []string
	touchesGlobal := false
	for _, p := range want.order {
		sc := want.scope[p]
		touchesGlobal = touchesGlobal || sc == ScopeGlobal
		if old, had := manual[p]; !had || old != sc {
			added = append(added, claimChange{Pattern: p, Scope: sc})
		}
	}
	for p, sc := range manual {
		if _, kept := want.scope[p]; !kept {
			removed = append(removed, p)
			touchesGlobal = touchesGlobal || sc == ScopeGlobal
		} else if sc == ScopeGlobal {
			touchesGlobal = true
		}
	}
	removed = append(removed, want.removeAuto...)
	sort.Strings(removed)
	if touchesGlobal && guard != nil {
		if err := guard(ctx); err != nil {
			return ClaimSet{}, err
		}
	}

	warnings, err := s.claimOverlaps(ctx, qs, row, want)
	if err != nil {
		return ClaimSet{}, err
	}
	version := row.ClaimsVersion
	if len(added) > 0 || len(removed) > 0 {
		actor := c.principalID()
		for _, p := range removed {
			if err := qs.DeleteClaim(ctx, q.DeleteClaimParams{RepoID: row.ID, Pattern: p}); err != nil {
				return ClaimSet{}, err
			}
		}
		for _, a := range added {
			if err := qs.UpsertManualClaim(ctx, q.UpsertManualClaimParams{RepoID: row.ID, Pattern: a.Pattern, Scope: a.Scope, Actor: actor}); err != nil {
				return ClaimSet{}, err
			}
		}
		if version, err = qs.BumpClaimsVersion(ctx, row.ID); err != nil {
			return ClaimSet{}, err
		}
		r, _ := toRepository(row, s.cat)
		if err := recordClaims(ctx, tx, s.rec, s.notif, c, r, SourceManual, added, removed, version); err != nil {
			return ClaimSet{}, err
		}
	}
	after, err := qs.ListRepositoryClaims(ctx, row.ID)
	if err != nil {
		return ClaimSet{}, err
	}
	return ClaimSet{Claims: toClaims(after), Version: version, Warnings: warnings}, nil
}

// claimOverlaps returns the warning claim_overlaps for the manual claims that
// overlap a claim of another repository of the format which is in effect
// together with it (R-REPO-023): either of the two is global, or the two
// repositories are members of one virtual repository. The detail does not name
// the other repository, because a holder of manage on this one need not be able
// to read it.
func (s *Service) claimOverlaps(ctx context.Context, qs *q.Queries, row q.Repository, want wantClaims) ([]Warning, error) {
	if len(want.order) == 0 {
		return nil, nil
	}
	others, err := qs.ListOtherClaims(ctx, q.ListOtherClaimsParams{RepoID: row.ID, Format: row.Format})
	if err != nil {
		return nil, err
	}
	var out []Warning
	for i, p := range want.order {
		mine, err := ParseClaimPattern(p)
		if err != nil {
			continue
		}
		for _, o := range others {
			theirs, err := ParseClaimPattern(o.Pattern)
			if err != nil || !mine.Overlaps(theirs) {
				continue
			}
			if want.scope[p] == ScopeGlobal || o.Scope == ScopeGlobal || o.SharesVirtual {
				out = append(out, Warning{
					Code: codeClaimOverlaps, Pointer: "/claims/" + strconv.Itoa(i),
					Detail: "the pattern overlaps a claim of another repository; the more specific claim decides where a name resolves from",
				})
				break
			}
		}
	}
	return out, nil
}

// recordClaims writes the audit event and the change-log row of a change of
// claims, in the transaction that made it.
func recordClaims(ctx context.Context, tx meta.Tx, rec audit.Recorder, notif Notifier, c Caller, r Repository,
	source string, added []claimChange, removed []string, version int64,
) error {
	if added == nil {
		added = []claimChange{}
	}
	if removed == nil {
		removed = []string{}
	}
	raw, err := json.Marshal(claimsEventDetails{Source: source, Added: added, Removed: removed, ClaimsVersion: version})
	if err != nil {
		return err
	}
	if err := rec.RecordTx(ctx, tx, audit.Event{
		Type: EventClaimsChanged, Actor: c.Actor, Outcome: audit.Success, Source: c.Source,
		Subject: audit.Subject{Kind: "repository", ID: r.ID, Name: r.Key}, Details: raw,
	}); err != nil {
		return err
	}
	return notif.Notify(tx, cluster.AreaClaims, 1, claimsPayload{ID: r.ID, Key: r.Key, Op: source})
}
