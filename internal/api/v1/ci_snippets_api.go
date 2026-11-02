package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The CI snippet operation (R-API-035, R-UI-035; R-CI-027 is the broader P1 requirement), work package W3. The
// answer is text/plain (writeSnippet) and holds no secret; the format parameter is npm or nuget. A project
// administrator reads the snippet of a mapping of its own tier through the project route (Q-CI-26).

func snippetRequest(kind, mapping string, format *string) ci.SnippetRequest {
	return ci.SnippetRequest{Kind: kind, Mapping: mapping, Format: derefStr(format)}
}

// writeSnippet answers the text of a snippet. It is plain text that nothing interprets: the content type is
// text/plain with nosniff, and the text is made of the base URL, names that match the patterns of mappings,
// issuers and repositories, and the audience of the issuer, never of a request body.
func writeSnippet(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text)) //nolint:gosec // G705: text/plain with nosniff; the text is made of validated names and configuration
}

// GetCiSnippet implements GET /api/v1/ci/issuers/{issuer}/snippets/{kind}.
func (s *Server) GetCiSnippet(w http.ResponseWriter, r *http.Request, issuer, kind string, params gen.GetCiSnippetParams) {
	svc, err := s.ciMappingService()
	var text string
	if err == nil {
		var format *string
		if params.Format != nil {
			f := string(*params.Format)
			format = &f
		}
		text, err = svc.Snippet(r.Context(), issuer, snippetRequest(kind, params.Mapping, format))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeSnippet(w, text)
}
