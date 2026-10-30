package edge

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const etag = `"5be0c1d2e8f3a4b5c6d7e8f90123456789abcdef0123456789abcdef01234567"`

var modified = time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC)

func request(method string, hdr ...string) *http.Request {
	r := httptest.NewRequest(method, "/files/r/a.bin", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

func TestByteRange(t *testing.T) {
	const size = 1000
	for _, tc := range []struct {
		name          string
		hdr           []string
		method        string
		start, length int64
		want          rangeResult
	}{
		{"R-STOR-012 the first 100 bytes", []string{"Range", "bytes=0-99"}, "GET", 0, 100, partialContent},
		{"R-STOR-012 an open range", []string{"Range", "bytes=900-"}, "GET", 900, 100, partialContent},
		{"R-STOR-012 a suffix range", []string{"Range", "bytes=-100"}, "GET", 900, 100, partialContent},
		{"R-STOR-012 a suffix longer than the file is the file", []string{"Range", "bytes=-5000"}, "GET", 0, 1000, partialContent},
		{"R-STOR-012 an end beyond the file is cut to it", []string{"Range", "bytes=990-5000"}, "GET", 990, 10, partialContent},
		{"R-STOR-012 a start beyond the file is unsatisfiable", []string{"Range", "bytes=5000000000-"}, "GET", 0, 0, unsatisfiable},
		{"R-STOR-012 a suffix of zero is unsatisfiable", []string{"Range", "bytes=-0"}, "GET", 0, 0, unsatisfiable},
		{"R-STOR-012 several ranges are answered with the whole content", []string{"Range", "bytes=0-9,20-29"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 an unknown unit is ignored", []string{"Range", "items=0-9"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 a malformed range is ignored", []string{"Range", "bytes=9-0"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 HEAD ignores Range", []string{"Range", "bytes=0-9"}, "HEAD", 0, 0, fullContent},
		{"R-STOR-012 If-Range with the ETag lets the range apply", []string{"Range", "bytes=100-", "If-Range", etag}, "GET", 100, 900, partialContent},
		{"R-STOR-012 If-Range with another ETag answers the whole content", []string{"Range", "bytes=100-", "If-Range", `"other"`}, "GET", 0, 0, fullContent},
		{"R-STOR-012 If-Range needs a strong tag", []string{"Range", "bytes=100-", "If-Range", "W/" + etag}, "GET", 0, 0, fullContent},
		{"R-STOR-012 If-Range with the date of the file lets the range apply", []string{"Range", "bytes=0-9", "If-Range", modified.Format(http.TimeFormat)}, "GET", 0, 10, partialContent},
		{"R-STOR-012 If-Range with another date answers the whole content", []string{"Range", "bytes=0-9", "If-Range", modified.Add(time.Hour).Format(http.TimeFormat)}, "GET", 0, 0, fullContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, length, got := byteRange(request(tc.method, tc.hdr...), etag, modified, size)
			if got != tc.want || (got == partialContent && (start != tc.start || length != tc.length)) {
				t.Fatalf("byteRange = %d, %d, %d; want %d, %d, %d", start, length, got, tc.start, tc.length, tc.want)
			}
		})
	}
	t.Run("R-STOR-012 any range of an empty file is unsatisfiable", func(t *testing.T) {
		if _, _, got := byteRange(request("GET", "Range", "bytes=0-0"), etag, modified, 0); got != unsatisfiable {
			t.Fatalf("got %d", got)
		}
	})
}

func TestEvaluate(t *testing.T) {
	later := modified.Add(time.Hour).Format(http.TimeFormat)
	earlier := modified.Add(-time.Hour).Format(http.TimeFormat)
	for _, tc := range []struct {
		name   string
		method string
		hdr    []string
		want   precondition
	}{
		{"R-STOR-012 no condition proceeds", "GET", nil, proceed},
		{"R-STOR-012 If-None-Match with the ETag is 304", "GET", []string{"If-None-Match", etag}, notModified},
		{"R-STOR-012 If-None-Match compares weakly", "GET", []string{"If-None-Match", "W/" + etag}, notModified},
		{"R-STOR-012 If-None-Match with a list", "GET", []string{"If-None-Match", `"x", ` + etag}, notModified},
		{"R-STOR-012 If-None-Match with another ETag proceeds", "GET", []string{"If-None-Match", `"x"`}, proceed},
		{"R-STOR-012 If-None-Match star is 304 for an existing file", "HEAD", []string{"If-None-Match", "*"}, notModified},
		{"R-API-012 If-None-Match star on a PUT fails the precondition", "PUT", []string{"If-None-Match", "*"}, preconditionFailed},
		{"R-STOR-012 If-Modified-Since at the modification time is 304", "GET", []string{"If-Modified-Since", modified.Format(http.TimeFormat)}, notModified},
		{"R-STOR-012 If-Modified-Since before it proceeds", "GET", []string{"If-Modified-Since", earlier}, proceed},
		{"R-STOR-012 If-None-Match wins over If-Modified-Since", "GET", []string{"If-None-Match", `"x"`, "If-Modified-Since", later}, proceed},
		{"RFC 9110 If-Match with another ETag is 412", "GET", []string{"If-Match", `"x"`}, preconditionFailed},
		{"RFC 9110 If-Match with the ETag proceeds", "GET", []string{"If-Match", etag}, proceed},
		{"RFC 9110 If-Match refuses a weak tag", "GET", []string{"If-Match", "W/" + etag}, preconditionFailed},
		{"RFC 9110 If-Unmodified-Since before the modification is 412", "GET", []string{"If-Unmodified-Since", earlier}, preconditionFailed},
		{"RFC 9110 If-Unmodified-Since after it proceeds", "GET", []string{"If-Unmodified-Since", later}, proceed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluate(request(tc.method, tc.hdr...), etag, modified); got != tc.want {
				t.Fatalf("evaluate = %d, want %d", got, tc.want)
			}
		})
	}
}
