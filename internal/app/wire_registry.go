package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/config"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	ociformat "github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// The registry root of an endpoint (R-OCI-001, R-OCI-003, Q-OCI-100): one enforcement point of the registry tokens' grants, the
// token service, the version check, the catalog, and the dispatcher's handler for everything below /v2/, mounted through the
// enforcement point as the only handler there. An endpoint whose surface leaves out the packages family has none, and the
// route table then admits no pattern below /v2/ (rt.registry stays nil); a replica without a format host (the shell profile)
// serves no registry either.

// The OCI data port offers the page of image names the catalog reads (R-OCI-017).
var _ registry.CatalogStore = (*ocidata.Store)(nil)

// ociUploadStart is the route that starts an upload and may mount a blob (R-OCI-012).
const ociUploadStart = "oci.upload.start"

// ociMountSource parses the `from` of a blob mount the way the format's handler does (the format's exported parser comes with the
// work package of the uploads and replaces this body: ociformat.MountSource). On a bound host `from` is an image name of the bound
// repository; otherwise "<projectKey>/<repoKey>/<name>", a repository key with the cache suffix naming the base repository.
func ociMountSource(from string, bound qname.Repo) (repo qname.Repo, name string, cache, ok bool) {
	if !bound.IsZero() {
		if !ociformat.ValidName(from) {
			return qname.Repo{}, "", false, false
		}
		return bound, from, false, true
	}
	parts := strings.SplitN(from, "/", 3)
	if len(parts) != 3 {
		return qname.Repo{}, "", false, false
	}
	key, cache := strings.CutSuffix(parts[1], qname.CacheSuffix)
	repo = qname.Repo{Project: parts[0], Key: key}
	if !repo.Valid() || !ociformat.ValidName(parts[2]) || !ociformat.ValidFullName(repo.Project, repo.Key, parts[2]) {
		return qname.Repo{}, "", false, false
	}
	return repo, parts[2], cache, true
}

// registerRegistry mounts the registry root on the router of one endpoint and sets rt.registry.
func (a *App) registerRegistry(rt *endpointRuntime, router *httpserver.Router) error {
	disp := part[formatHostArea](a.parts).disp
	if disp == nil || !rt.ep.Serves(config.SurfacePackages) {
		return nil
	}
	f := disp.RegistryFormat()
	if f == nil {
		return nil
	}
	au := a.authStage()
	stage, ok := au.stage.(*auth.Stage)
	if !ok || au.registry == nil {
		return errors.New("registry root: the authentication stage has no registry signer")
	}
	repos, decider := part[repositoryArea](a.parts).reader, part[filesArea](a.parts).decider
	metrics, err := a.registryTokenMetrics()
	if err != nil {
		return err
	}
	enforcer, err := registry.NewEnforcer(registry.EnforcerOptions{
		Matcher: disp, WriteError: f.WriteError, ValidName: ociformat.ValidName,
		Log: a.log.With("component", "registry"), MountRoute: ociUploadStart, MountSource: ociMountSource,
	})
	if err != nil {
		return err
	}
	token, err := registry.NewToken(registry.TokenOptions{
		Authenticate: a.registryTokenAuth(rt, stage), Signer: au.registry, Decider: decider, Repos: repos,
		Format: string(f.Descriptor().ID), ValidName: ociformat.ValidName, Documents: registry.NewDocuments(f), Metrics: metrics, Audit: a.auditRecorder(),
		WriteError: f.WriteError, Bound: disp.BoundRepository, Log: a.log.With("component", "registry-token"),
	})
	if err != nil {
		return err
	}
	store, ok := part[formatPortsArea](a.parts).bindings.Get(f.Descriptor().ID).(registry.CatalogStore)
	if !ok {
		return fmt.Errorf("registry root: the data port of %s offers no page of image names for the catalog", f.Descriptor().ID)
	}
	catalog, err := registry.NewCatalog(registry.CatalogOptions{
		Repos: repos, Decider: decider, Store: store, Format: string(f.Descriptor().ID), Documents: registry.NewDocuments(f), Bound: disp.BoundRepository, WriteError: f.WriteError,
	})
	if err != nil {
		return err
	}
	if err := registry.Register(registry.RegisterOptions{
		Router: router, Enforcer: enforcer, Dispatcher: disp, Format: f,
		Version: registry.NewVersion(registry.VersionOptions{WriteError: f.WriteError}), Catalog: catalog, Token: token,
	}); err != nil {
		return fmt.Errorf("registry root of endpoint %s: %w", rt.cfg.Name, err)
	}
	rt.registry = enforcer
	return nil
}

// registryTokenMetrics is the one set of counters of the token service, shared by every endpoint (R-OCI-034): it registers on
// the metrics registry once.
func (a *App) registryTokenMetrics() (*registrytoken.Metrics, error) {
	if a.registryMetrics != nil {
		return a.registryMetrics, nil
	}
	m, err := registrytoken.NewMetrics(a.tele.metrics.Registry())
	if err != nil {
		return nil, err
	}
	a.registryMetrics = m
	return m, nil
}

// registryTokenAuth runs the one credential of a token request through the authentication stage with the rules of the endpoint
// (R-OCI-004 step 3, Q-OCI-51) and converts the result to the caller the chain stores, as the chain's stage does: the minimum
// epoch of the request is read into the principal (R-AUTHZ-032), and the caller is recorded for the request log, so a token
// request the service authenticated names its caller there. The chain does not authenticate this route, so a rate-limit class
// keyed by principal sees a token request as anonymous and counts it in the bucket of the client address.
func (a *App) registryTokenAuth(rt *endpointRuntime, stage *auth.Stage) func(*http.Request, auth.Presented) (httpserver.Principal, error) {
	rules := a.credentialRules(rt)
	return func(r *http.Request, c auth.Presented) (httpserver.Principal, error) {
		p, err := withMinEpoch(func(r *http.Request) (httpserver.Principal, error) {
			res, err := stage.AuthenticateTokenRequest(r, rules, c)
			if err != nil {
				return httpserver.Principal{}, err
			}
			return principalFromResult(res), nil
		})(r)
		if err == nil {
			recordPrincipal(r, p)
		}
		return p, err
	}
}

// registryScope is the stage's Options.RegistryScope: the scope entry the challenge of a request below /v2/ names, from the
// enforcement point of the endpoint the request arrived on (the dispatcher's match and the mounted route's action).
func (a *App) registryScope(r *http.Request) string {
	ep := httpserver.EndpointFrom(r.Context())
	if ep == nil {
		return ""
	}
	for _, rt := range a.eps {
		if rt.cfg.Name == ep.Name() {
			return rt.registry.Scope(r)
		}
	}
	return ""
}
