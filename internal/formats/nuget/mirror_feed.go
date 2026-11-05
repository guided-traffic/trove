package nuget

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The catalog as a feed (Q-NUGET-95, Q-NUGET-96, Q-NUGET-100): the catalog index lists pages with the commitTimeStamp of
// their last commit, a page lists items of commits, an item has @type nuget:PackageDetails or nuget:PackageDelete, an ID, a
// version and the commitTimeStamp of its commit. Commits only ever get later, and the items of one commit have no order.
// Leaves are not read for discovery.

// feedCursor is the position of a catalog pass.
type feedCursor struct {
	// Commit is the commitTimeStamp, as the upstream wrote it, of the last commit whose names were all queued: the Since of
	// the next incremental pass. Empty before the first commit.
	Commit string `json:"commit,omitempty"`
	// Page is the address of the page in progress, set only while a pass stops inside a page: the next call reads it
	// first and does not need the index to find it.
	Page string `json:"page,omitempty"`
	// Skip is the number of names of the first commit after Commit that were queued already, for a commit that has more
	// names than one page of the engine takes (names are taken in the order of the name).
	Skip int `json:"skip,omitempty"`
}

// catalogCommit is the names of one commit of one page, distinct and in the order of the name.
type catalogCommit struct {
	stamp   time.Time
	raw     string
	entries []catalogEntry
}

// catalogEntry is one admitted ID of a commit; removed when every item of the ID in the commit is a PackageDelete.
type catalogEntry struct {
	name    string
	removed bool
}

// mirrorFeed returns the next names of a pass over Catalog/3.0.0: a full pass from the beginning, an incremental pass after
// the commit of the last complete pass. Whole commits are queued, so that the cursor is always the commit of the last commit
// queued whole; a commit larger than the page is queued in parts through Skip.
func mirrorFeed(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	d, err := feedDiscovery(ctx, u)
	if err != nil {
		return discover.Page{}, err
	}
	index := d.CatalogURL()
	if index == nil {
		return discover.Page{}, discover.ErrMethodUnavailable
	}
	start := rq.Cursor
	if len(start) == 0 && rq.Kind == discover.PassIncremental {
		start = rq.Since
	}
	var cur feedCursor
	if len(start) > 0 {
		if err := json.Unmarshal(start, &cur); err != nil || cur.Skip < 0 {
			return discover.Page{}, errBadCursor
		}
	}
	w := &feedWalk{ctx: ctx, u: u, rq: rq, index: index, limit: limitOf(rq), skip: cur.Skip, commit: cur.Commit, first: cur.Page, byName: map[string]int{}}
	if cur.Commit != "" {
		if w.after, err = parseStamp(cur.Commit); err != nil {
			return discover.Page{}, errBadCursor
		}
	}
	return w.run()
}

// feedWalk is one call of mirrorFeed.
type feedWalk struct {
	ctx   context.Context
	u     discover.Upstream
	rq    discover.PageRequest
	index *url.URL
	limit int

	commit string    // raw stamp of the last commit queued whole
	after  time.Time // its time; the zero time before the first commit
	skip   int       // names of the commit after `after` that are queued already
	first  string    // the page in progress of the cursor

	pending []catalogPage
	loaded  bool

	byName map[string]int // name -> index in order, for the dedupe across commits
	order  []catalogEntry
}

type catalogPage struct {
	id    string
	stamp time.Time
}

func (w *feedWalk) run() (discover.Page, error) {
	for {
		pageURL, ok, err := w.nextPage()
		if err != nil {
			return discover.Page{}, err
		}
		if !ok {
			return w.finish(true, ""), nil
		}
		commits, err := w.readPage(pageURL)
		if err != nil {
			return discover.Page{}, err
		}
		for _, c := range commits {
			entries := c.entries
			applied := 0
			if w.skip > 0 {
				applied = min(w.skip, len(entries))
				entries = entries[applied:]
				w.skip = 0
			}
			fresh := 0
			for _, e := range entries {
				if _, dup := w.byName[e.name]; !dup {
					fresh++
				}
			}
			switch {
			case len(w.order)+fresh <= w.limit:
				w.add(entries)
				w.commit, w.after = c.raw, c.stamp
			case len(w.order) == 0:
				// One commit with more names than a page: queue a part of it and come back for the rest.
				part := entries[:w.limit]
				w.add(part)
				return w.finishPartial(pageURL, applied+len(part)), nil
			default:
				return w.finish(false, pageURL), nil
			}
		}
	}
}

// nextPage returns the address of the next page to read: the page in progress of the cursor first, then, from the catalog
// index, the pages whose last commit is later than the last commit queued, earliest first. ok is false after the last.
func (w *feedWalk) nextPage() (string, bool, error) {
	if w.first != "" {
		p := w.first
		w.first = ""
		if _, err := remote.ContentURL(p); err != nil {
			return "", false, errBadCursor
		}
		return p, true, nil
	}
	if !w.loaded {
		pages, err := w.readIndex()
		if err != nil {
			return "", false, err
		}
		w.pending, w.loaded = pages, true
	}
	if len(w.pending) == 0 {
		return "", false, nil
	}
	p := w.pending[0]
	w.pending = w.pending[1:]
	return p.id, true, nil
}

// readIndex reads the catalog index and returns the pages that may hold a commit after the cursor, in the order of their
// last commit. It is read when the pass needs a page it has no address for: at the start, and after a page in progress.
func (w *feedWalk) readIndex() ([]catalogPage, error) {
	body, err := fetchJSON(w.ctx, w.u, w.index, "catalog index")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []struct {
			ID    string `json:"@id"`
			Stamp string `json:"commitTimeStamp"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, invalidDocument("The upstream's catalog index is not a catalog index: " + shortened(err.Error()))
	}
	var pages []catalogPage
	for _, it := range doc.Items {
		t, err := parseStamp(it.Stamp)
		if err != nil {
			return nil, invalidDocument("A page of the upstream's catalog index has a commitTimeStamp that is no time.")
		}
		if _, err := remote.ContentURL(it.ID); err != nil {
			return nil, invalidDocument("A page of the upstream's catalog index names an address that cannot be used: " + err.Error())
		}
		if t.After(w.after) {
			pages = append(pages, catalogPage{id: it.ID, stamp: t})
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if !pages[i].stamp.Equal(pages[j].stamp) {
			return pages[i].stamp.Before(pages[j].stamp)
		}
		return pages[i].id < pages[j].id
	})
	return pages, nil
}

// readPage reads one catalog page and returns its commits after the cursor, earliest first, with the admitted IDs of each.
func (w *feedWalk) readPage(address string) ([]catalogCommit, error) {
	target, err := remote.ContentURL(address)
	if err != nil {
		return nil, invalidDocument("A page of the upstream's catalog index names an address that cannot be used: " + err.Error())
	}
	body, err := fetchJSON(w.ctx, w.u, target, "catalog page")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []struct {
			Type    json.RawMessage `json:"@type"`
			Stamp   string          `json:"commitTimeStamp"`
			ID      string          `json:"nuget:id"`
			Version string          `json:"nuget:version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, invalidDocument("The upstream's catalog page is not a catalog page: " + shortened(err.Error()))
	}
	byStamp := map[int64]*catalogCommit{}
	removed := map[int64]map[string]bool{} // per commit and name: true while every item is a delete
	var stamps []int64
	for _, it := range doc.Items {
		t, err := parseStamp(it.Stamp)
		if err != nil {
			return nil, invalidDocument("An item of the upstream's catalog page has a commitTimeStamp that is no time.")
		}
		if !t.After(w.after) {
			continue
		}
		// A commit counts when it has any item, so that the cursor passes a commit of records this format does not read.
		k := t.UnixNano()
		c := byStamp[k]
		if c == nil {
			c = &catalogCommit{stamp: t, raw: it.Stamp}
			byStamp[k] = c
			removed[k] = map[string]bool{}
			stamps = append(stamps, k)
		}
		kind := catalogKind(it.Type)
		if kind == itemOther {
			continue
		}
		name, ok := admitted(w.rq.Scope, it.ID)
		if !ok {
			continue
		}
		if prev, seen := removed[k][name]; seen {
			removed[k][name] = prev && kind == itemDelete
		} else {
			removed[k][name] = kind == itemDelete
		}
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	out := make([]catalogCommit, 0, len(stamps))
	for _, k := range stamps {
		c := byStamp[k]
		for name, gone := range removed[k] {
			c.entries = append(c.entries, catalogEntry{name: name, removed: gone})
		}
		sort.Slice(c.entries, func(i, j int) bool { return c.entries[i].name < c.entries[j].name })
		out = append(out, *c)
	}
	return out, nil
}

const (
	itemOther = iota
	itemDetails
	itemDelete
)

// catalogKind reads the @type of a catalog item, a string or a list of strings, with or without the nuget: prefix.
func catalogKind(raw json.RawMessage) int {
	var types []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		types = []string{one}
	} else if json.Unmarshal(raw, &types) != nil {
		return itemOther
	}
	for _, t := range types {
		switch strings.ToLower(strings.TrimPrefix(t, "nuget:")) {
		case "packagedelete":
			return itemDelete
		case "packagedetails":
			return itemDetails
		}
	}
	return itemOther
}

// add queues the entries of a commit: an ID listed twice is queued once, as a name when any commit of the call carries a
// PackageDetails for it and as removed only when every record is a delete.
func (w *feedWalk) add(entries []catalogEntry) {
	for _, e := range entries {
		if i, dup := w.byName[e.name]; dup {
			w.order[i].removed = w.order[i].removed && e.removed
			continue
		}
		w.byName[e.name] = len(w.order)
		w.order = append(w.order, e)
	}
}

// finish ends the call at a commit boundary: the cursor is the last commit queued whole, with the page in progress when
// the call stopped inside one.
func (w *feedWalk) finish(done bool, page string) discover.Page {
	return w.page(done, feedCursor{Commit: w.commit, Page: page})
}

// finishPartial ends the call inside a commit that has more names than the call takes.
func (w *feedWalk) finishPartial(page string, skip int) discover.Page {
	return w.page(false, feedCursor{Commit: w.commit, Page: page, Skip: skip})
}

func (w *feedWalk) page(done bool, c feedCursor) discover.Page {
	p := discover.Page{Done: done}
	for _, e := range w.order {
		if e.removed {
			p.Removed = append(p.Removed, e.name)
		} else {
			p.Names = append(p.Names, e.name)
		}
	}
	raw, _ := json.Marshal(c)
	if len(raw) > discover.MaxCursorBytes {
		// An address too long to carry: the next call finds the page again through the index.
		c.Page = ""
		raw, _ = json.Marshal(c)
	}
	p.Cursor = raw
	return p
}

// parseStamp reads a commitTimeStamp: RFC 3339, with any number of fraction digits.
func parseStamp(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

func shortened(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
