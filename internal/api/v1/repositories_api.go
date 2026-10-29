package v1

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
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
	// read: the repository read and the list.
	Authz authz.Authorizer
	// Reader is the replica's snapshot of the repositories. The read of a
	// repository looks an unknown key up before it decides, so that a repository
	// created through another replica is usable at once (R-ARCH-011 step 2).
	Reader repository.Reader
	// AnonymousAccess is the runtime switch of R-AUTH-037. An anonymous caller is
	// answered 401 while it is off. Nil means no gate here (a unit test without a
	// decision point).
	AnonymousAccess func() bool
}

// capabilityReadConfig is the capability of reading configuration (R-AUTHZ-010).
const capabilityReadConfig = "repositories:read-config"

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
	if httpserver.PrincipalFrom(ctx).Anonymous && gate != nil && !gate() {
		return authz.Unauthenticated()
	}
	return nil
}

// repositoryCaller is the actor and the origin of a write, for the audit event.
func repositoryCaller(r *http.Request) repository.Caller {
	return repository.Caller{Actor: auditActor(r.Context()), Source: auditSource(r)}
}

// repositoryRep is the representation of a repository: the configuration document
// and the members the server adds. The request type is the response type, so a
// body obtained by GET can be sent back; the read-only members are ignored on the
// way in (readOnlyMembers).
type repositoryRep struct {
	repository.Document
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
		Document: d, Lifecycle: string(st.Repository.Lifecycle),
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
func addresses(base string, d repository.Document) []string {
	view := d.ConsumerView(protocolBase(d.Format))
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
		Description: st.Document.Description, Addresses: addresses(base, st.Document), Disabled: st.Repository.Disabled,
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
	v := st.Document.ConsumerView(protocolBase(st.Document.Format))
	v.Addresses = addresses(base, st.Document)
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
		return nil, asInvalidDocument(malformed("", "type", "the body must be a JSON object", err))
	}
	for _, k := range readOnlyMembers() {
		delete(m, k)
	}
	return json.Marshal(m)
}

// asInvalidDocument gives the body errors of a repository document their code of
// R-REPO-008: 400 invalid_document, with the pointer of the member.
func asInvalidDocument(err error) error {
	e, ok := errs.As(err)
	if !ok || e.Code != errs.CodeInvalid || len(e.Fields) == 0 {
		return err
	}
	return errs.E(errs.Invalid, errs.CodeInvalidDocument, e.Detail, errs.WithBadRequest(), errs.WithFields(e.Fields...))
}

// documentBody reads the configuration document of a create or a replace
// strictly: JSON, bounded, no duplicate and no unknown member, the read-only
// members ignored.
func (s *Server) documentBody(w http.ResponseWriter, r *http.Request) (raw, doc []byte, err error) {
	raw, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	if err != nil {
		return nil, nil, err
	}
	doc, err = stripReadOnly(raw)
	if err != nil {
		return nil, nil, asInvalidDocument(err)
	}
	if err := StrictUnmarshal(doc, &repository.Document{}); err != nil {
		return nil, nil, asInvalidDocument(err)
	}
	return raw, doc, nil
}

// patchBody reads a merge patch of a repository document: an object without
// duplicate members whose members the document knows, the read-only ones ignored.
func (s *Server) patchBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	raw, err := ReadMergePatch(w, r, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	if err != nil {
		return nil, asInvalidDocument(err)
	}
	doc, err := stripReadOnly(raw)
	if err != nil {
		return nil, asInvalidDocument(err)
	}
	var probe any
	if json.Unmarshal(doc, &probe) == nil {
		if p, ok := unknownMember(probe, reflect.TypeFor[repository.Document](), ""); ok {
			return nil, asInvalidDocument(malformed(p, "unknown", "the member is not part of the resource", nil))
		}
	}
	return doc, nil
}

// repositoryPrecondition turns the parsed If-Match into the service's input. A tag
// that was never issued, such as "0", matches nothing: the service then answers
// 412 with the current tag.
func repositoryPrecondition(p Precondition) repository.Precondition {
	if p.Any {
		return repository.MatchAny()
	}
	rev, _ := p.Revision("")
	if rev < 1 {
		return repository.MatchVersion(math.MaxInt64)
	}
	return repository.MatchVersion(rev)
}

func repositoryURL(r *http.Request, key string) string {
	return strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/") + "/api/v1/repositories/" + key
}

// knowRepository looks a key up that the replica's snapshot does not hold and
// brings the grant index up to the repository, before the decision, so that a
// repository created through another replica is usable at once (R-ARCH-011 step 2,
// R-API-052). The result of the lookup is not used, because the decision answers
// (R-AUTHZ-016); only a lookup or a catch-up that cannot finish in time is
// returned.
func (s *Server) knowRepository(ctx context.Context, key string) error {
	rd := s.o.Repositories.Reader
	if rd == nil {
		return nil
	}
	if _, err := rd.ByKey(ctx, key); err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.Unavailable {
			return err
		}
		return nil
	}
	if cu, ok := s.o.Repositories.Authz.(interface {
		CatchUp(ctx context.Context, repoKey string) error
	}); ok {
		return cu.CatchUp(ctx, key)
	}
	return nil
}

// ListFormats implements GET /api/v1/formats (R-REPO-003).
func (s *Server) ListFormats(w http.ResponseWriter, r *http.Request) {
	if err := s.anonymousGate(r.Context()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	type descriptor struct {
		Name                      string   `json:"name"`
		DisplayName               string   `json:"displayName"`
		Classes                   []string `json:"classes"`
		Layout                    string   `json:"layout"`
		ProtocolBasePath          string   `json:"protocolBasePath"`
		DefaultUpstreamURL        *string  `json:"defaultUpstreamUrl"`
		DefaultMetadataTTLSeconds *int     `json:"defaultMetadataTtlSeconds"`
		DefaultNotFoundTTLSeconds *int     `json:"defaultNotFoundTtlSeconds"`
		PatternSubject            string   `json:"patternSubject"`
		PriorityMembers           bool     `json:"priorityMembers"`
		PinsAndClaims             bool     `json:"pinsAndClaims"`
		Caps                      bool     `json:"caps"`
	}
	out := struct {
		Formats []descriptor `json:"formats"`
	}{Formats: []descriptor{}}
	for _, d := range formats.Descriptors() {
		x := descriptor{
			Name: string(d.ID), DisplayName: d.DisplayName, Classes: []string{}, Layout: d.Layout,
			ProtocolBasePath: d.ProtocolBasePath, DefaultMetadataTTLSeconds: d.DefaultMetadataTTLSeconds,
			DefaultNotFoundTTLSeconds: d.DefaultNotFoundTTLSeconds, PatternSubject: string(d.PatternSubject),
			PriorityMembers: d.PriorityMembers, PinsAndClaims: d.PinsAndClaims, Caps: d.Caps,
		}
		if d.DefaultUpstreamURL != "" {
			u := d.DefaultUpstreamURL
			x.DefaultUpstreamURL = &u
		}
		for _, c := range d.Classes {
			x.Classes = append(x.Classes, c.String())
		}
		out.Formats = append(out.Formats, x)
	}
	writeJSON(w, r, http.StatusOK, out)
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

// ListRepositories implements GET /api/v1/repositories (R-REPO-012).
func (s *Server) ListRepositories(w http.ResponseWriter, r *http.Request, _ gen.ListRepositoriesParams) {
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
	if len(lq.After) > 1 {
		httpserver.Problem(w, r, invalidCursor())
		return
	}
	if len(lq.After) == 1 {
		f.After = lq.After[0]
	}
	if err := s.anonymousGate(ctx); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	cfg, err := s.holds(ctx, capabilityReadConfig)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if view == "full" && !cfg {
		httpserver.Problem(w, r, errs.E(errs.Forbidden, errs.CodeForbidden, "view=full needs the permission "+capabilityReadConfig+"."))
		return
	}
	f.IncludeDeleting = cfg
	rows, err := s.visibleRepositories(r, svc, f, cfg, lq.First("project") != "")
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	base := httpserver.InfoFrom(ctx).BaseURL
	page := NewPage(lq, rows, func(st repository.Stored) []string { return []string{st.Document.Key} })
	if view == "full" {
		WritePage(w, r, MapPage(page, func(st repository.Stored) any { return toRep(st, true, false) }))
		return
	}
	WritePage(w, r, MapPage(page, func(st repository.Stored) any { return toSummary(base, st, cfg) }))
}

// repositoryFilter reads the filters of the list.
func repositoryFilter(lq ListQuery) (repository.ListFilter, string, error) {
	f := repository.ListFilter{
		Classes: splitValues(lq.Filter["class"]), Formats: splitValues(lq.Filter["format"]),
		Query: lq.First("q"), State: lq.First("state"),
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

// visibleRepositories reads the page of repositories the caller may see: all of
// them for a holder of repositories:read-config, else those it may read
// (R-AUTHZ-016). The filtering runs over the rows after the cursor, in key order,
// so the cursor stays the key of the last row of the page.
func (s *Server) visibleRepositories(r *http.Request, svc *repository.Service, f repository.ListFilter, cfg, byProject bool) ([]repository.Stored, error) {
	if byProject { // projects arrive after M1: no repository belongs to one
		return nil, nil
	}
	ctx := r.Context()
	res, err := svc.List(ctx, f)
	if err != nil {
		return nil, err
	}
	rows := res.Items
	if !cfg {
		if s.o.Repositories.Authz == nil {
			return nil, unavailable("The authorization state")
		}
		keys := make([]string, len(rows))
		for i, st := range rows {
			keys[i] = st.Document.Key
		}
		readable, err := s.o.Repositories.Authz.Readable(ctx, SubjectFrom(httpserver.PrincipalFrom(ctx)), keys)
		if err != nil {
			return nil, err
		}
		allowed := make(map[string]bool, len(readable))
		for _, k := range readable {
			allowed[k] = true
		}
		kept := rows[:0:0]
		for _, st := range rows {
			if allowed[st.Document.Key] {
				kept = append(kept, st)
			}
		}
		rows = kept
	}
	return rows, nil
}

// CreateRepository implements POST /api/v1/repositories (R-REPO-009).
func (s *Server) CreateRepository(w http.ResponseWriter, r *http.Request, _ gen.CreateRepositoryParams) {
	svc, err := s.repositoryService()
	if err == nil && s.idem == nil {
		err = unavailable("The repository service")
	}
	var raw, doc []byte
	if err == nil {
		raw, doc, err = s.documentBody(w, r)
	}
	var cfg bool
	if err == nil {
		cfg, err = s.holds(r.Context(), capabilityReadConfig)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	caller := repositoryCaller(r)
	err = s.idem.Do(w, r, raw, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		st, err := svc.CreateTx(ctx, tx, caller, doc)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated,
			Header: map[string]string{"Location": repositoryURL(r, st.Document.Key), "ETag": ETag(st.Repository.Version, "")},
			Body:   toRep(st, cfg, true),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// GetRepository implements GET /api/v1/repositories/{key} (R-REPO-010): the full
// document for a holder of repositories:read-config, the consumer view for a
// caller who may read the repository, and one 404 for everyone else, whether the
// repository exists or not (R-AUTHZ-016).
func (s *Server) GetRepository(w http.ResponseWriter, r *http.Request, key string, _ gen.GetRepositoryParams) {
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
	cfg, err := s.holds(ctx, capabilityReadConfig)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if !cfg {
		// Authorization first, then the lookup: a repository the caller may not read
		// is answered before anyone asks whether it exists.
		if s.o.Repositories.Authz == nil {
			httpserver.Problem(w, r, unavailable("The authorization state"))
			return
		}
		if err := s.knowRepository(ctx, key); err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		c := authz.Coordinate{Repository: key, Kind: authz.KindMetadata, Actions: []authz.Action{authz.Read}}
		if ep := httpserver.EndpointFrom(ctx); ep != nil {
			c.Endpoint = ep.Name()
		}
		if d := s.o.Repositories.Authz.Decide(ctx, SubjectFrom(httpserver.PrincipalFrom(ctx)), c); !d.Allowed() {
			httpserver.Problem(w, r, d.Err())
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
	tag := ETag(st.Repository.Version, view)
	if NotModified(w, r, tag) {
		return
	}
	SetETag(w, st.Repository.Version, view)
	if !cfg {
		writeJSON(w, r, http.StatusOK, consumerRep(httpserver.InfoFrom(ctx).BaseURL, st))
		return
	}
	writeJSON(w, r, http.StatusOK, toRep(st, true, false))
}

// ReplaceRepository implements PUT /api/v1/repositories/{key} (R-REPO-011).
func (s *Server) ReplaceRepository(w http.ResponseWriter, r *http.Request, key string, _ gen.ReplaceRepositoryParams) {
	s.writeRepository(w, r, func(svc *repository.Service, pre repository.Precondition, doc []byte) (repository.Stored, error) {
		return svc.Replace(r.Context(), repositoryCaller(r), key, pre, doc)
	}, func() ([]byte, error) {
		_, doc, err := s.documentBody(w, r)
		return doc, err
	})
}

// UpdateRepository implements PATCH /api/v1/repositories/{key} (R-REPO-011).
func (s *Server) UpdateRepository(w http.ResponseWriter, r *http.Request, key string, _ gen.UpdateRepositoryParams) {
	s.writeRepository(w, r, func(svc *repository.Service, pre repository.Precondition, patch []byte) (repository.Stored, error) {
		return svc.Patch(r.Context(), repositoryCaller(r), key, pre, patch)
	}, func() ([]byte, error) { return s.patchBody(w, r) })
}

// writeRepository is the shape of PUT and PATCH: 428 without If-Match before the
// body is read, the body, the write, the new tag.
func (s *Server) writeRepository(w http.ResponseWriter, r *http.Request,
	write func(*repository.Service, repository.Precondition, []byte) (repository.Stored, error), body func() ([]byte, error),
) {
	svc, err := s.repositoryService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var doc []byte
	if err == nil {
		doc, err = body()
	}
	var cfg bool
	if err == nil {
		cfg, err = s.holds(r.Context(), capabilityReadConfig)
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

// DeleteRepository implements DELETE /api/v1/repositories/{key} (R-REPO-013).
func (s *Server) DeleteRepository(w http.ResponseWriter, r *http.Request, key string, params gen.DeleteRepositoryParams) {
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
