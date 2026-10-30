package httpserver

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/httpserver/content"
)

// ContentKind selects the header set of a response that carries stored content or
// a page trove renders from it (R-ARCH-007, R-STOR-012, R-STOR-017). The headers
// themselves are set by internal/httpserver/content, which the format contract
// and the conformance kit import too; this is the call that knows the request.
type ContentKind = content.Kind

const (
	// ContentFile is stored content: nosniff, the sandbox policy, the same-site
	// resource policy and Content-Disposition: attachment with the file name.
	ContentFile = content.File
	// ContentIndex is a page trove renders itself, the directory index of a
	// folder: nosniff, the same-site resource policy and the policy without
	// sandbox, and no Content-Disposition.
	ContentIndex = content.Index
	// ContentDocument is a package document trove renders or relays: nosniff, the
	// sandbox policy and the resource policy, and no Content-Disposition.
	ContentDocument = content.Document
)

// ContentWriter wraps the writer of a response whose body is package content
// before the handler writes it (R-ARCH-007: the core sets the headers, not the
// handler); see [content.Wrap]. fileName is the name for Content-Disposition
// (ContentFile).
func ContentWriter(w http.ResponseWriter, kind ContentKind, fileName string) http.ResponseWriter {
	return ContentWriterFor(w, nil, kind, fileName)
}

// ContentWriterFor is [ContentWriter] for the response to r. When a session
// cookie authenticated r, the resource policy is same-origin instead of
// same-site (Q-ARCH-86); a token or anonymous response keeps same-site.
func ContentWriterFor(w http.ResponseWriter, r *http.Request, kind ContentKind, fileName string) http.ResponseWriter {
	return content.Wrap(w, kind, fileName, r != nil && SessionAuthenticated(r))
}

// SessionAuthenticated reports whether a session cookie authenticated the
// request, which is when its response needs the same-origin resource policy.
func SessionAuthenticated(r *http.Request) bool {
	return PrincipalFrom(r.Context()).Credential.Method == MethodSession
}
