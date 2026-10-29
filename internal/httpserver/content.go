package httpserver

import (
	"net/http"
	"strings"
)

// ContentKind selects the header set of a response that carries stored content or
// a page trove renders from it (R-ARCH-007, R-STOR-012, R-STOR-017).
type ContentKind int

const (
	// ContentFile is stored content: nosniff, the sandbox policy, the same-site
	// resource policy and Content-Disposition: attachment with the file name.
	ContentFile ContentKind = iota
	// ContentIndex is a page trove renders itself, the directory index of a
	// folder: nosniff, the same-site resource policy and the policy without
	// sandbox, and no Content-Disposition.
	ContentIndex
)

// The values of the content headers (R-ARCH-007).
const (
	contentCSPFile  = "sandbox; default-src 'none'"
	contentCSPIndex = "default-src 'none'"
)

// ContentWriter wraps the writer of a response whose body is package content
// before the handler writes it (R-ARCH-007: the core sets the headers, not the
// handler). The wrapper sets the headers of kind now and applies them again at
// WriteHeader and at the first Write, so a handler can set Content-Type but
// cannot remove or weaken the others, whatever it does to the header map in
// between. fileName is the name for Content-Disposition (ContentFile); it is
// percent-encoded as RFC 8187 asks.
func ContentWriter(w http.ResponseWriter, kind ContentKind, fileName string) http.ResponseWriter {
	return ContentWriterFor(w, nil, kind, fileName)
}

// ContentWriterFor is [ContentWriter] for the response to r. When a session
// cookie authenticated r, the resource policy is same-origin instead of
// same-site (Q-ARCH-86): SameSite=Lax sends the cookie with a subresource
// request from any page of the same site, so a sibling host could otherwise
// embed a file with its visitor's rights. The policy does not apply to
// navigations, which the UI's downloads are, nor to the UI's own image loads
// (api-and-ui.md R-UI-001); a token or anonymous response keeps same-site.
func ContentWriterFor(w http.ResponseWriter, r *http.Request, kind ContentKind, fileName string) http.ResponseWriter {
	cw := &contentWriter{ResponseWriter: w, kind: kind, name: fileName, corp: "same-site"}
	if r != nil && PrincipalFrom(r.Context()).Credential.Method == MethodSession {
		cw.corp = "same-origin"
	}
	cw.enforce()
	return cw
}

type contentWriter struct {
	http.ResponseWriter
	kind  ContentKind
	name  string
	corp  string
	wrote bool
}

func (w *contentWriter) enforce() {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", w.corp)
	switch w.kind {
	case ContentFile:
		h.Set("Content-Security-Policy", contentCSPFile)
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+encodeFileName(w.name))
	default:
		h.Set("Content-Security-Policy", contentCSPIndex)
		h.Del("Content-Disposition")
	}
}

// WriteHeader checks the headers again; a 1xx status is not the response.
func (w *contentWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.enforce()
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write checks the headers again at the first byte, which starts a 200.
func (w *contentWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.enforce()
		w.wrote = true
	}
	return w.ResponseWriter.Write(p)
}

// Flush implements http.Flusher.
func (w *contentWriter) Flush() {
	if !w.wrote {
		w.enforce()
		w.wrote = true
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *contentWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// encodeFileName percent-encodes a file name for the filename* parameter of RFC
// 8187: everything but the attr-char set is written as %XX of its UTF-8 bytes.
func encodeFileName(name string) string {
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
