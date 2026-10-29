package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sort"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// PermissionSystemRead is the permission of the installation facts and the
// replica registry (R-API-038).
const PermissionSystemRead = "system.read"

// Identity names the replica that answers.
type Identity struct {
	Replica string // the instance ID (pod name)
	Role    string // web, worker or all
}

// Access decides whether the caller holds a permission. The authentication
// stage of the chain has already turned credentials into a principal; the
// identity packages replace the M0 implementation in the composition root.
type Access interface {
	// Require returns nil, or an *errs.Error: Unauthenticated for an anonymous
	// caller who needs credentials, Forbidden for a caller without the permission.
	Require(ctx context.Context, permission string) error
}

// ReplicaLister lists the replica registry (R-OPS-003).
type ReplicaLister interface {
	Replicas(ctx context.Context) ([]gen.Replica, error)
}

// Facts are the installation facts the composition root knows and this package
// cannot derive: they come from system_state, the configuration and the
// infrastructure clients (R-OPS-004). A block that is not available is nil.
type Facts struct {
	InstallationID *string
	Incarnation    *string
	Database       *gen.DatabaseFacts
	Valkey         gen.ValkeyFacts
	ObjectStore    gen.ObjectStoreFacts
	Tracing        bool
	Webhooks       bool
	ConfigAsCode   bool
	Formats        []string
	MemoryLimit    int64
}

// FactSource supplies Facts.
type FactSource interface {
	Facts(ctx context.Context) (Facts, error)
}

// Options configure the handlers.
type Options struct {
	Document *Document
	Identity Identity
	// Ready reports whether the replica may take work (R-OPS-001); ping answers from it.
	Ready func() bool
	// PublicVersion is system.public_version.
	PublicVersion bool
	Access        Access
	Replicas      ReplicaLister
	Facts         FactSource
	// Formats is the list of served package formats; empty in M0.
	Formats []string
}

// Server implements gen.ServerInterface.
type Server struct {
	o Options
}

var _ gen.ServerInterface = (*Server)(nil)

// New returns the handlers.
func New(o Options) *Server { return &Server{o: o} }

// Register mounts the operations on the router. The caller then runs
// Document.CheckRoutes over Router.Patterns() once every route is registered.
func (s *Server) Register(r *httpserver.Router) {
	gen.HandlerWithOptions(s, gen.StdHTTPServerOptions{
		BaseRouter:       r,
		ErrorHandlerFunc: bindError,
	})
}

// bindError renders a parameter binding failure of the generated wrapper.
func bindError(w http.ResponseWriter, r *http.Request, err error) {
	httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalid, "A request parameter is malformed.", errs.WithCause(err)))
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}

// GetPing implements GET /api/v1/system/ping (R-OPS-002).
func (s *Server) GetPing(w http.ResponseWriter, _ *http.Request) {
	if s.o.Ready != nil && !s.o.Ready() {
		writeText(w, http.StatusServiceUnavailable, "UNAVAILABLE")
		return
	}
	writeText(w, http.StatusOK, "OK")
}

// GetVersion implements GET /api/v1/system/version (R-OPS-002, Q-OPS-3).
func (s *Server) GetVersion(w http.ResponseWriter, r *http.Request) {
	if !s.o.PublicVersion && httpserver.PrincipalFrom(r.Context()).Anonymous {
		httpserver.Problem(w, r, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated,
			"This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer")))
		return
	}
	writeJSON(w, r, http.StatusOK, s.version())
}

func (s *Server) version() gen.Version {
	bi := buildinfo.Get()
	formats := s.o.Formats
	if formats == nil {
		formats = []string{}
	}
	return gen.Version{Version: bi.Version, Revision: bi.Commit, BuildTime: bi.Date, GoVersion: bi.GoVersion, Formats: formats}
}

// GetSystemInfo implements GET /api/v1/system/info (R-OPS-004).
func (s *Server) GetSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.o.Access.Require(ctx, PermissionSystemRead); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	facts, err := s.o.Facts.Facts(ctx)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.systemInfo"))
		return
	}
	replicas, err := s.o.Replicas.Replicas(ctx)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.systemInfo"))
		return
	}
	info := httpserver.InfoFrom(ctx)
	bi := buildinfo.Get()

	var out gen.SystemInfo
	out.InstallationId, out.Incarnation = facts.InstallationID, facts.Incarnation
	out.AnsweredBy.Replica, out.AnsweredBy.Role = s.o.Identity.Replica, gen.SystemInfoAnsweredByRole(s.o.Identity.Role)
	out.Version.Version, out.Version.Revision, out.Version.GoVersion = bi.Version, bi.Commit, bi.GoVersion
	out.BaseUrl, out.BaseUrlSource = info.BaseURL, gen.SystemInfoBaseUrlSource(info.BaseURLSource)
	out.MaintenanceMode = "normal"
	out.Replicas = Summarise(replicas)
	out.Database, out.Valkey, out.ObjectStore = facts.Database, facts.Valkey, facts.ObjectStore
	if out.Valkey.DegradedReplicas == nil {
		n := 0
		for _, rep := range replicas {
			if !rep.Lost && rep.Valkey != nil && rep.Valkey.State == gen.ValkeyViewStateDegraded {
				n++
			}
		}
		out.Valkey.DegradedReplicas = &n
	}
	out.Features.Tracing, out.Features.Webhooks, out.Features.ConfigAsCode = facts.Tracing, facts.Webhooks, facts.ConfigAsCode
	out.Formats = facts.Formats
	if out.Formats == nil {
		out.Formats = []string{}
	}
	out.Runtime.Goos, out.Runtime.Goarch, out.Runtime.Gomaxprocs = runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0)
	out.Runtime.MemoryLimitBytes = facts.MemoryLimit
	writeJSON(w, r, http.StatusOK, out)
}

// Summarise folds the replica list into the per-installation summary of
// R-OPS-004. A lost replica is neither live nor ready.
func Summarise(replicas []gen.Replica) gen.ReplicaSummary {
	sum := gen.ReplicaSummary{Versions: map[string]int{}, Roles: map[string]gen.RoleSummary{}}
	nodes := map[string]map[string]bool{}
	for _, rep := range replicas {
		if rep.Lost {
			continue
		}
		sum.Live++
		ready := rep.State == gen.Ready
		if ready {
			sum.Ready++
		}
		sum.Versions[rep.Version]++
		for _, role := range rep.Roles {
			rs := sum.Roles[string(role)]
			rs.Live++
			if ready {
				rs.Ready++
			}
			if len(rep.Degraded) > 0 {
				rs.Degraded++
			}
			if rep.Node != nil {
				if nodes[string(role)] == nil {
					nodes[string(role)] = map[string]bool{}
				}
				nodes[string(role)][*rep.Node] = true
			}
			rs.Nodes = len(nodes[string(role)])
			sum.Roles[string(role)] = rs
		}
	}
	return sum
}

// ListReplicas implements GET /api/v1/system/replicas (R-OPS-003).
func (s *Server) ListReplicas(w http.ResponseWriter, r *http.Request) {
	if err := s.o.Access.Require(r.Context(), PermissionSystemRead); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	items, err := s.o.Replicas.Replicas(r.Context())
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listReplicas"))
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []gen.Replica{}
	}
	writeJSON(w, r, http.StatusOK, gen.ReplicaList{Items: items})
}

// GetOpenApiJson implements GET /api/v1/openapi.json (R-API-002).
func (s *Server) GetOpenApiJson(w http.ResponseWriter, r *http.Request) {
	b, err := s.o.Document.JSON(httpserver.InfoFrom(r.Context()).BaseURL)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.openapi"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// GetOpenApiYaml implements GET /api/v1/openapi.yaml (R-API-002).
func (s *Server) GetOpenApiYaml(w http.ResponseWriter, r *http.Request) {
	b, err := s.o.Document.YAML(httpserver.InfoFrom(r.Context()).BaseURL)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.openapi"))
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// GetProblemType implements GET /api/v1/problems/{code} (R-API-009).
func (s *Server) GetProblemType(w http.ResponseWriter, r *http.Request, code string) {
	info, ok := errs.Lookup(code)
	if !ok {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No problem code has this name."))
		return
	}
	status := errs.E(info.Kind, info.Code, "").Status()
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSONKeepCache(w, r, http.StatusOK, gen.ProblemType{Code: info.Code, Title: info.Title, Status: status, Description: info.Detail})
}

// writeJSONKeepCache is writeJSON for answers that may be cached.
func writeJSONKeepCache(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
