package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/repository"
)

// The packages operations of R-API-029 and the client setup operation of R-API-030.
// A package of the API is a format and a normalized name across repositories; the
// read model (internal/packages/view) answers it for the versions the caller may
// read, and this file turns its answers into the contract: the filters and sorts,
// the cursors, the strong tags and the problem answers.

// PackageServices are what the package operations need. The composition root fills
// it (internal/app/wire_packages.go); without View every operation answers 503.
type PackageServices struct {
	// View is the read model.
	View *view.Service
	// Decider begins the request's index and answers the repository-level questions;
	// Scoper classifies the repositories for a listing. The engine is both.
	Decider authz.Decider
	Scoper  authz.ReadScoper
	// Reader is the snapshot of the repositories: the topology (which virtual
	// repository contains which) and the format and class of one repository.
	Reader repository.Reader
	// Readme renders the README of a version.
	Readme *readme.Renderer
	// TokenEnvVar is ui.client_setup.token_env_var and TokenTTL ui.client_setup.token_ttl.
	TokenEnvVar string
	TokenTTL    time.Duration
}

func (s *Server) packageServices() (PackageServices, error) {
	ps := s.o.Packages
	if ps.View == nil || ps.Decider == nil || ps.Scoper == nil || ps.Reader == nil {
		return ps, unavailable("The package view")
	}
	return ps, nil
}

// packageScope opens the request's index and builds what the caller may read. The
// anonymous gate runs first: a caller without a credential is answered 401 while
// anonymous access is off, like on the repository list.
func (s *Server) packageScope(r *http.Request, ps PackageServices, req view.ScopeRequest) (context.Context, view.Scope, error) {
	ctx := r.Context()
	if err := s.anonymousGate(ctx); err != nil {
		return ctx, view.Scope{}, err
	}
	repos, err := ps.Reader.List(ctx)
	if err != nil {
		return ctx, view.Scope{}, err
	}
	topo := view.NewTopology(repos)
	keys := append(topo.Physical(), topo.Virtual()...)
	ctx, acc, err := requestHolder(ctx, ps.Decider, keys...)
	if err != nil {
		return ctx, view.Scope{}, err
	}
	rs, err := ps.Scoper.ReadScopeOn(ctx, acc.Index(), acc.Subject, topo.Physical())
	if err != nil {
		return ctx, view.Scope{}, err
	}
	virtuals, err := acc.Readable(ctx, topo.Virtual())
	if err != nil {
		return ctx, view.Scope{}, err
	}
	sc, err := ps.View.Scope(ctx, rs, topo, virtuals, req)
	return ctx, sc, err
}

func packageNotFound() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
}

// writeTagged answers 200 with a strong tag over the representation, or 304 when the
// request holds that tag (R-API-029). The tag is a hash of the bytes sent, so two
// callers who see different versions never share a tag.
func writeTagged(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}

func summaryOf(p view.Package) gen.PackageSummary {
	return gen.PackageSummary{
		Id: p.ID, Format: p.Format, Name: p.Name, Description: p.Description, LatestVersion: p.LatestVersion,
		Keywords: p.Keywords, Repositories: p.Repositories, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		VersionCount: p.VersionCount, DownloadCount: p.DownloadCount,
	}
}

// packageFilter reads the filters of the list.
func packageFilter(lq ListQuery) (view.Filter, error) {
	f := view.Filter{
		Formats: splitValues(lq.Filter["format"]),
		Q:       lq.First("q"), Keyword: lq.First("keyword"), NamePrefix: lq.First("namePrefix"),
	}
	if reps, ok := lq.Filter["repository"]; ok {
		f.Repositories, f.HasRepositories = splitValues(reps), true
	}
	for _, p := range []struct {
		name string
		to   **time.Time
	}{{"createdFrom", &f.CreatedFrom}, {"createdTo", &f.CreatedTo}} {
		if raw := lq.First(p.name); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return f, badParam(p.name, "must be an RFC 3339 date and time")
			}
			*p.to = &t
		}
	}
	return f, nil
}

// ListPackages implements GET /api/v1/packages (R-API-029).
func (s *Server) ListPackages(w http.ResponseWriter, r *http.Request, _ gen.ListPackagesParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters:     []string{"format", "repository", "q", "namePrefix", "keyword", "createdFrom", "createdTo"},
		Sorts:       []string{"name", "createdAt", "updatedAt"},
		DefaultSort: "name",
	}, s.o.API.Config)
	var f view.Filter
	if err == nil {
		f, err = packageFilter(lq)
	}
	order := view.Order{Sort: lq.Sort, Desc: lq.Desc}
	var after *view.After
	if err == nil && lq.After != nil {
		if after, err = order.ParseAfter(lq.After); err != nil {
			err = invalidCursor()
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: f.Formats, NamePrefix: f.NamePrefix})
	var rows []view.Package
	if err == nil {
		rows, err = ps.View.List(ctx, sc, f, order, after, lq.Limit)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, rows, order.Key), summaryOf))
}

// LookupPackage implements GET /api/v1/packages/lookup (R-API-029).
func (s *Server) LookupPackage(w http.ResponseWriter, r *http.Request, params gen.LookupPackageParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	norm, ok := normalizeName(params.Format, params.Name)
	if !ok {
		httpserver.Problem(w, r, packageNotFound())
		return
	}
	s.writePackage(w, r, ps, params.Format, norm)
}

// GetPackage implements GET /api/v1/packages/{packageId} (R-API-029).
func (s *Server) GetPackage(w http.ResponseWriter, r *http.Request, id gen.PackageId, _ gen.GetPackageParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, ok := view.ParseID(id)
	if !ok {
		httpserver.Problem(w, r, packageNotFound())
		return
	}
	s.writePackage(w, r, ps, format, name)
}

func (s *Server) writePackage(w http.ResponseWriter, r *http.Request, ps PackageServices, format, name string) {
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: []string{format}, ExactName: name})
	var p view.Package
	var found bool
	if err == nil {
		p, found, err = ps.View.Package(ctx, sc, format, name)
	}
	if err == nil && !found {
		err = packageNotFound()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeTagged(w, r, summaryOf(p))
}

// normalizeName applies the package-name rule of the format: the name as the
// format stores it. A name the rule refuses, and a format without a rule, stand for
// no package.
func normalizeName(format, name string) (string, bool) {
	f, ok := formats.Lookup(format)
	if !ok || f.View() == nil {
		return "", false
	}
	n := f.Descriptor().Names
	if n == nil {
		return name, name != ""
	}
	return n.Normalize(name)
}

// ListPackageVersions implements GET /api/v1/packages/{packageId}/versions
// (R-API-029).
func (s *Server) ListPackageVersions(w http.ResponseWriter, r *http.Request, id gen.PackageId, _ gen.ListPackageVersionsParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, ok := view.ParseID(id)
	if !ok {
		httpserver.Problem(w, r, packageNotFound())
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters: []string{"prerelease", "repository"}, Sorts: []string{"publishedAt", "version"}, DefaultSort: "-publishedAt",
	}, s.o.API.Config)
	var f view.VersionFilter
	if err == nil {
		if raw := lq.First("prerelease"); raw != "" {
			b, perr := strconv.ParseBool(raw)
			if perr != nil || (raw != "true" && raw != "false") {
				err = badParam("prerelease", "must be true or false")
			}
			f.Prerelease = &b
		}
		if reps, ok := lq.Filter["repository"]; ok {
			f.Repositories, f.HasRepositories = splitValues(reps), true
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	order := view.VersionOrder{Sort: lq.Sort, Desc: lq.Desc}
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: []string{format}, ExactName: name})
	var rows []view.VersionSummary
	found := false
	if err == nil {
		rows, found, err = ps.View.Versions(ctx, sc, format, name, f, order, lq.After, lq.Limit)
		if view.IsBadCursor(err) {
			err = invalidCursor()
		}
	}
	if err == nil && !found {
		err = packageNotFound()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, rows, order.VersionKey), func(v view.VersionSummary) gen.PackageVersionSummary {
		return gen.PackageVersionSummary{
			Version: v.Version, Prerelease: v.Prerelease, PublishedAt: v.PublishedAt, SizeBytes: v.SizeBytes,
			Repositories: v.Repositories, DownloadCount: v.DownloadCount,
		}
	}))
}

// GetPackageVersion implements GET /api/v1/packages/{packageId}/versions/{version}
// (R-API-029).
func (s *Server) GetPackageVersion(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath, _ gen.GetPackageVersionParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, ok := view.ParseID(id)
	if !ok {
		httpserver.Problem(w, r, packageNotFound())
		return
	}
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: []string{format}, ExactName: name})
	var d view.VersionDetail
	found := false
	if err == nil {
		d, found, err = ps.View.Version(ctx, sc, format, name, version)
	}
	if err == nil && !found {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	base := httpserver.InfoFrom(ctx).BaseURL
	writeTagged(w, r, versionOf(base, d))
}

func versionOf(base string, d view.VersionDetail) gen.PackageVersion {
	out := gen.PackageVersion{
		PackageId: d.PackageID, Format: d.Format, Name: d.Name, Version: d.Version, Prerelease: d.Prerelease,
		PublishedAt: d.PublishedAt, Licenses: d.Licenses, SizeBytes: d.SizeBytes, DownloadCount: d.DownloadCount,
		HasReadme: d.HasReadme, Metadata: d.Metadata,
		Repositories: make([]gen.PackageVersionRepository, 0, len(d.Repositories)),
		Files:        make([]gen.PackageFile, 0, len(d.Files)),
	}
	for _, r := range d.Repositories {
		out.Repositories = append(out.Repositories, gen.PackageVersionRepository{
			Key: r.Key, Class: gen.PackageVersionRepositoryClass(r.Class), PublishedAt: r.PublishedAt,
		})
	}
	for _, f := range d.Files {
		out.Files = append(out.Files, gen.PackageFile{
			Path: f.Path, SizeBytes: f.SizeBytes, Sha256: f.SHA256, DownloadUrl: downloadURL(base, d.Format, f.Repo, f.Path),
		})
	}
	return out
}

// downloadURL is the URL a client downloads a file from: the format's protocol
// path of the repository when the format has one, else the raw file path. Every
// segment is escaped once (R-ARCH-007).
func downloadURL(base, format, repo, path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	escaped := strings.Join(segs, "/")
	base = strings.TrimSuffix(base, "/")
	if f, ok := formats.Lookup(format); ok && len(f.Routes()) > 0 {
		return base + strings.ReplaceAll(f.Descriptor().ProtocolBasePath, "{key}", repo) + escaped
	}
	return base + "/files/" + url.PathEscape(repo) + "/" + escaped
}

// readmeTag is the strong tag of a README answer: the source, the renderer version
// and the hash of the image origin list (R-API-029). It is computed before the
// rendering, so a request that holds it costs no rendering.
func readmeTag(rd *readme.Renderer, source string) string {
	h := sha256.New()
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(readme.Version)))
	h.Write([]byte{0})
	h.Write([]byte(rd.PolicyHash()))
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// GetPackageVersionReadme implements
// GET /api/v1/packages/{packageId}/versions/{version}/readme (R-API-029, R-UI-008).
func (s *Server) GetPackageVersionReadme(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath, _ gen.GetPackageVersionReadmeParams) {
	ps, err := s.packageServices()
	if err == nil && ps.Readme == nil {
		err = unavailable("The README renderer")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	format, name, ok := view.ParseID(id)
	// One answer for a version that is not visible and for one without a README: the
	// difference would tell a caller that a hidden version exists.
	none := errs.E(errs.NotFound, errs.CodeNotFound, "The version has no README.")
	if !ok {
		httpserver.Problem(w, r, none)
		return
	}
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: []string{format}, ExactName: name})
	var rd view.Readme
	found := false
	if err == nil {
		rd, found, err = ps.View.ReadmeOf(ctx, sc, format, name, version)
	}
	if err == nil && !found {
		err = none
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := readmeTag(ps.Readme, rd.Source)
	if NotModified(w, r, tag) {
		return
	}
	res := ps.Readme.Render(rd.Source)
	b, err := json.Marshal(gen.PackageReadme{
		ContentType: "text/markdown", Html: res.HTML, Truncated: res.Truncated,
		ExternalImagesBlocked: res.ExternalImagesBlocked,
	})
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}

// GetClientSetup implements GET /api/v1/repositories/{repoKey}/client-setup
// (R-API-030). The operation's declaration (read on the repository) has been
// enforced by the time the handler runs.
func (s *Server) GetClientSetup(w http.ResponseWriter, r *http.Request, repoKey string, params gen.GetClientSetupParams) {
	ps, err := s.packageServices()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out, err := s.clientSetup(r, ps, repoKey, params)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

// maxSetupValue bounds a value the snippets embed.
const maxSetupValue = 256

// setupValues collects the values of the client setup that end up in a snippet and
// refuses one above maxSetupValue, with a control character, in the shape of a
// token, or, for the package and the version, starting with "-": a client reads
// such an argument as an option, quoted or not, and "--registry=…" would point a
// pasted install elsewhere (Q-NPM-50).
func setupValues(p gen.GetClientSetupParams) (map[string]string, error) {
	values := map[string]string{"client": p.Client}
	if p.Package != nil {
		values["package"] = *p.Package
	}
	if p.Version != nil {
		values["version"] = *p.Version
	}
	if p.Scope != nil {
		values["scope"] = *p.Scope
	}
	for name, v := range values {
		switch {
		case len(v) > maxSetupValue || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return nil, badParam(name, "is too long or holds a control character")
		case holdsTokenShape(v):
			return nil, badParam(name, "must not hold a token")
		case (name == "package" || name == "version") && strings.HasPrefix(v, "-"):
			return nil, badParam(name, "must not start with \"-\"")
		}
	}
	return values, nil
}

func (s *Server) clientSetup(r *http.Request, ps PackageServices, repoKey string, p gen.GetClientSetupParams) (gen.ClientSetup, error) {
	ctx := r.Context()
	creds := credPlaceholder
	if p.Credentials != nil {
		creds = string(*p.Credentials)
	}
	if !slices.Contains([]string{credPlaceholder, credEnv, credNone}, creds) {
		return gen.ClientSetup{}, badParam("credentials", "must be placeholder, env or none")
	}
	values, err := setupValues(p)
	if err != nil {
		return gen.ClientSetup{}, err
	}
	var scopeName string
	if sc := values["scope"]; sc != "" {
		scopeName = strings.TrimPrefix(sc, "@")
		if scopeName == "" || strings.ContainsAny(scopeName, "/@") {
			return gen.ClientSetup{}, badParam("scope", "must be an npm scope such as @team-a")
		}
	}

	repo, err := ps.Reader.ByKey(ctx, repoKey)
	if err != nil {
		return gen.ClientSetup{}, err
	}
	f, ok := formats.Lookup(repo.Format)
	if !ok || len(f.SetupTemplates()) == 0 {
		return gen.ClientSetup{}, badParam("client", "is not offered for the format of this repository")
	}
	templates := f.SetupTemplates()
	if !slices.Contains(setupClients(templates), p.Client) {
		return gen.ClientSetup{}, badParam("client", "is not one of "+strings.Join(setupClients(templates), ", "))
	}
	if pkg := values["package"]; pkg != "" {
		// The package is a name of the format, validated and normalized by the
		// format's one name rule (R-AUTHZ-005), before it reaches a snippet.
		norm, ok := "", false
		if names := f.Descriptor().Names; names != nil {
			norm, ok = names.Normalize(pkg)
		}
		if !ok {
			return gen.ClientSetup{}, badParam("package", "is not a package name of the format "+repo.Format)
		}
		values["package"] = norm
	}

	// The addresses come from the configured base URL of the endpoint, never from the
	// Host the request carried: the snippets end up in files users copy.
	info := httpserver.InfoFrom(ctx)
	if info.BaseURLSource != "config" || info.BaseURL == "" {
		return gen.ClientSetup{}, errs.E(errs.Unavailable, errs.CodeUnavailable,
			"The client setup needs server.base_url (or the base URL of the endpoint); it is not configured.")
	}
	registry := strings.TrimSuffix(info.BaseURL, "/") + strings.ReplaceAll(f.Descriptor().ProtocolBasePath, "{key}", repoKey)
	u, err := url.Parse(registry)
	if err != nil {
		return gen.ClientSetup{}, errs.Wrap(err, "api.clientSetup")
	}
	registryKey := "//" + u.Host + u.Path

	ctx, acc, err := requestHolder(ctx, ps.Decider, repoKey)
	if err != nil {
		return gen.ClientSetup{}, err
	}
	target, mayPublish := s.publishTarget(acc, repo)
	requiresAuth := true
	if keys, err := acc.ReadableAnonymously(ctx, []string{repoKey}); err == nil {
		requiresAuth = len(keys) == 0
	}

	snippets, err := renderSnippets(setupInput{
		Templates: templates, Client: p.Client, Credentials: creds, TokenEnvVar: ps.TokenEnvVar,
		Data: formats.SetupData{
			RegistryURL: registry, Repository: repoKey, Endpoint: acc.Subject.Endpoint,
			RegistryKey: registryKey, RequiresAuth: requiresAuth,
		},
		Package: values["package"], Version: values["version"], Scope: scopeName, MayPublish: mayPublish,
	})
	if err != nil {
		return gen.ClientSetup{}, errs.Wrap(err, "api.clientSetup")
	}
	out := gen.ClientSetup{
		Repository: repoKey, Format: repo.Format, Client: p.Client, Credentials: gen.ClientSetupCredentials(creds),
		RegistryUrl: registry, EndpointPath: registryKey, PublishTarget: target,
		TokenTtlSeconds: int64(ps.TokenTTL / time.Second), Snippets: make([]gen.ClientSnippet, 0, len(snippets)),
	}
	if !acc.Subject.Anonymous {
		name := acc.Subject.Name
		out.Username = &name
	}
	for _, sn := range snippets {
		if holdsTokenShape(sn.Content) {
			// Not reachable with a template over SetupData; a defect that would
			// otherwise hand a token to a caller.
			return gen.ClientSetup{}, errs.E(errs.Internal, errs.CodeInternal, "A setup snippet holds a token.")
		}
		cs := gen.ClientSnippet{Id: sn.ID, Tab: gen.ClientSnippetTab(sn.Tab), Language: gen.ClientSnippetLanguage(sn.Language), Content: sn.Content}
		if sn.FileName != "" {
			fn := sn.FileName
			cs.FileName = &fn
		}
		out.Snippets = append(out.Snippets, cs)
	}
	return out, nil
}

// publishTarget returns the repository a publish through repo goes to and whether
// the caller may deploy there: repo itself when it is a local repository, the
// deployment target of a virtual one, none for a remote one.
func (s *Server) publishTarget(acc *edge.Access, repo repository.Repository) (*string, bool) {
	var key string
	switch repo.Class {
	case repository.Local:
		key = repo.Key
	case repository.Virtual:
		t, ok := repo.DeployTarget()
		if !ok {
			return nil, false
		}
		key = t.Key
	default:
		return nil, false
	}
	if acc.Subject.Anonymous {
		return nil, false
	}
	ok, err := acc.Check([]authz.CheckItem{{Repository: key, Action: authz.Deploy}})
	if err != nil || len(ok) != 1 || !ok[0] {
		return nil, false
	}
	return &key, true
}
