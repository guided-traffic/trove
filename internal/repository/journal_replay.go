package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/vuln"
)

// Replay of the repository events of the revocation journal (R-HA-054, R-REPO-061): the narrowings of an update
// (repository.narrowed) and the claims of a repository (repository.claims_changed). Every part removes: it is applied to
// whatever the restored database holds, and where applying it could widen what the repository serves, the repository is
// disabled and the entry reported suspended (Q-REPO-247 to Q-REPO-250). The replay writes no audit event.
func init() {
	journal.Register(journal.Handler{
		Domain: "repository",
		Events: []string{EventNarrowed, EventClaimsChanged},
		Apply:  replayRepository,
	})
}

// replayPayloadVersion is the version of the change payloads this replay writes, the version of the areas repo_config, authz
// and claims.
const replayPayloadVersion = 1

// The bounds of an entry that the reader enforces: the lists a valid update can write. An entry beyond them is forged.
const (
	maxEntryRefs = 10000                        // repositories.max_virtual_members at its highest
	maxEntryPins = MaxPinRules * MaxPinPatterns // every pattern of every rule
	maxEntryKeys = 32                           // the certificates deb.upstreamKeys holds at most
)

// validFingerprint reports whether s is a v4 fingerprint: 40 upper-case hexadecimal digits.
func validFingerprint(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func replayRepository(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	qs, err := queries(tx)
	if err != nil {
		return journal.Result{}, err
	}
	r := &replayer{ctx: ctx, tx: tx, env: env, o: o, qs: qs, cat: catalog.Default()}
	if o.Event == EventClaimsChanged {
		return r.claims()
	}
	return r.narrowed()
}

// replayer is the replay of one entry: the locked repository as the restored database holds it, and what the parts did to it.
type replayer struct {
	ctx context.Context
	tx  meta.Tx
	env journal.Env
	o   journal.Object
	qs  *q.Queries
	cat *catalog.Registry

	id      int64
	text    string // the ID as decimal text, strconv.FormatInt(id, 10)
	subject string // project/key
	class   Class
	format  string
	info    catalog.Entry
	known   bool
	doc     map[string]json.RawMessage
	set     Settings // the stored document, typed

	disabled  bool     // the document says disabled, as it was or as a part left it
	touched   bool     // the document or the rows were written
	changed   []string // the parts that changed the repository
	suspended []string // the parts that disabled it
}

// open locks the repository of the entry and reads it. A repository that is missing or being deleted is unknown; a stored
// document that does not read is refused as disabled by every reader, so the entry is already in effect.
func (r *replayer) open(id int64) (res journal.Result, ok bool, err error) {
	text := strconv.FormatInt(id, 10)
	rows, err := r.qs.JournalRepositoryLock(r.ctx, id)
	if err != nil {
		return journal.Result{}, false, err
	}
	if len(rows) == 0 || Lifecycle(rows[0].Lifecycle) == Deleting {
		return r.o.Result(journal.SubjectUnknown, text), false, nil
	}
	row := rows[0]
	r.id, r.text, r.class = id, text, Class(row.Class)
	r.subject = row.ProjectKey + "/" + row.Key
	if r.format, err = r.qs.JournalRepositoryFormat(r.ctx, id); err != nil {
		return journal.Result{}, false, err
	}
	r.info, r.known = r.cat.Lookup(r.format)
	var readable bool
	if r.doc, r.set, readable = decodeStored(row.Config); !readable {
		return r.o.Result(journal.AlreadyInEffect, r.subject), false, nil
	}
	r.disabled = r.set.Disabled
	return journal.Result{}, true, nil
}

// decodeStored reads a stored document as a map, to write back what the replay does not touch, and as Settings. A document
// that is no JSON object, or does not decode, is refused as disabled by every reader.
func decodeStored(raw []byte) (doc map[string]json.RawMessage, set Settings, ok bool) {
	if json.Unmarshal(raw, &doc) != nil || doc == nil || json.Unmarshal(raw, &set) != nil {
		return nil, Settings{}, false
	}
	set.normalize()
	return doc, set, true
}

// fold applies the format's name rule to name patterns; other formats compare as written.
func (r *replayer) fold(list []string) []string {
	if r.cat != nil && r.known && r.info.PatternSubject == SubjectName {
		return foldAll(r.cat, r.format, list)
	}
	return list
}

func (r *replayer) patternSubject() PatternSubject {
	if r.known {
		return r.info.PatternSubject
	}
	return SubjectPath
}

// suspend disables the repository for a part that fails closed. A repository that is disabled already stays as it is and
// nothing is reported: an administrator reviews it either way, and a second replay finds the same.
func (r *replayer) suspend(part string) {
	if r.disabled {
		return
	}
	r.disabled, r.touched = true, true
	r.doc["disabled"] = json.RawMessage("true")
	r.set.Disabled = true
	r.suspended = append(r.suspended, part)
}

func (r *replayer) applied(part string) {
	if !slices.Contains(r.changed, part) {
		r.changed = append(r.changed, part)
	}
}

func (r *replayer) setMember(name string, v any) {
	raw, _ := json.Marshal(v) // strings, bools and the settings struct always marshal
	r.doc[name] = raw
	r.touched = true
}

// readNarrowed reads the entry of repository.narrowed strictly and returns the repository ID; a reason says why it is
// malformed (R-REPO-061: the lists are bounded as a valid update writes them, and every ID parses).
func readNarrowed(o journal.Object) (e narrowedEntry, id int64, why string) {
	if !o.Into(&e) {
		return e, 0, "the entry does not read"
	}
	id, ok := parseID(e.RepositoryID)
	if !ok {
		return e, 0, "repositoryId is not an ID"
	}
	switch {
	case len(e.Include) > MaxPatterns || len(e.ExcludeAdded) > MaxPatterns:
		return e, 0, "a pattern list exceeds " + strconv.Itoa(MaxPatterns)
	case len(e.MembersRemoved) > maxEntryRefs || len(e.PinsAdded) > maxEntryPins || len(e.PinMembersRemoved) > maxEntryPins:
		return e, 0, "a list is longer than a repository can hold"
	case len(e.parts()) == 0:
		return e, 0, "the entry carries no part"
	}
	if len(e.DebUpstreamKeysRemoved) > maxEntryKeys {
		return e, 0, "a list of fingerprints is longer than a remote can hold"
	}
	for _, fp := range e.DebUpstreamKeysRemoved {
		if !validFingerprint(fp) {
			return e, 0, "a fingerprint is not 40 hexadecimal digits"
		}
	}
	if _, ok := refIDs(e.MembersRemoved); !ok {
		return e, 0, "a member is not an ID"
	}
	for _, list := range [][]pinPart{e.PinsAdded, e.PinMembersRemoved} {
		for _, p := range list {
			if _, ok := refIDs(p.Members); !ok || len(p.Members) > maxEntryRefs {
				return e, 0, "a pin rule member is not an ID"
			}
		}
	}
	if v := e.Vulnerabilities; v != nil && (len(v.Before.Validate()) > 0 || len(v.After.Validate()) > 0) {
		return e, 0, "the vulnerabilities member holds an invalid value"
	}
	return e, id, ""
}

func parseID(text string) (int64, bool) {
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0
}

func refIDs(refs []repoRef) ([]int64, bool) {
	out := make([]int64, 0, len(refs))
	for _, ref := range refs {
		id, ok := parseID(ref.RepositoryID)
		if !ok {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// checkPatterns checks the patterns of the entry as an update checks them (R-REPO-008): the format's pattern rules for the
// include and exclude lists, the claim and pin-rule form for the pin patterns. It returns why an entry is malformed.
func (r *replayer) checkPatterns(e narrowedEntry) string {
	for _, list := range [][]string{e.Include, e.ExcludeAdded} {
		for _, p := range list {
			if _, err := CompilePattern(r.patternSubject(), p); err != nil {
				return "a pattern does not pass the format's rules: " + err.Error()
			}
		}
	}
	for _, list := range [][]pinPart{e.PinsAdded, e.PinMembersRemoved} {
		for _, p := range list {
			if _, err := parseFolded(r.cat, r.format, p.Pattern); err != nil {
				return "a pin-rule pattern does not parse: " + err.Error()
			}
		}
	}
	return ""
}

// narrowed replays repository.narrowed: the steps 1 to 7 of R-REPO-061, in that order.
func (r *replayer) narrowed() (journal.Result, error) {
	e, id, why := readNarrowed(r.o)
	if why != "" {
		return r.o.Malformed(why), nil
	}
	if res, ok, err := r.open(id); !ok || err != nil {
		return res, err
	}
	if why := r.checkPatterns(e); why != "" {
		return r.o.Malformed(why), nil
	}
	r.include(e.Include)
	r.excludes(e.ExcludeAdded)
	r.autoClaim(e.AutoClaim)
	if r.class == Virtual {
		if err := r.virtualParts(e); err != nil {
			return journal.Result{}, err
		}
	}
	if err := r.vulnerabilities(e.Vulnerabilities); err != nil {
		return journal.Result{}, err
	}
	if err := r.debUpstreamKeysRemoved(e.DebUpstreamKeysRemoved); err != nil {
		return journal.Result{}, err
	}
	return r.finish(e.parts())
}

// include is step 1: an empty restored list takes the journaled one, otherwise the patterns both lists hold stay, and none
// left disables the repository (Q-REPO-247).
func (r *replayer) include(want []string) {
	if len(want) == 0 {
		return
	}
	have := r.set.IncludePatterns
	next := slices.Clone(want)
	if len(have) > 0 {
		// Compared in the folded form of the format's name rule, so a pattern spelled in another case is the same pattern, and a
		// restored pattern stays when a journaled one provably covers it (a consolidating edit narrows to the pattern inside).
		journaled := r.fold(want)
		next = slices.DeleteFunc(slices.Clone(have), func(p string) bool {
			fp := r.fold([]string{p})[0]
			return !slices.ContainsFunc(journaled, func(q string) bool { return patternCovers(q, fp) })
		})
	}
	switch {
	case len(next) == 0:
		r.suspend(partInclude)
	case !slices.Equal(next, have):
		r.set.IncludePatterns = next
		r.setMember("includePatterns", next)
		r.applied(partInclude)
	}
}

// excludes is step 2: each pattern the restored list lacks is added; a list beyond 100 patterns disables the repository.
func (r *replayer) excludes(add []string) {
	have := r.set.ExcludePatterns
	next := slices.Clone(have)
	for _, p := range add {
		if !slices.Contains(next, p) {
			next = append(next, p)
		}
	}
	switch {
	case len(next) == len(have):
	case len(next) > MaxPatterns:
		r.suspend(partExcludeAdded)
	default:
		r.set.ExcludePatterns = next
		r.setMember("excludePatterns", next)
		r.applied(partExcludeAdded)
	}
}

// autoClaim is step 3: local.autoClaim is set to true on a local repository of a format that claims.
func (r *replayer) autoClaim(on bool) {
	if !on || r.class != Local || !r.known || !r.info.PinsAndClaims || !r.info.PackageNames || effectiveAutoClaim(r.info, r.known, &r.set) {
		return
	}
	local := map[string]json.RawMessage{}
	if raw, ok := r.doc["local"]; ok {
		_ = json.Unmarshal(raw, &local) // a section that is no object is rewritten as one
		if local == nil {
			local = map[string]json.RawMessage{}
		}
	}
	local["autoClaim"] = json.RawMessage("true")
	r.setMember("local", local)
	if r.set.Local == nil {
		r.set.Local = &LocalSettings{}
	}
	on = true
	r.set.Local.AutoClaim = &on
	r.applied(partAutoClaim)
}

// cloneRel copies the rows, so that the parts change a working copy and writeVirtual compares it with the stored rows.
func cloneRel(rel *virtualRel) *virtualRel {
	out := &virtualRel{members: slices.Clone(rel.members)}
	if rel.target != nil {
		t := *rel.target
		out.target = &t
	}
	for _, p := range rel.pins {
		out.pins = append(out.pins, relPin{patterns: slices.Clone(p.patterns), members: slices.Clone(p.members)})
	}
	return out
}

// virtualParts applies the steps 4 to 6 to the rows of a virtual repository and writes the rows that differ with the
// statements of an update, so the guards of the database apply (Q-REPO-234).
func (r *replayer) virtualParts(e narrowedEntry) error {
	rels, err := loadVirtualRels(r.ctx, r.qs, []int64{r.id})
	if err != nil {
		return err
	}
	old := rels[r.id]
	if old == nil {
		old = &virtualRel{}
	}
	next := cloneRel(old)
	if err := r.membersRemoved(next, e.MembersRemoved); err != nil {
		return err
	}
	r.pinMembersRemoved(next, e.PinMembersRemoved)
	if err := r.pinsAdded(next, e.PinsAdded); err != nil {
		return err
	}
	membersChanged, pinsChanged, err := writeVirtual(r.ctx, r.qs, r.id, next, old)
	if err != nil {
		return err
	}
	r.touched = r.touched || membersChanged || pinsChanged
	if pinsChanged {
		return r.env.Notify(r.tx, cluster.AreaClaims, replayPayloadVersion, claimsPayload{ID: r.text, Key: r.subject, Op: "pins"})
	}
	return nil
}

// membersRemoved is step 4: each member is taken out of the member list, every pin rule and the deploy target, unless it is
// a local repository that holds a claim of scope members while the repository keeps any other member: the claim would leave
// effect in it, and its names would reach a remote member or, with a forged entry, another local one that does not hold them,
// so the repository is disabled instead (Q-REPO-248).
func (r *replayer) membersRemoved(next *virtualRel, list []repoRef) error {
	ids, _ := refIDs(list) // read strictly
	gone := make([]int64, 0, len(ids))
	for _, m := range next.members {
		if slices.Contains(ids, m.id) {
			gone = append(gone, m.id)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	var kept []int64
	for _, m := range next.members {
		if !slices.Contains(ids, m.id) {
			kept = append(kept, m.id)
		}
	}
	holders, err := r.qs.JournalMembersClaimHolders(r.ctx, gone)
	if err != nil {
		return err
	}
	for _, id := range gone {
		if len(kept) > 0 && slices.Contains(holders, id) {
			r.suspend(partMembersRemoved)
			continue
		}
		next.dropMember(id)
		r.applied(partMembersRemoved)
	}
	return nil
}

// dropMember takes a member out of the list, the pin rules and the deploy target.
func (rel *virtualRel) dropMember(id int64) {
	notID := func(m relRef) bool { return m.id != id }
	rel.members = slices.DeleteFunc(rel.members, func(m relRef) bool { return !notID(m) })
	for i := range rel.pins {
		rel.pins[i].members = slices.DeleteFunc(rel.pins[i].members, func(m relRef) bool { return !notID(m) })
	}
	if rel.target != nil && rel.target.id == id {
		rel.target = nil
	}
}

func (rel *virtualRel) ruleOf(pattern string) int {
	return slices.IndexFunc(rel.pins, func(p relPin) bool { return slices.Contains(p.patterns, pattern) })
}

// pinMembersRemoved is step 5: the members leave the rule that holds the pattern; a rule left without members answers 404
// for its names. When no rule holds the pattern the restored repository consults every member for it, which the entry does
// not describe, and the repository is disabled.
func (r *replayer) pinMembersRemoved(next *virtualRel, parts []pinPart) {
	for _, part := range parts {
		pat, _ := parseFolded(r.cat, r.format, part.Pattern) // checked
		i := next.ruleOf(pat.String())
		if i < 0 {
			r.suspend(partPinMembersRemoved)
			continue
		}
		ids, _ := refIDs(part.Members)
		before := len(next.pins[i].members)
		next.pins[i].members = slices.DeleteFunc(next.pins[i].members, func(m relRef) bool { return slices.Contains(ids, m.id) })
		if len(next.pins[i].members) != before {
			r.applied(partPinMembersRemoved)
		}
	}
}

// pinsAdded is step 6: a rule that holds the pattern keeps only the members the entry names. Otherwise a rule with the
// pattern and the entry's members that are members of the restored repository is added, unless a claim in effect in the
// repository or a pattern of another rule overlaps the pattern; then the repository is disabled (Q-REPO-249). The rules of
// the restored repository decide, not the rules this entry adds: any rule selects a subset of the members a name reached
// before, whichever of the added rules is the more specific.
func (r *replayer) pinsAdded(next *virtualRel, parts []pinPart) error {
	if len(parts) == 0 {
		return nil
	}
	claims, err := r.qs.JournalFormatClaims(r.ctx, r.format)
	if err != nil {
		return err
	}
	restored := slices.Clone(next.pins)
	for _, part := range parts {
		pat, _ := parseFolded(r.cat, r.format, part.Pattern) // checked
		ids, _ := refIDs(part.Members)
		if i := next.ruleOf(pat.String()); i >= 0 {
			before := len(next.pins[i].members)
			next.pins[i].members = slices.DeleteFunc(next.pins[i].members, func(m relRef) bool { return !slices.Contains(ids, m.id) })
			if len(next.pins[i].members) != before {
				r.applied(partPinsAdded)
			}
			continue
		}
		if len(next.pins) >= MaxPinRules || pinOverlaps(r.cat, r.format, pat, restored, next.members, claims) {
			r.suspend(partPinsAdded)
			continue
		}
		rule := relPin{patterns: []string{pat.String()}}
		for _, m := range next.members {
			if slices.Contains(ids, m.id) {
				rule.members = append(rule.members, m)
			}
		}
		next.pins = append(next.pins, rule)
		r.applied(partPinsAdded)
	}
	return nil
}

// pinOverlaps reports whether a claim in effect in the repository (a global claim, or a claim of one of its members) or a
// pattern of one of the rules overlaps the pattern. A stored pattern that does not parse is treated as overlapping.
func pinOverlaps(cat *catalog.Registry, format string, pat ClaimPattern, rules []relPin, members []relRef, claims []q.JournalFormatClaimsRow) bool {
	for _, c := range claims {
		if c.Scope != ScopeGlobal && !slices.ContainsFunc(members, func(m relRef) bool { return m.id == c.RepoID }) {
			continue
		}
		if other, err := ParseClaimPattern(c.Pattern); err != nil || other.Overlaps(pat) {
			return true
		}
	}
	for _, rule := range rules {
		for _, p := range rule.patterns {
			if other, err := parseFolded(cat, format, p); err != nil || other.Overlaps(pat) {
				return true
			}
		}
	}
	return false
}

// moreBlocking is step 7 for one member: each field takes the more blocking of the restored and the journaled value, in the
// order of Q-VULN-69.
func moreBlocking(cur, journaled vuln.Settings) vuln.Settings {
	out := cur
	out.Enabled = cur.Enabled || journaled.Enabled
	if matchRank(journaled.MatchPublicAdvisories) > matchRank(cur.MatchPublicAdvisories) {
		out.MatchPublicAdvisories = journaled.MatchPublicAdvisories
	}
	out.BlockMalicious = cur.BlockMalicious || journaled.BlockMalicious
	if journaled.OnMissingData == vuln.MissingBlock {
		out.OnMissingData = vuln.MissingBlock
	}
	return out
}

// vulnerabilities is step 7. When `enabled` or `matchPublicAdvisories` moved, a rescan of the repository is enqueued in the
// replay's transaction, because the setting blocks nothing until the stored versions are evaluated again (R-VULN-011,
// Q-HA-180). A repository that has no such member leaves the part out.
func (r *replayer) vulnerabilities(c *vulnChange) error {
	if c == nil || r.class == Virtual || !r.known || (r.info.Ecosystem == "" && r.info.ImageEcosystem == "") {
		return nil
	}
	cur := vuln.DefaultSettings()
	if r.set.Vulnerabilities != nil {
		cur = *r.set.Vulnerabilities
	}
	next := moreBlocking(cur, c.After)
	if next == cur {
		return nil
	}
	r.set.Vulnerabilities = &next
	r.setMember("vulnerabilities", next)
	r.applied(partVulnerabilities)
	if next.Enabled == cur.Enabled && next.MatchPublicAdvisories == cur.MatchPublicAdvisories {
		return nil
	}
	id, _ := parseID(r.text)
	_, err := jobs.EnqueueTx(r.ctx, r.tx, vuln.RescanKindSpec(), jobs.EnqueueSpec{
		Kind: vuln.KindRescan, Args: vuln.RescanArgs{Repository: r.subject, RepositoryID: id}, DedupKey: vuln.RescanKey(id),
		TargetKind: "repository", TargetKey: r.subject,
	})
	return err
}

// finish writes the document when a part touched it or the rows, tells the change areas, and builds the result: suspended
// when a part disabled the repository, applied when a part changed it, already in effect otherwise.
func (r *replayer) finish(parts []string) (journal.Result, error) {
	if r.touched {
		if err := r.writeConfig(); err != nil {
			return journal.Result{}, err
		}
	}
	switch {
	case len(r.suspended) > 0:
		res := r.o.Result(journal.Suspended, r.subject)
		res.Detail, _ = json.Marshal(map[string]any{
			"repository": r.subject, "parts": parts, "suspendedBy": strings.Join(r.suspended, ", "), "entry": r.o.Entry,
		})
		return res, nil
	case len(r.changed) > 0:
		res := r.o.Result(journal.Applied, r.subject)
		res.Detail, _ = json.Marshal(map[string]any{"repository": r.subject, "parts": r.changed})
		return res, nil
	}
	return r.o.Result(journal.AlreadyInEffect, r.subject), nil
}

// writeConfig stores the document, which raises config_version, and tells the areas repo_config and authz as the replay of a
// disable does.
func (r *replayer) writeConfig() error {
	raw, err := json.Marshal(r.doc)
	if err != nil {
		return err
	}
	ver, err := r.qs.JournalRepositorySetConfig(r.ctx, q.JournalRepositorySetConfigParams{ID: r.id, Config: string(raw)})
	if err != nil {
		return err
	}
	if len(ver) == 0 {
		return errReplayLifecycle
	}
	payload := changePayload{ID: r.text, Key: r.subject, Op: strings.TrimPrefix(EventUpdated, "repository."), Version: ver[0]}
	if err := r.env.Notify(r.tx, cluster.AreaRepoConfig, replayPayloadVersion, payload); err != nil {
		return err
	}
	return r.env.Notify(r.tx, cluster.AreaAuthz, replayPayloadVersion, map[string]string{"type": "repository", "id": r.text})
}

var errReplayLifecycle = errors.New("repository: the replay matched no active row although the row was locked")

// claimsEntryBound is the most claims a valid entry lists.
const claimsEntryBound = MaxClaims

// readClaims reads the entry of repository.claims_changed strictly.
func readClaims(o journal.Object) (e claimsEntry, id int64, why string) {
	if !o.Into(&e) {
		return e, 0, "the entry does not read"
	}
	id, ok := parseID(e.RepositoryID)
	switch {
	case !ok:
		return e, 0, "repositoryId is not an ID"
	case e.Source != SourceManual && e.Source != SourceAuto:
		return e, 0, "source is neither manual nor auto"
	case e.Added == nil:
		return e, 0, "added is missing"
	case len(e.Added) > claimsEntryBound:
		return e, 0, "more claims than a repository holds"
	}
	for _, c := range e.Added {
		if c.Scope != ScopeMembers && c.Scope != ScopeGlobal {
			return e, 0, "a scope is neither members nor global"
		}
	}
	return e, id, ""
}

// claims replays repository.claims_changed on a local repository of a format with claims: each added claim is created, or
// widened to global; an auto claim follows the rule of a first publish; a manual claim that overlaps a claim in effect
// together with it is created and its repository disabled (Q-REPO-250). The limit of 1000 claims does not stop a replayed
// claim: a claim only narrows, and a dropped one would leave a name unclaimed. A repository left above it is noted.
func (r *replayer) claims() (journal.Result, error) {
	e, id, why := readClaims(r.o)
	if why != "" {
		return r.o.Malformed(why), nil
	}
	if res, ok, err := r.open(id); !ok || err != nil {
		return res, err
	}
	if r.class != Local || !r.known || !r.info.PinsAndClaims || (e.Source == SourceAuto && !r.info.PackageNames) {
		return r.o.Result(journal.SubjectUnknown, r.text), nil
	}
	patterns, why := r.foldClaims(e.Added, e.Source)
	if why != "" {
		return r.o.Malformed(why), nil
	}
	existing, err := r.qs.ListRepositoryClaims(r.ctx, r.id)
	if err != nil {
		return journal.Result{}, err
	}
	cr := &claimReplay{r: r, source: e.Source, existing: existing}
	for i, c := range e.Added {
		if err := cr.one(patterns[i], c.Scope); err != nil {
			return journal.Result{}, err
		}
	}
	if cr.changed {
		if _, err := r.qs.BumpClaimsVersion(r.ctx, r.id); err != nil {
			return journal.Result{}, err
		}
		if err := r.env.Notify(r.tx, cluster.AreaClaims, replayPayloadVersion, claimsPayload{ID: r.text, Key: r.subject, Op: "journal"}); err != nil {
			return journal.Result{}, err
		}
		r.applied("claims")
	}
	res, err := r.finish([]string{"claims"})
	if n := len(cr.existing); err == nil && n > MaxClaims {
		// A claim only narrows, and a dropped one leaves a name unclaimed, so the limit does not stop a replayed claim.
		res.Note = fmt.Sprintf("%s holds %d claims, above the limit of %d: remove claims that are no longer needed", r.subject, n, MaxClaims)
	}
	return res, err
}

// foldClaims folds the patterns of the added claims with the format's name rule, as an API write stores them, and says why
// an entry is malformed when one does not parse.
func (r *replayer) foldClaims(added []claimChange, source string) (patterns []string, why string) {
	patterns = make([]string, len(added))
	for i, c := range added {
		pat, err := parseFolded(r.cat, r.format, c.Pattern)
		if err != nil {
			return nil, "a claim pattern does not parse: " + err.Error()
		}
		if source == SourceAuto && (pat.kind != claimExact || c.Scope != ScopeMembers) {
			return nil, "an auto claim is one exact name of scope members"
		}
		patterns[i] = pat.String()
	}
	return patterns, ""
}

// claimReplay is the claims of one entry being replayed: the restored claims of the repository, kept current as claims are
// created, and the claims of the other repositories, read when a manual claim needs them.
type claimReplay struct {
	r        *replayer
	source   string
	existing []q.ListRepositoryClaimsRow
	others   []q.ListOtherClaimsRow
	loaded   bool
	changed  bool
}

func (c *claimReplay) one(pattern, scope string) error {
	r := c.r
	i := slices.IndexFunc(c.existing, func(x q.ListRepositoryClaimsRow) bool { return x.Pattern == pattern })
	switch {
	case i >= 0 && (c.existing[i].Scope == scope || c.existing[i].Scope == ScopeGlobal):
		return nil
	case i >= 0:
		n, err := r.qs.JournalWidenClaim(r.ctx, q.JournalWidenClaimParams{RepoID: r.id, Pattern: pattern})
		c.existing[i].Scope = ScopeGlobal
		c.changed = c.changed || n > 0
		return err
	case c.source == SourceAuto:
		return c.auto(pattern)
	}
	if err := r.qs.UpsertManualClaim(r.ctx, q.UpsertManualClaimParams{RepoID: r.id, Pattern: pattern, Scope: scope}); err != nil {
		return err
	}
	c.existing = append(c.existing, q.ListRepositoryClaimsRow{Pattern: pattern, Source: SourceManual, Scope: scope})
	c.changed = true
	return c.checkOverlap(pattern, scope)
}

// auto creates the claim of a first publish unless a claim in effect together with it covers the name.
func (c *claimReplay) auto(pattern string) error {
	r := c.r
	if ownClaimCovers(pattern, c.existing) {
		return nil
	}
	if err := c.loadOthers(); err != nil {
		return err
	}
	if otherClaimCovers(pattern, c.others) {
		return nil // as a first publish: another claim covers the name
	}
	n, err := r.qs.InsertAutoClaim(r.ctx, q.InsertAutoClaimParams{RepoID: r.id, Pattern: pattern})
	if err != nil {
		return err
	}
	if n > 0 {
		c.existing = append(c.existing, q.ListRepositoryClaimsRow{Pattern: pattern, Source: SourceAuto, Scope: ScopeMembers})
		c.changed = true
	}
	return nil
}

func (c *claimReplay) loadOthers() error {
	if c.loaded {
		return nil
	}
	others, err := c.r.qs.ListOtherClaims(c.r.ctx, q.ListOtherClaimsParams{RepoID: c.r.id, Format: c.r.format})
	c.others, c.loaded = others, err == nil
	return err
}

// checkOverlap disables the repository when the manual claim overlaps a claim of another repository that is in effect
// together with it: either of the two is global, or the repositories are members of one virtual repository (the condition of
// claim_overlaps). The claim stays; a claim of a disabled repository stays in effect, so the overlapping names answer 404
// instead of moving to it (Q-REPO-250).
func (c *claimReplay) checkOverlap(pattern, scope string) error {
	if err := c.loadOthers(); err != nil {
		return err
	}
	mine, err := ParseClaimPattern(pattern)
	if err != nil {
		return err // the pattern is folded and was parsed already
	}
	for _, o := range c.others {
		theirs, err := ParseClaimPattern(o.Pattern)
		if err != nil || !mine.Overlaps(theirs) {
			continue
		}
		if scope == ScopeGlobal || o.Scope == ScopeGlobal || o.SharesVirtual {
			c.r.suspend("claims")
			return nil
		}
	}
	return nil
}

// debUpstreamKeysRemoved is step 8: the fingerprints an update took out of deb.upstreamKeys of a remote Debian repository are taken
// out of the restored document again, and the releases they verified are unverified until a key that stays verifies them
// (R-DEB-020, R-DEB-032). A restored document that holds keys not named in the entry keeps them. A mirror or a member of a virtual
// repository left without a key is disabled: it would serve content nobody verifies (fail closed). The part acts on a remote Debian
// repository only; an entry for another repository leaves nothing to do.
func (r *replayer) debUpstreamKeysRemoved(remove []string) error {
	if len(remove) == 0 || r.format != debFormat || r.class != Remote {
		return nil
	}
	if err := runKeysRemovedHooks(r.ctx, r.tx, CreatedRepo{ID: r.id, Format: r.format}, remove); err != nil {
		return err
	}
	m := debMemberMap(r.doc[debFormat])
	keys := debKeysOf(m)
	left := 0
	if keys != "" {
		certs, perr := openpgp.ParseStoredCertificates([]byte(keys), openpgp.Limits{})
		if perr != nil {
			// Keys that do not read verify nothing: the repository fails closed.
			r.suspend(partDebUpstreamKeysRemoved)
			return nil //nolint:nilerr // the entry is applied: the repository is disabled instead
		}
		kept := make([]openpgp.Certificate, 0, len(certs))
		for _, c := range certs {
			if !slices.Contains(remove, c.Fingerprint) {
				kept = append(kept, c)
			}
		}
		if len(kept) != len(certs) {
			if len(kept) == 0 {
				delete(m, "upstreamKeys")
				delete(m, debFingerprints)
			} else {
				raw, _ := json.Marshal(string(openpgp.ArmorCertificates(kept...))) // a string always marshals
				fps := make([]string, 0, len(kept))
				for _, c := range kept {
					fps = append(fps, c.Fingerprint)
				}
				rawFps, _ := json.Marshal(fps) // strings always marshal
				m["upstreamKeys"], m[debFingerprints] = raw, rawFps
			}
			r.setMember(debFormat, m)
			r.applied(partDebUpstreamKeysRemoved)
		}
		left = len(kept)
	}
	if left > 0 {
		return nil
	}
	mirror := (&Document{Remote: r.doc["remote"]})
	member, err := r.qs.ListReferencingVirtuals(r.ctx, r.id)
	if err != nil {
		return err
	}
	if remoteMode(mirror) == ModeMirror || len(member) > 0 {
		r.suspend(partDebUpstreamKeysRemoved)
	}
	return nil
}
