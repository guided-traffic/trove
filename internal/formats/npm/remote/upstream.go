package remote

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// Accept is the Accept header of a document request: the full document, the form
// that carries time and _npmUser (R-NPM-024). The abbreviated form is derived from it.
const Accept = "application/json"

// URL returns the upstream URL of a canonical repository path below base. A package
// document is at <upstream>/<name with %2f> (R-NPM-024); a tarball is at the canonical
// path itself, <upstream>/<scope>/<name>/-/<file>, the form the documents of the public
// registry name (R-REPO-043: the upstream base plus the canonical path). Every segment
// is encoded as one path segment, so a segment that decodes to a dot never reaches the
// wire; a path with such a segment, an empty one or a NUL is refused.
func URL(base *url.URL, canonical string, document bool) (*url.URL, error) {
	segs := strings.Split(canonical, "/")
	if document && strings.HasPrefix(canonical, "@") && len(segs) >= 2 {
		segs = append([]string{segs[0] + "/" + segs[1]}, segs[2:]...)
	}
	u := *base
	u.User, u.Fragment, u.RawQuery = nil, "", ""
	path := strings.TrimSuffix(base.Path, "/")
	raw := strings.TrimSuffix(base.EscapedPath(), "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsRune(s, 0) {
			return nil, errs.E(errs.Invalid, errs.CodeInvalidPath, "The path has a segment that cannot be sent to an upstream.")
		}
		path += "/" + s
		raw += "/" + url.PathEscape(s)
	}
	u.Path, u.RawPath = path, raw
	return &u, nil
}

// Request builds the upstream request of a path. The engine adds the validators of
// the stored copy and the credential.
func Request(ctx context.Context, base *url.URL, canonical string, document bool) (*http.Request, error) {
	u, err := URL(base, canonical, document)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if document {
		req.Header.Set("Accept", Accept)
		req.Header.Set("Accept-Encoding", "gzip")
	}
	return req, nil
}
