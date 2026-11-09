package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The method listing: the catalog of the distribution API, `GET /v2/_catalog` (oci.md "Mirror discovery"). A registry that offers it
// lists the names it holds, in pages of n with the last name as the position; Docker Hub does not offer it.

// catalogURL is the address of the catalog of the upstream: its origin and `/v2/_catalog`, never below the namespace prefix (the
// catalog is the registry's).
func catalogURL(base *url.URL, n int, last string) *url.URL {
	u := *base
	u.User, u.Fragment, u.RawFragment = nil, "", ""
	u.Path, u.RawPath = "/v2/_catalog", ""
	q := url.Values{"n": {strconv.Itoa(n)}}
	if last != "" {
		q.Set("last", last)
	}
	u.RawQuery = q.Encode()
	return &u
}

// catalogDocument is a page of the catalog; a nil Repositories is a document that is no catalog.
type catalogDocument struct {
	Repositories *[]string `json:"repositories"`
}

// readCatalog reads a page of the catalog. offered is false for an answer that is no catalog: any status but 200, or a body without a
// repositories array.
func readCatalog(ctx context.Context, u discover.Upstream, n int, last string) (names []string, resp *discover.Response, offered bool, err error) {
	resp, err = u.Get(ctx, catalogURL(u.Base(), n, last), "application/json", MaxDocumentBytes)
	if err != nil {
		return nil, nil, false, err
	}
	if resp.Status != http.StatusOK {
		return nil, resp, false, nil
	}
	var doc catalogDocument
	lim := untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: MaxDocumentBytes})
	if err := untrusted.UnmarshalJSON(resp.Body, lim, &doc); err != nil || doc.Repositories == nil {
		return nil, resp, false, nil //nolint:nilerr // a body that is no catalog document means the registry offers none
	}
	return *doc.Repositories, resp, true, nil
}

// catalogOffered asks for one name: the upstream offers the catalog when it answers with a repositories array.
func catalogOffered(ctx context.Context, u discover.Upstream) (bool, error) {
	_, _, offered, err := readCatalog(ctx, u, 1, "")
	// A registry without a catalog can refuse the token of the catalog scope instead of the catalog itself (Docker Hub's realm answers 400 to
	// `registry:catalog:*`): the engine reports that as an unexpected status, which here means "no catalog", and the exact names go on.
	if e, ok := errs.As(err); ok && e.Code == errs.CodeUpstreamUnexpectedStatus {
		return false, nil
	}
	return offered, err
}

// nextLast is the position after a page: the `last` of the Link the upstream sent when it sent one, else the last name of the page.
// The Link is read for its query only; the next request is always built here, on the upstream origin.
func nextLast(h http.Header, fallback string) (last string, more bool) {
	if !hasNextLink(h) {
		return fallback, false
	}
	for _, v := range h.Values("Link") {
		start := strings.Index(v, "<")
		end := strings.Index(v, ">")
		if start < 0 || end <= start {
			continue
		}
		if ref, err := url.Parse(v[start+1 : end]); err == nil {
			if l := ref.Query().Get("last"); l != "" {
				return l, true
			}
		}
	}
	return fallback, true
}

// mirrorListing returns the next page of names of the catalog. Each name below the remote's namespace prefix loses the prefix and is
// checked against the scope. A page shorter than n without a Link ends the catalog. The cursor is the last name of the page as the
// upstream spelled it.
func mirrorListing(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	var c struct {
		Last string `json:"last"`
	}
	if len(rq.Cursor) > 0 {
		if err := json.Unmarshal(rq.Cursor, &c); err != nil {
			return discover.Page{}, errBadCursor
		}
	}
	n := min(catalogPage, limitOf(rq))
	raw, resp, offered, err := readCatalog(ctx, u, n, c.Last)
	if err != nil {
		return discover.Page{}, err
	}
	if !offered {
		if c.Last == "" {
			return discover.Page{}, discover.ErrMethodUnavailable
		}
		return discover.Page{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, fmt.Sprintf("The upstream answered %d for a page of its catalog.", resp.Status))
	}
	prefix := namePrefix(u.Base())
	page := discover.Page{}
	for _, full := range raw {
		name := full
		if prefix != "" {
			rest, ok := strings.CutPrefix(full, prefix+"/")
			if !ok {
				continue
			}
			name = rest
		}
		if admitted(rq.Scope, name) {
			page.Names = append(page.Names, name)
		}
	}
	if len(raw) == 0 {
		page.Done = true
		return page, nil
	}
	last, more := nextLast(resp.Header, raw[len(raw)-1])
	if (!more && len(raw) < n) || last == c.Last {
		page.Done = true
		return page, nil
	}
	page.Cursor, _ = json.Marshal(map[string]string{"last": last})
	return page, nil
}
