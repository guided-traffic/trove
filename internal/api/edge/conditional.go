package edge

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The conditional and range rules of RFC 9110 sections 13 and 14 for a file whose
// validator is its content address: a strong ETag, "<sha256 hex>" (R-STOR-012).
// Every replica evaluates them from the item row alone, so a request cut at one
// replica resumes on any other with Range and If-Range (R-ARCH-024 step 5).

// etagMatches reports whether a header value (If-Match, If-None-Match, If-Range)
// names the entity tag. "*" matches. strong compares byte for byte and refuses a
// weak tag; weak ignores the W/ prefix.
func etagMatches(header, etag string, strong bool) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		if strong && strings.HasPrefix(tag, "W/") {
			continue
		}
		if strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

// precondition is the outcome of the conditional headers.
type precondition int

const (
	proceed precondition = iota
	notModified
	preconditionFailed
)

// evaluate applies If-Match, If-Unmodified-Since, If-None-Match and
// If-Modified-Since in the order of RFC 9110 section 13.2.2. modified has second
// precision, like the HTTP date.
func evaluate(r *http.Request, etag string, modified time.Time) precondition {
	h := r.Header
	if v := h.Get("If-Match"); v != "" {
		if !etagMatches(v, etag, true) {
			return preconditionFailed
		}
	} else if t, ok := parseDate(h.Get("If-Unmodified-Since")); ok && modified.After(t) {
		return preconditionFailed
	}
	if v := h.Get("If-None-Match"); v != "" {
		if etagMatches(v, etag, false) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				return notModified
			}
			return preconditionFailed
		}
	} else if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if t, ok := parseDate(h.Get("If-Modified-Since")); ok && !modified.After(t) {
			return notModified
		}
	}
	return proceed
}

func parseDate(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := http.ParseTime(v)
	return t, err == nil
}

// rangeResult is what the Range header asks for.
type rangeResult int

const (
	// fullContent: no Range, one that is ignored (several ranges, an unknown unit,
	// invalid syntax, an If-Range that does not match), or HEAD.
	fullContent rangeResult = iota
	// partialContent: one satisfiable range.
	partialContent
	// unsatisfiable: a range that starts beyond the end (416).
	unsatisfiable
)

// byteRange evaluates Range and If-Range for a file of size bytes: a single byte
// range is served with 206 (R-STOR-012); several are answered with the whole
// content, which RFC 9110 permits; If-Range with the ETag or the date (a date
// must equal the modification time exactly) lets the range apply, anything else
// answers the whole content.
func byteRange(r *http.Request, etag string, modified time.Time, size int64) (start, length int64, res rangeResult) {
	spec := r.Header.Get("Range")
	if spec == "" || r.Method != http.MethodGet {
		return 0, 0, fullContent
	}
	if ir := strings.TrimSpace(r.Header.Get("If-Range")); ir != "" {
		if strings.HasPrefix(ir, `"`) || strings.HasPrefix(ir, "W/") {
			if !etagMatches(ir, etag, true) {
				return 0, 0, fullContent
			}
		} else if t, ok := parseDate(ir); !ok || !t.Equal(modified) {
			return 0, 0, fullContent
		}
	}
	unit, ranges, ok := strings.Cut(spec, "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(unit), "bytes") || strings.Contains(ranges, ",") {
		return 0, 0, fullContent
	}
	first, last, ok := strings.Cut(strings.TrimSpace(ranges), "-")
	if !ok {
		return 0, 0, fullContent
	}
	if first == "" { // suffix range: the last n bytes
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, fullContent
		}
		if n == 0 || size == 0 {
			return 0, 0, unsatisfiable
		}
		n = min(n, size)
		return size - n, n, partialContent
	}
	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil || from < 0 {
		return 0, 0, fullContent
	}
	to := size - 1
	if last != "" {
		t, err := strconv.ParseInt(last, 10, 64)
		if err != nil || t < from {
			return 0, 0, fullContent
		}
		to = min(t, size-1)
	}
	if from >= size {
		return 0, 0, unsatisfiable
	}
	return from, to - from + 1, partialContent
}
