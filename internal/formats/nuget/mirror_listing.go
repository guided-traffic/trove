package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// listingCursor is the position of a listing pass (nuget.md "Cursor", Q-NUGET-100): the rows read so far and the hit count the
// search reported with them, so that a refusal later says "<n> of <total>".
type listingCursor struct {
	Skip  int `json:"skip"`
	Total int `json:"total,omitempty"`
}

// mirrorListing pages the search resource with an empty query (Q-NUGET-98): take=1000 and a skip that grows by the rows
// read, until a page is empty or short, totalHits is reached or the upstream refuses a larger skip. A listing that ends
// before totalHits ends the pass as truncated with the note "listing truncated at <n> of <totalHits>" (Q-NUGET-101). The
// search lists no unlisted package, and it has no incremental pass: Page.Cursor stays nil at the end.
func mirrorListing(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	d, err := feedDiscovery(ctx, u)
	if err != nil {
		return discover.Page{}, err
	}
	if d == nil || d.Search == nil {
		return discover.Page{}, discover.ErrMethodUnavailable
	}
	var cur listingCursor
	if len(rq.Cursor) > 0 {
		if err := json.Unmarshal(rq.Cursor, &cur); err != nil || cur.Skip < 0 || cur.Total < 0 {
			return discover.Page{}, errBadCursor
		}
	}
	target, _ := d.SearchURL(url.Values{
		"q": {""}, "prerelease": {"true"}, "semVerLevel": {"2.0.0"},
		"take": {strconv.Itoa(searchTake)}, "skip": {strconv.Itoa(cur.Skip)},
	})
	resp, err := u.Get(ctx, target, "application/json", 0)
	if err != nil {
		return discover.Page{}, err
	}
	if cur.Skip > 0 && resp.Status >= 400 && resp.Status < 500 {
		// The upstream refuses a skip that it answered a smaller one of: its window ends here.
		return discover.Page{Done: true, Truncated: true, Note: listingNote(cur.Skip, cur.Total)}, nil
	}
	body, err := fetchResponse(resp, "search")
	if err != nil {
		return discover.Page{}, err
	}
	var res struct {
		TotalHits int `json:"totalHits"`
		Data      []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return discover.Page{}, invalidDocument("The upstream's search answer is not a search result: " + err.Error())
	}
	total := max(res.TotalHits, 0)

	limit := limitOf(rq)
	page := discover.Page{}
	seen := map[string]bool{}
	consumed := 0
	for _, row := range res.Data {
		if len(page.Names) >= limit {
			break
		}
		consumed++
		if name, ok := admitted(rq.Scope, row.ID); ok && !seen[name] {
			seen[name] = true
			page.Names = append(page.Names, name)
		}
	}
	next := cur.Skip + consumed
	if consumed == len(res.Data) && (len(res.Data) < searchTake || next >= total) {
		page.Done = true
		if next < total {
			page.Truncated, page.Note = true, listingNote(next, total)
		}
		return page, nil
	}
	// The rows are not all read, or a full page may be followed by another: read on from the next row.
	page.Cursor, _ = json.Marshal(listingCursor{Skip: next, Total: total})
	return page, nil
}

// listingNote is the note of a truncated listing, in words for the status: no URL, no credential, at most 256 characters.
func listingNote(read, total int) string {
	note := fmt.Sprintf("listing truncated at %d of %d", read, total)
	if len(note) > maxNote {
		note = note[:maxNote]
	}
	return note
}
