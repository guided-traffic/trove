package nuget

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// The vulnerability information of the protocol's VulnerabilityInfo/6.7.0 resource (R-VULN-018): the index at
// <feed>/vulnerabilities/index.json lists one page, <feed>/vulnerabilities/trove.json, which holds the advisories of trove's
// own NuGet data keyed by the lower-case package ID. Every repository class lists and serves it while nuget.vulnerabilityInfo
// is local and the NuGet data is complete.

// The resource and its documents.
const (
	resVulnerabilities = "vulnerabilities"
	vulnPageFile       = "trove.json"
	// vulnEcosystem is the OSV ecosystem of NuGet.
	vulnEcosystem = "NuGet"
	// vulnPageName is the @name of the one entry of the index.
	vulnPageName    = "trove"
	vulnPageComment = "Advisory data mirrored by trove"
	// vulnCacheControl is the Cache-Control of both documents (R-VULN-018): private, because the answer is per feed.
	vulnCacheControl = "private, max-age=1800"
)

const (
	routeVulnIndex = "nuget.vulnerabilities.index"
	routeVulnPage  = "nuget.vulnerabilities.page"
)

// The values of nuget.vulnerabilityInfo (R-VULN-018). upstream is the pass-through of R-NUGET-023, which is P1 and not
// built: the schema refuses it.
const (
	VulnInfoLocal = "local"
	VulnInfoOff   = "off"
)

// vulnInfoModeOf resolves nuget.vulnerabilityInfo of a repository (R-VULN-018, Q-VULN-44): local needs the installation's
// vulnerabilities.enabled; a document that leaves the mode out is local while the installation answers audits from its
// own data, else off (the default of remote and virtual repositories would be upstream, which is refused until R-NUGET-023
// is built).
func vulnInfoModeOf(d formats.VulnerabilityData, s settings) string {
	switch s.VulnerabilityInfo {
	case VulnInfoLocal:
		if d.Enabled() {
			return VulnInfoLocal
		}
	case "":
		if d.LocalByDefault() {
			return VulnInfoLocal
		}
	}
	return VulnInfoOff
}

// vulnListed says whether the service index of the repository lists VulnerabilityInfo: the mode is local and the NuGet
// data is complete, as the protocol asks of a feed without data (R-VULN-018). The cache view of a remote repository and an
// endpoint with exposure rules do not list it, as the npm audit is not answered there (Q-VULN-19, Q-VULN-71): the page names
// the packages of the installation's data minus its internal names, which tells a caller which names a hidden package or a
// claim stands for.
func vulnListed(rq *formats.Request) bool {
	if vulnRefusedOn(rq) {
		return false
	}
	d := rq.Host.Vulnerabilities()
	return vulnInfoModeOf(d, settingsOf(rq.Repo.Config)) == VulnInfoLocal && d.State(vulnEcosystem).Complete
}

// vulnRefusedOn is whether the request addresses a view on which the resource is neither listed nor served: the cache view and
// an endpoint with exposure rules (Q-VULN-71).
func vulnRefusedOn(rq *formats.Request) bool { return rq.CacheView || rq.Endpoint.Rules }

// matchVulnerabilities matches "v3/vulnerabilities/{file}" for one of the two documents.
func matchVulnerabilities(file string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		t, ok := feedSegments(escaped, resVulnerabilities, 1)
		if !ok || t[0] != file {
			return nil, false
		}
		return formats.Params{}, true
	}
}

// vulnAvailable decides whether the documents are served: 404 when the repository does not serve trove's data (the resource
// is not listed either), 503 vulnerability_data_unavailable while the NuGet data is not complete.
func vulnAvailable(rq *formats.Request) (formats.VulnerabilityData, error) {
	d := rq.Host.Vulnerabilities()
	if vulnRefusedOn(rq) || vulnInfoModeOf(d, settingsOf(rq.Repo.Config)) != VulnInfoLocal {
		return nil, errNoSuchResource()
	}
	if !d.State(vulnEcosystem).Complete {
		return nil, errs.E(errs.Unavailable, errs.CodeVulnerabilityDataUnavailable, "trove's advisory data for NuGet is not imported yet.")
	}
	return d, nil
}

// etagOf is the weak validator over the fields, length-prefixed so that two field lists never share a hash.
func etagOf(fields ...string) string {
	h := sha256.New()
	for _, s := range fields {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	return `W/"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// vulnIndexETag covers the render version, the feed the entry's @id names and the data's state: its epoch and the time of
// its last sync, which @updated shows (Q-NUGET-9).
func vulnIndexETag(feed string, st advisory.DataState) string {
	return etagOf("nuget-vulnerability-index", strconv.Itoa(RenderVersion), feed,
		strconv.FormatInt(st.Epoch, 10), strconv.FormatInt(st.SyncedAt.Unix(), 10))
}

// vulnPageETag covers the render version and the page's inputs: the data epoch and the applied epochs of the claims and
// repo_config areas (formats.VulnerabilityData.PageKey). The page is the same for every repository, so the repository is no
// input.
func vulnPageETag(d formats.VulnerabilityData) string {
	return etagOf("nuget-vulnerability-page", strconv.Itoa(RenderVersion), strconv.FormatInt(d.State(vulnEcosystem).Epoch, 10), d.PageKey(vulnEcosystem))
}

// replyVuln sends a document of the resource, or its 304.
func replyVuln(rq *formats.Request, w http.ResponseWriter, etag string, body func() ([]byte, error)) error {
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", vulnCacheControl)
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	b, err := body()
	if err != nil {
		return err
	}
	h.Set("Content-Type", document.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(b)
	return err
}

// handleVulnIndex serves <feed>/vulnerabilities/index.json: an array of one entry, the page of trove's data (R-VULN-018).
func (f *Format) handleVulnIndex(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	d, err := vulnAvailable(rq)
	if err != nil {
		return err
	}
	feed := rq.Host.Links(rq).Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/"
	st := d.State(vulnEcosystem)
	return replyVuln(rq, w, vulnIndexETag(feed, st), func() ([]byte, error) {
		return document.Marshal([]vulnIndexEntry{{
			Name: vulnPageName, ID: feed + resVulnerabilities + "/" + vulnPageFile, Updated: document.Timestamp(st.SyncedAt), Comment: vulnPageComment,
		}})
	})
}

type vulnIndexEntry struct {
	Name    string `json:"@name"`
	ID      string `json:"@id"`
	Updated string `json:"@updated"`
	Comment string `json:"comment"`
}

// handleVulnPage serves <feed>/vulnerabilities/trove.json: from the replica's page cache, rendered on a miss (R-VULN-018).
func (f *Format) handleVulnPage(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	d, err := vulnAvailable(rq)
	if err != nil {
		return err
	}
	return replyVuln(rq, w, vulnPageETag(d), func() ([]byte, error) {
		key := strconv.Itoa(RenderVersion) + "/" + d.PageKey(vulnEcosystem)
		return d.Page(ctx, key, func(ctx context.Context) ([]byte, error) { return renderVulnPage(ctx, d) })
	})
}

// vulnEntry is one entry of a package's list on the page.
type vulnEntry struct {
	Severity int    `json:"severity"`
	URL      string `json:"url"`
	Versions string `json:"versions"`
}

// renderVulnPage renders the page: every non-withdrawn advisory of the NuGet data that is not about an internal name
// (R-VULN-010), keyed by the lower-case package ID, one entry per affected range or explicit version.
func renderVulnPage(ctx context.Context, d formats.VulnerabilityData) ([]byte, error) {
	list, err := d.All(ctx, vulnEcosystem)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, a := range list {
		for _, e := range a.Affected {
			if !seen[e.Name] {
				seen[e.Name] = true
				names = append(names, e.Name)
			}
		}
	}
	sort.Strings(names)
	internal, err := d.InternalNames(ctx, ID, names)
	if err != nil {
		return nil, err
	}
	page := map[string][]vulnEntry{}
	dup := map[string]bool{}
	for _, a := range list {
		sev, url := nugetSeverity(a), advisory.URLOf(a.CanonicalID, a.Aliases)
		for _, e := range a.Affected {
			if internal[e.Name] {
				continue
			}
			id := strings.ToLower(e.Name)
			for _, iv := range affectedIntervals(e) {
				entry := vulnEntry{Severity: sev, URL: url, Versions: iv}
				if k := id + "\x00" + url + "\x00" + iv; !dup[k] {
					dup[k] = true
					page[id] = append(page[id], entry)
				}
			}
		}
	}
	return document.Marshal(page)
}

// nugetSeverity maps the level to the integers of the protocol: 0 low, 1 medium, 2 high, 3 critical; unknown is 2, a
// malicious record 3 and none 0 (R-VULN-018).
func nugetSeverity(a advisory.Advisory) int {
	if a.Kind == advisory.KindMalicious {
		return 3
	}
	switch a.Severity {
	case advisory.SeverityCritical:
		return 3
	case advisory.SeverityHigh, advisory.SeverityUnknown:
		return 2
	case advisory.SeverityMedium:
		return 1
	}
	return 0
}

// affectedIntervals renders the entry as NuGet version intervals: "[A, F)", "[A, L]", "[A, )", "(, F)" and "(, L]" for an
// interval that starts at introduced "0", "(, )" for every version and "[V]" per explicit version.
func affectedIntervals(e advisory.Affected) []string {
	if e.EveryVersion {
		return []string{"(, )"}
	}
	var out []string
	for _, r := range e.Ranges {
		out = append(out, rangeIntervals(r)...)
	}
	for _, v := range e.Versions {
		if p, err := version.Parse(v); err == nil {
			out = append(out, "["+p.Normalized()+"]")
		}
	}
	return out
}

// rangeIntervals renders the intervals of one OSV range over NuGet's order. A range that is not SEMVER or ECOSYSTEM, or one
// of whose event versions does not parse, renders nothing: it matches nothing either (R-VULN-009).
func rangeIntervals(r advisory.Range) []string {
	if r.Type != advisory.RangeSemver && r.Type != advisory.RangeEcosystem {
		return nil
	}
	type event struct {
		kind advisory.EventKind
		zero bool
		v    version.Version
	}
	events := make([]event, 0, len(r.Events))
	for _, ev := range r.Events {
		if ev.Kind == advisory.EventIntroduced && ev.Version == "0" {
			events = append(events, event{kind: ev.Kind, zero: true})
			continue
		}
		v, err := version.Parse(ev.Version)
		if err != nil {
			return nil
		}
		events = append(events, event{kind: ev.Kind, v: v})
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.zero != b.zero {
			return a.zero
		}
		if !a.zero {
			if c := version.Compare(a.v, b.v); c != 0 {
				return c < 0
			}
		}
		return a.kind == advisory.EventIntroduced && b.kind != advisory.EventIntroduced
	})
	var out []string
	open, lower := false, event{}
	emit := func(upper string) {
		lo := "(, "
		if !lower.zero {
			lo = "[" + lower.v.Normalized() + ", "
		}
		if upper == "" {
			upper = ")"
		}
		out = append(out, lo+upper)
		open = false
	}
	for _, ev := range events {
		switch ev.kind {
		case advisory.EventIntroduced:
			if !open {
				open, lower = true, ev
			}
		case advisory.EventFixed:
			if open {
				emit(ev.v.Normalized() + ")")
			}
		case advisory.EventLastAffected:
			if open {
				emit(ev.v.Normalized() + "]")
			}
		case advisory.EventLimit:
			if open {
				emit(ev.v.Normalized() + ")")
			}
			return out
		}
	}
	if open {
		emit("")
	}
	return out
}
