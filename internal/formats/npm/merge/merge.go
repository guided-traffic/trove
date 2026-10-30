// Package merge builds the package document of a virtual npm repository from the
// documents of its members (R-NPM-030, R-NPM-031): one entry per version from the
// member that tier resolution would pick for that version's tarball, the dist-tags by
// the configured strategies, the time map, and the top-level fields of the version
// latest points to.
//
// It works on the full documents the members render or store and returns a full
// document; it knows no repository, no request and no store. Like every leaf of the
// format it imports the standard library and its siblings only. dist.tarball in the
// result is that of the member the entry came from: the caller sets it for the
// serving repository after the merge has been cached, so that the cached bytes hold
// no address of a request.
package merge

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// The strategies of npm.latestStrategy and npm.distTagStrategy (R-NPM-031).
const (
	HighestMemberLatest = "highest-member-latest"
	NewestPublished     = "newest-published"
	MemberOrder         = "member-order"
	HighestSemver       = "highest-semver"
)

// Tier is where a part of a merge came from; the tiers are walked in this order.
type Tier uint8

// The tiers of R-REPO-019 step 6. A part of a remote member is TierRemote; the
// versions of it whose tarball is cached are in the second tier (Part.CachedVersions).
const (
	TierLocal Tier = iota + 1
	TierRemoteCached
	TierRemote
)

// Part is one member's full document.
type Part struct {
	// Member names the member for the conflict report.
	Member   string
	Tier     Tier
	Position int
	Doc      []byte
	// CachedVersions are the versions of a remote member whose tarball is cached.
	CachedVersions map[string]bool
	// Restricted says that the caller may not fill this member's cache: the part
	// supplies only CachedVersions, so that every version the document lists can be
	// downloaded (R-AUTHZ-008).
	Restricted bool
}

// Options are the settings of the virtual repository's npm section.
type Options struct {
	// Name is the package name of the request.
	Name string
	// LatestStrategy and DistTagStrategy default to highest-member-latest and
	// member-order.
	LatestStrategy, DistTagStrategy string
	// Rev, when not empty, is the _rev of the document.
	Rev string
}

// Conflict is a version that two members hold with different integrity: a sign of
// shadowing or of a confusion attempt (R-NPM-030 item 6).
type Conflict struct {
	Version string
	// Winner is the member the entry came from, Other the member that holds other bytes.
	Winner, Other string
}

// Result is a merged document with what the merge learned on the way.
type Result struct {
	Doc       []byte
	Conflicts []Conflict
	// Sources maps a version to the member its entry came from.
	Sources map[string]string
}

type member struct {
	Part
	index int
	p     *remote.Parsed
	tags  map[string]string
	times map[string]string
}

// eligible reports whether the part may supply a version: it holds it, and a part the
// caller may not fill supplies cached versions only.
func (m *member) eligible(v string) bool {
	if _, ok := m.p.Versions[v]; !ok {
		return false
	}
	return !m.Restricted || m.CachedVersions[v]
}

// tier is the tier the part supplies a version from.
func (m *member) tier(v string) Tier {
	if m.Tier == TierRemote && m.CachedVersions[v] {
		return TierRemoteCached
	}
	return m.Tier
}

// before orders two members for one version: the lowest tier, then the lowest position.
func before(a, b *member, v string) bool {
	if ta, tb := a.tier(v), b.tier(v); ta != tb {
		return ta < tb
	}
	if a.Position != b.Position {
		return a.Position < b.Position
	}
	return a.index < b.index
}

// Do merges the parts. The result is a function of the parts alone, not of the order
// they are passed in beyond (tier, position). A merge that holds no version is
// not found, like a document of a local package without a live version.
func Do(parts []Part, o Options) (*Result, error) {
	ms := make([]*member, 0, len(parts))
	for i, pt := range parts {
		p, err := remote.Parse(pt.Doc)
		if err != nil {
			return nil, err
		}
		ms = append(ms, &member{Part: pt, index: i, p: p, tags: p.Tags(), times: p.Times()})
	}

	// The per-version source rule.
	winner := map[string]*member{}
	var res Result
	res.Sources = map[string]string{}
	for _, m := range ms {
		for v := range m.p.Versions {
			if !m.eligible(v) {
				continue
			}
			if w, ok := winner[v]; !ok || before(m, w, v) {
				winner[v] = m
			}
		}
	}
	if len(winner) == 0 {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	versions := make([]string, 0, len(winner))
	for v := range winner {
		versions = append(versions, v)
	}
	versions = remote.OrderVersions(versions)
	for _, v := range versions {
		res.Sources[v] = winner[v].Member
	}
	res.Conflicts = conflicts(ms, winner, versions)

	// A member contributes when it supplies at least one version.
	var contributing []*member
	for _, m := range ms {
		for v := range winner {
			if m.eligible(v) {
				contributing = append(contributing, m)
				break
			}
		}
	}
	sort.SliceStable(contributing, func(i, j int) bool {
		a, b := contributing[i], contributing[j]
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.index < b.index
	})

	tags := mergeTags(contributing, winner, versions, o)
	timeObj := mergeTime(winner, versions)

	var w builder
	w.str("_id", o.Name)
	if o.Rev != "" {
		w.str("_rev", o.Rev)
	}
	w.str("name", o.Name)
	tagJSON, _ := json.Marshal(sortedTags(tags))
	w.raw("dist-tags", tagJSON)
	var vo builder
	for _, v := range versions {
		vo.raw(v, winner[v].p.Versions[v])
	}
	w.raw("versions", vo.bytes())
	if timeObj != nil {
		w.raw("time", timeObj)
	}
	if lw := winner[tags["latest"]]; tags["latest"] != "" && lw != nil {
		for _, k := range topLevel() {
			if v, ok := lw.p.Top[k]; ok {
				w.raw(k, v)
			}
		}
	}
	res.Doc = w.bytes()
	return &res, nil
}

// topLevel are the members that come from the member the version latest points to
// (R-NPM-030 item 4); readmeFilename belongs to the readme.
func topLevel() []string {
	return []string{"description", "keywords", "license", "homepage", "repository", "bugs", "author", "maintainers", "readme", "readmeFilename"}
}

// integrityOf is what identifies the bytes of a version entry: its sha512 integrity,
// else its shasum.
func integrityOf(raw json.RawMessage) string {
	var e struct {
		Dist struct {
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if e.Dist.Integrity != "" {
		return e.Dist.Integrity
	}
	return e.Dist.Shasum
}

// conflicts lists the versions that members the caller may use hold with other bytes
// than the winner.
func conflicts(ms []*member, winner map[string]*member, versions []string) []Conflict {
	var out []Conflict
	for _, v := range versions {
		w := winner[v]
		want := integrityOf(w.p.Versions[v])
		for _, m := range ms {
			if m == w || !m.eligible(v) {
				continue
			}
			if got := integrityOf(m.p.Versions[v]); got != "" && want != "" && got != want {
				out = append(out, Conflict{Version: v, Winner: w.Member, Other: m.Member})
			}
		}
	}
	return out
}

// mergeTags applies R-NPM-031. Only tags that point to a version the merge holds can
// be chosen: a member's value for a version nobody supplies is dropped before the
// strategy looks at it, so that it cannot take latest away from a member that points
// to a version that can be installed.
func mergeTags(contributing []*member, winner map[string]*member, versions []string, o Options) map[string]string {
	latestStrategy, tagStrategy := o.LatestStrategy, o.DistTagStrategy
	if latestStrategy == "" {
		latestStrategy = HighestMemberLatest
	}
	if tagStrategy == "" {
		tagStrategy = MemberOrder
	}
	published := func(v string) time.Time {
		w := winner[v]
		if w == nil {
			return time.Time{}
		}
		return remote.ParseTime(w.times[v])
	}
	names := map[string]bool{}
	for _, m := range contributing {
		for t := range m.tags {
			names[t] = true
		}
	}
	out := map[string]string{}
	for t := range names {
		var cands []string // in resolution order
		for _, m := range contributing {
			if v, ok := m.tags[t]; ok {
				if _, held := winner[v]; held {
					cands = append(cands, v)
				}
			}
		}
		if len(cands) == 0 {
			continue
		}
		strategy := tagStrategy
		if t == "latest" {
			strategy = latestStrategy
		}
		switch strategy {
		case HighestSemver, HighestMemberLatest:
			out[t] = highest(cands, t == "latest")
		case NewestPublished:
			out[t] = newest(cands, published)
		default:
			out[t] = cands[0]
		}
	}
	if _, ok := out["latest"]; !ok {
		// No member's latest can be installed: the highest release stands in, so
		// that a client that asks for latest gets a version.
		if v := highest(versions, true); v != "" {
			out["latest"] = v
		}
	}
	return out
}

// highest is the highest of the versions by SemVer precedence; with preferRelease a
// version without a pre-release is chosen when there is one.
func highest(vs []string, preferRelease bool) string {
	best := ""
	var bestV semver.Version
	for _, s := range vs {
		v, err := semver.Parse(s)
		if err != nil {
			continue
		}
		if best != "" {
			switch {
			case preferRelease && bestV.IsPrerelease() != v.IsPrerelease():
				if v.IsPrerelease() {
					continue
				}
			case semver.CompareBuild(v, bestV) <= 0:
				continue
			}
		}
		best, bestV = s, v
	}
	if best == "" && len(vs) > 0 {
		best = vs[0]
	}
	return best
}

// newest is the version with the latest publish time; the earlier candidate wins a tie.
func newest(vs []string, published func(string) time.Time) string {
	best, bestAt := "", time.Time{}
	for _, v := range vs {
		at := published(v)
		if best == "" || at.After(bestAt) {
			best, bestAt = v, at
		}
	}
	return best
}

// mergeTime is the time map: created is the earliest and modified the latest of the
// values of the members that supplied a version, and each version has the time of the
// member its entry came from (R-NPM-030 item 3).
func mergeTime(winner map[string]*member, versions []string) json.RawMessage {
	var created, modified string
	var createdAt, modifiedAt time.Time
	seen := map[*member]bool{}
	for _, v := range versions {
		m := winner[v]
		if seen[m] {
			continue
		}
		seen[m] = true
		if c := remote.ParseTime(m.times["created"]); !c.IsZero() && (created == "" || c.Before(createdAt)) {
			created, createdAt = m.times["created"], c
		}
		if t := remote.ParseTime(m.times["modified"]); !t.IsZero() && (modified == "" || t.After(modifiedAt)) {
			modified, modifiedAt = m.times["modified"], t
		}
	}
	var b builder
	if created != "" {
		b.str("created", created)
	}
	if modified != "" {
		b.str("modified", modified)
	}
	for _, v := range versions {
		if t := winner[v].times[v]; t != "" {
			b.str(v, t)
		}
	}
	if b.n == 0 {
		return nil
	}
	return b.bytes()
}

func sortedTags(m map[string]string) *orderedStrings {
	o := &orderedStrings{}
	for k := range m {
		o.keys = append(o.keys, k)
	}
	sort.Strings(o.keys)
	for _, k := range o.keys {
		o.values = append(o.values, m[k])
	}
	return o
}

// orderedStrings is a JSON object of strings with its keys in a fixed order.
type orderedStrings struct{ keys, values []string }

func (o *orderedStrings) MarshalJSON() ([]byte, error) {
	var b builder
	for i := range o.keys {
		b.str(o.keys[i], o.values[i])
	}
	return b.bytes(), nil
}

// builder writes a JSON object member by member.
type builder struct {
	b bytes.Buffer
	n int
}

func (o *builder) key(k string) {
	if o.n == 0 {
		o.b.WriteByte('{')
	} else {
		o.b.WriteByte(',')
	}
	o.n++
	kb, _ := json.Marshal(k)
	o.b.Write(kb)
	o.b.WriteByte(':')
}

func (o *builder) raw(k string, v json.RawMessage) {
	o.key(k)
	if err := json.Compact(&o.b, v); err != nil {
		o.b.WriteString("null")
	}
}

func (o *builder) str(k, v string) {
	o.key(k)
	vb, _ := json.Marshal(v)
	o.b.Write(vb)
}

func (o *builder) bytes() []byte {
	if o.n == 0 {
		return []byte("{}")
	}
	o.b.WriteByte('}')
	return o.b.Bytes()
}
