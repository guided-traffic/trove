package ansible

import (
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// The upstream documents a remote stores as they came, and their rendering for a client (R-ANS-017, R-ANS-020).

func invalidDocument(what, why string) error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent "+what+" that trove cannot use: "+why+".")
}

// upstreamCollection is the part of a collection document of the public Galaxy that trove reads.
type upstreamCollection struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	HighestVersion *struct {
		Version string `json:"version"`
	} `json:"highest_version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// parseUpstreamCollection reads a collection document an upstream sent into the summary the collection document is rendered from.
func parseUpstreamCollection(doc []byte) (rows.CollectionSummary, error) {
	if len(doc) > MaxDocumentBytes {
		return rows.CollectionSummary{}, invalidDocument("a collection document", "it is larger than the limit of documents held in memory")
	}
	var u upstreamCollection
	if err := json.Unmarshal(doc, &u); err != nil || u.Namespace == "" || u.Name == "" {
		return rows.CollectionSummary{}, invalidDocument("a collection document", "it has no namespace and name")
	}
	s := rows.CollectionSummary{
		Namespace: strings.ToLower(u.Namespace), Name: strings.ToLower(u.Name), CreatedAt: parseTime(u.CreatedAt), UpdatedAt: parseTime(u.UpdatedAt),
	}
	if u.HighestVersion != nil {
		s.HighestVersion = u.HighestVersion.Version
	}
	return s, nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// upstreamPage is a page of the upstream's version list as the offset arithmetic needs it.
type upstreamPage struct {
	entries []galaxydoc.VersionEntry
	count   int
}

func parsePage(doc []byte) (upstreamPage, error) {
	entries, count, _, err := galaxydoc.ParseUpstreamList(doc)
	if err != nil {
		return upstreamPage{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent a version list that trove cannot use: "+err.Error()+".", errs.WithCause(err))
	}
	if count > MaxListVersions {
		return upstreamPage{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream lists "+strconv.Itoa(count)+" versions of one collection; a remote reads at most "+strconv.Itoa(MaxListVersions)+" per request.")
	}
	return upstreamPage{entries: entries, count: count}, nil
}

// hostListed reports whether a lower-case host matches one of the host patterns of remote.extraHosts.
func hostListed(patterns []string, host string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, host); err == nil && ok {
			return true
		}
	}
	return false
}

// roleHostAllowed reports whether a role version may be fetched from the host of rawURL: an https URL on a host of the repository's
// extraHosts (R-ANS-020, Q-ANS-7). The upstream's own host is not among them unless it is listed.
func roleHostAllowed(patterns []string, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return hostListed(patterns, host)
}

// collectionHostAllowed reports whether the archive of a collection may be fetched from the host of u: the upstream's own host or one
// of the repository's extraHosts (R-ANS-017). The scheme must be http or https.
func collectionHostAllowed(patterns []string, upstream, u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == strings.ToLower(upstream.Hostname()) {
		return true
	}
	return hostListed(patterns, host)
}

// roleVersionsPage is a parsed page of a role's version list: the generic object (every member of the upstream's page that trove
// passes) and the entries that survive the host rule.
type roleVersionsPage struct {
	doc map[string]json.RawMessage
	// kept are the entries a client is shown, in the upstream's order, with the ref of each.
	kept []roleEntry
	// dropped counts the entries the host rule or the grammar of the version name removed.
	dropped int
}

type roleEntry struct {
	obj map[string]json.RawMessage
	ref string
	// url is the upstream's download_url, the URL the archive path maps to.
	url string
}

// parseRoleVersions reads a page of a role's version list. allowed decides the download_url of an entry; an entry whose name is no
// version name, whose download_url is not allowed, or which has none, is dropped (R-ANS-020).
func parseRoleVersions(doc []byte, allowed func(string) bool) (roleVersionsPage, error) {
	var page roleVersionsPage
	if len(doc) > MaxDocumentBytes || json.Unmarshal(doc, &page.doc) != nil || page.doc == nil {
		return page, invalidDocument("a role's version list", "it is not a JSON object")
	}
	var results []map[string]json.RawMessage
	if raw, ok := page.doc["results"]; ok {
		if err := json.Unmarshal(raw, &results); err != nil {
			return page, invalidDocument("a role's version list", "results is not a list of objects")
		}
	}
	for _, e := range results {
		var name, dl string
		_ = json.Unmarshal(e["name"], &name) //nolint:errcheck // a member of another type is an entry without one
		_ = json.Unmarshal(e["download_url"], &dl)
		if !roleRefOK(name) || dl == "" || !allowed(dl) {
			page.dropped++
			continue
		}
		page.kept = append(page.kept, roleEntry{obj: e, ref: name, url: dl})
	}
	return page, nil
}

// pageParam reads the page number of an upstream link (next_link, next): "page" of its query, 0 when there is none.
func pageParam(link string) int {
	u, err := url.Parse(link)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil || n < 1 || n > maxRolePage {
		return 0
	}
	return n
}

// renderRoleVersions renders a page of a role's version list for the repository whose URL is repo (with its trailing slash): the
// entries that survive the host rule, each download_url on the repository's role archive route, and the links of the page on the
// repository's own routes (R-ANS-020). keepRepo names the prefix of an address that is already the repository's: Rewrite of an
// already rewritten document keeps those entries, so that it is idempotent; the serving path passes "".
func renderRoleVersions(doc []byte, id string, repo string, allowed func(string) bool, keepRepo string) ([]byte, error) {
	own := ""
	if keepRepo != "" {
		own = keepRepo + "v1/roles/archives/" + id + "/"
	}
	page, err := parseRoleVersions(doc, func(u string) bool {
		return allowed(u) || (own != "" && strings.HasPrefix(u, own))
	})
	if err != nil {
		return nil, err
	}
	results := make([]map[string]json.RawMessage, 0, len(page.kept))
	for _, e := range page.kept {
		obj := e.obj
		obj["download_url"] = rawString(repo + "v1/roles/archives/" + id + "/" + e.ref + ".tar.gz")
		results = append(results, obj)
	}
	return renderRolePage(page.doc, results, repo, "v1/roles/"+id+"/versions/", nil)
}

// renderRolePage writes the members of a v1 page with the given results and the links of the page on the repository's address
// base + path. extra are query parameters every link carries (the lookup's owner and name).
func renderRolePage(doc map[string]json.RawMessage, results []map[string]json.RawMessage, repo, path string, extra url.Values) ([]byte, error) {
	out := make(map[string]json.RawMessage, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	rawResults, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	out["results"] = rawResults
	cur := 1
	_ = json.Unmarshal(doc["cur_page"], &cur) //nolint:errcheck // absent: the first page
	link := func(upstream json.RawMessage, fallbackPage int) json.RawMessage {
		var s string
		if json.Unmarshal(upstream, &s) != nil || s == "" {
			return json.RawMessage("null")
		}
		page := pageParam(s)
		if page == 0 {
			page = fallbackPage
		}
		q := url.Values{}
		for k, v := range extra {
			q[k] = v
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(roleVersionsPageSize))
		return rawString(repo + path + "?" + q.Encode())
	}
	for _, k := range []string{"next", "next_link"} {
		if v, ok := doc[k]; ok {
			out[k] = link(v, cur+1)
		}
	}
	for _, k := range []string{"previous", "previous_link"} {
		if v, ok := doc[k]; ok {
			out[k] = link(v, max(cur-1, 1))
		}
	}
	return json.Marshal(out)
}

// renderRoleLookup renders a role lookup for the repository whose URL is repo: the roles as the upstream sent them, their own
// addresses (url, related) on the repository's routes, the links of the page on the repository's routes. Every other member passes
// unchanged (R-ANS-020).
func renderRoleLookup(doc []byte, repo string, owner, name string) ([]byte, error) {
	var page map[string]json.RawMessage
	if len(doc) > MaxDocumentBytes || json.Unmarshal(doc, &page) != nil || page == nil {
		return nil, invalidDocument("a role lookup", "it is not a JSON object")
	}
	var results []map[string]json.RawMessage
	if raw, ok := page["results"]; ok {
		if err := json.Unmarshal(raw, &results); err != nil {
			return nil, invalidDocument("a role lookup", "results is not a list of objects")
		}
	}
	kept := results[:0]
	for _, r := range results {
		var id int64
		if json.Unmarshal(r["id"], &id) != nil || id < 1 || !digits(strconv.FormatInt(id, 10), maxRoleIDDigits) {
			continue // a role without a usable id cannot be addressed
		}
		sid := strconv.FormatInt(id, 10)
		r["url"] = rawString(repo + "v1/roles/" + sid + "/")
		if raw, ok := r["related"]; ok {
			var rel map[string]string
			if json.Unmarshal(raw, &rel) == nil {
				for k := range rel {
					rel[k] = repo + "v1/roles/" + sid + "/" + k + "/"
				}
				r["related"], _ = json.Marshal(rel) //nolint:errcheck // a map of strings
			}
		}
		kept = append(kept, r)
	}
	q := url.Values{}
	if owner != "" {
		q.Set("owner__username", owner)
		q.Set("name", name)
	}
	return renderRolePage(page, kept, repo, "v1/roles/", q)
}

func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s) //nolint:errcheck // a string always marshals
	return b
}
