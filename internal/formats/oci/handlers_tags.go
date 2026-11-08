package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// MaxPage is the most entries a page of a list holds, and the default (R-OCI-015, R-OCI-016, R-OCI-017).
const MaxPage = 1000

// jsonReply writes a JSON body with its length, so that a client reads it without chunking.
func jsonReply(w http.ResponseWriter, status int, contentType string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "The registry could not encode its answer.", errs.WithCause(err))
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

// pageSize reads the `n` parameter of a list: absent is MaxPage, a number above MaxPage is cut to it, and anything that is not a
// non-negative decimal number answers 400 (R-OCI-015).
func pageSize(q url.Values) (int, error) {
	raw := q.Get("n")
	if raw == "" {
		return MaxPage, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter n must be a non-negative number.")
	}
	return min(n, MaxPage), nil
}

// tagsListLink is the Link header of a page that is followed by more: the same path with n and the last tag of the page (R-OCI-015).
func tagsListLink(rq *formats.Request, n int, last string) string {
	return "<" + rq.URL.EscapedPath() + "?n=" + strconv.Itoa(n) + "&last=" + url.QueryEscape(last) + `>; rel="next"`
}

// tagsDoc is the answer of a tags list.
type tagsDoc struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// handleTagsList is GET /v2/<p>/<r>/<name>/tags/list?n=&last= (R-OCI-015): the tags of the image name in byte order, a page of at
// most n (1,000 at most), starting after `last`, with a Link to the next page when more follow. A tag whose manifest a journal replay
// withheld is not listed (R-OCI-033). A name the repository holds a manifest or a blob of, but no tag, answers an empty list; a name
// it holds nothing of answers 404 NAME_UNKNOWN.
func (f *Format) handleTagsList(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	switch rq.Repo.Class {
	case formats.Remote:
		return f.remoteTagsList(ctx, rq, w)
	case formats.Local:
	default:
		return f.virtualTagsList(ctx, rq, w)
	}
	name := rq.Params.Get("name")
	// The gate is the package document of the name, its tags list (R-AUTHZ-005, Q-OCI-140); each tag is decided by its own path below,
	// as a pull by tag is.
	read := []formats.Action{formats.ActionRead}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: TaglistPath(name), Actions: read}); err != nil {
		return err
	}
	query := rq.URL.Query()
	n, err := pageSize(query)
	if err != nil {
		return err
	}
	last := query.Get("last")
	if last != "" && !ValidTag(last) {
		return errs.E(errs.Invalid, errs.CodeOCITagInvalid, "The parameter last is not a tag.")
	}
	st, err := portOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	doc := tagsDoc{Name: rq.Host.Links(rq).ClientName(name), Tags: []string{}}
	next := ""
	if n > 0 {
		var err error
		if doc.Tags, next, err = visibleTags(ctx, rq, st, repo.ID, name, last, n); err != nil {
			return err
		}
	}
	if len(doc.Tags) == 0 {
		held, err := st.Holds(ctx, repo.ID, name)
		if err != nil {
			return err
		}
		if !held {
			return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The repository holds nothing of the image name.")
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	if next != "" {
		w.Header().Set("Link", tagsListLink(rq, n, next))
	}
	return jsonReply(w, http.StatusOK, "application/json", doc)
}

// tagsScanBudget bounds the tags one request reads past without listing them (the ones the caller may not read).
const tagsScanBudget = 10 * MaxPage

// visibleTags reads the tags of the name after the cursor in chunks, decides the chunk for the caller with one listing-form check (no audit
// event per entry, R-AUTHZ-016) and keeps the readable ones until n are found. The cursor of the next page is the last tag **listed**, never
// a tag the caller may not read: a hidden name must not reach the caller in a Link (Q-OCI-76). A page that reaches the end of its budget
// without a single readable tag cannot go on without naming a hidden tag, and is refused instead.
func visibleTags(ctx context.Context, rq *formats.Request, st Store, repoID int64, name, last string, n int) (tags []string, next string, err error) {
	tags = []string{}
	cursor, scanned := last, 0
	for {
		page, err := st.Tags(ctx, repoID, name, cursor, min(MaxPage, max(n-len(tags), 64)))
		if err != nil {
			return nil, "", err
		}
		paths := make([]string, len(page.Tags))
		for i, t := range page.Tags {
			paths[i] = TagPath(name, t)
		}
		allowed, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths)
		if err != nil {
			return nil, "", err
		}
		for i, t := range page.Tags {
			scanned++
			if !allowed[i] {
				continue
			}
			tags = append(tags, t)
			if len(tags) == n {
				// More follow when this chunk goes on or the store has a further page.
				if i < len(page.Tags)-1 || page.Next != "" {
					next = t
				}
				return tags, next, nil
			}
		}
		if page.Next == "" {
			return tags, "", nil
		}
		if scanned >= tagsScanBudget {
			if len(tags) == 0 {
				return nil, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported,
					"More than 10,000 consecutive tags of the image name are not readable for the caller; the list cannot go on without naming one of them.")
			}
			return tags, tags[len(tags)-1], nil
		}
		cursor = page.Next
	}
}
