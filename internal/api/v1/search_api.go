package v1

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The suggestions of the UI's search field (R-API-060 to R-API-064): one request answers the best matches of a text for each kind
// the field shows, among what the caller may read. It is no list of R-API-010; the results page reads the lists with `q`.
//
// The order of the checks is fixed so that no status depends on the text: the parameters (400), the anonymous gate (401), the
// capability of the admin kinds (403), and only then a lookup (Q-API-272).

// The kinds of the suggestions, as the parameter `kind` names them.
const (
	kindRepository = "repository"
	kindPackage    = "package"
	kindProject    = "project"
	kindUser       = "user"
	kindGroup      = "group"
)

// The bounds of a request (R-API-060, Q-API-281).
const (
	suggestMinText      = 2
	suggestMaxText      = 100
	suggestDefaultLimit = 5
	suggestMaxLimit     = 10
)

// suggestKinds lists every kind, in the order of the answer's members.
func suggestKinds() []string {
	return []string{kindRepository, kindPackage, kindProject, kindUser, kindGroup}
}

// defaultSuggestKinds are the kinds of a request without `kind`.
func defaultSuggestKinds() []string { return []string{kindRepository, kindPackage, kindProject} }

// suggestRequest is a request of the suggestions after its parameters were checked.
type suggestRequest struct {
	// text is q trimmed and lower-cased (R-API-062).
	text string
	// kinds are the kinds asked, each once.
	kinds map[string]bool
	limit int
}

// wants reports whether the request asks for a kind.
func (sr suggestRequest) wants(kind string) bool { return sr.kinds[kind] }

// suggestParamError is the 400 of a parameter, with the parameter as the pointer.
func suggestParamError(name, code, detail string) error {
	return errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter "+strconv.Quote(name)+" "+detail+".", errs.WithBadRequest(),
		errs.WithFields(errs.FieldError{Pointer: name, Code: code, Detail: detail}))
}

// parseSuggest checks the parameters of a request (R-API-060).
func parseSuggest(params gen.GetSearchSuggestionsParams) (suggestRequest, error) {
	text := strings.TrimSpace(params.Q)
	if n := utf8.RuneCountInString(text); n < suggestMinText || n > suggestMaxText {
		return suggestRequest{}, suggestParamError("q", "length", "must hold 2 to 100 characters after trimming")
	}
	sr := suggestRequest{text: strings.ToLower(text), kinds: map[string]bool{}, limit: suggestDefaultLimit}
	asked := defaultSuggestKinds()
	if params.Kind != nil {
		raw := make([]string, 0, len(*params.Kind))
		for _, k := range *params.Kind {
			raw = append(raw, string(k))
		}
		asked = splitValues(raw)
		if len(asked) == 0 {
			return suggestRequest{}, suggestParamError("kind", "empty", "names no kind")
		}
	}
	for _, k := range asked {
		if !slices.Contains(suggestKinds(), k) {
			return suggestRequest{}, suggestParamError("kind", "enum", "has the unknown value "+strconv.Quote(k))
		}
		sr.kinds[k] = true
	}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > suggestMaxLimit {
			return suggestRequest{}, suggestParamError("limit", "range", "must be 1 to 10")
		}
		sr.limit = *params.Limit
	}
	return sr, nil
}

// GetSearchSuggestions implements GET /api/v1/search/suggestions (R-API-060, R-API-061).
func (s *Server) GetSearchSuggestions(w http.ResponseWriter, r *http.Request, params gen.GetSearchSuggestionsParams) {
	ctx := r.Context()
	sr, err := parseSuggest(params)
	if err == nil {
		err = s.anonymousGate(ctx)
	}
	if err == nil && (sr.wants(kindUser) || sr.wants(kindGroup)) {
		err = s.requireIdentitiesRead(r)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, emptySuggestions(sr))
}

// requireIdentitiesRead refuses the admin kinds to a caller without identities:read, before any lookup and with the same body
// whatever the text (Q-API-272).
func (s *Server) requireIdentitiesRead(r *http.Request) error {
	held, err := s.holds(r.Context(), string(authz.IdentitiesRead))
	if err != nil {
		return err
	}
	if !held {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "Users and groups are suggested to a holder of "+string(authz.IdentitiesRead)+".")
	}
	return nil
}

// emptySuggestions is the answer with an empty member for each kind asked.
func emptySuggestions(sr suggestRequest) gen.SearchSuggestions {
	var out gen.SearchSuggestions
	if sr.wants(kindRepository) {
		out.Repositories = &gen.RepositorySuggestions{Items: []gen.RepositorySuggestion{}}
	}
	if sr.wants(kindPackage) {
		out.Packages = &gen.PackageSuggestions{Items: []gen.PackageSuggestion{}}
	}
	if sr.wants(kindProject) {
		out.Projects = &gen.ProjectSuggestions{Items: []gen.ProjectSuggestion{}}
	}
	if sr.wants(kindUser) {
		out.Users = &gen.UserSuggestions{Items: []gen.UserSuggestion{}}
	}
	if sr.wants(kindGroup) {
		out.Groups = &gen.GroupSuggestions{Items: []gen.GroupSuggestion{}}
	}
	return out
}
