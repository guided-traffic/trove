package oci

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/tagorder"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The tags of a name: the upstream's tags list read page by page, and the choice of the tags a mirror keeps (oci.md "Mirror discovery",
// Q-OCI-180).

// tagsListURL is the address of a page of a name's tags list on the upstream.
func tagsListURL(base *url.URL, name, last string) *url.URL {
	u := registryURL(base, name, "/tags/list")
	q := url.Values{"n": {strconv.Itoa(tagsPage)}}
	if last != "" {
		q.Set("last", last)
	}
	u.RawQuery = q.Encode()
	return u
}

// readTags reads the tags list of an upstream name through Upstream.Get, in pages of tagsPage, at most MaxTaglistEntries tags
// (R-OCI-020): a name with more fails its plan with the reason too_many_tags. found is false for a name the upstream does not have: a
// 404, or the 401 a registry answers to a valid token for an image that does not exist (Q-OCI-127). The tags are in byte order,
// without repeats, and only those the grammar allows.
func (a remoteAdapter) readTags(ctx context.Context, u discover.Upstream, name string) (tags []string, found bool, err error) {
	last := ""
	for range maxTagPages {
		resp, err := u.Get(ctx, tagsListURL(u.Base(), name, last), "application/json", MaxDocumentBytes)
		if err != nil {
			return nil, false, err
		}
		switch {
		case resp.Status == http.StatusNotFound,
			resp.Status == http.StatusUnauthorized && a.StoodMissing(formats.Path{Path: TaglistPath(name)}, resp.Header):
			return nil, false, nil
		case resp.Status != http.StatusOK:
			return nil, false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, fmt.Sprintf("The upstream answered %d for the tags list of an image.", resp.Status))
		}
		doc, err := parseTaglist(resp.Body)
		if err != nil {
			return nil, false, err
		}
		tags = append(tags, doc.Tags...)
		if len(tags) > MaxTaglistEntries {
			return nil, false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "too_many_tags: the image has more than 100,000 tags")
		}
		next, more := nextLast(resp.Header, "")
		if !more || len(doc.Tags) == 0 {
			slices.Sort(tags)
			return slices.Compact(tags), true, nil
		}
		if next == "" {
			next = doc.Tags[len(doc.Tags)-1]
		}
		if next == last {
			slices.Sort(tags)
			return slices.Compact(tags), true, nil
		}
		last = next
	}
	return nil, false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "too_many_tags: the image has more than 100,000 tags")
}

// matchTag reports whether a tag matches a pattern of `*` (any run, also empty) and `?` (one character). The grammar of a tag has
// no escape, so the pattern has none either.
func matchTag(pattern, tag string) bool {
	p, t := 0, 0
	star, mark := -1, 0
	for t < len(tag) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == tag[t]):
			p++
			t++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, t
			p++
		case star >= 0:
			p = star + 1
			mark++
			t = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func isWildcardPattern(p string) bool { return strings.ContainsAny(p, "*?") }

// chosenTag is a tag the mirror keeps.
type chosenTag struct {
	Tag string
	// Pinned says an exact pattern of oci.mirror.tags names the tag: it is kept whatever maxVersionsPerPackage says.
	Pinned bool
}

// selectTags keeps the tags that match oci.mirror.tags and none of oci.mirror.excludeTags. The tags a wildcard pattern matched come
// first, newest first by the tag order of Q-OCI-17; the tags an exact pattern names come after them, also newest first, marked
// Pinned. The engine counts the newest maxVersionsPerPackage of the plan's versions and keeps a pinned one besides them, so putting
// the pinned tags last makes the bound count the wildcard matches only (R-OCI-026: `latest` never takes the place of a release).
func selectTags(all []string, ms MirrorSettings) []chosenTag {
	var wild, pinned []chosenTag
	for _, tag := range all {
		if !ValidTag(tag) || slices.ContainsFunc(ms.ExcludeTags, func(p string) bool { return matchTag(p, tag) }) {
			continue
		}
		exact, matched := false, false
		for _, p := range ms.Tags {
			if isWildcardPattern(p) {
				matched = matched || matchTag(p, tag)
			} else if p == tag {
				exact = true
			}
		}
		switch {
		case exact:
			pinned = append(pinned, chosenTag{Tag: tag, Pinned: true})
		case matched:
			wild = append(wild, chosenTag{Tag: tag})
		}
	}
	newestFirst := func(a, b chosenTag) int { return tagorder.Compare(b.Tag, a.Tag) }
	slices.SortFunc(wild, newestFirst)
	slices.SortFunc(pinned, newestFirst)
	return append(wild, pinned...)
}
