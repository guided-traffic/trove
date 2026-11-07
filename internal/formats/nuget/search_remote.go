package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/search"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// The search of a remote repository (R-NUGET-016): the query is forwarded to the search resource
// of the upstream's service index through the engine's pass-through (Host.Remote().Query), which
// owns the request, the per-replica cache proxy_query_results, the circuit and the populate check
// (Q-ARCH-270). What a caller sees of the answer is decided here, on every use: the results the
// remote's patterns, a claim or a pin rule keep away and the versions the caller may not read are
// removed, and the addresses are the serving repository's. A query is not forwarded when a term
// is protected (the withheld-query rule), when forwardSearch is off, on the cache view and on an
// endpoint that fills no caches; the remote then answers with its cache-only search, which is the
// search of R-NUGET-010 over its rows of origin cache, and it does so as well when the upstream
// cannot be asked or fails.

// defaultRemoteResultTTL is nuget.search.remote_result_ttl when a replica was not bound with the
// configuration (Q-NUGET-16).
const defaultRemoteResultTTL = 60 * time.Second

// searchSource is the remote a search asks: the repository the client addressed, or a remote
// member of the virtual repository it addressed (R-NUGET-018 step 1).
type searchSource struct {
	// rq is the request as the client addressed it. The withheld-query rule, the claims of the
	// results and the read scope are decided on it, so through a virtual repository they are
	// that repository's (R-NUGET-018).
	rq *formats.Request
	// call is the request addressed to the remote: the pass-through runs on it, so the remote's
	// client, cache and populate check apply (rq.Member keeps the addressed repository), and
	// call.Repo is the remote, whose settings and patterns apply.
	call *formats.Request
	ref  formats.RepoRef
	// merged says that a virtual repository merges the results: it removes what its claims and
	// pin rules keep away itself (R-NUGET-018 step 2), so the results are not asked about twice.
	merged bool
	// admits is the test of the include and exclude patterns the cache-only search applies in its
	// scan: the remote's own and, for a member, the virtual repository's as well (Q-NUGET-77), so
	// that the count it reports holds no ID the merge would remove. nil when none restricts.
	admits func(lowerID string) bool
	// budget is the scan budget of the request; the members of a virtual repository share one
	// (Q-NUGET-76).
	budget *scanBudget
}

// runRemoteSearch is runSearch for a remote repository addressed directly.
func (f *Format) runRemoteSearch(ctx context.Context, rq *formats.Request, st Store, p search.Params) ([]rows.SearchHit, int, error) {
	s := searchSource{rq: rq, call: rq, ref: rq.RepoRef(), admits: patternsOf(rq.Repo), budget: newScanBudget()}
	return f.remoteSearch(ctx, s, st, p)
}

// searchRemoteMember is the search of a remote member of a virtual repository: the forwarded
// search of the member with the virtual repository's claims and pin rules, or its cache-only
// search (R-NUGET-018 step 1) with the patterns the merge applies to the member (admits) and the
// request's scan budget.
func (f *Format) searchRemoteMember(ctx context.Context, rq *formats.Request, st Store, m formats.PlannedMember, p search.Params, admits func(string) bool, budget *scanBudget) ([]rows.SearchHit, int, error) {
	call, err := memberRequest(rq, m.Member.Name)
	if err != nil {
		return nil, 0, err
	}
	s := searchSource{rq: rq, call: call, ref: m.Member, merged: true, admits: admits, budget: budget}
	return f.remoteSearch(ctx, s, st, p)
}

// remoteSearch answers a search of one remote: forwarded when it may be, else, and when the
// upstream cannot be asked, from the remote's cache.
func (f *Format) remoteSearch(ctx context.Context, s searchSource, st Store, p search.Params) ([]rows.SearchHit, int, error) {
	scope, err := s.rq.Host.Resolver().ReadScope(ctx, s.rq, []qname.Repo{s.ref.Name})
	if err != nil {
		return nil, 0, err
	}
	forward, err := f.mayForward(ctx, s, p)
	if err != nil {
		return nil, 0, err
	}
	if forward {
		hits, total, answered, err := f.forwardSearch(ctx, s, scope, p)
		if err != nil || answered {
			return hits, total, err
		}
	}
	return searchRepo(ctx, st, scope, searchTarget{key: s.ref.Name, id: s.ref.ID, admits: s.admits}, p, s.budget)
}

// mayForward decides whether a query goes to the upstream (R-NUGET-016, Q-NUGET-79): not when the
// remote turned forwarding off, not on its cache view, not on an endpoint that fills no caches
// (R-NUGET-034, R-EP-019), not when a version: term is no version (the query matches nothing),
// and not when a name of the query is protected for this remote; a withheld query is counted.
func (f *Format) mayForward(ctx context.Context, s searchSource, p search.Params) (bool, error) {
	switch {
	case !settingsOf(s.call.Repo.Config).ForwardSearch, s.rq.CacheView, namesNoVersion(p.Terms):
		return false, nil
	case s.rq.Subject.Endpoint != "" && !s.rq.Subject.CacheFill:
		return false, nil
	}
	for _, name := range withheldNames(p.Terms) {
		away, err := s.rq.Host.Resolver().KeepsAway(ctx, s.rq, s.ref, name)
		if err != nil {
			return false, err
		}
		if away {
			f.replicaOf(s.rq.Host).search.withheldQuery(s.ref.Name.String())
			return false, nil
		}
	}
	return true, nil
}

// withheldNames are the names the withheld-query rule asks about for a query (R-NUGET-016,
// Q-NUGET-80): the value of every term (Parse dropped a field prefix and folded it like an ID),
// a phrase split into its words, and in every word each run of the characters an ID may hold,
// with and without separators at its ends, so that punctuation an upstream ignores does not
// carry a protected name past the rule.
func withheldNames(terms []rows.SearchTerm) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, t := range terms {
		for _, w := range strings.Fields(t.Value) {
			add(w)
			for _, run := range strings.FieldsFunc(w, notIDRune) {
				add(run)
				add(strings.Trim(run, ".-_"))
			}
		}
	}
	return out
}

// notIDRune reports whether a character cannot be part of a package ID (R-NUGET-003).
func notIDRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_'
}

// forwardSearch asks the upstream through the engine and keeps what the caller may see of the
// answer. answered is false when the remote answers from its cache instead: it may not contact
// its upstream for this caller and holds no copy (formats.ErrOffline), or the upstream failed,
// has no search resource or answered with something that is no search result; a failure is
// counted.
func (f *Format) forwardSearch(ctx context.Context, s searchSource, scope formats.ReadScope, p search.Params) (hits []rows.SearchHit, total int, answered bool, err error) {
	rep := f.replicaOf(s.rq.Host)
	params := forwardParams(p)
	res, err := s.rq.Host.Remote().Query(ctx, s.call, formats.Query{
		Path: formats.Path{Path: FeedSegment + "/" + resSearch}, Params: params, TTL: rep.cfg.Search.RemoteResultTTL,
	})
	var up upstreamSearch
	if err == nil {
		up, err = parseUpstreamSearch(res.Body)
	}
	switch {
	case err == nil:
		hits, total, err = f.keepForCaller(ctx, s, scope, p, up)
		return hits, total, err == nil, err
	case ctx.Err() != nil:
		return nil, 0, false, ctx.Err()
	case errors.Is(err, formats.ErrOffline):
		return nil, 0, false, nil
	case isInternalFault(err):
		return nil, 0, false, err
	}
	rep.search.forwardFailed(s.ref.Name.String())
	logOf(s.rq).Info("the forwarded search of a NuGet remote failed; the remote answers from its cache",
		"repo", s.ref.Name, "error", err)
	return nil, 0, false, nil
}

// isInternalFault reports an error the host or the format raised as a fault of trove's own (an
// *errs.Error of kind internal): it is answered as one and not hidden behind the cache-only search.
// Any other error is the upstream's or the storage's, and the remote answers from its cache.
func isInternalFault(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Kind == errs.Internal
}

func logOf(rq *formats.Request) *slog.Logger {
	if rq.Log != nil {
		return rq.Log
	}
	return slog.Default()
}

// forwardParams are the parameters of the upstream request (R-NUGET-016): the terms of the
// client's q as Parse read them and the withheld rule checked them (forwardedQ), and the filters
// and the page in the form trove validated and clamped them to, so that equal searches share one
// cached answer.
func forwardParams(p search.Params) url.Values {
	v := url.Values{}
	if q := forwardedQ(p.Terms); q != "" {
		v.Set("q", q)
	}
	v.Set("skip", strconv.Itoa(p.Skip))
	v.Set("take", strconv.Itoa(p.Take))
	v.Set("prerelease", strconv.FormatBool(p.Prerelease))
	if p.SemVer2 {
		v.Set("semVerLevel", "2.0.0")
	}
	if p.PackageType != "" {
		v.Set("packageType", p.PackageType)
	}
	return v
}

// forwardedQ writes the terms of a query back in the query syntax of R-NUGET-010: a field and its
// colon, a phrase in quotes. The upstream is sent these and not the client's text, so nothing
// reaches it that the withheld rule did not see (Q-NUGET-80); the text of a version: term that is
// no version, which Parse folds to "", never leaves (mayForward does not forward such a query).
func forwardedQ(terms []rows.SearchTerm) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		v := t.Value
		if t.Phrase || strings.ContainsAny(v, " \t") {
			v = `"` + v + `"`
		}
		if t.Field != "" {
			v = t.Field + ":" + v
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, " ")
}

// namesNoVersion reports whether a version: term of the query is no version: such a query
// matches nothing (R-NUGET-010), so it is not forwarded and the cache-only search answers it.
func namesNoVersion(terms []rows.SearchTerm) bool {
	return slices.ContainsFunc(terms, func(t rows.SearchTerm) bool { return t.Field == search.FieldVersion && t.Value == "" })
}

// upstreamSearch is an upstream's answer to a search: the count and the results, each read on
// its own, so that one result an upstream got wrong costs that result only.
type upstreamSearch struct {
	TotalHits int64             `json:"totalHits"`
	Data      []json.RawMessage `json:"data"`
}

// maxSearchAnswerBytes bounds an upstream's search answer, inflated. The answer is parsed again on
// every use (the engine keeps the bytes, the caller's filter applies each time), so a gzip coding
// the upstream applies anyway is undone on every use too: the bound keeps that cost, and a
// replica's memory per search, far below MaxDocumentBytes. An answer of take=20 from nuget.org is
// some tens of KiB; a larger answer is no answer, and the remote answers from its cache and counts
// a failure.
const maxSearchAnswerBytes = 8 << 20

// parseUpstreamSearch reads the answer as received: a gzip coding the upstream applied anyway
// is undone within maxSearchAnswerBytes.
func parseUpstreamSearch(body []byte) (upstreamSearch, error) {
	doc, err := remote.Decode(body, maxSearchAnswerBytes)
	if err != nil {
		return upstreamSearch{}, err
	}
	var up upstreamSearch
	if err := json.Unmarshal(doc, &up); err != nil {
		return upstreamSearch{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's search answer is not a search document.", errs.WithCause(err))
	}
	return up, nil
}

// upstreamResult is one result of an upstream's search as the protocol describes it
// (R-NUGET-016): the members trove renders again, and the ones it keeps.
type upstreamResult struct {
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	Description     string          `json:"description"`
	Summary         string          `json:"summary"`
	Title           string          `json:"title"`
	IconURL         string          `json:"iconUrl"`
	LicenseURL      string          `json:"licenseUrl"`
	ProjectURL      string          `json:"projectUrl"`
	Tags            json.RawMessage `json:"tags"`
	Authors         json.RawMessage `json:"authors"`
	Owners          json.RawMessage `json:"owners"`
	TotalDownloads  int64           `json:"totalDownloads"`
	Verified        bool            `json:"verified"`
	Deprecation     json.RawMessage `json:"deprecation"`
	Vulnerabilities json.RawMessage `json:"vulnerabilities"`
	PackageTypes    []struct {
		Name string `json:"name"`
	} `json:"packageTypes"`
	Versions []struct {
		Version   string `json:"version"`
		Downloads int64  `json:"downloads"`
	} `json:"versions"`
}

// keepForCaller turns the upstream's page into the hits this caller may see and reduces the
// upstream's count by the results removed from the page (R-NUGET-016). The page is cut to the
// take that was asked for, whatever the upstream returned.
func (f *Format) keepForCaller(ctx context.Context, s searchSource, scope formats.ReadScope, p search.Params, up upstreamSearch) ([]rows.SearchHit, int, error) {
	data := up.Data[:min(len(up.Data), p.Take)]
	hits := make([]rows.SearchHit, 0, len(data))
	removed := 0
	for _, raw := range data {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		h, ok, err := f.forwardedHit(ctx, s, scope, p.Terms, raw)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			removed++
			continue
		}
		hits = append(hits, h)
	}
	upTotal := int(min(max(up.TotalHits, 0), math.MaxInt32))
	return hits, max(upTotal-removed, p.Skip+len(hits)), nil
}

// forwardedHit is one upstream result as this caller may see it; ok is false for a result that
// is removed: one that is no NuGet package, one the remote's include and exclude patterns do not
// admit, one a claim or a pin rule keeps away from the remote (addressed directly; a virtual
// repository decides that in its merge), one of which the caller may read no version, and one
// the upstream may have matched on a version the caller may not read (matchedOnReadable).
func (f *Format) forwardedHit(ctx context.Context, s searchSource, scope formats.ReadScope, terms []rows.SearchTerm, raw json.RawMessage) (rows.SearchHit, bool, error) {
	h, ok := resultHit(raw)
	if !ok || !s.call.Repo.Admits(h.LowerID, false) {
		return rows.SearchHit{}, false, nil
	}
	if !s.merged {
		away, err := s.rq.Host.Resolver().KeepsAway(ctx, s.rq, s.ref, h.LowerID)
		if err != nil || away {
			return rows.SearchHit{}, false, err
		}
	}
	switch scope.Shape(s.ref.Name) {
	case formats.ReadAll:
		return h, true, nil
	case formats.ReadRows:
		kept, ok := readableOnly(h, func(v string) bool { return scope.MayRead(s.ref.Name, h.LowerID, v) })
		if !ok || !matchedOnReadable(h, kept, terms) {
			return rows.SearchHit{}, false, nil
		}
		return kept, true, nil
	}
	return rows.SearchHit{}, false, nil
}

// matchedOnReadable reports whether the upstream's match of a result rests on versions the caller
// may read (R-NUGET-034, Q-NUGET-74 for the local search): every version: term must name a version
// left in kept, and when the upstream's latest version was removed, whose metadata the upstream
// matched the text terms against, the query may hold no other term. Otherwise the presence of the
// result, and totalHits, would tell what a hidden version is or says.
func matchedOnReadable(upstream, kept rows.SearchHit, terms []rows.SearchTerm) bool {
	readable := make([]string, len(kept.Listed))
	for i, v := range kept.Listed {
		readable[i] = v.LowerVersion
	}
	if !namesReadable(versionTermsOf(terms), readable) {
		return false
	}
	if kept.Latest.LowerVersion == upstream.Latest.LowerVersion {
		return true
	}
	return !slices.ContainsFunc(terms, func(t rows.SearchTerm) bool { return t.Field != search.FieldVersion })
}

// resultHit reads one result of an upstream's answer; ok is false for one that is no result.
func resultHit(raw json.RawMessage) (rows.SearchHit, bool) {
	var u upstreamResult
	if json.Unmarshal(raw, &u) != nil {
		return rows.SearchHit{}, false
	}
	return u.hit()
}

// hit converts an upstream result into the hit the search renders and the merge unites
// (Q-NUGET-78): the versions in URL form, ascending, with their download counts; the metadata
// of the result as the metadata of its latest version; the owners, the verified flag, the
// deprecation, the vulnerabilities and the total downloads as the latest version's forwarded
// members. The deprecation and the vulnerabilities come without the upstream's addresses, as
// in the remote's registration: every "@id" and "@type" is dropped at any depth, an advisoryUrl
// is kept only as an absolute http(s) link without user info (Q-NUGET-85). ok is false for a
// result whose ID is no NuGet ID, that lists no version, or that lists more versions than trove
// reads of one ID (remote.MaxLeaves, Q-NUGET-83).
func (u upstreamResult) hit() (rows.SearchHit, bool) {
	lower, ok := lowerID(u.ID)
	if !ok || len(u.Versions) > remote.MaxLeaves {
		return rows.SearchHit{}, false
	}
	listed := make([]rows.VersionEntry, 0, len(u.Versions))
	seen := make(map[string]bool, len(u.Versions))
	for _, uv := range u.Versions {
		v, err := version.Parse(uv.Version)
		if err != nil || seen[v.URLForm()] {
			continue
		}
		seen[v.URLForm()] = true
		listed = append(listed, rows.VersionEntry{
			LowerVersion: v.URLForm(), FullVersion: v.String(), Downloads: max(uv.Downloads, 0),
			Listed: true, IsPrerelease: v.IsPrerelease(), IsSemver2: v.IsSemver2(),
		})
	}
	if len(listed) == 0 {
		return rows.SearchHit{}, false
	}
	slices.SortStableFunc(listed, func(a, b rows.VersionEntry) int { return compareVersions(a.LowerVersion, b.LowerVersion) })
	latest := listed[len(listed)-1]
	h := rows.SearchHit{LowerID: lower, DisplayName: u.ID, Listed: listed}
	h.Latest = rows.RegistrationVersion{
		VersionEntry: latest,
		Forwarded: &rows.Forwarded{
			Owners: textList(u.Owners), Verified: u.Verified, TotalDownloads: max(u.TotalDownloads, 0),
			Deprecation: remote.Deprecation(u.Deprecation), Vulnerabilities: remote.Vulnerabilities(u.Vulnerabilities),
		},
	}
	// The metadata describes the upstream's latest version; it is the latest version's only when
	// the upstream's own list agrees.
	if v, err := version.Parse(u.Version); err == nil && v.URLForm() == latest.LowerVersion {
		h.Latest.PackageTypes, h.Latest.Metadata = u.metadata()
	} else {
		h.Latest.Forwarded.Deprecation, h.Latest.Forwarded.Vulnerabilities = nil, nil
	}
	return h, true
}

// metadata returns the lowercased package type names and the metadata object of R-NUGET-005
// with the members a search result shows.
func (u upstreamResult) metadata() ([]string, []byte) {
	md := rows.Metadata{
		Title: u.Title, Summary: u.Summary, Description: u.Description, Authors: strings.Join(textList(u.Authors), ", "),
		Tags: textList(u.Tags), ProjectURL: u.ProjectURL, IconURL: u.IconURL, LicenseURL: u.LicenseURL,
	}
	var types []string
	for _, t := range u.PackageTypes {
		if t.Name != "" {
			md.PackageTypes = append(md.PackageTypes, rows.PackageType{Name: t.Name})
			types = append(types, strings.ToLower(t.Name))
		}
	}
	b, err := json.Marshal(md)
	if err != nil {
		return types, nil
	}
	return types, b
}

// textList reads a member that is a list of strings or one string; a string is one entry, as
// the protocol gives owners and authors either way. Tags given as one string are its words.
func textList(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return slices.DeleteFunc(list, func(s string) bool { return strings.TrimSpace(s) == "" })
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return []string{text}
	}
	return []string{}
}

// readableOnly keeps the versions of a forwarded hit the caller may read (R-NUGET-034): ok is
// false when none is left. When a version was removed, the total downloads are the sum of the
// versions shown; when the upstream's latest version was removed, the metadata, the deprecation
// and the vulnerabilities that describe it are left out, because they would show what a hidden
// version says (Q-NUGET-78).
func readableOnly(h rows.SearchHit, mayRead func(lowerVersion string) bool) (rows.SearchHit, bool) {
	kept := slices.DeleteFunc(slices.Clone(h.Listed), func(v rows.VersionEntry) bool { return !mayRead(v.LowerVersion) })
	if len(kept) == 0 {
		return rows.SearchHit{}, false
	}
	if len(kept) == len(h.Listed) {
		return h, true
	}
	fw := *h.Latest.Forwarded
	fw.TotalDownloads = 0
	for _, v := range kept {
		fw.TotalDownloads += v.Downloads
	}
	latest := kept[len(kept)-1]
	out := h
	out.Listed = kept
	if latest.LowerVersion == h.Latest.LowerVersion {
		out.Latest.Forwarded = &fw
		return out, true
	}
	fw.Deprecation, fw.Vulnerabilities = nil, nil
	out.Latest = rows.RegistrationVersion{VersionEntry: latest, Forwarded: &fw}
	return out, true
}
