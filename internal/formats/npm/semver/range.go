package semver

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// MaxRangeLength bounds the range text ParseRange reads (Q-NPM-16). node-semver
// has no bound; a range is typed by a person, and the bound keeps the cost of
// parsing independent of what a caller sends.
const MaxRangeLength = 4096

// Options are node-semver's range options.
type Options struct {
	// Loose accepts the deviations of node-semver's loose mode: "=" and spaces
	// before a version, leading zeros, a pre-release without its hyphen, and
	// comparators that are not valid are dropped instead of refused.
	Loose bool

	// IncludePrerelease lets a pre-release satisfy a range without a comparator
	// of the same [major, minor, patch] that carries a pre-release, and makes
	// the lower bound of x-ranges, tildes and carets its lowest pre-release.
	IncludePrerelease bool
}

// Range is a parsed node-semver range: an OR of AND-ed comparators. It is
// immutable and safe for concurrent use.
type Range struct {
	opts Options
	sets [][]comparator
}

type comparator struct {
	op  string // "", ">", ">=", "<" or "<="
	ver Version
	any bool
}

// nullSet is the comparator no version satisfies.
const nullSet = "<0.0.0-0"

func (c comparator) value() string {
	if c.any {
		return ""
	}
	return c.op + c.ver.Canonical()
}

// ParseRange parses a node-semver range. In strict mode (the zero Options) it
// refuses exactly the text node-semver's validRange refuses.
func ParseRange(expr string, opts Options) (*Range, error) {
	if len(expr) > MaxRangeLength {
		return nil, invalidRange(expr)
	}
	p := rangeParser{opts: opts, t: compile()}
	raw := collapseSpace(jsTrim(expr))

	var sets [][]comparator
	for _, part := range strings.Split(raw, "||") {
		set, err := p.parseRange(jsTrim(part))
		if err != nil {
			return nil, invalidRange(expr)
		}
		if len(set) > 0 {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return nil, invalidRange(expr)
	}
	if len(sets) > 1 {
		first := sets[0]
		kept := sets[:0:0]
		for _, s := range sets {
			if s[0].value() != nullSet {
				kept = append(kept, s)
			}
		}
		sets = kept
		if len(sets) == 0 {
			sets = [][]comparator{first}
		} else if len(sets) > 1 {
			for _, s := range sets {
				if len(s) == 1 && s[0].any {
					sets = [][]comparator{s}
					break
				}
			}
		}
	}
	return &Range{opts: opts, sets: sets}, nil
}

// Validate reports whether expr is a range npm itself accepts (validRange in
// strict mode).
func Validate(expr string) error {
	_, err := ParseRange(expr, Options{})
	return err
}

// ParseForExposure parses an exposure rule's version range (R-NPM-048). The
// text must be one node-semver accepts, and the returned range is built with
// IncludePrerelease: a rule decides pre-releases with its own prereleases
// flag, never with the range's pre-release rule (Q-NPM-16). Acceptance is
// checked in both option sets, because the desugaring differs at the largest
// version numbers.
func ParseForExposure(expr string) (*Range, error) {
	if err := Validate(expr); err != nil {
		return nil, err
	}
	return ParseRange(expr, Options{IncludePrerelease: true})
}

// String returns node-semver's formatted range: comparators joined by a space,
// sets by "||", and "*" for a range that accepts everything. ParseRange of the
// result with the same options accepts the same versions; like node-semver it
// may drop a ">=0.0.0" once more, so the text can shrink on the first re-parse.
func (r *Range) String() string {
	var b strings.Builder
	for i, set := range r.sets {
		if i > 0 {
			b.WriteString("||")
		}
		for k, c := range set {
			if k > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(c.value())
		}
	}
	if b.Len() == 0 {
		return "*"
	}
	return b.String()
}

// Options returns the options the range was parsed with.
func (r *Range) Options() Options { return r.opts }

// Satisfies reports whether v lies in the range, including node-semver's
// pre-release rule: a version with a pre-release satisfies a set only when a
// comparator of that set has a pre-release on the same [major, minor, patch],
// unless the range was parsed with IncludePrerelease.
func (r *Range) Satisfies(v Version) bool {
	for _, set := range r.sets {
		if r.testSet(set, v) {
			return true
		}
	}
	return false
}

// SatisfiesString parses s the way the range was parsed (loose or strict) and
// reports Satisfies; text that is not a version satisfies nothing.
func (r *Range) SatisfiesString(s string) bool {
	v, err := ParseNode(s, r.opts.Loose)
	return err == nil && r.Satisfies(v)
}

func (r *Range) testSet(set []comparator, v Version) bool {
	for _, c := range set {
		if !c.test(v) {
			return false
		}
	}
	if len(v.Pre) > 0 && !r.opts.IncludePrerelease {
		for _, c := range set {
			if c.any || len(c.ver.Pre) == 0 {
				continue
			}
			if c.ver.Major == v.Major && c.ver.Minor == v.Minor && c.ver.Patch == v.Patch {
				return true
			}
		}
		return false
	}
	return true
}

func (c comparator) test(v Version) bool {
	if c.any {
		return true
	}
	cmp := Compare(v, c.ver)
	switch c.op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	}
	return cmp == 0
}

// MaxSatisfying returns the highest version of vs that satisfies r. Among
// versions of equal precedence the first one wins, as in node-semver.
func MaxSatisfying(vs []Version, r *Range) (Version, bool) {
	var best Version
	found := false
	for _, v := range vs {
		if r.Satisfies(v) && (!found || Compare(best, v) < 0) {
			best, found = v, true
		}
	}
	return best, found
}

// MaxSatisfyingStrings is MaxSatisfying over version texts, parsed the way r
// was parsed; texts that are not versions are skipped. It returns the text as
// given.
func MaxSatisfyingStrings(versions []string, r *Range) (string, bool) {
	var best Version
	var bestText string
	found := false
	for _, s := range versions {
		v, err := ParseNode(s, r.opts.Loose)
		if err != nil || !r.Satisfies(v) {
			continue
		}
		if !found || Compare(best, v) < 0 {
			best, bestText, found = v, s, true
		}
	}
	return bestText, found
}

func sortVersions(vs []Version, descending bool) {
	sort.SliceStable(vs, func(i, j int) bool {
		if descending {
			return CompareBuild(vs[j], vs[i]) < 0
		}
		return CompareBuild(vs[i], vs[j]) < 0
	})
}

func invalidRange(expr string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalidRange, "invalid version range "+quote(expr))
}

// rangeParser ports Range.parseRange and the helpers it calls.
type rangeParser struct {
	opts Options
	t    *tokens
}

type errInvalid struct{}

func (errInvalid) Error() string { return "invalid comparator" }

func (p rangeParser) parseRange(r string) ([]comparator, error) {
	t := p.t
	r = t.buildStrip.ReplaceAllString(r, "")

	hyphen := t.hyphen
	if p.opts.Loose {
		hyphen = t.hyphenLoose
	}
	if m := hyphen.FindStringSubmatch(r); m != nil {
		r = p.hyphenReplace(m)
	}
	r = t.comparatorTrim.ReplaceAllString(r, "${1}${2}${3}")
	r = t.tildeTrim.ReplaceAllString(r, "${1}~")
	r = t.caretTrim.ReplaceAllString(r, "${1}^")

	parts := strings.Split(r, " ")
	for i, c := range parts {
		parts[i] = p.parseComparatorText(c)
	}
	list := splitSpace(strings.Join(parts, " "))
	gte0 := t.gte0
	if p.opts.IncludePrerelease {
		gte0 = t.gte0pre
	}
	for i, c := range list {
		c = jsTrim(c)
		if loc := gte0.FindStringIndex(c); loc != nil {
			c = c[:loc[0]] + c[loc[1]:]
		}
		list[i] = c
	}
	if p.opts.Loose {
		kept := list[:0]
		for _, c := range list {
			if t.comparatorLoos.MatchString(c) {
				kept = append(kept, c)
			}
		}
		list = kept
	}

	var order []string
	byValue := map[string]comparator{}
	comps := make([]comparator, 0, len(list))
	for _, text := range list {
		// All comparators are built before the null set is looked for, so an
		// invalid one after it still refuses the range.
		c, err := p.newComparator(text)
		if err != nil {
			return nil, err
		}
		comps = append(comps, c)
	}
	for _, c := range comps {
		v := c.value()
		if v == nullSet {
			return []comparator{c}, nil
		}
		if _, seen := byValue[v]; !seen {
			order = append(order, v)
		}
		byValue[v] = c
	}
	if len(order) > 1 {
		if _, ok := byValue[""]; ok {
			delete(byValue, "")
			kept := order[:0]
			for _, v := range order {
				if v != "" {
					kept = append(kept, v)
				}
			}
			order = kept
		}
	}
	out := make([]comparator, 0, len(order))
	for _, v := range order {
		out = append(out, byValue[v])
	}
	return out, nil
}

// newComparator is "new Comparator(text, options)".
func (p rangeParser) newComparator(text string) (comparator, error) {
	text = strings.Join(splitSpace(jsTrim(text)), " ")
	re := p.t.comparator
	if p.opts.Loose {
		re = p.t.comparatorLoos
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return comparator{}, errInvalid{}
	}
	op := m[1]
	if op == "=" {
		op = ""
	}
	if m[2] == "" {
		return comparator{op: op, any: true}, nil
	}
	v, err := ParseNode(m[2], p.opts.Loose)
	if err != nil {
		return comparator{}, err
	}
	return comparator{op: op, ver: v}, nil
}

// parseComparatorText turns carets, tildes, x-ranges and stars into plain
// comparators (the parseComparator function of range.js). Build metadata has
// been stripped from the whole range before.
func (p rangeParser) parseComparatorText(comp string) string {
	comp = p.replaceCarets(comp)
	comp = p.replaceTildes(comp)
	comp = p.replaceXRanges(comp)
	return p.replaceStars(comp)
}

func isX(id string) bool { return id == "" || strings.EqualFold(id, "x") || id == "*" }

func invalidXRangeOrder(M, m, pa string) bool {
	return (isX(M) && !isX(m)) || (isX(m) && pa != "" && !isX(pa))
}

func mapWords(s string, f func(string) string) string {
	words := splitSpace(jsTrim(s))
	for i, w := range words {
		words[i] = f(w)
	}
	return strings.Join(words, " ")
}

func (p rangeParser) replaceTildes(comp string) string {
	return mapWords(comp, p.replaceTilde)
}

func (p rangeParser) replaceTilde(comp string) string {
	re := p.t.tilde
	if p.opts.Loose {
		re = p.t.tildeLoose
	}
	m := re.FindStringSubmatch(comp)
	if m == nil {
		return comp
	}
	M, mi, pa, pr := m[1], m[2], m[3], m[4]
	z := p.lowest()
	switch {
	case isX(M):
		return ""
	case isX(mi):
		return ">=" + M + ".0.0" + z + " <" + plusOne(M) + ".0.0-0"
	case isX(pa):
		return ">=" + M + "." + mi + ".0" + z + " <" + M + "." + plusOne(mi) + ".0-0"
	case pr != "":
		return ">=" + M + "." + mi + "." + pa + "-" + pr + " <" + M + "." + plusOne(mi) + ".0-0"
	}
	return ">=" + M + "." + mi + "." + pa + " <" + M + "." + plusOne(mi) + ".0-0"
}

func (p rangeParser) replaceCarets(comp string) string {
	return mapWords(comp, p.replaceCaret)
}

func (p rangeParser) replaceCaret(comp string) string {
	re := p.t.caret
	if p.opts.Loose {
		re = p.t.caretLoose
	}
	m := re.FindStringSubmatch(comp)
	if m == nil {
		return comp
	}
	M, mi, pa, pr := m[1], m[2], m[3], m[4]
	z := p.lowest()
	switch {
	case isX(M):
		return ""
	case isX(mi):
		return ">=" + M + ".0.0" + z + " <" + plusOne(M) + ".0.0-0"
	case isX(pa):
		if M == "0" {
			return ">=" + M + "." + mi + ".0" + z + " <" + M + "." + plusOne(mi) + ".0-0"
		}
		return ">=" + M + "." + mi + ".0" + z + " <" + plusOne(M) + ".0.0-0"
	}
	low := ">=" + M + "." + mi + "." + pa
	if pr != "" {
		low += "-" + pr
	}
	switch {
	case M != "0":
		return low + " <" + plusOne(M) + ".0.0-0"
	case mi == "0":
		return low + " <" + M + "." + mi + "." + plusOne(pa) + "-0"
	}
	return low + " <" + M + "." + plusOne(mi) + ".0-0"
}

// lowest is the suffix of a lower bound that includes pre-releases.
func (p rangeParser) lowest() string {
	if p.opts.IncludePrerelease {
		return "-0"
	}
	return ""
}

func (p rangeParser) replaceXRanges(comp string) string {
	words := splitSpace(comp)
	for i, w := range words {
		words[i] = p.replaceXRange(w)
	}
	return strings.Join(words, " ")
}

func (p rangeParser) replaceXRange(comp string) string {
	comp = jsTrim(comp)
	re := p.t.xrange
	if p.opts.Loose {
		re = p.t.xrangeLoose
	}
	g := re.FindStringSubmatch(comp)
	if g == nil {
		return comp
	}
	gt, M, m, pa := g[1], g[2], g[3], g[4]
	if invalidXRangeOrder(M, m, pa) {
		return comp
	}
	xM := isX(M)
	xm := xM || isX(m)
	xp := xm || isX(pa)
	anyX := xp
	if gt == "=" && anyX {
		gt = ""
	}
	pr := p.lowest()
	ret := g[0]
	switch {
	case xM:
		if gt == ">" || gt == "<" {
			ret = nullSet
		} else {
			ret = "*"
		}
	case gt != "" && anyX:
		if xm {
			m = "0"
		}
		pa = "0"
		switch gt {
		case ">":
			gt = ">="
			if xm {
				M, m, pa = plusOne(M), "0", "0"
			} else {
				m, pa = plusOne(m), "0"
			}
		case "<=":
			gt = "<"
			if xm {
				M = plusOne(M)
			} else {
				m = plusOne(m)
			}
		}
		if gt == "<" {
			pr = "-0"
		}
		ret = gt + M + "." + m + "." + pa + pr
	case xm:
		ret = ">=" + M + ".0.0" + pr + " <" + plusOne(M) + ".0.0-0"
	case xp:
		ret = ">=" + M + "." + m + ".0" + pr + " <" + M + "." + plusOne(m) + ".0-0"
	}
	return ret
}

// replaceStars removes the first star with its operator: a star is "any
// version", and an empty comparator means that.
func (p rangeParser) replaceStars(comp string) string {
	comp = jsTrim(comp)
	if loc := p.t.star.FindStringIndex(comp); loc != nil {
		comp = comp[:loc[0]] + comp[loc[1]:]
	}
	return comp
}

// hyphenReplace turns "1.2.3 - 2.3.4" into its comparators; m holds the
// submatches of the hyphen pattern.
func (p rangeParser) hyphenReplace(m []string) string {
	from, fM, fm, fp, fpr := m[1], m[2], m[3], m[4], m[5]
	to, tM, tm, tp, tpr := m[7], m[8], m[9], m[10], m[11]
	z := p.lowest()
	switch {
	case isX(fM):
		from = ""
	case isX(fm):
		from = ">=" + fM + ".0.0" + z
	case isX(fp):
		from = ">=" + fM + "." + fm + ".0" + z
	case fpr != "":
		from = ">=" + from
	default:
		from = ">=" + from + z
	}
	switch {
	case isX(tM):
		to = ""
	case isX(tm):
		to = "<" + plusOne(tM) + ".0.0-0"
	case isX(tp):
		to = "<" + tM + "." + plusOne(tm) + ".0-0"
	case tpr != "":
		to = "<=" + tM + "." + tm + "." + tp + "-" + tpr
	case p.opts.IncludePrerelease:
		to = "<" + tM + "." + tm + "." + plusOne(tp) + "-0"
	default:
		to = "<=" + to
	}
	return jsTrim(from + " " + to)
}

// plusOne is "+s + 1" printed as JavaScript prints a number, so a component
// beyond the safe integers yields text no version grammar accepts, exactly as
// in node-semver.
func plusOne(s string) string {
	f, _ := strconv.ParseFloat(s, 64)
	f++
	switch {
	case math.IsInf(f, 0):
		return "Infinity"
	case f >= 1e21:
		return "1e+21"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
