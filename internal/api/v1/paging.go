package v1

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Cursor pagination, filtering and sorting of every list endpoint (R-API-010,
// R-API-011). A list handler does three things:
//
//	q, err := ParseList(r, spec, s.o.API)            // limit, cursor, filters, sort; 400 on anything unknown
//	rows, err := store.List(ctx, q.Filter, q.After, q.FetchLimit())   // one keyset query
//	WritePage(w, r, NewPage(q, rows, func(row T) []string { return sortKey(row) }))
//
// The cursor is self-contained: it holds the sort-key values of the last item
// and a fingerprint of the filters and the sort order, nothing else, so the
// next page may come from any replica and no result set or cursor state lives
// in a process (R-API-051, INV-18).

// cursorVersion is the version this release emits. Every release decodes all
// versions in decodableCursorVersions, which the next minor release extends
// before it starts emitting the new one (expand/contract, R-API-010).
const cursorVersion = 1

func decodableCursorVersions() []int { return []int{1} }

// ListSpec declares what one list endpoint accepts (R-API-011).
type ListSpec struct {
	// Filters are the filter parameters, by their full name: "format" (equality,
	// repeatable), "q", "namePrefix", "createdFrom", "createdTo". A parameter
	// not listed answers 400 malformed_request naming it.
	Filters []string
	// Sorts are the sortable fields. The first is the default unless
	// DefaultSort names another. Every order ends with the item's unique
	// identifier as the tie-breaker, which the store appends (R-API-010).
	Sorts []string
	// DefaultSort is "field" or "-field"; empty means Sorts[0] ascending.
	DefaultSort string
	// Total allows includeTotal=true (only for lists whose content does not
	// depend on the caller's permissions).
	Total bool
}

// ListQuery is a parsed list request.
type ListQuery struct {
	// Limit is the page size, between 1 and api.page_size_max.
	Limit int
	// Filter maps each filter parameter that was sent to its values, in the
	// order sent. A parameter that was not sent is absent.
	Filter map[string][]string
	// Sort is the sort field and Desc its direction.
	Sort string
	Desc bool
	// After holds the sort-key values of the last item of the previous page,
	// from the cursor, in the order the page function produced them; nil on the
	// first page. The store compares (sort field, id) > After (or < for Desc).
	After []string
	// IncludeTotal is includeTotal=true on a list that allows it.
	IncludeTotal bool

	fingerprint string
}

// FetchLimit is the number of rows the store query must fetch: one more than
// the page, to learn whether a next page exists.
func (q ListQuery) FetchLimit() int { return q.Limit + 1 }

// First returns the value of a filter that is sent at most once, or "".
func (q ListQuery) First(name string) string {
	if v := q.Filter[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ParseList reads limit, cursor, the declared filters, sort and includeTotal.
// An unknown parameter or sort field answers 400 malformed_request, a bad
// limit the same, and a cursor that does not decode or was issued for other
// filters or another sort answers 400 invalid_cursor. A limit above
// api.page_size_max is clamped to it (Q-API-16).
func ParseList(r *http.Request, spec ListSpec, cfg config.API) (ListQuery, error) {
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ListQuery{}, errs.E(errs.Invalid, errs.CodeInvalid, "The query string is malformed.", errs.WithCause(err))
	}
	allowed := map[string]bool{"limit": true, "cursor": true, "sort": len(spec.Sorts) > 0, "includeTotal": spec.Total}
	for _, f := range spec.Filters {
		allowed[f] = true
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !allowed[k] {
			return ListQuery{}, errs.E(errs.Invalid, errs.CodeInvalid, fmt.Sprintf("The query parameter %q is not known to this endpoint.", k))
		}
	}
	q := ListQuery{Limit: cfg.PageSizeDefault, Filter: map[string][]string{}}
	if v := vals.Get("limit"); v != "" || vals.Has("limit") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return ListQuery{}, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"limit\" must be an integer of 1 or more.")
		}
		q.Limit = min(n, cfg.PageSizeMax)
	}
	if err := q.parseSort(vals.Get("sort"), spec); err != nil {
		return ListQuery{}, err
	}
	for _, f := range spec.Filters {
		if vs, ok := vals[f]; ok {
			q.Filter[f] = vs
		}
	}
	if v := vals.Get("includeTotal"); v != "" {
		switch v {
		case "true":
			q.IncludeTotal = true
		case "false":
		default:
			return ListQuery{}, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"includeTotal\" must be true or false.")
		}
	}
	q.fingerprint = fingerprint(q)
	if c := vals.Get("cursor"); vals.Has("cursor") {
		after, err := decodeCursor(c, q.fingerprint)
		if err != nil {
			return ListQuery{}, err
		}
		q.After = after
	}
	return q, nil
}

func (q *ListQuery) parseSort(raw string, spec ListSpec) error {
	if len(spec.Sorts) == 0 {
		return nil
	}
	s := raw
	if s == "" {
		s = spec.DefaultSort
	}
	if s == "" {
		s = spec.Sorts[0]
	}
	q.Sort, q.Desc = strings.TrimPrefix(s, "-"), strings.HasPrefix(s, "-")
	for _, f := range spec.Sorts {
		if f == q.Sort {
			return nil
		}
	}
	return errs.E(errs.Invalid, errs.CodeInvalid, fmt.Sprintf("The sort field %q is not known to this endpoint; use one of %s.", q.Sort, strings.Join(spec.Sorts, ", ")))
}

// fingerprint is a SHA-256 over a canonical form of the filters the request
// uses and the sort order, so a release that adds an optional filter does not
// change the fingerprint of a request that does not use it (R-API-010). The
// values of one filter are sorted: OR within a field has no order.
func fingerprint(q ListQuery) string {
	names := make([]string, 0, len(q.Filter))
	for k := range q.Filter {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		vs := append([]string(nil), q.Filter[k]...)
		sort.Strings(vs)
		b.WriteString(strconv.Quote(k))
		for _, v := range vs {
			b.WriteByte('=')
			b.WriteString(strconv.Quote(v))
		}
		b.WriteByte('\n')
	}
	dir := "+"
	if q.Desc {
		dir = "-"
	}
	b.WriteString("sort " + dir + strconv.Quote(q.Sort))
	sum := sha256.Sum256([]byte(b.String()))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// cursorBody is the JSON inside a cursor.
type cursorBody struct {
	Key         []string `json:"k"`
	Fingerprint string   `json:"f"`
}

// EncodeCursor builds the opaque cursor of the last item of a page: a
// versioned base64url encoding of its sort-key values and the fingerprint.
func (q ListQuery) EncodeCursor(key []string) string {
	b, _ := json.Marshal(cursorBody{Key: key, Fingerprint: q.fingerprint})
	inner := "v" + strconv.Itoa(cursorVersion) + "." + base64.RawURLEncoding.EncodeToString(b)
	return base64.RawURLEncoding.EncodeToString([]byte(inner))
}

func invalidCursor() error {
	return errs.E(errs.Invalid, errs.CodeInvalidCursor,
		"The cursor is not valid for this request. Start the traversal again without a cursor.")
}

func decodeCursor(c, wantFingerprint string) ([]string, error) {
	outer, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, invalidCursor()
	}
	ver, payload, ok := strings.Cut(string(outer), ".")
	if !ok || !strings.HasPrefix(ver, "v") {
		return nil, invalidCursor()
	}
	n, err := strconv.Atoi(ver[1:])
	if err != nil || !containsInt(decodableCursorVersions(), n) {
		return nil, invalidCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, invalidCursor()
	}
	var body cursorBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Fingerprint != wantFingerprint || len(body.Key) == 0 {
		return nil, invalidCursor()
	}
	return body.Key, nil
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

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
