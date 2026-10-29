package v1

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// Optimistic concurrency (R-API-012). A mutable configuration resource carries
// an integer revision that grows with every change (the owner names the column);
// GET answers it as a strong ETag, PUT and PATCH require If-Match, DELETE honours
// it, and GET answers 304 for a matching If-None-Match. The tag is the revision
// in quotes, "4"; where the representation depends on the caller's view (for
// example repository configuration with or without repositories:read-config)
// it is "<revision>-<view>".

// ETag renders the strong entity tag of a revision. view is empty for a
// resource whose representation does not depend on the caller.
func ETag(revision int64, view string) string {
	if view == "" {
		return `"` + strconv.FormatInt(revision, 10) + `"`
	}
	return `"` + strconv.FormatInt(revision, 10) + "-" + view + `"`
}

// SetETag sets the ETag response header.
func SetETag(w http.ResponseWriter, revision int64, view string) {
	w.Header().Set("ETag", ETag(revision, view))
}

// Precondition is the If-Match header of a write, parsed.
type Precondition struct {
	// Present is true when the request carried If-Match.
	Present bool
	// Any is If-Match: *, which passes when the resource exists.
	Any bool
	// Tag is the entity tag, with its quotes, when Present and not Any.
	Tag string
}

// ParseIfMatch reads If-Match. A list of tags and a weak tag are refused with
// 400 invalid_header: the revision is compared for strong equality, and a list
// has no single revision to put in the UPDATE.
func ParseIfMatch(r *http.Request) (Precondition, error) {
	vals := r.Header.Values("If-Match")
	if len(vals) == 0 {
		return Precondition{}, nil
	}
	v := strings.TrimSpace(strings.Join(vals, ","))
	switch {
	case v == "*":
		return Precondition{Present: true, Any: true}, nil
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && !strings.ContainsAny(v[1:len(v)-1], `",`):
		return Precondition{Present: true, Tag: v}, nil
	}
	return Precondition{}, errs.E(errs.Invalid, errs.CodeInvalidHeader, "If-Match must be one strong entity tag, such as \"4\", or *.")
}

// Require is the rule of R-API-012 for PUT and PATCH: without If-Match the
// answer is 428 precondition_required, whose detail names the header. A client
// that deliberately overwrites sends If-Match: *.
func (p Precondition) Require() error {
	if p.Present {
		return nil
	}
	return errs.E(errs.PreconditionRequired, errs.CodePreconditionRequired,
		"This operation requires the If-Match header: send the ETag you loaded, or * to overwrite deliberately.")
}

// Revision returns the revision to put in the UPDATE of the data model of
// R-API-012 (revision = $2, with NULL meaning no check) for a resource whose
// tags carry view: ok is false for If-Match: * or no header, and then the
// update has no revision condition. A tag that is not of the form
// "<revision>" or "<revision>-<view>" for this view cannot match anything the
// server issued; it returns the revision 0, which no resource has, so the
// UPDATE finds no row and the caller answers 412.
func (p Precondition) Revision(view string) (rev int64, ok bool) {
	if !p.Present || p.Any {
		return 0, false
	}
	inner := p.Tag[1 : len(p.Tag)-1]
	num, rest, hasView := strings.Cut(inner, "-")
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 1 || hasView != (view != "") || (hasView && rest != view) {
		return 0, true
	}
	return n, true
}

// Matches reports whether the precondition holds for a resource at revision in
// view; use it where the revision is compared in memory.
func (p Precondition) Matches(revision int64, view string) bool {
	if !p.Present || p.Any {
		return true
	}
	return p.Tag == ETag(revision, view)
}

// expectedRevision reads If-Match into the service's expected revision:
// errs.AnyRevision for no header or *, the revision otherwise (0 for a tag the
// server never issued, which matches nothing).
func expectedRevision(p Precondition) int64 {
	if rev, ok := p.Revision(""); ok {
		return rev
	}
	return errs.AnyRevision
}

// Failed is the 412 of a mismatching If-Match for a handler that compares before
// it calls the domain (R-API-012): the one error of [errs.Stale], with the
// extension currentEtag. The domain packages return the same error themselves.
func Failed(currentRevision int64) error { return errs.Stale("The resource", currentRevision) }

// NotModified answers a GET whose If-None-Match matches etag with 304 and
// reports true; the handler then returns. The ETag header is set on the
// answer. If-None-Match: * matches every existing resource. Weak comparison
// applies, as RFC 9110 section 13.1.2 says for If-None-Match.
func NotModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	vals := r.Header.Values("If-None-Match")
	if len(vals) == 0 {
		return false
	}
	match := false
	for _, v := range vals {
		for _, tag := range strings.Split(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				match = true
			}
		}
	}
	if !match {
		return false
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotModified)
	return true
}
