package v1

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/qname"
)

// The suggestions of the UI's search field (R-API-060 to R-API-064): one request answers the best matches of a text for each kind
// the field shows, among what the caller may read. It is no list of R-API-010; the results page reads the lists with `q`.
//
// The order of the checks is fixed so that no status depends on the text: the parameters (400), the anonymous gate (401), the
// capability of the admin kinds (403), and only then a lookup (Q-API-272). Each kind is decided by the rule of its list, on the
// index of the request, and ranked and cut only after the filter (R-API-061):
//
//   - a repository over the repositories of the request's index, in memory: readable, or exposed to a holder of
//     repositories:read-config or to an Owner of its project, as GET /api/v1/repositories decides;
//   - a package through the read scope of GET /api/v1/packages (view.Service.Suggest);
//   - a project by the rule of GET /api/v1/projects (projectListed);
//   - a user or a group for a holder of identities:read, through the principal manager.

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
	// text is q trimmed and lower-cased, terms its parts between white space (R-API-062).
	text  string
	terms []string
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
	text = strings.ToLower(text)
	sr := suggestRequest{text: text, terms: suggestTerms(text), kinds: map[string]bool{}, limit: suggestDefaultLimit}
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
	var out gen.SearchSuggestions
	if err == nil {
		out, err = s.suggest(r, sr)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, out)
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

// suggest answers every kind the request asks for, each decided on the one index of the request.
func (s *Server) suggest(r *http.Request, sr suggestRequest) (gen.SearchSuggestions, error) {
	out := emptySuggestions(sr)
	ctx := r.Context()
	var acc *edge.Access
	if sr.wants(kindRepository) || sr.wants(kindPackage) {
		d := s.o.Repositories.Authz
		if d == nil {
			d = s.o.Packages.Decider
		}
		var err error
		if ctx, acc, err = requestHolder(ctx, d); err != nil {
			return out, err
		}
		if acc == nil {
			return out, errNoDecisionPoint()
		}
	}
	var err error
	if sr.wants(kindRepository) {
		out.Repositories, err = s.suggestRepositories(ctx, acc, sr)
	}
	if err == nil && sr.wants(kindPackage) {
		out.Packages, err = s.suggestPackages(ctx, acc, sr)
	}
	if err == nil && sr.wants(kindProject) {
		out.Projects, err = s.suggestProjects(r.WithContext(ctx), sr)
	}
	if err == nil && (sr.wants(kindUser) || sr.wants(kindGroup)) {
		err = s.suggestIdentities(ctx, sr, &out)
	}
	return out, err
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

// suggestRepositories decides the matching repositories of the request's index as GET /api/v1/repositories decides its rows
// (visibleRepositories): a holder of repositories:read-config sees what the endpoint exposes, an Owner of a project the
// repositories of its project the same way, every other caller what it may read. A repository being deleted is not in the index.
func (s *Server) suggestRepositories(ctx context.Context, acc *edge.Access, sr suggestRequest) (*gen.RepositorySuggestions, error) {
	own, err := s.configScope(ctx)
	if err != nil {
		return nil, err
	}
	visible := func(names []qname.Repo) ([]qname.Repo, error) {
		if own.global {
			return acc.Exposed(names), nil
		}
		out, err := acc.Readable(ctx, names)
		if err != nil {
			return nil, err
		}
		var owned []qname.Repo
		for _, n := range names {
			if own.sees(n.Project) {
				owned = append(owned, n)
			}
		}
		return append(out, acc.Exposed(owned)...), nil
	}
	return repositorySuggestions(sr, acc.Index().Repositories(), visible)
}

// repositorySuggestions keeps the repositories that match the text and that visible admits, ranks them and cuts them at the limit
// (R-API-061, R-API-062): visible decides every match before the cut, so the answer and `more` hold readable matches only.
func repositorySuggestions(sr suggestRequest, facts []authz.RepositoryFacts, visible func([]qname.Repo) ([]qname.Repo, error)) (*gen.RepositorySuggestions, error) {
	matches := map[qname.Repo]ranked[gen.RepositorySuggestion]{}
	names := make([]qname.Repo, 0, len(facts))
	for _, f := range facts {
		rank := repositoryRank(sr.text, sr.terms, f.Name)
		if rank < 0 {
			continue
		}
		names = append(names, f.Name)
		matches[f.Name] = ranked[gen.RepositorySuggestion]{rank: rank, order: f.Name.String(), item: gen.RepositorySuggestion{
			Project: f.Name.Project, Key: f.Name.Key, Format: f.Format, Class: gen.RepositorySuggestionClass(f.Class),
		}}
	}
	if len(names) == 0 {
		return &gen.RepositorySuggestions{Items: []gen.RepositorySuggestion{}}, nil
	}
	seen, err := visible(names)
	if err != nil {
		return nil, err
	}
	kept := make([]ranked[gen.RepositorySuggestion], 0, len(seen))
	for _, n := range seen {
		if m, ok := matches[n]; ok {
			kept = append(kept, m)
			delete(matches, n) // a name visible twice (read and owned) counts once
		}
	}
	items, more := cutRanked(kept, sr.limit)
	return &gen.RepositorySuggestions{Items: items, More: more}, nil
}

// suggestPackages decides the packages through the read scope of GET /api/v1/packages on the request's index (R-API-061). The
// topology is the replica's snapshot of the repositories; one the index does not know yet reads as none, which the propagation
// bound of R-API-052 ends.
func (s *Server) suggestPackages(ctx context.Context, acc *edge.Access, sr suggestRequest) (*gen.PackageSuggestions, error) {
	ps, err := s.packageServices()
	if err != nil {
		return nil, err
	}
	repos, err := ps.Reader.List(ctx)
	if err != nil {
		return nil, err
	}
	topo := view.NewTopology(repos)
	rs, err := ps.Scoper.ReadScopeOn(ctx, acc.Index(), acc.Subject, qname.Repo{}, topo.Names(topo.Physical()))
	if err != nil {
		return nil, err
	}
	low, high, ok := prefixBounds(sr.text)
	got, more, err := ps.View.Suggest(ctx, rs, topo, view.SuggestQuery{
		Text: sr.text, Low: low, High: high, HasHigh: ok, TSQuery: suggestTSQuery(suggestWords(sr.text)), Limit: sr.limit,
	})
	if err != nil {
		return nil, err
	}
	out := &gen.PackageSuggestions{Items: make([]gen.PackageSuggestion, 0, len(got)), More: more}
	for _, p := range got {
		out.Items = append(out.Items, gen.PackageSuggestion{Id: p.ID, Format: p.Format, Name: p.Name, Project: p.Repo.Project, Repository: p.Repo.Key})
	}
	return out, nil
}

// suggestProjects decides the projects by the rule of GET /api/v1/projects: a member sees its projects, a holder of projects:read
// every project, any caller the projects whose level admits it (R-AUTHZ-035 rule 8). One statement reads every project.
func (s *Server) suggestProjects(r *http.Request, sr suggestRequest) (*gen.ProjectSuggestions, error) {
	ctx := r.Context()
	if s.o.API.Store == nil {
		return nil, unavailable("The project store")
	}
	var rows []q.SearchProjectsRow
	if err := s.o.API.Store.Read(ctx, func(db q.DBTX) (err error) {
		rows, err = q.New(db).SearchProjects(ctx)
		return err
	}); err != nil {
		return nil, errs.Wrap(err, "search.projects")
	}
	roles, all, err := s.memberProjects(ctx, string(authz.ProjectsRead))
	if err != nil {
		return nil, err
	}
	sub := caller(r)
	listed := func(key, visibility string) bool {
		return projectListed(sub, project.Project{Key: key, Visibility: visibility}, roles, all, false)
	}
	return projectSuggestions(sr, rows, listed), nil
}

// projectSuggestions keeps the projects that match the text and that listed admits, ranks them and cuts them at the limit.
func projectSuggestions(sr suggestRequest, rows []q.SearchProjectsRow, listed func(key, visibility string) bool) *gen.ProjectSuggestions {
	kept := make([]ranked[gen.ProjectSuggestion], 0, len(rows))
	for _, p := range rows {
		if !listed(p.Key, p.Visibility) {
			continue
		}
		if rank := projectRank(sr.text, sr.terms, p.Key, p.DisplayName); rank >= 0 {
			kept = append(kept, ranked[gen.ProjectSuggestion]{rank: rank, order: p.Key, item: gen.ProjectSuggestion{Key: p.Key, DisplayName: p.DisplayName}})
		}
	}
	items, more := cutRanked(kept, sr.limit)
	return &gen.ProjectSuggestions{Items: items, More: more}
}

// suggestIdentities fills the users and the groups the request asks for; the caller holds identities:read.
func (s *Server) suggestIdentities(ctx context.Context, sr suggestRequest, out *gen.SearchSuggestions) error {
	m := s.o.Auth.Manager
	if m == nil {
		return unavailable("The identity store")
	}
	if sr.wants(kindUser) {
		users, more, err := m.SuggestUsers(ctx, sr.text, sr.terms, sr.limit)
		if err != nil {
			return err
		}
		out.Users = &gen.UserSuggestions{Items: make([]gen.UserSuggestion, 0, len(users)), More: more}
		for _, u := range users {
			out.Users.Items = append(out.Users.Items, gen.UserSuggestion{Id: uuidOf(u.ID), Username: u.Username, DisplayName: optString(u.DisplayName), Disabled: u.Disabled})
		}
	}
	if sr.wants(kindGroup) {
		groups, more, err := m.SuggestGroups(ctx, sr.text, sr.terms, sr.limit)
		if err != nil {
			return err
		}
		out.Groups = &gen.GroupSuggestions{Items: make([]gen.GroupSuggestion, 0, len(groups)), More: more}
		for _, g := range groups {
			out.Groups.Items = append(out.Groups.Items, gen.GroupSuggestion{Name: g.Name, Description: g.Description})
		}
	}
	return nil
}
