package v1

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Cursor pagination, filtering and sorting of every list endpoint (R-API-010,
// R-API-011). A list handler does three things:
//
//	q, err := ParseList(r, spec, s.o.API)            // limit, cursor, filters, sort; 400 on anything unknown
//	rows, err := store.List(ctx, q.Filter, q.After, q.FetchLimit())   // one keyset query
//	WritePage(w, r, NewPage(q, rows, func(row T) []string { return sortKey(row) }))
//
// The parse half (the query, the cursor and its fingerprint) is shared with the
// raw /files/ routes and lives in internal/api/edge; the page half is here.

// ListSpec declares what one list endpoint accepts (R-API-011).
type ListSpec = edge.ListSpec

// ListQuery is a parsed list request.
type ListQuery = edge.ListQuery

// ParseList reads limit, cursor, the declared filters, sort and includeTotal
// ([edge.ParseList]).
func ParseList(r *http.Request, spec ListSpec, cfg config.API) (ListQuery, error) {
	return edge.ParseList(r, spec, cfg)
}

func invalidCursor() error { return edge.InvalidCursor() }

// Page is the envelope of every list endpoint (R-API-010): items and nextCursor
// (null on the last page), plus total where the list allows it.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
	Total      *int64  `json:"total,omitempty"`
}

// NewPage builds the page from the rows of a store query that fetched
// q.FetchLimit() rows in the sort order. key returns the sort-key values of a
// row in the order the store compares them (the sort field's value, then the
// unique identifier); the last item of the page supplies the next cursor.
func NewPage[T any](q ListQuery, rows []T, key func(T) []string) Page[T] {
	p := Page[T]{Items: rows}
	if len(rows) > q.Limit {
		p.Items = rows[:q.Limit]
		c := q.EncodeCursor(key(p.Items[len(p.Items)-1]))
		p.NextCursor = &c
	}
	if p.Items == nil {
		p.Items = []T{}
	}
	return p
}

// MapPage converts the items of a page, keeping the cursor and the total: a
// store returns rows, the page is built from the rows (their sort keys give the
// cursor), and the API answers the representation.
func MapPage[T, U any](p Page[T], f func(T) U) Page[U] {
	out := Page[U]{Items: make([]U, 0, len(p.Items)), NextCursor: p.NextCursor, Total: p.Total}
	for _, it := range p.Items {
		out.Items = append(out.Items, f(it))
	}
	return out
}

// WithTotal adds total, for a list that allows includeTotal.
func (p Page[T]) WithTotal(n int64) Page[T] {
	p.Total = &n
	return p
}

// WritePage answers 200 with the envelope and, when a next page exists, the
// header Link: <...>; rel="next" (RFC 8288) with the URL of the next page built
// from the public base URL.
func WritePage[T any](w http.ResponseWriter, r *http.Request, p Page[T]) {
	if p.NextCursor != nil {
		w.Header().Set("Link", "<"+nextURL(r, *p.NextCursor)+`>; rel="next"`)
	}
	writeJSON(w, r, http.StatusOK, p)
}

// nextURL is the request URL with cursor replaced, on the public base URL
// (R-OPS-005), never on unvalidated headers.
func nextURL(r *http.Request, cursor string) string {
	vals := r.URL.Query()
	vals.Set("cursor", cursor)
	u := url.URL{Path: r.URL.Path, RawQuery: vals.Encode()}
	return strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/") + u.String()
}
