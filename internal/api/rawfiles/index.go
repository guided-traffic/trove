package rawfiles

import (
	"html"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// index answers GET and HEAD of a folder (R-STOR-017): HTML for a client that
// prefers text/html, JSON with the same entries otherwise. The page is rendered
// by trove, not stored content, so it is written through the index header set
// (nosniff, same-site, the policy without sandbox, no Content-Disposition). Every
// name is HTML-escaped, and every link is `./` followed by the percent-encoded
// name, so a name such as `javascript:x` stays a relative path. The listing holds
// the entries the caller may read, in pages of storage.listing limits: the
// `limit` and `cursor` query parameters of the management API.
func (h *Handler) index(w http.ResponseWriter, r *http.Request, caller files.Caller, key string, p repository.Cleaned) {
	lq, err := edge.ParseList(r, edge.ListSpec{}, h.api)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	after := ""
	if len(lq.After) == 1 {
		after = lq.After[0]
	} else if lq.After != nil {
		httpserver.Problem(w, r, edge.InvalidCursor())
		return
	}
	page, err := h.svc.Children(r.Context(), caller, key, files.ChildrenQuery{
		Path: p.Path, Folders: true, After: after, Limit: lq.Limit, Strict: true,
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	next := ""
	if page.Next != "" {
		next = lq.EncodeCursor([]string{page.Next})
	}
	cw := httpserver.ContentWriterFor(w, r, httpserver.ContentIndex, "")
	cw.Header().Set("Cache-Control", edge.CacheControl(r))
	if wantsHTML(r.Header.Get("Accept")) {
		cw.Header().Set("Content-Type", "text/html; charset=utf-8")
		cw.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = cw.Write([]byte(renderHTML(key, p.Path, page.Items, next, r)))
		}
		return
	}
	body := struct {
		Repository string        `json:"repository"`
		Path       string        `json:"path"`
		Items      []items.Child `json:"items"`
		NextCursor *string       `json:"nextCursor"`
	}{Repository: key, Path: p.Path, Items: page.Items}
	if next != "" {
		body.NextCursor = &next
	}
	edge.WriteJSON(cw, r, http.StatusOK, body)
}

// wantsHTML reports whether text/html is the preferred type of an Accept header:
// listed with a quality above that of every other type, or first at equal quality.
// An absent header or a wildcard prefers JSON.
func wantsHTML(accept string) bool {
	best, bestQ := "", -1.0
	for _, part := range strings.Split(accept, ",") {
		mt, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
		q := 1.0
		for _, prm := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(prm), "="); ok && strings.EqualFold(k, "q") {
				if f, ok := parseQ(v); ok {
					q = f
				}
			}
		}
		if q > 0 && q > bestQ && (mt == "text/html" || mt == "application/json") {
			best, bestQ = mt, q
		}
	}
	return best == "text/html"
}

func parseQ(v string) (float64, bool) {
	var whole, frac float64
	div := 1.0
	seenDot := false
	for _, c := range strings.TrimSpace(v) {
		switch {
		case c == '.' && !seenDot:
			seenDot = true
		case c >= '0' && c <= '9':
			if seenDot {
				div *= 10
				frac += float64(c-'0') / div
			} else {
				whole = whole*10 + float64(c-'0')
			}
		default:
			return 0, false
		}
	}
	return whole + frac, true
}

// encodeSegment percent-encodes a name for a relative link: the unreserved
// characters and the sub-delimiters stay, everything else, ":" and "/" included,
// is written as %XX of its UTF-8 bytes.
func encodeSegment(name string) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("-._~!$&'()*+,;=@", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upper[c>>4])
			b.WriteByte(upper[c&15])
		}
	}
	return b.String()
}

func renderHTML(key, path string, entries []items.Child, next string, r *http.Request) string {
	title := "/" + key + "/"
	if path != "" {
		title += path + "/"
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>Index of " + html.EscapeString(title) + "</title></head>\n")
	b.WriteString("<body><h1>Index of " + html.EscapeString(title) + "</h1>\n<ul>\n")
	if path != "" {
		b.WriteString("<li><a href=\"../\">../</a></li>\n")
	}
	for _, it := range entries {
		name, href := it.Path, "./"+encodeSegment(it.Path)
		if it.Kind == items.KindFolder {
			name += "/"
			href += "/"
		}
		b.WriteString("<li><a href=\"" + html.EscapeString(href) + "\">" + html.EscapeString(name) + "</a></li>\n")
	}
	b.WriteString("</ul>\n")
	if next != "" {
		q := r.URL.Query()
		q.Set("cursor", next)
		b.WriteString("<p><a rel=\"next\" href=\"?" + html.EscapeString(q.Encode()) + "\">next</a></p>\n")
	}
	b.WriteString("</body></html>\n")
	return b.String()
}
