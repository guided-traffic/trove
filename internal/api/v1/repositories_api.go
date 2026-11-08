package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// The repository operations of R-REPO-003 and R-REPO-009 to R-REPO-013. The
// handlers are thin: the domain service validates, writes and records
// (internal/repository); this file turns the HTTP conventions (strict bodies,
// ETag and If-Match, merge patch, idempotency keys, cursors) into its inputs and
// its results into the representation. The write operations declare
// repositories:write-config; the reads decide in the handler what the caller may
// see, because that depends on the repository (R-AUTHZ-016).

// RepositoryServices are what the repository operations need beyond the common
// services.
type RepositoryServices struct {
	// Service is the write side of the repository model; nil disables the
	// operations with 503.
	Service *repository.Service
	// Authz is the decision point for the reads that depend on what the caller may
	// read: the repository read and the list. It decides per request; nil answers 503.
	Authz authz.Decider
	// Reader is the replica's snapshot of the repositories. The read of a
	// repository looks an unknown key up before it decides, so that a repository
	// created through another replica is usable at once (R-ARCH-011 step 2).
	Reader repository.Reader
	// AnonymousAccess is the runtime switch of R-AUTH-037. An anonymous caller is
	// answered 401 while it is off, and 503 when the switch cannot be read. Nil
	// means no gate here (a unit test without a decision point).
	AnonymousAccess func(ctx context.Context) (bool, error)
}

// capabilityReadConfig is the capability of reading configuration and
// capabilityWriteConfig that of writing it (R-AUTHZ-010). An Owner of a project administers the
// repositories of its project without them (Q-AUTHZ-208).
const (
	capabilityReadConfig  = string(authz.RepositoriesRead)
	capabilityWriteConfig = string(authz.RepositoriesWrite)
)

// consumerView is the view part of the ETag of a read by a caller without
// repositories:read-config (R-API-012): the representation depends on the view.
const consumerView = "consumer"

func (s *Server) repositoryService() (*repository.Service, error) {
	if s.o.Repositories.Service == nil {
		return nil, unavailable("The repository service")
	}
	return s.o.Repositories.Service, nil
}

// anonymousGate answers 401 for an anonymous caller while anonymous access is off.
func (s *Server) anonymousGate(ctx context.Context) error {
	gate := s.o.Repositories.AnonymousAccess
	if !httpserver.PrincipalFrom(ctx).Anonymous || gate == nil {
		return nil
	}
	on, err := gate(ctx)
	if err != nil {
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The authentication settings could not be read.", errs.WithRetryAfter(time.Second), errs.WithCause(err))
	}
	if !on {
		return authz.Unauthenticated()
	}
	return nil
}

// repositoryCaller is the actor and the origin of a write, for the audit event.
func repositoryCaller(r *http.Request) repository.Caller {
	return repository.Caller{Actor: ActorFrom(httpserver.PrincipalFrom(r.Context())), Source: auditSource(r), Endpoint: SubjectFrom(r.Context()).Endpoint}
}

// writeCaller is the caller of a write of a repository document (create, replace, patch). The declaration
// of these operations admits a holder of repositories:write-config and an Owner of the project; a caller that
// does not hold the capability is allowed by the role owner alone and is Restricted (R-AUTHZ-026: the upstream
// rules of Q-REPO-127, Q-REPO-128, Q-REPO-142 and Q-REPO-193). It may weaken the member vulnerabilities only
// with vulnerabilities:triage (Q-VULN-69). The questions are asked without an audit event.
func (s *Server) writeCaller(r *http.Request) (repository.Caller, error) {
	c := repositoryCaller(r)
	ctx := r.Context()
	cfg, err := s.holds(ctx, capabilityWriteConfig)
	if err != nil {
		return repository.Caller{}, err
	}
	c.Restricted = !cfg
	if c.MayWeakenVulnerabilities, err = s.holds(ctx, string(authz.VulnerabilitiesTriage)); err != nil {
		return repository.Caller{}, err
	}
	return c, nil
}

// repositoryRep is the representation of a repository: the configuration document
// and the members the server adds. The request type is the response type, so a
// body obtained by GET can be sent back; the read-only members are ignored on the
// way in (readOnlyMembers).
type repositoryRep struct {
	repository.Document
	// Project is the key of the project the repository belongs to (Q-AUTHZ-206). It is read-only: the path
	// names the project, and a body that names another answers 422.
	Project        *string               `json:"project"`
	Lifecycle      string                `json:"lifecycle"`
	CreatedAt      time.Time             `json:"createdAt"`
	UpdatedAt      time.Time             `json:"updatedAt"`
	LastActivityAt *time.Time            `json:"lastActivityAt"`
	Warnings       *[]repository.Warning `json:"warnings,omitempty"`
}

// readOnlyMembers are the members of the representation that the server adds.
func readOnlyMembers() []string {
	return []string{"lifecycle", "createdAt", "updatedAt", "lastActivityAt", "warnings"}
}

// toRep renders a stored repository. A caller without repositories:read-config
// does not see the notes (R-REPO-004). withWarnings adds the findings of a write.
func toRep(st repository.Stored, notes, withWarnings bool) repositoryRep {
	d := st.Document
	if !notes {
		d = d.WithoutAdminNotes()
	}
	rep := repositoryRep{
		Document: d, Project: optString(st.Repository.Name.Project), Lifecycle: string(st.Repository.Lifecycle),
		CreatedAt: st.Repository.CreatedAt.UTC(), UpdatedAt: st.Repository.UpdatedAt.UTC(),
	}
	if t := st.Repository.LastActivityAt; !t.IsZero() {
		u := t.UTC()
		rep.LastActivityAt = &u
	}
	if withWarnings {
		w := st.Warnings
		if w == nil {
			w = []repository.Warning{}
		}
		rep.Warnings = &w
	}
	return rep
}

// protocolBase returns the client address template of a format.
func protocolBase(format string) string {
	if f, ok := formats.Lookup(format); ok {
		return f.Descriptor().ProtocolBasePath
	}
	return ""
}

// addresses are the client addresses of a repository on the public base URL.
func addresses(base string, name qname.Repo, d repository.Document) []string {
	view := d.ConsumerView(protocolBase(d.Format), name)
	out := make([]string, len(view.Addresses))
	for i, a := range view.Addresses {
		out[i] = strings.TrimSuffix(base, "/") + a
	}
	return out
}

// summaryRep is an entry of the default list view (R-REPO-012).
type summaryRep struct {
	Key            string     `json:"key"`
	Class          string     `json:"class"`
	Format         string     `json:"format"`
	Description    string     `json:"description"`
	Addresses      []string   `json:"addresses"`
	Disabled       bool       `json:"disabled"`
	Lifecycle      string     `json:"lifecycle,omitempty"`
	LastActivityAt *time.Time `json:"lastActivityAt"`
	Project        *string    `json:"project"`
}

func toSummary(base string, st repository.Stored, showLifecycle bool) summaryRep {
	r := summaryRep{
		Key: st.Document.Key, Class: string(st.Document.Class), Format: st.Document.Format,
		Description: st.Document.Description, Addresses: addresses(base, st.Repository.Name, st.Document), Disabled: st.Repository.Disabled,
		Project: optString(st.Repository.Name.Project),
	}
	if t := st.Repository.LastActivityAt; !t.IsZero() {
		u := t.UTC()
		r.LastActivityAt = &u
	}
	if showLifecycle && st.Repository.Lifecycle != repository.Active {
		r.Lifecycle = string(st.Repository.Lifecycle)
	}
	return r
}

// consumerRep is the view of a caller who may read a repository but not its
// configuration (R-REPO-010).
func consumerRep(base string, st repository.Stored) repository.ConsumerView {
	v := st.Document.ConsumerView(protocolBase(st.Document.Format), st.Repository.Name)
	v.Addresses = addresses(base, st.Repository.Name, st.Document)
	return v
}

// stripReadOnly removes the members the server adds from a body that was obtained
// by GET, after the syntax was checked, so that an unknown member can still be
// told from a read-only one.
func stripReadOnly(body []byte) ([]byte, error) {
	if err := checkSyntax(body); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, malformed("", "type", "the body must be a JSON object", err)
	}
	for _, k := range readOnlyMembers() {
		delete(m, k)
	}
	return json.Marshal(m)
}

// takeProject removes the "project" member from a document body and returns it:
// nil when the body has none, the key for a string, "" for null. The member is not
// part of the configuration; the handler compares it with the project of the path
// ([sameProject]).
func takeProject(doc []byte) ([]byte, *string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, nil, malformed("", "type", "the body must be a JSON object", err)
	}
	raw, ok := m["project"]
	if !ok {
		return doc, nil, nil
	}
	delete(m, "project")
	var key *string
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, nil, malformed("/project", "type", "the project is a key or null", err)
	}
	project := derefString(key)
	rest, err := json.Marshal(m)
	return rest, &project, err
}

// documentBody reads the configuration document of a create or a replace
// strictly: JSON, bounded, no duplicate and no unknown member, the read-only
// members ignored. The "project" member is returned apart.
func (s *Server) documentBody(w http.ResponseWriter, r *http.Request) (raw, doc []byte, project *string, err error) {
	raw, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	if err != nil {
		return nil, nil, nil, err
	}
	doc, err = stripReadOnly(raw)
	if err == nil {
		doc, project, err = takeProject(doc)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if err := StrictUnmarshal(doc, &repository.Document{}); err != nil {
		return nil, nil, nil, err
	}
	return raw, doc, project, nil
}

// patchBody reads a merge patch of a repository document: an object without
// duplicate members whose members the document knows, the read-only ones ignored.
func (s *Server) patchBody(w http.ResponseWriter, r *http.Request) (doc []byte, project *string, err error) {
	raw, err := ReadMergePatch(w, r, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	if err != nil {
		return nil, nil, err
	}
	doc, err = stripReadOnly(raw)
	if err == nil {
		doc, project, err = takeProject(doc)
	}
	if err != nil {
		return nil, nil, err
	}
	var probe any
	if json.Unmarshal(doc, &probe) == nil {
		if p, ok := unknownMember(probe, reflect.TypeFor[repository.Document](), ""); ok {
			return nil, nil, malformed(p, "unknown", "the member is not part of the resource", nil)
		}
	}
	return doc, project, nil
}

// sameProject is the 422 of a body that names another project than the path does (Q-AUTHZ-206): the
// repository stays in the project of its path, and the move operation changes it.
func sameProject(body *string, path string) error {
	if body == nil || *body == path {
		return nil
	}
	return errs.E(errs.Invalid, errs.CodeValidation, "The project of a repository is the one of the path; the move operation changes it.",
		errs.WithFields(errs.FieldError{Pointer: "/project", Code: "immutable", Detail: "the project is the one of the path"}))
}

// repositoryPrecondition turns the parsed If-Match into the service's input. A tag
// that was never issued, such as "0", matches nothing: the service then answers
// 412 with the current tag.
func repositoryPrecondition(p Precondition) repository.Precondition {
	if p.Any {
		return repository.MatchAny()
	}
	rev, _ := withoutActivity(p).Revision("")
	if rev < 1 {
		return repository.MatchVersion(math.MaxInt64)
	}
	return repository.MatchVersion(rev)
}

// repositoryETag is the tag of a read of one repository. The representation
// carries lastActivityAt, which changes without a new revision, and a strong
// validator must change with every observable change of the representation
// (RFC 9110 section 8.8.1): the tag is the revision, then the activity time as
// ".<microseconds>" when there is one. A write answers the plain revision tag,
// and a write accepts both forms; the suffix is not part of the concurrency
// check, because a download is no conflict with an edit (R-API-012).
func repositoryETag(st repository.Stored, view string) string {
	tag := ETag(st.Repository.Version, view)
	if t := st.Repository.LastActivityAt; !t.IsZero() {
		tag = tag[:len(tag)-1] + "." + strconv.FormatInt(t.UnixMicro(), 10) + `"`
	}
	return tag
}

// withoutActivity removes the activity suffix of repositoryETag from an If-Match
// tag, so that the tag of a read is the revision of the write.
func withoutActivity(p Precondition) Precondition {
	if !p.Present || p.Any {
		return p
	}
	inner := p.Tag[1 : len(p.Tag)-1]
	if i := strings.LastIndexByte(inner, '.'); i > 0 {
		if _, err := strconv.ParseInt(inner[i+1:], 10, 64); err == nil {
			p.Tag = `"` + inner[:i] + `"`
		}
	}
	return p
}

func repositoryURL(r *http.Request, name qname.Repo) string {
	return strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/") + "/api/v1/projects/" + url.PathEscape(name.Project) + "/repositories/" + url.PathEscape(name.Key)
}

// ListFormats implements GET /api/v1/formats (R-REPO-003).
func (s *Server) ListFormats(w http.ResponseWriter, r *http.Request) {
	if err := s.anonymousGate(r.Context()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	type client struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	type descriptor struct {
		Name                      string   `json:"name"`
		DisplayName               string   `json:"displayName"`
		Classes                   []string `json:"classes"`
		Clients                   []client `json:"clients"`
		Layout                    string   `json:"layout"`
		ProtocolBasePath          string   `json:"protocolBasePath"`
		DefaultUpstreamURL        *string  `json:"defaultUpstreamUrl"`
		DefaultExtraHosts         []string `json:"defaultExtraHosts"`
		DefaultCredentialHosts    []string `json:"defaultCredentialHosts"`
		DefaultMetadataTTLSeconds *int     `json:"defaultMetadataTtlSeconds"`
		DefaultNotFoundTTLSeconds *int     `json:"defaultNotFoundTtlSeconds"`
		PatternSubject            string   `json:"patternSubject"`
		PriorityMembers           bool     `json:"priorityMembers"`
		PinsAndClaims             bool     `json:"pinsAndClaims"`
		Caps                      bool     `json:"caps"`
		// Mirror is the catalog fact Entry.Mirror: a remote of the format can run in mode mirror (R-UI-021).
		Mirror bool `json:"mirror"`
		// SettingsSchema is the JSON Schema of the format's settings member; null for
		// a format without settings.
		SettingsSchema json.RawMessage `json:"settingsSchema"`
	}
	out := struct {
		Formats []descriptor `json:"formats"`
		BaseURL *string      `json:"baseUrl"`
	}{Formats: []descriptor{}}
	// The configured base URL of the endpoint, never one built from the Host header
	// (Q-UI-75): a caller needs no permission to learn the address it was given.
	if info := httpserver.InfoFrom(r.Context()); info.BaseURLSource == "config" && info.BaseURL != "" {
		b := strings.TrimSuffix(info.BaseURL, "/")
		out.BaseURL = &b
	}
	for _, d := range formats.Descriptors() {
		x := descriptor{
			Name: string(d.ID), DisplayName: d.DisplayName, Classes: []string{}, Clients: []client{}, Layout: d.Layout, DefaultExtraHosts: append([]string{}, d.DefaultExtraHosts...),
			DefaultCredentialHosts: append([]string{}, d.DefaultCredentialHosts...),
			ProtocolBasePath:       d.ProtocolBasePath, DefaultMetadataTTLSeconds: d.DefaultMetadataTTLSeconds,
			DefaultNotFoundTTLSeconds: d.DefaultNotFoundTTLSeconds, PatternSubject: string(d.PatternSubject),
			PriorityMembers: d.PriorityMembers, PinsAndClaims: d.PinsAndClaims, Caps: d.Caps, SettingsSchema: json.RawMessage("null"),
		}
		if len(d.SettingsSchema) > 0 {
			x.SettingsSchema = d.SettingsSchema
		}
		x.Mirror = mirrorFact(catalog.Default(), string(d.ID))
		if d.DefaultUpstreamURL != "" {
			u := d.DefaultUpstreamURL
			x.DefaultUpstreamURL = &u
		}
		for _, c := range d.Classes {
			x.Classes = append(x.Classes, string(c))
		}
		for _, c := range d.Clients {
			x.Clients = append(x.Clients, client{ID: c.ID, DisplayName: c.DisplayName})
		}
		out.Formats = append(out.Formats, x)
	}
	writeJSON(w, r, http.StatusOK, out)
}

// mirrorFact is the catalog fact Entry.Mirror of a format: whether a remote repository of it can run in mode mirror
// (R-REPO-050, R-UI-021). A format the catalog does not know cannot.
func mirrorFact(reg *catalog.Registry, format string) bool {
	e, ok := reg.Lookup(format)
	return ok && e.Mirror
}

// repositoryListSpec declares what the list accepts.
func repositoryListSpec() ListSpec {
	return ListSpec{Filters: []string{"class", "format", "q", "state", "project", "view"}}
}

// splitValues flattens repeated and comma-separated values of a filter.
func splitValues(vals []string) []string {
	var out []string
	for _, v := range vals {
		for part := range strings.SplitSeq(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func badParam(name, why string) error {
	return errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter "+`"`+name+`"`+" "+why+".")
}

// ListRepositories implements GET /api/v1/repositories (R-REPO-012): every repository of the installation the
// caller may see, ordered by project key and key, with the project of each.
func (s *Server) ListRepositories(w http.ResponseWriter, r *http.Request, _ gen.ListRepositoriesParams) {
	s.listRepositories(w, r, "")
}

// ListProjectRepositories implements GET /api/v1/projects/{projectKey}/repositories.
func (s *Server) ListProjectRepositories(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.ListProjectRepositoriesParams) {
	s.listRepositories(w, r, projectKey)
}

// listRepositories is the list of both operations; project narrows it to one project, "" lists all.
func (s *Server) listRepositories(w http.ResponseWriter, r *http.Request, project string) {
	ctx := r.Context()
	svc, err := s.repositoryService()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, repositoryListSpec(), s.o.API.Config)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	f, view, err := repositoryFilter(lq)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if project != "" {
		f.Project = project
	}
	if len(lq.After) > 1 {
		httpserver.Problem(w, r, invalidCursor())
		return
	}
	if len(lq.After) == 1 {
		after, perr := qname.ParseRepo(lq.After[0])
		if perr != nil {
			httpserver.Problem(w, r, invalidCursor())
			return
		}
		f.After = after
	}
	if err := s.anonymousGate(ctx); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz)
	var own *configScope
	if err == nil {
		own, err = s.configScope(ctx)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if view == "full" && !own.global && !own.anyOwned() {
		httpserver.Problem(w, r, errs.E(errs.Forbidden, errs.CodeForbidden, "view=full needs the permission "+capabilityReadConfig+"."))
		return
	}
	f.IncludeDeleting = own.global || own.anyOwned()
	rows, err := s.visibleRepositories(ctx, acc, svc, f, own, view == "full")
	if err == nil && project != "" && len(rows) == 0 {
		err = s.projectSeen(ctx, acc, svc, project, own)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	base := httpserver.InfoFrom(ctx).BaseURL
	page := NewPage(lq, rows, func(st repository.Stored) []string { return []string{st.Repository.Name.String()} })
	if view == "full" {
		WritePage(w, r, MapPage(page, func(st repository.Stored) any { return toRep(st, true, false) }))
		return
	}
	WritePage(w, r, MapPage(page, func(st repository.Stored) any { return toSummary(base, st, own.sees(st.Repository.Name.Project)) }))
}

// projectSeen decides the empty list of a project's repositories (R-REPO-012) as hideProject decides a refusal
// (R-AUTHZ-016): 404 for a project that does not exist, and for one the caller cannot see, so that the answer tells
// the two apart for nobody. A caller sees the project when it holds projects:read or repositories:read-config, is a
// member of it (as far as its token's scope reaches, Q-AUTHZ-215), or reads one of its repositories whatever the
// filters and the cursor of the request; to the others the project answers like an unknown one. It runs only for an
// empty page, so a list that shows a repository costs nothing more.
func (s *Server) projectSeen(ctx context.Context, acc *edge.Access, svc *repository.Service, project string, own *configScope) error {
	missing := errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The project %q does not exist.", project))
	sees := own.global || own.owned[project]
	if !sees {
		held, err := s.holds(ctx, string(authz.ProjectsRead))
		if err != nil {
			return err
		}
		sees = held
	}
	if pa, ok := s.o.Access.(ProjectAccess); ok && !sees {
		members, err := pa.Memberships(ctx)
		if err != nil {
			return err
		}
		for _, m := range members {
			sees = sees || m.Key == project
		}
	}
	if sees {
		// The index of the request holds every project, so a member and a holder of a capability learn whether it
		// exists on the same index their decisions are made on.
		if acc == nil || !acc.Index().HasProject(project) {
			return missing
		}
		return nil
	}
	rows, err := s.visibleRepositories(ctx, acc, svc, repository.ListFilter{Project: project}, own, false)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return missing
	}
	return nil
}

// configScope is what the caller sees of the configuration: all of it with repositories:read-config, otherwise
// that of the projects it is an Owner of (Q-AUTHZ-208).
type configScope struct {
	global bool
	owned  map[string]bool
}

func (c *configScope) anyOwned() bool { return len(c.owned) > 0 }

// sees reports whether the caller sees the configuration of the repositories of the project.
func (c *configScope) sees(project string) bool { return c.global || c.owned[project] }

// configScope reads the scope on the request's index.
func (s *Server) configScope(ctx context.Context) (*configScope, error) {
	held, err := s.holds(ctx, capabilityReadConfig)
	if err != nil {
		return nil, err
	}
	c := &configScope{global: held, owned: map[string]bool{}}
	pa, ok := s.o.Access.(ProjectAccess)
	if held || !ok {
		return c, nil
	}
	members, err := pa.Memberships(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.Role != authz.ProjectRoleOwner {
			continue
		}
		if owns, err := pa.Owns(ctx, m.Key); err != nil {
			return nil, err
		} else if owns {
			c.owned[m.Key] = true
		}
	}
	return c, nil
}

// repositoryFilter reads the filters of the list.
func repositoryFilter(lq ListQuery) (repository.ListFilter, string, error) {
	f := repository.ListFilter{
		Classes: splitValues(lq.Filter["class"]), Formats: splitValues(lq.Filter["format"]),
		Query: lq.First("q"), State: lq.First("state"), Project: lq.First("project"),
	}
	for _, c := range f.Classes {
		if c != "local" && c != "remote" && c != "virtual" {
			return f, "", badParam("class", "has the unknown value "+c)
		}
	}
	if f.State != "" && f.State != "inactive" {
		return f, "", badParam("state", "must be inactive")
	}
	view := lq.First("view")
	if view == "" {
		view = "summary"
	}
	if view != "summary" && view != "full" {
		return f, "", badParam("view", "must be summary or full")
	}
	return f, view, nil
}

// visibleRepositories reads the page of repositories the caller may see: for a
// holder of repositories:read-config every repository the endpoint exposes,
// else those it may read, which the exposure narrows as well (R-AUTHZ-016,
// R-EP-014). An Owner of a project sees the repositories of its project in the same
// way, those being deleted included (Q-AUTHZ-208); full documents (view=full) are those
// of such repositories only. The filtering runs over the rows after the cursor, in
// (project, key) order, so the cursor stays the name of the last row of the page.
func (s *Server) visibleRepositories(ctx context.Context, acc *edge.Access, svc *repository.Service, f repository.ListFilter, own *configScope, full bool) ([]repository.Stored, error) {
	res, err := svc.List(ctx, f)
	if err != nil {
		return nil, err
	}
	rows := res.Items
	var names, owned []qname.Repo
	for _, st := range rows {
		names = append(names, st.Repository.Name)
		if !own.global && own.sees(st.Repository.Name.Project) {
			owned = append(owned, st.Repository.Name)
		}
	}
	visible, err := s.visibleKeys(ctx, acc, names, own.global)
	if err == nil && len(owned) > 0 {
		var extra []qname.Repo
		if extra, err = s.visibleKeys(ctx, acc, owned, true); err == nil {
			visible = append(visible, extra...)
		}
	}
	if err != nil {
		return nil, err
	}
	allowed := make(map[qname.Repo]bool, len(visible))
	for _, k := range visible {
		allowed[k] = true
	}
	kept := rows[:0:0]
	for _, st := range rows {
		mine := own.sees(st.Repository.Name.Project)
		switch {
		case !allowed[st.Repository.Name]:
		case !mine && (full || st.Repository.Lifecycle != repository.Active):
		default:
			kept = append(kept, st)
		}
	}
	return kept, nil
}

// visibleKeys filters repository names for the subject of the request, on the
// index the request holds. A holder of repositories:read-config needs no read
// grant, but the exposure ceiling of the request's endpoint binds it like every
// subject (R-EP-013, Q-EP-12): config selects the view, never what the endpoint
// shows. Without a holder no decision point is wired and nothing is shown.
func (s *Server) visibleKeys(ctx context.Context, acc *edge.Access, keys []qname.Repo, cfg bool) ([]qname.Repo, error) {
	if acc == nil {
		return nil, errNoDecisionPoint()
	}
	if !cfg {
		return acc.Readable(ctx, keys)
	}
	return acc.Exposed(keys), nil
}

// CreateRepository implements POST /api/v1/repositories (R-REPO-009). The old unscoped route is gone: the
// path names the project (see [Server.CreateProjectRepository]).

// CreateProjectRepository implements POST /api/v1/projects/{projectKey}/repositories (R-REPO-009).
func (s *Server) CreateProjectRepository(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.CreateProjectRepositoryParams) {
	svc, err := s.repositoryService()
	if err == nil && s.idem == nil {
		err = unavailable("The repository service")
	}
	var raw, doc []byte
	var project *string
	if err == nil {
		raw, doc, project, err = s.documentBody(w, r)
	}
	if err == nil {
		err = sameProject(project, projectKey)
	}
	var cfg bool
	if err == nil {
		cfg, err = s.readsConfig(r.Context(), projectKey)
	}
	var caller repository.Caller
	if err == nil {
		caller, err = s.writeCaller(r)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = s.idem.Do(w, r, raw, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		st, err := svc.CreateTx(ctx, tx, caller, projectKey, doc)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated,
			Header: map[string]string{"Location": repositoryURL(r, st.Repository.Name), "ETag": ETag(st.Repository.Version, "")},
			Body:   toRep(st, cfg, true),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// readsConfig reports whether the caller sees the full configuration of the repositories of the
// project: it holds repositories:read-config, or it is an Owner of the project (Q-AUTHZ-208).
func (s *Server) readsConfig(ctx context.Context, project string) (bool, error) {
	held, err := s.holds(ctx, capabilityReadConfig)
	if err != nil || held {
		return held, err
	}
	pa, ok := s.o.Access.(ProjectAccess)
	if !ok {
		return false, nil
	}
	return pa.Owns(ctx, project)
}

// GetRepository implements GET /api/v1/projects/{projectKey}/repositories/{repoKey} (R-REPO-010): the full
// document for a holder of repositories:read-config, the consumer view for a
// caller who may read the repository, and one 404 for everyone else, whether the
// repository exists or not (R-AUTHZ-016).
func (s *Server) GetRepository(w http.ResponseWriter, r *http.Request, projectKey, repoKey string, _ gen.GetRepositoryParams) {
	key := qname.Repo{Project: projectKey, Key: repoKey}
	ctx := r.Context()
	svc, err := s.repositoryService()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if err := s.anonymousGate(ctx); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, acc, err := requestHolder(ctx, s.o.Repositories.Authz, key)
	var cfg bool
	if err == nil {
		cfg, err = s.readsConfig(ctx, projectKey)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if !cfg {
		// Authorization first, then the lookup: the decision point looks an unknown key
		// up itself (authz.Decider.Begin) and answers before the result is used, so a
		// repository the caller may not read is answered like a missing one.
		if acc == nil {
			httpserver.Problem(w, r, errNoDecisionPoint())
			return
		}
		c := authz.Coordinate{Repository: key, Kind: authz.KindMetadata, Actions: []authz.Action{authz.Read}}
		if d := acc.Decide(ctx, c); !d.Allowed() {
			httpserver.Problem(w, r, d.Err())
			return
		}
	} else {
		// The ceiling binds a configuration reader too, before the lookup, so a
		// repository the endpoint does not expose answers like a missing one
		// (R-EP-013, R-EP-014).
		exposed, err := s.visibleKeys(ctx, acc, []qname.Repo{key}, true)
		if err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		if len(exposed) == 0 {
			httpserver.Problem(w, r, authz.NotFound())
			return
		}
	}
	st, err := svc.Get(ctx, key)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Code == errs.CodeRepositoryNotFound && !cfg {
			err = authz.NotFound() // the same bytes as a repository the caller may not read
		}
		httpserver.Problem(w, r, err)
		return
	}
	if !cfg && st.Repository.Lifecycle != repository.Active {
		httpserver.Problem(w, r, authz.NotFound())
		return
	}
	view := ""
	if !cfg {
		view = consumerView
	}
	tag := repositoryETag(st, view)
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	if !cfg {
		writeJSON(w, r, http.StatusOK, consumerRep(httpserver.InfoFrom(ctx).BaseURL, st))
		return
	}
	writeJSON(w, r, http.StatusOK, toRep(st, true, false))
}

// ReplaceRepository implements PUT /api/v1/projects/{projectKey}/repositories/{repoKey} (R-REPO-011).
func (s *Server) ReplaceRepository(w http.ResponseWriter, r *http.Request, projectKey, repoKey string, _ gen.ReplaceRepositoryParams) {
	name := qname.Repo{Project: projectKey, Key: repoKey}
	s.writeRepository(w, r, name, func(svc *repository.Service, pre repository.Precondition, doc []byte) (repository.Stored, error) {
		caller, err := s.writeCaller(r)
		if err != nil {
			return repository.Stored{}, err
		}
		return svc.Replace(r.Context(), caller, name, pre, doc)
	}, func() ([]byte, *string, error) {
		_, doc, project, err := s.documentBody(w, r)
		return doc, project, err
	})
}

// UpdateRepository implements PATCH /api/v1/projects/{projectKey}/repositories/{repoKey} (R-REPO-011).
func (s *Server) UpdateRepository(w http.ResponseWriter, r *http.Request, projectKey, repoKey string, _ gen.UpdateRepositoryParams) {
	name := qname.Repo{Project: projectKey, Key: repoKey}
	s.writeRepository(w, r, name, func(svc *repository.Service, pre repository.Precondition, patch []byte) (repository.Stored, error) {
		caller, err := s.writeCaller(r)
		if err != nil {
			return repository.Stored{}, err
		}
		return svc.Patch(r.Context(), caller, name, pre, MergePatcher(patch))
	}, func() ([]byte, *string, error) { return s.patchBody(w, r) })
}

// writeRepository is the shape of PUT and PATCH: 428 without If-Match before the body is read, the
// body, the write, the new tag.
func (s *Server) writeRepository(w http.ResponseWriter, r *http.Request, name qname.Repo,
	write func(*repository.Service, repository.Precondition, []byte) (repository.Stored, error), body func() ([]byte, *string, error),
) {
	svc, err := s.repositoryService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var doc []byte
	var project *string
	if err == nil {
		doc, project, err = body()
	}
	if err == nil {
		err = sameProject(project, name.Project)
	}
	var cfg bool
	if err == nil {
		cfg, err = s.readsConfig(r.Context(), name.Project)
	}
	var st repository.Stored
	if err == nil {
		st, err = write(svc, repositoryPrecondition(pre), doc)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, st.Repository.Version, "")
	writeJSON(w, r, http.StatusOK, toRep(st, cfg, true))
}

// DeleteRepository implements DELETE /api/v1/projects/{projectKey}/repositories/{repoKey} (R-REPO-013).
func (s *Server) DeleteRepository(w http.ResponseWriter, r *http.Request, projectKey, repoKey string, params gen.DeleteRepositoryParams) {
	key := qname.Repo{Project: projectKey, Key: repoKey}
	svc, err := s.repositoryService()
	if err == nil && s.idem == nil {
		err = unavailable("The repository service")
	}
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var cond *repository.Precondition
	if pre.Present {
		c := repositoryPrecondition(pre)
		cond = &c
	}
	detach := params.Detach != nil && *params.Detach
	caller := repositoryCaller(r)
	err = s.idem.Do(w, r, nil, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		res, err := svc.DeleteTx(ctx, tx, caller, key, detach, cond)
		if err != nil {
			return Outcome{}, err
		}
		if res.Done {
			return Outcome{Status: http.StatusNoContent}, nil
		}
		return Outcome{
			Status: http.StatusAccepted,
			Header: map[string]string{"Location": jobLocation(r, res.JobID)},
			Body:   gen.JobReferenceEnvelope{Job: jobReference(res.JobID, repository.DeleteJobKind, res.JobState)},
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// MoveRepository implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/move (R-REPO-060, Q-REPO-217):
// the address of the repository changes, its content does not. The declaration admits the holder of
// repositories:write-config and an Owner of the source project; the second must own the target as well
// (checkMoveTarget) and is restricted: a key selector of a global policy keeps it from moving the repository
// (Q-REPO-232). The answer is the document at the new address.
func (s *Server) MoveRepository(w http.ResponseWriter, r *http.Request, projectKey, repoKey string) {
	svc, err := s.projects()
	var rs *repository.Service
	if err == nil {
		rs, err = s.repositoryService()
	}
	var in gen.MoveRepositoryRequest
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil && !qname.ValidProjectKey(in.Project) {
		err = errs.E(errs.Invalid, errs.CodeValidation, "The target project is not a project key.",
			errs.WithFields(errs.FieldError{Pointer: "/project", Code: "invalid_key", Detail: "must be a project key"}))
	}
	var restricted bool
	if err == nil {
		restricted, err = s.checkMoveTarget(r, in.Project)
	}
	var to qname.Repo
	var epoch int64
	if err == nil {
		to, epoch, err = svc.MoveRepository(r.Context(), caller(r), qname.Repo{Project: projectKey, Key: repoKey}, in.Project, restricted)
	}
	var st repository.Stored
	if err == nil {
		st, err = rs.Get(r.Context(), to)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.Header().Set("Location", repositoryURL(r, to))
	w.Header().Set("ETag", repositoryETag(st, ""))
	writeJSON(w, r, http.StatusOK, toRep(st, true, false))
}

// checkMoveTarget is the second half of the move's permission: the holder of repositories:write-config moves to
// any project; an Owner of the source project must own the target as well, and is restricted (Q-REPO-232). A
// target project the caller is no member of answers like a missing one (404), a member whose role is below Owner
// 403 (R-REPO-060).
func (s *Server) checkMoveTarget(r *http.Request, target string) (restricted bool, err error) {
	ctx := r.Context()
	held, err := s.holds(ctx, "repositories:write-config")
	if err != nil || held {
		return false, err
	}
	refused := errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	pa, ok := s.o.Access.(ProjectAccess)
	if !ok {
		return true, refused
	}
	owns, err := pa.Owns(ctx, target)
	if err != nil || owns {
		return true, err
	}
	members, err := pa.Memberships(ctx)
	if err != nil {
		return true, err
	}
	for _, m := range members {
		if m.Key == target {
			return true, refused
		}
	}
	return true, errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The project %q does not exist.", target))
}
