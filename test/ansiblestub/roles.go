package ansiblestub

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The documents of the Galaxy v1 role API, in the shapes the public Galaxy answers with: a paged object with count, next_link and
// results, where a link is relative to the host. A role is not an archive the Galaxy stores: each version names a tag and a
// download_url that is a GitHub archive, "https://github.com/<user>/<repo>/archive/<tag>.tar.gz".

func roleNotFound() map[string]any { return map[string]any{"detail": "Not found."} }

func roleHref(id int) string { return "/api/v1/roles/" + strconv.Itoa(id) + "/" }

// roleVersionTime is the commit time of the n-th tag of a role.
func roleVersionTime(n int) time.Time { return corpusStart.AddDate(0, 0, n) }

// v1Page renders a v1 list: items is the whole list, the page and its size come from the query.
func v1Page(path string, q url.Values, total int, build func(lo, hi int) []any) map[string]any {
	size, page := 10, 1
	if v, err := strconv.Atoi(q.Get("page_size")); err == nil {
		size = max(1, min(v, 100))
	}
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 0 {
		page = v
	}
	pages := max(1, (total+size-1)/size)
	lo := min((page-1)*size, total)
	hi := min(lo+size, total)
	link := func(p int) any {
		nq := url.Values{}
		for k, v := range q {
			if k != "page" {
				nq[k] = v
			}
		}
		nq.Set("page", strconv.Itoa(p))
		nq.Set("page_size", strconv.Itoa(size))
		return path + "?" + nq.Encode()
	}
	var next, prev any
	if page < pages {
		next = link(page + 1)
	}
	if page > 1 {
		prev = link(page - 1)
	}
	return map[string]any{
		"count": total, "next": next, "previous": prev, "next_link": next, "previous_link": prev,
		"cur_page": page, "num_pages": pages, "page_size": size, "results": build(lo, hi),
	}
}

func roleDocument(r *roleDef) map[string]any {
	versions := make([]map[string]any, 0, len(r.versions))
	for i, v := range r.versions {
		versions = append(versions, map[string]any{"id": r.id*1000 + i, "name": v.name, "release_date": stamp(roleVersionTime(i))})
	}
	return map[string]any{
		"id": r.id, "url": roleHref(r.id),
		"related": map[string]string{"versions": roleHref(r.id) + "versions/"},
		"summary_fields": map[string]any{
			"namespace":          map[string]any{"id": r.id, "name": r.owner},
			"provider_namespace": map[string]any{"id": r.id, "name": r.user},
			"repository":         map[string]any{"name": r.repo, "original_name": r.repo},
			"tags":               []string{}, "dependencies": []string{}, "platforms": []any{},
			"versions": versions,
		},
		"created": stamp(corpusStart), "modified": stamp(corpusStart), "imported": stamp(corpusStart),
		"github_user": r.user, "github_repo": r.repo, "github_branch": r.branch, "github_reference": "",
		"name": r.name, "description": r.summaryTx, "role_type": "ANS", "min_ansible_version": "2.14", "license": "MIT",
		"is_valid": true, "active": true, "download_count": 0,
	}
}

// roleLookup answers v1/roles/ with the roles that match owner__username and name, the whole list without them.
func roleLookup(roles []*roleDef, q url.Values) map[string]any {
	var match []*roleDef
	for _, r := range roles {
		if o := q.Get("owner__username"); o != "" && o != r.owner {
			continue
		}
		if n := q.Get("name"); n != "" && n != r.name {
			continue
		}
		match = append(match, r)
	}
	return v1Page("/api/v1/roles/", q, len(match), func(lo, hi int) []any {
		out := make([]any, 0, hi-lo)
		for _, r := range match[lo:hi] {
			out = append(out, roleDocument(r))
		}
		return out
	})
}

// roleVersions answers v1/roles/<id>/versions/, oldest tag first. Each download_url is on the host of the version's site.
func (s *Server) roleVersions(requestHost string, r *roleDef, q url.Values) map[string]any {
	return v1Page(roleHref(r.id)+"versions/", q, len(r.versions), func(lo, hi int) []any {
		out := make([]any, 0, hi-lo)
		for i := lo; i < hi; i++ {
			v := r.versions[i]
			out = append(out, map[string]any{
				"id": r.id*1000 + i, "url": roleHref(r.id) + "versions/" + strconv.Itoa(r.id*1000+i) + "/",
				"related": map[string]string{}, "name": v.name, "loose_version": v.name,
				"release_date": stamp(roleVersionTime(i)), "commit_date": stamp(roleVersionTime(i)),
				"commit_sha":     strings.Repeat("a", 39) + strconv.Itoa(i%10),
				"download_url":   s.siteBase(v.site, requestHost) + "/" + r.user + "/" + r.repo + "/archive/" + v.name + ".tar.gz",
				"summary_fields": map[string]any{"role": map[string]any{"id": r.id, "name": r.name}},
			})
		}
		return out
	})
}
