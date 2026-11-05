package npm

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The method `feed` of the public registry (R-REPO-051, Q-NPM-69): the replication service at replicate.npmjs.com. A full
// pass reads the root's `update_seq`, then lists names with `registry/_all_docs`, one key range per include pattern's
// literal prefix; an incremental pass reads `registry/_changes` after the stored sequence. Every id is normalized by the
// name rule and checked with Scope.Admits before it is returned.

// keyRangeEnd closes a prefix range: the prefix followed by U+FFF0 sorts after every name that starts with the prefix.
const keyRangeEnd = "￰"

// keyRange is one range of the id space: every id when all, otherwise from start to end, both inclusive.
type keyRange struct {
	all        bool
	start, end string
}

// keyRanges returns the ranges that cover the include patterns: one per literal prefix, a prefix that another one
// contains left out, and the whole key space when there is no include pattern or one has no literal prefix.
func keyRanges(include []string) []keyRange {
	if len(include) == 0 {
		return []keyRange{{all: true}}
	}
	set := map[string]bool{}
	for _, p := range include {
		prefix, _ := discover.LiteralPrefix(p)
		if prefix == "" {
			return []keyRange{{all: true}}
		}
		set[prefix] = true
	}
	prefixes := make([]string, 0, len(set))
	for p := range set {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	var out []keyRange
	for _, p := range prefixes {
		if n := len(out); n > 0 && strings.HasPrefix(p, strings.TrimSuffix(out[n-1].end, keyRangeEnd)) {
			continue // sorted: a prefix that extends the previous kept one lies inside its range
		}
		out = append(out, keyRange{start: p, end: p + keyRangeEnd})
	}
	return out
}

func intp(n int) *int { return &n }

// feedFull returns one page of a full pass: the first call reads the root's `update_seq`, every call one `_all_docs`
// request of the cursor's key range.
func feedFull(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	cur, err := readCursor(rq.Cursor)
	if err != nil {
		return discover.Page{}, err
	}
	if len(rq.Cursor) == 0 {
		var root struct {
			UpdateSeq json.RawMessage `json:"update_seq"`
		}
		if err := replicationGet(ctx, u, "/", "", &root); err != nil {
			return discover.Page{}, err
		}
		if cur.Seq, err = seqText(root.UpdateSeq); err != nil {
			return discover.Page{}, err
		}
	}
	if cur.Seq == "" {
		return discover.Page{}, errors.New("npm mirror: the cursor of a full pass has no sequence")
	}
	ranges := keyRanges(rq.Scope.Include)
	idx := cur.rangeIndex()
	if idx >= len(ranges) {
		return doneAt(cur.Seq)
	}
	rg := ranges[idx]

	var q []string
	switch {
	case cur.After != "":
		q = append(q, "startkey="+url.QueryEscape(jsonString(cur.After)))
	case !rg.all:
		q = append(q, "startkey="+url.QueryEscape(jsonString(rg.start)))
	}
	if !rg.all {
		q = append(q, "endkey="+url.QueryEscape(jsonString(rg.end)))
	}
	q = append(q, "limit="+strconv.Itoa(mirrorPageLimit))
	var docs struct {
		Rows []struct {
			ID string `json:"id"`
		} `json:"rows"`
	}
	if err := replicationGet(ctx, u, "/registry/_all_docs", strings.Join(q, "&"), &docs); err != nil {
		return discover.Page{}, err
	}

	limit := pageSize(rq)
	var page discover.Page
	seen := map[string]bool{}
	last, truncated := cur.After, false
	for _, row := range docs.Rows {
		if cur.After != "" && row.ID == cur.After {
			continue // startkey is inclusive: the last id of the previous page comes again
		}
		if norm, ok := mirrorAdmitted(rq.Scope, row.ID); ok && !seen[norm] {
			if len(page.Names) == limit {
				truncated = true
				break
			}
			seen[norm] = true
			page.Names = append(page.Names, norm)
		}
		last = row.ID
	}

	next := mirrorCursor{Seq: cur.Seq, After: last, Range: intp(idx)}
	switch {
	case truncated:
	case len(docs.Rows) < mirrorPageLimit: // the range is read to its end
		if idx+1 >= len(ranges) {
			page.Done = true
			next = mirrorCursor{Seq: cur.Seq}
		} else {
			next = mirrorCursor{Seq: cur.Seq, Range: intp(idx + 1)}
		}
	case last == cur.After:
		return discover.Page{}, errors.New("npm mirror: a full page of _all_docs made no progress")
	}
	if page.Cursor, err = next.encode(); err != nil {
		return discover.Page{}, err
	}
	return page, nil
}

func doneAt(seq string) (discover.Page, error) {
	c, err := mirrorCursor{Seq: seq}.encode()
	return discover.Page{Done: true, Cursor: c}, err
}

// feedIncremental returns one page of an incremental pass: the changes after the cursor's sequence, or after the sequence
// of the last complete pass for the first page. A change marked `deleted` goes to Page.Removed.
func feedIncremental(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	cur, err := readCursor(rq.Cursor)
	if err != nil {
		return discover.Page{}, err
	}
	if cur.Seq == "" {
		prev, err := readCursor(rq.Since)
		if err != nil {
			return discover.Page{}, err
		}
		cur.Seq = prev.Seq
	}
	if cur.Seq == "" {
		return discover.Page{}, errors.New("npm mirror: an incremental pass needs the sequence of the last complete pass")
	}
	var feed struct {
		Results []struct {
			Seq     json.RawMessage `json:"seq"`
			ID      string          `json:"id"`
			Deleted bool            `json:"deleted"`
		} `json:"results"`
		LastSeq json.RawMessage `json:"last_seq"`
	}
	q := "since=" + url.QueryEscape(cur.Seq) + "&limit=" + strconv.Itoa(mirrorPageLimit)
	if err := replicationGet(ctx, u, "/registry/_changes", q, &feed); err != nil {
		return discover.Page{}, err
	}

	limit := pageSize(rq)
	removed := map[string]bool{} // by normalized name: the last change of a name decides
	var order []string
	seq, truncated := cur.Seq, false
	for _, ch := range feed.Results {
		at, err := seqText(ch.Seq)
		if err != nil {
			return discover.Page{}, err
		}
		if norm, ok := mirrorAdmitted(rq.Scope, ch.ID); ok {
			if _, dup := removed[norm]; !dup {
				if len(order) == limit {
					truncated = true
					break
				}
				order = append(order, norm)
			}
			removed[norm] = ch.Deleted
		}
		seq = at
	}
	var page discover.Page
	for _, n := range order {
		if removed[n] {
			page.Removed = append(page.Removed, n)
		} else {
			page.Names = append(page.Names, n)
		}
	}
	if !truncated {
		if last, err := seqText(feed.LastSeq); err == nil {
			seq = last
		}
		page.Done = len(feed.Results) < mirrorPageLimit
	}
	if page.Cursor, err = (mirrorCursor{Seq: seq}).encode(); err != nil {
		return discover.Page{}, err
	}
	return page, nil
}
