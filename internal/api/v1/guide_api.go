package v1

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages/readme"
)

// The guide operations of R-API-053: a format's guide sections (formats.Guide),
// executed over the data of the repository and the caller, with the setup snippets
// of R-API-030 attached where a section names them. The snippets go through the
// renderer of the client setup, so a snippet of the guide is the one the setup page
// shows for the same repository, client and credential mode.

// GetRepositoryGuide implements GET /api/v1/repositories/{repoKey}/guide. The
// operation's declaration (read on the repository) has been enforced by the time
// the handler runs.
func (s *Server) GetRepositoryGuide(w http.ResponseWriter, r *http.Request, repoKey string, params gen.GetRepositoryGuideParams) {
	ps, err := s.packageServices()
	if err == nil && ps.Guides == nil {
		err = unavailable("The guide renderer")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out, err := s.repositoryGuide(r.Context(), ps, repoKey, params.Client)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

func (s *Server) repositoryGuide(ctx context.Context, ps PackageServices, repoKey, client string) (gen.Guide, error) {
	repo, err := ps.Reader.ByKey(ctx, repoKey)
	if err != nil {
		return gen.Guide{}, err
	}
	f, err := guideFormat(repo.Format, client)
	if err != nil {
		return gen.Guide{}, err
	}
	registry, registryKey, err := setupAddress(ctx, f, repoKey, "The guide")
	if err != nil {
		return gen.Guide{}, err
	}
	ctx, acc, err := requestHolder(ctx, ps.Decider, repoKey)
	if err != nil {
		return gen.Guide{}, err
	}
	target, mayPublish := s.publishTarget(acc, repo)
	requiresAuth := true
	if keys, err := acc.ReadableAnonymously(ctx, []string{repoKey}); err == nil {
		requiresAuth = len(keys) == 0
	}
	in := guideInput{
		Format: f, Class: repo.Class, Client: client, Anonymous: acc.Subject.Anonymous, ExposureRules: exposureRules(ctx),
		TokenEnvVar: ps.TokenEnvVar, MayPublish: mayPublish,
		Data: formats.SetupData{
			RegistryURL: registry, Repository: repoKey, Endpoint: acc.Subject.Endpoint, RegistryKey: registryKey, RequiresAuth: requiresAuth,
		},
	}
	if target != nil {
		in.PublishTarget = *target
	}
	return renderGuide(ps.Guides, in)
}

// GetFormatGuide implements GET /api/v1/formats/{format}/guide: the guide of a class
// without a repository, open to every caller that may list the formats.
func (s *Server) GetFormatGuide(w http.ResponseWriter, r *http.Request, format string, params gen.GetFormatGuideParams) {
	ctx := r.Context()
	if err := s.anonymousGate(ctx); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ps := s.o.Packages
	if ps.Guides == nil {
		httpserver.Problem(w, r, unavailable("The guide renderer"))
		return
	}
	out, err := formatGuide(ctx, ps, format, formats.Class(params.Class), params.Client)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

func formatGuide(ctx context.Context, ps PackageServices, format string, class formats.Class, client string) (gen.Guide, error) {
	if _, ok := formats.Lookup(format); !ok {
		return gen.Guide{}, errs.E(errs.NotFound, errs.CodeNotFound, "No format has this name; GET /api/v1/formats lists the formats.")
	}
	f, err := guideFormat(format, client)
	if err != nil {
		return gen.Guide{}, err
	}
	if !f.Descriptor().Supports(class) {
		return gen.Guide{}, badParam("class", "is not a class of the format "+format)
	}
	registry, registryKey, err := setupAddress(ctx, f, formats.GuideRepositoryPlaceholder, "The guide")
	if err != nil {
		return gen.Guide{}, err
	}
	sub := SubjectFrom(ctx)
	return renderGuide(ps.Guides, guideInput{
		Format: f, Class: class, Client: client, Anonymous: sub.Anonymous, ExposureRules: exposureRules(ctx), TokenEnvVar: ps.TokenEnvVar,
		Data: formats.SetupData{RegistryURL: registry, Endpoint: sub.Endpoint, RegistryKey: registryKey, RequiresAuth: true},
	})
}

// guideFormat returns the format of a guide and checks the client against the
// format's clients.
func guideFormat(format, client string) (formats.Format, error) {
	f, ok := formats.Lookup(format)
	if !ok || len(f.Guide()) == 0 {
		return nil, badParam("client", "is not offered for the format "+format)
	}
	var ids []string
	for _, c := range f.Descriptor().Clients {
		ids = append(ids, c.ID)
	}
	if !slices.Contains(ids, client) {
		return nil, badParam("client", "is not one of "+strings.Join(ids, ", "))
	}
	return f, nil
}

// exposureRules reports whether the endpoint the request arrived on serves under
// exposure rules.
func exposureRules(ctx context.Context) bool {
	ep := httpserver.EndpointFrom(ctx)
	return ep != nil && ep.ExposureMode() == config.ExposureRules
}

// guideInput is everything a guide is made of.
type guideInput struct {
	Format formats.Format
	Class  formats.Class
	Client string
	// Data is the data of the setup templates; RequiresAuth is cleared for an
	// anonymous caller, as the client setup clears it for credentials=none.
	Data formats.SetupData
	// PublishTarget names the target only when MayPublish.
	PublishTarget string
	MayPublish    bool
	Anonymous     bool
	ExposureRules bool
	TokenEnvVar   string
}

// renderGuide runs the sections that apply to the class and the client in topic
// order, rewrites the credential reference by the caller's mode, renders the
// Markdown and attaches the setup snippets.
func renderGuide(rd *readme.Renderer, in guideInput) (gen.Guide, error) {
	creds := credEnv
	if in.Anonymous {
		creds = credNone
		in.Data.RequiresAuth = false
	}
	setup := setupInput{
		Templates: in.Format.SetupTemplates(), Client: in.Client, Credentials: creds, TokenEnvVar: in.TokenEnvVar,
		Data: in.Data, MayPublish: in.MayPublish,
	}
	snippets, err := renderSnippets(setup)
	if err != nil {
		return gen.Guide{}, errs.Wrap(err, "api.renderGuide")
	}
	data := formats.GuideData{
		SetupData: in.Data, Class: in.Class, Client: in.Client, Anonymous: in.Anonymous,
		ExposureRules: in.ExposureRules, TokenEnvVar: in.TokenEnvVar,
	}
	if in.MayPublish {
		data.PublishTarget = in.PublishTarget
	}
	out := gen.Guide{
		Format: string(in.Format.Descriptor().ID), Class: gen.GuideClass(in.Class), Client: in.Client,
		RegistryUrl: in.Data.RegistryURL, Credentials: gen.GuideCredentials(creds), ExposureRules: in.ExposureRules,
		Sections: []gen.GuideSection{},
	}
	if in.Data.Repository != "" {
		key := in.Data.Repository
		out.Repository = &key
	}
	if data.PublishTarget != "" {
		t := data.PublishTarget
		out.PublishTarget = &t
	}
	for _, sec := range orderedSections(in.Format.Guide()) {
		if !sec.AppliesTo(in.Class, in.Client) {
			continue
		}
		gs, ok, err := guideSection(rd, sec, data, setup, snippets)
		if err != nil {
			return gen.Guide{}, err
		}
		if ok {
			out.Sections = append(out.Sections, gs)
		}
	}
	return out, nil
}

// guideSection renders one section; ok is false for a section that says nothing.
func guideSection(rd *readme.Renderer, sec formats.GuideSection, data formats.GuideData, setup setupInput, snippets []snippet) (gen.GuideSection, bool, error) {
	md, err := formats.ExecuteGuide(sec, data)
	if err != nil {
		return gen.GuideSection{}, false, errs.Wrap(err, "api.guideSection "+sec.ID)
	}
	md = applyCredentials(md, setup)
	gs := gen.GuideSection{Id: sec.ID, Topic: gen.GuideSectionTopic(sec.Topic), Title: sec.Title, Snippets: []gen.ClientSnippet{}}
	if sec.Snippets != "" {
		for _, sn := range snippets {
			if sn.Tab == sec.Snippets {
				gs.Snippets = append(gs.Snippets, clientSnippet(sn))
			}
		}
	}
	if strings.TrimSpace(md) == "" && len(gs.Snippets) == 0 {
		return gen.GuideSection{}, false, nil
	}
	if strings.TrimSpace(md) != "" {
		gs.Html = rd.Render(md).HTML
	}
	shown := []string{md, gs.Html}
	for _, sn := range gs.Snippets {
		shown = append(shown, sn.Content)
	}
	if slices.ContainsFunc(shown, holdsTokenShape) {
		// Not reachable with a section over GuideData; a defect that would otherwise
		// hand a token to a caller.
		return gen.GuideSection{}, false, errs.E(errs.Internal, errs.CodeInternal, "A guide section holds a token.")
	}
	return gs, true, nil
}

// orderedSections sorts the sections by topic, stable within a topic.
func orderedSections(secs []formats.GuideSection) []formats.GuideSection {
	topics := formats.GuideTopics()
	out := slices.Clone(secs)
	slices.SortStableFunc(out, func(a, b formats.GuideSection) int {
		return slices.Index(topics, a.Topic) - slices.Index(topics, b.Topic)
	})
	return out
}
