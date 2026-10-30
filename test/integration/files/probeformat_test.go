//go:build integration

package files

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// probeStore is the data port of the probe format: what its hooks reach through
// event.Data (Q-ARCH-193).
type probeStore interface{ Name() string }

type probePort struct{}

func (probePort) Name() string { return "probe-port" }

// probeFormat is a package format of the test: a package is a name, a version is
// "<name>-<version>.tgz" below "<name>/-/", and the hooks keep the generic package
// rows. It is registered for the whole test binary, so the repository model accepts
// the format and the dispatcher serves its routes.
type probeFormat struct{ formats.Base }

var _ formats.Format = probeFormat{}

// probeLog records what the hooks of the probe format were told.
var probeLog = struct {
	mu      sync.Mutex
	removed []formats.ItemsEvent
	data    []any
	failFor string
}{}

func probeReset() {
	probeLog.mu.Lock()
	defer probeLog.mu.Unlock()
	probeLog.removed, probeLog.data, probeLog.failFor = nil, nil, ""
}

type lowerNames struct{}

func (lowerNames) Normalize(name string) (string, bool) { return strings.ToLower(name), name != "" }

func init() { formats.Register(probeFormat{}) }

func (probeFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "probe", DisplayName: "Probe", Classes: []formats.Class{formats.Local}, Layout: "probe",
			ProtocolBasePath: "/api/probe/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
		},
		ContractVersion: formats.ContractVersion, Names: lowerNames{}, PropertyPrefix: "probe.",
		DataPort: reflect.TypeFor[probeStore](),
	}
}

func (f probeFormat) Routes() []formats.Route {
	return []formats.Route{
		{
			Name: "probe.package.get", Method: http.MethodGet, Template: "/{package}", Examples: []string{"left-pad"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: f.packageDoc,
		},
		{
			Name: "probe.file.get", Method: http.MethodGet, Template: "/{package}/-/{file}", Examples: []string{"left-pad/-/left-pad-1.0.0.tgz"},
			Action: formats.ActionRead, Kind: formats.Download, Handler: f.fileGet,
		},
		{
			Name: "probe.file.put", Method: http.MethodPut, Template: "/{package}/-/{file}", Examples: []string{"left-pad/-/left-pad-1.0.0.tgz"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Handler: f.filePut,
		},
		{
			Name: "probe.tag.put", Method: http.MethodPut, Template: "/{package}/tag/{tag}", Examples: []string{"left-pad/tag/latest"},
			Action: formats.ActionDeploy, Kind: formats.Admin, Handler: f.tagPut,
		},
		{
			Name: "probe.leak.get", Method: http.MethodGet, Template: "/{package}/leak", Examples: []string{"left-pad/leak"},
			Action: formats.ActionRead, Kind: formats.Volatile, Handler: f.leak,
		},
	}
}

func (f probeFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(f.Routes(), m, p)
}

func (probeFormat) Classify(p formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.Content}
}

func (probeFormat) PackageDocumentPath(name string) (formats.Path, bool) {
	return formats.Path{Path: name}, true
}

func (probeFormat) Exposure() formats.ExposureAdapter { return probeExposure{} }

// WriteError is the probe protocol's own error shape: a JSON body of its own and a
// header a client could key on. The chain's refusals reach it too (Q-ARCH-214).
func (probeFormat) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e, _ := errs.As(err)
	status, code := http.StatusInternalServerError, "internal"
	if e != nil {
		status, code = e.Status(), e.Code
		if len(e.Allow) > 0 {
			w.Header().Set("Allow", strings.Join(e.Allow, ", "))
		}
		if e.Challenge != "" {
			w.Header().Set("WWW-Authenticate", e.Challenge)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Probe-Error", code)
	w.WriteHeader(status)
	detail := ""
	if e != nil {
		detail = e.Detail
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"probe_error": code, "detail": detail})
}

// ServeCheck refuses the items of a package whose name contains "tombstoned":
// format state the generic item row does not carry (R-ARCH-008).
func (probeFormat) ServeCheck(_ context.Context, _ formats.Host, _ formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	if strings.Contains(it.Path, "tombstoned") {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The version was removed.")
	}
	return nil
}

func (probeFormat) OnCommit(ctx context.Context, _ formats.Tx, c formats.CommitEvent) error {
	probeLog.mu.Lock()
	probeLog.data = append(probeLog.data, c.Data)
	fail := probeLog.failFor
	probeLog.mu.Unlock()
	if c.Inspection == nil { // a raw upload through /files/ keeps no package rows
		return nil
	}
	if fail != "" && c.Inspection.Package == fail {
		return errors.New("the probe format refused the package")
	}
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, c.Inspection.Package, c.Inspection.Package)
	if err != nil {
		return err
	}
	item := c.Item
	_, err = c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{Version: c.Inspection.Version, LeadItem: &item})
	if err != nil {
		return err
	}
	return c.Packages.SetProperties(ctx, c.Item, map[string]string{"probe.name": c.Inspection.Package})
}

func (probeFormat) OnItemsRemoved(ctx context.Context, _ formats.Tx, e formats.ItemsEvent) error {
	probeLog.mu.Lock()
	probeLog.removed = append(probeLog.removed, e)
	probeLog.mu.Unlock()
	for _, it := range e.Items {
		if it.Version != 0 {
			if err := e.Packages.TrashOrDeleteVersion(ctx, it.Version, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (probeFormat) packageDoc(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := rq.Params.Get("package")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	vs, rev, err := rq.Host.Packages().Versions(ctx, rq.RepoRef(), name)
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		return errs.E(errs.NotFound, errs.CodeNotFound, "No such package.")
	}
	doc := map[string]any{"name": name, "rev": int64(rev), "base": rq.Host.Links(rq).Repository()}
	var versions []string
	for _, v := range vs {
		versions = append(versions, v.Version)
	}
	doc["versions"] = versions
	if rq.URL.Query().Get("strip") != "" { // a handler that tries to weaken the headers
		w.Header().Del("Content-Security-Policy")
		w.Header().Del("X-Content-Type-Options")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(doc)
}

func (probeFormat) fileGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := rq.Params.Get("package")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), rq.Path)
	if err != nil {
		return err
	}
	if !found {
		return errs.E(errs.NotFound, errs.CodeNotFound, "No such file.")
	}
	return rq.Host.Items().Serve(ctx, rq, w, it, formats.ServeMayRevalidate)
}

func (probeFormat) filePut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, file := rq.Params.Get("package"), rq.Params.Get("file")
	version := strings.TrimSuffix(strings.TrimPrefix(file, name+"-"), ".tgz")
	// The identity is what the file name declares; a real format reads it from the
	// content. Authorization checks that name (R-ARCH-014 step 5).
	sp, err := rq.Host.Ingest().Spool(ctx, rq, rq.Body, formats.IngestOptions{})
	if err != nil {
		return err
	}
	defer rq.Host.Ingest().Close(sp)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Version: version, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	_, err = rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
		Path: rq.Path, MediaType: "application/gzip", CreateOnly: true,
		Inspection: &formats.Inspection{Package: name, Version: version},
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

// tagPut changes a package without content: only the side row of the format would
// change, so the handler raises the revision itself (BumpRevision).
func (probeFormat) tagPut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := rq.Params.Get("package")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	repo := rq.RepoRef()
	err := rq.Host.Write(ctx, rq, func(_ formats.Tx, wr formats.PackageWriter) error {
		pkg, _, err := wr.EnsureLocked(ctx, repo, name, name)
		if err != nil {
			return err
		}
		_, _, err = wr.BumpRevision(ctx, pkg)
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// leak is a route of kind Volatile that returns package-derived bytes: a contract
// violation the conformance kit catches (R-ARCH-010 check 7). The dispatcher does not
// wrap it, so it must not receive the content headers.
func (probeFormat) leak(_ context.Context, _ *formats.Request, w http.ResponseWriter) error {
	_, _ = w.Write([]byte("{}"))
	return nil
}

// probeExposure is the exposure adapter of the probe format: narrowing by package.
type probeExposure struct{}

func (probeExposure) ValidateRange(string) error     { return errors.New("no versions") }
func (probeExposure) Prerelease(string) (bool, bool) { return false, false }
func (probeExposure) InRange(string, string) bool    { return false }
func (probeExposure) Dimensions() formats.ExposureDimensions {
	return formats.ExposureDimensions{Packages: true}
}

func (probeExposure) FilterDocument(_ context.Context, _ formats.Path, d []byte, _ formats.ExposureView) ([]byte, error) {
	return d, nil
}
