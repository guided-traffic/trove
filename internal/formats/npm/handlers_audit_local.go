package npm

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// The audit in mode local (R-VULN-017): trove answers the bulk endpoint from its own advisory data, on local, remote and
// virtual repositories, and sends nothing out. A virtual repository answers itself and consults no member. The names the
// installation treats as internal are withheld (R-VULN-010), the advisories of the others are read in one query for the
// whole body and an advisory is listed when it affects at least one requested version of the name (R-VULN-009).

// auditEcosystem is the OSV ecosystem of npm.
const auditEcosystem = "npm"

// The texts of R-VULN-017; the guide quotes them (guide_test.go).
const (
	auditLocalNotice = "trove audited %d of %d packages from its own advisory data; %d internal names were not matched against public advisories"
	// auditUnimported is the 503 before the npm data is complete.
	auditUnimported = "trove's advisory data for npm is not imported yet"
	// auditMaxVersionsPerName and auditMaxVersions bound the versions of one name and of the whole body in mode local, which
	// evaluates every version against every advisory of its name (S4, Q-VULN-71): a lockfile lists a handful of versions per
	// name, and a body above these bounds is a request for work the answer does not need. 413 npm_too_large.
	auditMaxVersionsPerName = 500
	auditMaxVersions        = 200_000
	// malwareCWE is the weakness a malicious record without CWE identifiers is reported with.
	malwareCWE = "CWE-506"
)

// auditModeOf resolves the audit mode of a repository of the class (R-VULN-017, Q-VULN-44, Q-ARCH-341). npm.audit.mode
// local answers from trove's data, which needs the installation's vulnerabilities.enabled and answers like disabled
// otherwise. forward needs a class that can forward (a local repository has no upstream) and answers like disabled
// otherwise. A document that sets no mode is local while the installation answers audits from its own data, else forward
// on remote and virtual repositories and disabled on local ones (R-NPM-038).
func auditModeOf(h formats.Host, class formats.Class, s auditSettings) string {
	d := h.Vulnerabilities()
	switch s.Mode {
	case auditLocal:
		if d.Enabled() {
			return auditLocal
		}
		return auditDisabled
	case auditForward:
		if class == formats.Local {
			return auditDisabled
		}
		return auditForward
	case auditDisabled:
		return auditDisabled
	}
	switch {
	case d.LocalByDefault():
		return auditLocal
	case class == formats.Local:
		return auditDisabled
	}
	return auditForward
}

// localAuditAllowed is the 501 of the requests that mode local does not answer: the cache view of a remote repository and
// an endpoint with exposure rules, where the withheld names would tell a caller which names the installation treats as
// internal, also those the endpoint hides (R-NPM-048, Q-VULN-19). An endpoint that does not fill caches is answered: mode
// local contacts no upstream.
func localAuditAllowed(rq *formats.Request) error {
	if rq.CacheView || rq.Endpoint.Rules {
		return auditUnavailable(rq.Repo.Name)
	}
	return nil
}

// handleAuditLocal answers POST -/npm/v1/security/advisories/bulk from trove's advisory data.
func (f *Format) handleAuditLocal(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if err := localAuditAllowed(rq); err != nil {
		return err
	}
	set := settingsOf(rq.Repo.Config).Audit
	body, err := readAuditBody(rq.Request, rq.Repo.Name, set.MaxBodySize)
	if err != nil {
		return err
	}
	if err := checkLocalBounds(rq.Repo.Name, body); err != nil {
		return err
	}
	data := rq.Host.Vulnerabilities()
	state := data.State(auditEcosystem)
	if !state.Complete {
		return errs.E(errs.Unavailable, errs.CodeVulnerabilityDataUnavailable, auditUnimported)
	}
	names := make([]string, 0, len(body))
	for n := range body {
		if _, err := name.Parse(n); err == nil { // a name that is no npm name has no advisory
			names = append(names, n)
		}
	}
	sort.Strings(names)
	internal, err := data.InternalNames(ctx, ID, names)
	if err != nil {
		return err
	}
	ask := make([]string, 0, len(names))
	for _, n := range names {
		if !internal[n] {
			ask = append(ask, n)
		}
	}
	found, err := data.ForNames(ctx, auditEcosystem, ask)
	if err != nil {
		return err
	}
	out, err := renderLocalAudit(ctx, body, ask, found)
	if err != nil {
		return err
	}
	withheld := 0
	for n := range body {
		if internal[n] {
			withheld++
		}
	}
	notice := ""
	if withheld > 0 {
		notice = fmt.Sprintf(auditLocalNotice, len(body)-withheld, len(body), withheld)
		if state.Stale {
			notice += "; advisory data is " + ageText(state.Age(time.Now())) + " old"
		}
	}
	auditHeaders(w, headerValue(notice))
	return writeJSON(w, http.StatusOK, out)
}

// checkLocalBounds refuses a body whose versions the local audit would have to evaluate in a number that no lockfile has.
func checkLocalBounds(key string, body map[string][]string) error {
	total := 0
	for n, vs := range body {
		total += len(vs)
		if len(vs) > auditMaxVersionsPerName || total > auditMaxVersions {
			return errs.E(errs.TooLarge, errs.CodeNPMTooLarge, fmt.Sprintf(
				"The audit body exceeds what %s answers from its own advisory data: at most %d versions of one package (%q has %d) and %d versions in all.",
				key, auditMaxVersionsPerName, n, len(vs), auditMaxVersions))
		}
	}
	return nil
}

// headerValue strips CR and LF from a header value (R-VULN-017).
func headerValue(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// ageText says how old the data is as a whole number of days, hours or minutes.
func ageText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

// localAdvisory is one advisory of the answer, in the members and the order the public registry writes them.
type localAdvisory struct {
	ID                 int64     `json:"id"`
	URL                string    `json:"url"`
	Title              string    `json:"title"`
	Severity           string    `json:"severity"`
	VulnerableVersions string    `json:"vulnerable_versions"`
	CWE                []string  `json:"cwe"`
	CVSS               localCVSS `json:"cvss"`
}

// localCVSS is the cvss member: the score (0 when there is none) and the vector (null when there is none).
type localCVSS struct {
	Score        float64 `json:"score"`
	VectorString *string `json:"vectorString"`
}

// advisoryGroup is the records of one name that share a canonical identifier: the advisory the answer lists once.
type advisoryGroup struct {
	// head is the record whose fields the answer shows: the one published under the canonical identifier, else the first.
	head advisory.Advisory
	// entries are the affected entries of the name over every record of the group.
	entries []advisory.Affected
}

// groupAdvisories groups the records of a name by canonical identifier, in the order of the identifiers.
func groupAdvisories(list []advisory.Advisory) []advisoryGroup {
	var out []advisoryGroup
	at := map[string]int{}
	for _, a := range list {
		i, ok := at[a.CanonicalID]
		if !ok {
			at[a.CanonicalID] = len(out)
			out = append(out, advisoryGroup{head: a, entries: slicesClone(a.Affected)})
			continue
		}
		g := &out[i]
		if a.ID == a.CanonicalID && g.head.ID != g.head.CanonicalID {
			g.head = a
		}
		g.entries = append(g.entries, a.Affected...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].head.CanonicalID < out[j].head.CanonicalID })
	return out
}

func slicesClone(in []advisory.Affected) []advisory.Affected {
	return append([]advisory.Affected(nil), in...)
}

// requestedVersion is a version of the body: the text as sent and its canonical form when it parses.
type requestedVersion struct {
	raw, canonical string
	valid          bool
	parsed         semver.Version // set when valid; parsed once, for the order
}

// requestedVersions normalizes the versions of one name, dropping duplicates, in the order of precedence.
func requestedVersions(in []string) []requestedVersion {
	seen := map[string]bool{}
	var out []requestedVersion
	for _, v := range in {
		c, ok := semver.Clean(v)
		key := v
		if ok {
			key = c
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		rv := requestedVersion{raw: v, canonical: key, valid: ok}
		if ok {
			rv.parsed, rv.valid = parseOnce(key)
		}
		out = append(out, rv)
	}
	sort.SliceStable(out, func(i, j int) bool { return versionLess(out[i], out[j]) })
	return out
}

func parseOnce(s string) (semver.Version, bool) {
	v, err := semver.Parse(s)
	return v, err == nil
}

func versionLess(a, b requestedVersion) bool {
	if a.valid && b.valid {
		if c := semver.Compare(a.parsed, b.parsed); c != 0 {
			return c < 0
		}
	}
	if a.valid != b.valid {
		return a.valid
	}
	return a.canonical < b.canonical
}

// renderLocalAudit builds the answer: for each asked name the advisories that affect a requested version, with the ids of
// one answer assigned together (Q-VULN-17). A name without an advisory is absent.
func renderLocalAudit(ctx context.Context, body map[string][]string, ask []string, found map[string][]advisory.Advisory) (map[string][]localAdvisory, error) {
	type hit struct {
		g        advisoryGroup
		text     string
		affected []requestedVersion
	}
	hits := map[string][]hit{}
	var canonicals []string
	for _, n := range ask {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		versions := requestedVersions(body[n])
		for _, g := range groupAdvisories(found[n]) {
			// The ranges of an entry are normalized and sorted once, not once per requested version.
			prepared := make([]*match.Entry, len(g.entries))
			for i, e := range g.entries {
				prepared[i] = match.Prepare(vulnAdapter, e)
			}
			var affected []requestedVersion
			for i, v := range versions {
				if i%256 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				for _, e := range prepared {
					if e.Affects(v.canonical).Affected {
						affected = append(affected, v)
						break
					}
				}
			}
			if len(affected) == 0 {
				continue
			}
			hits[n] = append(hits[n], hit{g: g, text: vulnerableVersions(g.entries, affected), affected: affected})
			canonicals = append(canonicals, g.head.CanonicalID)
		}
	}
	ids := advisory.NPMAuditIDs(canonicals)
	out := map[string][]localAdvisory{}
	for n, hs := range hits {
		for _, h := range hs {
			out[n] = append(out[n], renderAdvisory(n, h.g.head, ids[h.g.head.CanonicalID], h.text))
		}
	}
	return out, nil
}

// renderAdvisory is the answer's member of one advisory (R-VULN-017).
func renderAdvisory(pkg string, a advisory.Advisory, id int64, versions string) localAdvisory {
	title := a.Summary
	switch {
	case title == "" && a.Kind == advisory.KindMalicious:
		title = "Malicious code in " + pkg
	case title == "":
		title = a.CanonicalID
	}
	cwe := a.CWEIDs
	if len(cwe) == 0 && a.Kind == advisory.KindMalicious {
		cwe = []string{malwareCWE}
	}
	if cwe == nil {
		cwe = []string{}
	}
	out := localAdvisory{
		ID: id, URL: advisory.URLOf(a.CanonicalID, a.Aliases), Title: title, Severity: npmSeverity(a),
		VulnerableVersions: versions, CWE: cwe, CVSS: localCVSS{Score: a.CVSS.Score},
	}
	if a.CVSS.Vector != "" {
		v := a.CVSS.Vector
		out.CVSS.VectorString = &v
	}
	return out
}

// npmSeverity maps the level to the words npm prints: moderate for medium, info for none, high for unknown, and critical
// for a malicious record whatever its level.
func npmSeverity(a advisory.Advisory) string {
	if a.Kind == advisory.KindMalicious {
		return "critical"
	}
	switch a.Severity {
	case advisory.SeverityCritical:
		return "critical"
	case advisory.SeverityHigh, advisory.SeverityUnknown:
		return "high"
	case advisory.SeverityMedium:
		return "moderate"
	case advisory.SeverityLow:
		return "low"
	case advisory.SeverityNone:
		return "info"
	}
	return "high"
}
