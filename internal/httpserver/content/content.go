// Package content wraps the writer of a response whose body is package content,
// or a page or document trove renders from it, so that the headers of R-ARCH-007
// hold whatever the handler does (R-ARCH-007: the core sets the headers, not the
// handler).
//
// It imports the standard library only. It sits below the format contract and
// the HTTP shell alike: internal/httpserver offers the request-aware call, the
// format contract (internal/formats) runs every route of kind Download, Metadata
// and Computed through it, and the conformance kit asserts its result.
package content

import (
	"net/http"
	"strings"
)

// Kind selects the header set of a response (R-ARCH-007, R-STOR-012, R-STOR-017).
type Kind int

const (
	// File is stored content: nosniff, the sandbox policy, the resource policy
	// and Content-Disposition: attachment with the file name.
	File Kind = iota
	// Index is a page trove renders itself, the directory index of a folder:
	// nosniff, the resource policy and the policy without sandbox, and no
	// Content-Disposition.
	Index
	// Document is a document derived from package content or metadata that
	// trove renders or relays (a package document, a service index): nosniff,
	// the sandbox policy and the resource policy, and no Content-Disposition,
	// because a client reads it as a response and does not save it as a file.
	Document
)

// The values of the content headers (R-ARCH-007).
const (
	cspSandbox = "sandbox; default-src 'none'"
	cspIndex   = "default-src 'none'"

	// SameSite and SameOrigin are the values of Cross-Origin-Resource-Policy.
	SameSite   = "same-site"
	SameOrigin = "same-origin"
)

// Wrap wraps w before the handler writes the body. The wrapper sets the headers
// of kind now and applies them again at WriteHeader and at the first Write, so a
// handler can set Content-Type but cannot remove or weaken the others, whatever
// it does to the header map in between. fileName is the name for
// Content-Disposition (File); it is percent-encoded as RFC 8187 asks.
//
// sameOrigin selects the stricter resource policy: it is true when a session
// cookie authenticated the request (Q-ARCH-86). SameSite=Lax sends the cookie
// with a subresource request from any page of the same site, so a sibling host
// could otherwise embed a file with its visitor's rights. The policy does not
// apply to navigations, which the UI's downloads are, nor to the UI's own image
// loads (api-and-ui.md R-UI-001); a token or anonymous response keeps same-site.
func Wrap(w http.ResponseWriter, kind Kind, fileName string, sameOrigin bool) http.ResponseWriter {
	cw := &writer{ResponseWriter: w, kind: kind, name: fileName, corp: SameSite}
	if sameOrigin {
		cw.corp = SameOrigin
	}
	cw.enforce()
	return cw
}

// Headers returns the header set of a kind for a file name, as Wrap sets it. The
// conformance kit and the tests compare a response with it.
func Headers(kind Kind, fileName string, sameOrigin bool) http.Header {
	h := http.Header{}
	rec := &headerOnly{h: h}
	Wrap(rec, kind, fileName, sameOrigin)
	return h
}

type headerOnly struct{ h http.Header }

func (r *headerOnly) Header() http.Header         { return r.h }
func (r *headerOnly) Write(p []byte) (int, error) { return len(p), nil }
func (r *headerOnly) WriteHeader(int)             {}

type writer struct {
	http.ResponseWriter
	kind  Kind
	name  string
	corp  string
	wrote bool
}

func (w *writer) enforce() {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", w.corp)
	switch w.kind {
	case File:
		h.Set("Content-Security-Policy", cspSandbox)
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+EncodeFileName(w.name))
	case Document:
		h.Set("Content-Security-Policy", cspSandbox)
		h.Del("Content-Disposition")
	default:
		h.Set("Content-Security-Policy", cspIndex)
		h.Del("Content-Disposition")
	}
}

// WriteHeader checks the headers again; a 1xx status is not the response.
func (w *writer) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.enforce()
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write checks the headers again at the first byte, which starts a 200.
func (w *writer) Write(p []byte) (int, error) {
	if !w.wrote {
		w.enforce()
		w.wrote = true
	}
	return w.ResponseWriter.Write(p)
}

// Flush implements http.Flusher.
func (w *writer) Flush() {
	if !w.wrote {
		w.enforce()
		w.wrote = true
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// EncodeFileName percent-encodes a file name for the filename* parameter of RFC
// 8187: everything but the attr-char set is written as %XX of its UTF-8 bytes.
func EncodeFileName(name string) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upper[c>>4])
			b.WriteByte(upper[c&15])
		}
	}
	return b.String()
}
