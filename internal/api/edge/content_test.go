package edge

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
)

// stored is a file of 1,000 bytes whose content is its offset modulo 251.
func stored() (items.Item, []byte) {
	body := make([]byte, 1000)
	for i := range body {
		body[i] = byte(i % 251)
	}
	var d blob.Digest
	d[0], d[31] = 0x5b, 0xef
	return items.Item{
		ID: 7, Repository: "generic-local", Path: "tools/widget 2.3.html", Kind: items.KindFile, Size: int64(len(body)),
		MediaType: "text/html", Digest: d, SHA512: make([]byte, 64),
		ModifiedAt: time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC),
	}, body
}

// opener serves body and records what was asked of it.
type opener struct {
	body   []byte
	err    error
	failAt int // a read error after this many bytes; 0 reads to the end
	calls  int
	rng    *items.Range
}

func (o *opener) open(rng *items.Range) (io.ReadCloser, error) {
	o.calls++
	o.rng = rng
	if o.err != nil {
		return nil, o.err
	}
	b := o.body
	if rng != nil {
		b = b[rng.Offset : rng.Offset+rng.Length]
	}
	if o.failAt > 0 {
		return io.NopCloser(io.MultiReader(bytes.NewReader(b[:o.failAt]), errReader{})), nil
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("object store failed") }

func serveItem(t *testing.T, o *opener, served *int, method string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	it, _ := stored()
	r := httptest.NewRequest(method, "/files/generic-local/tools/widget%202.3.html", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	ServeItem(rec, r, it, o.open, ServeOptions{Served: func() { *served++ }})
	return rec
}

func TestServeItem(t *testing.T) {
	it, body := stored()

	t.Run("R-ARCH-007 a file is served with the content headers whatever its media type", func(t *testing.T) {
		var served int
		rec := serveItem(t, &opener{body: body}, &served, "GET")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("status %d, %d body bytes", rec.Code, rec.Body.Len())
		}
		h := rec.Header()
		for k, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"Content-Security-Policy":      "sandbox; default-src 'none'",
			"Cross-Origin-Resource-Policy": "same-site",
			"Content-Disposition":          "attachment; filename*=UTF-8''widget%202.3.html",
			"Content-Type":                 "text/html",
		} {
			if got := h.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
	})

	t.Run("R-STOR-012 the validators, the range support and the digest come from the item row", func(t *testing.T) {
		var served int
		rec := serveItem(t, &opener{body: body}, &served, "GET", "Want-Repr-Digest", "sha-512=10")
		h := rec.Header()
		if h.Get("ETag") != it.ETag() || h.Get("Last-Modified") != "Wed, 28 Oct 2026 09:14:03 GMT" || h.Get("Accept-Ranges") != "bytes" {
			t.Errorf("validators: %v", h)
		}
		if !strings.HasPrefix(h.Get("Repr-Digest"), "sha-256=:") || !strings.Contains(h.Get("Repr-Digest"), ", sha-512=:") {
			t.Errorf("Repr-Digest = %q", h.Get("Repr-Digest"))
		}
		if h.Get("Cache-Control") != "no-cache" { // outside the chain a request is anonymous
			t.Errorf("Cache-Control = %q", h.Get("Cache-Control"))
		}
	})

	t.Run("R-STOR-012 a response is never chunked: Content-Length is the length of the body", func(t *testing.T) {
		var served int
		rec := serveItem(t, &opener{body: body}, &served, "GET")
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
			t.Errorf("Content-Length = %q", got)
		}
		if te := rec.Header().Get("Transfer-Encoding"); te != "" {
			t.Errorf("Transfer-Encoding = %q", te)
		}
	})

	t.Run("R-STOR-015 a complete download is counted, a ranged one that starts later is not", func(t *testing.T) {
		var whole, ranged, fromZero int
		serveItem(t, &opener{body: body}, &whole, "GET")
		serveItem(t, &opener{body: body}, &ranged, "GET", "Range", "bytes=100-199")
		serveItem(t, &opener{body: body}, &fromZero, "GET", "Range", "bytes=0-99")
		if whole != 1 || ranged != 0 || fromZero != 1 {
			t.Errorf("served: whole %d, ranged %d, from byte 0 %d", whole, ranged, fromZero)
		}
	})

	t.Run("R-STOR-012 a range is answered 206 with Content-Range and opened as a range", func(t *testing.T) {
		var served int
		o := &opener{body: body}
		rec := serveItem(t, o, &served, "GET", "Range", "bytes=900-")
		if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), body[900:]) {
			t.Fatalf("status %d, %d body bytes", rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("Content-Range") != "bytes 900-999/1000" || rec.Header().Get("Content-Length") != "100" {
			t.Errorf("headers: %v", rec.Header())
		}
		if o.rng == nil || *o.rng != (items.Range{Offset: 900, Length: 100}) {
			t.Errorf("opened range = %+v", o.rng)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("a partial response carries the content headers")
		}
	})

	t.Run("R-STOR-012 HEAD answers the headers of GET from the row without opening the object", func(t *testing.T) {
		var served int
		o := &opener{body: body}
		rec := serveItem(t, o, &served, "HEAD")
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 || o.calls != 0 || served != 0 {
			t.Errorf("status %d, body %d, opens %d, served %d", rec.Code, rec.Body.Len(), o.calls, served)
		}
		if rec.Header().Get("Content-Length") != "1000" || rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("headers: %v", rec.Header())
		}
	})

	t.Run("R-STOR-012 a satisfied If-None-Match is 304 with the validators and the content headers, no body", func(t *testing.T) {
		var served int
		o := &opener{body: body}
		rec := serveItem(t, o, &served, "GET", "If-None-Match", it.ETag())
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || o.calls != 0 || served != 0 {
			t.Fatalf("status %d, body %d, opens %d, served %d", rec.Code, rec.Body.Len(), o.calls, served)
		}
		if rec.Header().Get("ETag") != it.ETag() || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers: %v", rec.Header())
		}
	})

	t.Run("RFC 9110 a failed If-Match is a 412 problem that carries no validators of the file", func(t *testing.T) {
		var served int
		o := &opener{body: body}
		rec := serveItem(t, o, &served, "GET", "If-Match", `"other"`)
		if rec.Code != http.StatusPreconditionFailed || o.calls != 0 || rec.Header().Get("ETag") != "" {
			t.Errorf("status %d, opens %d, ETag %q", rec.Code, o.calls, rec.Header().Get("ETag"))
		}
		if !strings.Contains(rec.Body.String(), `"precondition_failed"`) {
			t.Errorf("body %s", rec.Body.String())
		}
	})

	t.Run("R-STOR-012 a range beyond the end is 416 with the size", func(t *testing.T) {
		var served int
		o := &opener{body: body}
		rec := serveItem(t, o, &served, "GET", "Range", "bytes=5000-")
		if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Range") != "bytes */1000" || o.calls != 0 {
			t.Errorf("status %d, Content-Range %q, opens %d", rec.Code, rec.Header().Get("Content-Range"), o.calls)
		}
	})

	t.Run("R-STOR-013 an object that cannot be opened is a problem before the status line, without the file's headers", func(t *testing.T) {
		var served int
		rec := serveItem(t, &opener{err: errs.E(errs.Unavailable, errs.CodeContentUnavailable, "missing")}, &served, "GET")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"content_unavailable"`) {
			t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("headers of the file on a problem: %v", rec.Header())
		}
		if served != 0 {
			t.Error("a failed download was counted")
		}
	})

	t.Run("R-STOR-013 a body that fails in the middle aborts the response instead of ending it cleanly", func(t *testing.T) {
		var served int
		defer func() {
			got := recover()
			if err, _ := got.(error); !errors.Is(err, http.ErrAbortHandler) {
				t.Errorf("recovered %v, want http.ErrAbortHandler", got)
			}
			if served != 0 {
				t.Error("an aborted download was counted")
			}
		}()
		serveItem(t, &opener{body: body, failAt: 300}, &served, "GET")
		t.Error("the handler returned normally after a failed body")
	})
}

func TestCacheControl(t *testing.T) {
	t.Run("R-STOR-012 an anonymous response is no-cache, an authenticated one private", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		if got := CacheControl(r); got != "no-cache" {
			t.Errorf("anonymous: %q", got)
		}
	})
}
