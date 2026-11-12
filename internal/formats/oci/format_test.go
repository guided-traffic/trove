package oci

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/servefacts"
)

// The contract-level tests of the skeleton. They carry no requirement ID: the requirements of the OCI page are counted as
// implemented when the work package that builds their behaviour names them in its tests.

// TestR_REPO_062_TheOCIPresetsAreTheFourRegistriesTheOwnerNamed: Docker Hub (the default, its token service auth.docker.io
// receiving the credential), Quay, GitHub (ghcr.io) and GitLab (its token service gitlab.com receiving it), in that order.
func TestR_REPO_062_TheOCIPresetsAreTheFourRegistriesTheOwnerNamed(t *testing.T) {
	want := []catalog.UpstreamPreset{
		{
			ID: "docker-hub", DisplayName: "Docker Hub", URL: "https://registry-1.docker.io/", Default: true,
			ExtraHosts: []catalog.PresetHost{{Pattern: "auth.docker.io", SendCredentials: true}},
		},
		{ID: "quay", DisplayName: "Quay", URL: "https://quay.io/"},
		{ID: "ghcr", DisplayName: "GitHub (ghcr.io)", URL: "https://ghcr.io/"},
		{
			ID: "gitlab", DisplayName: "GitLab", URL: "https://registry.gitlab.com/",
			ExtraHosts: []catalog.PresetHost{{Pattern: "gitlab.com", SendCredentials: true}},
		},
	}
	if got := New().Descriptor().UpstreamPresets; !reflect.DeepEqual(got, want) {
		t.Fatalf("presets = %+v", got)
	}
}

func TestDescriptorDeclaresTheRegistryRoot(t *testing.T) {
	d := New().Descriptor()
	if d.ID != "oci" || !d.RegistryRoot || d.ProtocolBasePath != "/v2/{project}/{key}/" {
		t.Fatalf("descriptor = %+v", d.Facts)
	}
	if !d.Supports(formats.Local) || !d.Supports(formats.Remote) || !d.Supports(formats.Virtual) {
		t.Errorf("classes = %v; the format registers local, remote and virtual", d.Classes)
	}
	if d.ProbePath != "v2/" || !d.ProbeChallengeReachable {
		t.Errorf("probe %q reachable on challenge %v", d.ProbePath, d.ProbeChallengeReachable)
	}
	if !d.VerifyAnnouncedDigests || d.MaxDocumentBytes != 4<<20 || d.DataPort != reflect.TypeFor[Store]() {
		t.Errorf("verify %v, document bytes %d, port %v", d.VerifyAnnouncedDigests, d.MaxDocumentBytes, d.DataPort)
	}
	if err := formats.Validate(New()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationFillsTheCatalogWithTheRegistryRoot(t *testing.T) {
	reg := catalog.NewRegistry()
	reg.Register(formats.CatalogEntry(New()))
	e, ok := reg.Lookup("oci")
	if !ok || !e.RegistryRoot || !e.HasSettings() || e.Names == nil {
		t.Fatalf("entry = %+v, %v", e.Facts, ok)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a second format declared the registry root")
		}
	}()
	other := formats.CatalogEntry(New())
	other.ID = "oci-2"
	reg.Register(other)
}

func TestTheKeyAndTheImageNameAreOneAddress(t *testing.T) {
	// The image reference of the page: host, project key, repository key, image name. The repository part is the base path of the
	// format with the keys filled in.
	got := qname.ProtocolBase(New().Descriptor().ProtocolBasePath, qname.Repo{Project: "web", Key: "docker"})
	if got != "/v2/web/docker/" {
		t.Fatalf("base = %q", got)
	}
}

// builtRoutes are the routes that have a handler; a work package that builds an operation adds its routes here.
var builtRoutes = map[string]bool{
	routeManifestGetTag: true, routeManifestGetDgs: true, routeManifestPut: true, routeBlobGet: true,
	routeTagsList: true, routeReferrers: true, routeManifestDelete: true, routeBlobDelete: true,
	routeUploadStart: true, routeUploadStatus: true, routeUploadPatch: true, routeUploadPut: true, routeUploadCancel: true,
}

func TestTheRoutesThatAreNotBuiltAnswer501(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Data: unbuiltStore{}})
	for _, r := range f.Routes() {
		if builtRoutes[r.Name] {
			continue
		}
		for _, ex := range r.Examples {
			resp, err := h.Serve(context.Background(), r.Method, ex, nil, false)
			if err != nil {
				t.Fatalf("%s %s: %v", r.Method, ex, err)
			}
			if resp.Code != http.StatusNotImplemented {
				t.Errorf("%s %s: status %d, want 501: %s", r.Method, ex, resp.Code, resp.Body)
				continue
			}
			var body struct {
				Errors []struct{ Code, Message string } `json:"errors"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 || body.Errors[0].Code != "UNSUPPORTED" {
				t.Errorf("%s %s: body %s (%v)", r.Method, ex, resp.Body, err)
			}
		}
	}
}

func TestRoutesMatchTheProtocolPaths(t *testing.T) {
	f := New()
	const dgst = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	for _, tc := range []struct {
		method, path, route string
		params              formats.Params
	}{
		{"GET", "app/manifests/1", routeManifestGetTag, formats.Params{"name": "app", "reference": "1"}},
		{"HEAD", "team/app/manifests/latest", routeManifestGetTag, formats.Params{"name": "team/app", "reference": "latest"}},
		{"GET", "team/app/manifests/" + dgst, routeManifestGetDgs, formats.Params{"name": "team/app", "reference": dgst}},
		{"PUT", "a/manifests/1", routeManifestPut, nil},
		{"DELETE", "a/manifests/" + dgst, routeManifestDelete, nil},
		{"GET", "a/blobs/" + dgst, routeBlobGet, formats.Params{"name": "a", "digest": dgst}},
		{"DELETE", "a/b/c/blobs/" + dgst, routeBlobDelete, nil},
		{"POST", "a/blobs/uploads/", routeUploadStart, formats.Params{"name": "a"}},
		{"GET", "a/b/blobs/uploads/0e4b8d38", routeUploadStatus, formats.Params{"name": "a/b", "session": "0e4b8d38"}},
		{"PATCH", "a/blobs/uploads/s", routeUploadPatch, nil},
		{"PUT", "a/blobs/uploads/s", routeUploadPut, nil},
		{"DELETE", "a/blobs/uploads/s", routeUploadCancel, nil},
		{"GET", "a/tags/list", routeTagsList, formats.Params{"name": "a"}},
		{"GET", "a/referrers/" + dgst, routeReferrers, formats.Params{"name": "a", "digest": dgst}},
		// A name may hold the words of the protocol; the operation is read from the end.
		{"GET", "blobs/manifests/manifests/1", routeManifestGetTag, formats.Params{"name": "blobs/manifests", "reference": "1"}},
		// Nothing is decoded by the matcher: an encoded character stays in the name, which the grammar refuses (below).
		{"GET", "a%2Fb/manifests/1", routeManifestGetTag, formats.Params{"name": "a%2Fb", "reference": "1"}},
	} {
		r, params, err := f.Match(tc.method, tc.path)
		if err != nil || r.Name != tc.route {
			t.Errorf("%s %s: route %v, %v; want %s", tc.method, tc.path, r, err, tc.route)
			continue
		}
		for k, v := range tc.params {
			if params[k] != v {
				t.Errorf("%s %s: param %s = %q, want %q", tc.method, tc.path, k, params[k], v)
			}
		}
	}
	for _, p := range []string{"", "app", "app/manifests", "app/manifests/", "/", "app//manifests/1", "app/tags/other", "manifests/1"} {
		if r, _, err := f.Match("GET", p); err == nil {
			t.Errorf("GET %q matched %s", p, r.Name)
		}
	}
	// Without its trailing slash the upload start is a blob with a malformed digest, which the handler answers DIGEST_INVALID.
	if r, _, err := f.Match("GET", "app/blobs/uploads"); err != nil || r.Name != routeBlobGet {
		t.Errorf("GET app/blobs/uploads: %v, %v", r, err)
	}
	if _, _, err := f.Match("POST", "app/manifests/1"); errs.KindOf(err) != errs.MethodNotAllowed {
		t.Errorf("POST on a manifest: %v", err)
	}
}

// TestWritesAreServedByLocalAndVirtualRepositoriesOnly: a remote repository answers 405 for a write, and a write addressed to a
// virtual repository reaches the handler with the virtual repository as the addressed one (the dispatcher's redirect to the deployment
// target would drop it).
func TestWritesAreServedByLocalAndVirtualRepositoriesOnly(t *testing.T) {
	for _, r := range New().Routes() {
		write := r.Action != formats.ActionRead
		if write != (len(r.Classes) == 2 && r.Classes[0] == formats.Local && r.Classes[1] == formats.Virtual) {
			t.Errorf("route %s: action %v, classes %v", r.Name, r.Action, r.Classes)
		}
	}
}

func TestR_OCI_008_WriteErrorMapsEveryRowOfTheTable(t *testing.T) {
	type row struct {
		name   string
		err    error
		status int
		code   string
		header map[string]string
	}
	rows := []row{
		{"name invalid", errs.E(errs.Invalid, errs.CodeOCINameInvalid, "x"), 400, "NAME_INVALID", nil},
		{"tag invalid", errs.E(errs.Invalid, errs.CodeOCITagInvalid, "x"), 400, "TAG_INVALID", nil},
		{"digest invalid", errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "x"), 400, "DIGEST_INVALID", nil},
		{"an unclean path", errs.E(errs.Invalid, errs.CodeInvalidPath, "x"), 400, "NAME_INVALID", nil},
		{"repository unknown", errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "x"), 404, "NAME_UNKNOWN", nil},
		{"manifest unknown", errs.E(errs.NotFound, errs.CodeOCIManifestUnknown, "x"), 404, "MANIFEST_UNKNOWN", nil},
		{"blob unknown", errs.E(errs.NotFound, errs.CodeOCIBlobUnknown, "x"), 404, "BLOB_UNKNOWN", nil},
		{"upload unknown", errs.E(errs.NotFound, errs.CodeOCIBlobUploadUnknown, "x"), 404, "BLOB_UPLOAD_UNKNOWN", nil},
		{"chunk out of order", errs.E(errs.RangeNotSatisfiable, errs.CodeOCIBlobUploadInvalid, "x", errs.WithHeader("Range", "0-9")), 416, "BLOB_UPLOAD_INVALID", map[string]string{"Range": "0-9"}},
		{"manifest invalid", errs.E(errs.Invalid, errs.CodeOCIManifestInvalid, "x"), 400, "MANIFEST_INVALID", nil},
		{"manifest references an unknown blob", errs.E(errs.Invalid, errs.CodeOCIManifestBlobUnknown, "x"), 400, "MANIFEST_BLOB_UNKNOWN", nil},
		{"too large", errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, "x"), 413, "SIZE_INVALID", nil},
		{"quota", errs.E(errs.InsufficientStorage, errs.CodeInsufficientStorage, "x"), 413, "SIZE_INVALID", nil},
		{
			"unauthenticated", errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "x", errs.WithChallenge(`Bearer realm="https://trove.example.com/v2/_token"`)), 401, "UNAUTHORIZED",
			map[string]string{"WWW-Authenticate": `Bearer realm="https://trove.example.com/v2/_token"`},
		},
		{"denied", errs.E(errs.Forbidden, errs.CodeForbidden, "x"), 403, "DENIED", nil},
		{"not offered", errs.E(errs.Invalid, errs.CodeOCIUnsupported, "x"), 400, "UNSUPPORTED", nil},
		{"method not allowed", errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "x", errs.WithAllow("GET", "HEAD")), 405, "UNSUPPORTED", map[string]string{"Allow": "GET, HEAD"}},
		{"rate limit", errs.E(errs.RateLimited, errs.CodeRateLimited, "x", errs.WithRetryAfter(3*time.Second)), 429, "TOOMANYREQUESTS", map[string]string{"Retry-After": "3"}},
		{"an upstream 429", errs.E(errs.Unavailable, errs.CodeUpstreamRateLimited, "x", errs.WithRetryAfter(21600*time.Second)), 503, "TOOMANYREQUESTS", map[string]string{"Retry-After": "21600"}},
		{"unavailable", errs.E(errs.Unavailable, errs.CodeUnavailable, "x", errs.WithRetryAfter(time.Second)), 503, "UNAVAILABLE", map[string]string{"Retry-After": "1"}},
		{"upstream failure", errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "x"), 502, "UNAVAILABLE", nil},
		{"upstream timeout", errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "x"), 504, "UNAVAILABLE", nil},
		{"not implemented", errs.E(errs.NotImplemented, errs.CodeNotImplemented, "x"), 501, "UNSUPPORTED", nil},
		{"internal", errs.E(errs.Internal, errs.CodeInternal, "secret detail"), 500, "UNKNOWN", nil},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			New().WriteError(w, httptest.NewRequest(http.MethodGet, "/v2/web/docker/app/manifests/1", nil), tc.err)
			if w.Code != tc.status {
				t.Errorf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			var body struct {
				Errors []struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Detail  any    `json:"detail"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 || body.Errors[0].Code != tc.code {
				t.Fatalf("body %s (%v), want code %s", w.Body, err, tc.code)
			}
			if body.Errors[0].Detail != nil || body.Errors[0].Message == "" {
				t.Errorf("entry = %+v", body.Errors[0])
			}
			if strings.Contains(w.Body.String(), "secret detail") {
				t.Errorf("an internal error leaks its detail: %s", w.Body)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := w.Header().Get("Docker-Distribution-API-Version"); got != "registry/2.0" {
				t.Errorf("Docker-Distribution-API-Version = %q", got)
			}
			for k, v := range tc.header {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
	t.Run("a HEAD request gets the headers and no body", func(t *testing.T) {
		w := httptest.NewRecorder()
		New().WriteError(w, httptest.NewRequest(http.MethodHead, "/v2/web/docker/app/manifests/1", nil), errs.E(errs.NotFound, errs.CodeOCIManifestUnknown, "x"))
		if w.Code != 404 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
			t.Errorf("%d, %d body bytes, Content-Length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
		}
	})
}

func TestInspectRefusesAnOversizedOrMalformedManifest(t *testing.T) {
	f := New()
	for name, body := range map[string]string{
		"not JSON":       "FROM scratch",
		"a JSON array":   "[1,2]",
		"nested 100,000": strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000),
		"two documents":  `{} {}`,
	} {
		in := formats.InspectInput{
			Path: formats.Path{Path: ManifestPath("app", digestZero)}, Body: formattest.Spool([]byte(body)),
			Limits: f.Descriptor().Limits,
		}
		if _, err := f.Inspect(context.Background(), in); err == nil {
			t.Errorf("%s: accepted", name)
		} else if k := errs.KindOf(err); k != errs.Invalid && k != errs.TooLarge {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := formats.InspectInput{
		Path: formats.Path{Path: TagPath("team/app", "1")}, Limits: f.Descriptor().Limits,
		Body: formattest.Spool([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)),
	}
	if got, err := f.Inspect(context.Background(), ok); err != nil || got == nil || got.Package != "team/app" {
		t.Errorf("a manifest: %+v, %v", got, err)
	}
	// A blob is not read: layers are never parsed.
	blob := formats.InspectInput{Path: formats.Path{Path: BlobPath("app", digestZero)}, Body: formattest.Spool([]byte("not json")), Limits: f.Descriptor().Limits}
	if got, err := f.Inspect(context.Background(), blob); err != nil || got != nil {
		t.Errorf("a blob: %+v, %v", got, err)
	}
}

func TestAnImageNameOutsideTheGrammarAnswersNameInvalidBeforeTheHandler(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Data: unbuiltStore{}})
	const dgst = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	// "it" and "main" are the project and the key of the kit's repository: the whole name is "it/main/<name>".
	long := strings.Repeat("a", 255-len("it/main/")+1)
	for _, c := range [][2]string{
		{"GET", "App/manifests/1"},
		{"PUT", "App/manifests/1"},
		{"GET", "app/Sub/manifests/1"},
		{"GET", "a%2Fb/manifests/1"},
		{"GET", "a%2fb/blobs/" + dgst},
		{"GET", "a%41/tags/list"},
		{"GET", "app-/manifests/1"},
		{"POST", "a__/blobs/uploads/"},
		{"GET", "_tags/manifests/1"},
		{"GET", long + "/manifests/1"},
	} {
		resp, err := h.Serve(context.Background(), c[0], c[1], nil, false)
		if err != nil || resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), `"NAME_INVALID"`) {
			t.Errorf("%s %q: %v %d %s", c[0], c[1], err, resp.Code, resp.Body)
		}
	}
	// The longest name that fits is accepted by the check: the handler answers, and the repository holds no such tag.
	ok := strings.Repeat("a", 255-len("it/main/"))
	if resp, err := h.Serve(context.Background(), http.MethodGet, ok+"/manifests/1", nil, false); err != nil || resp.Code != http.StatusNotFound || !strings.Contains(resp.Body.String(), `"MANIFEST_UNKNOWN"`) {
		t.Errorf("a name of 255 bytes with the repository: %v %v", err, resp)
	}
}

// memTombstones is the in-memory twin of the part of the data port the serve check reads.
type memTombstones struct {
	Store
	manifests, withheld map[[32]byte]bool
}

func (m memTombstones) Manifest(_ context.Context, _ int64, _ string, d [32]byte) (rows.Manifest, bool, error) {
	return rows.Manifest{}, m.manifests[d], nil
}

func (m memTombstones) Tombstone(_ context.Context, _ int64, _ string, d [32]byte) (rows.Tombstone, bool, error) {
	return rows.Tombstone{}, m.withheld[d], nil
}

// The serve check of a manifest and a tag item decides on the facts the item lookup read, or on the data port when it did not: a
// withheld manifest and a manifest without its row are a missing manifest; a blob and a remote repository are not checked
// (R-OCI-033, Q-OCI-33).
func TestServeCheckWithholdsAManifestTheJournalReplayWithheld(t *testing.T) {
	f := New()
	const dgst = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	local := formats.RepoRef{ID: 7, Name: qname.Repo{Project: "it", Key: "main"}, Class: formats.Local, Format: ID}
	check := func(repo formats.RepoRef, path string, facts servefacts.Facts, h formats.Host) error {
		return f.ServeCheck(context.Background(), h, repo, formats.ItemRef{Repo: repo, Path: path, Facts: facts}, formats.ServeStored)
	}
	h := formattest.NewHost(f, formattest.HostOptions{Data: unbuiltStore{}})
	for _, path := range []string{ManifestPath("app", dgst), TagPath("team/app", "1")} {
		key := f.ServeFactsKey(local, formats.Path{Path: path})
		if key.Kind != formats.ServeOCILocal || key.Package == "" {
			t.Fatalf("key of %q = %+v", path, key)
		}
		if err := check(local, path, servefacts.Facts{Key: key, OCIManifest: true}, h); err != nil {
			t.Errorf("%s served: %v", path, err)
		}
		for name, facts := range map[string]servefacts.Facts{
			"withheld":        {Key: key, OCIManifest: true, Tombstoned: true},
			"without its row": {Key: key},
		} {
			if err := check(local, path, facts, h); errs.KindOf(err) != errs.NotFound {
				t.Errorf("%s %s: %v", path, name, err)
			}
		}
	}
	for name, c := range map[string]struct {
		repo formats.RepoRef
		path string
	}{
		"a blob":              {local, BlobPath("app", dgst)},
		"a remote repository": {formats.RepoRef{ID: 8, Name: local.Name, Class: formats.Remote, Format: ID}, ManifestPath("app", dgst)},
		"a path outside":      {local, "app/other"},
	} {
		if k := f.ServeFactsKey(c.repo, formats.Path{Path: c.path}); !k.Zero() {
			t.Errorf("%s has the key %+v", name, k)
		}
		if err := check(c.repo, c.path, servefacts.Facts{}, h); err != nil {
			t.Errorf("%s is checked: %v", name, err)
		}
	}
	t.Run("without facts the data port decides", func(t *testing.T) {
		body := []byte(`{"schemaVersion":2}`)
		sum := sha256.Sum256(body)
		gh := formattest.NewHost(f, formattest.HostOptions{Data: memTombstones{manifests: map[[32]byte]bool{sum: true}, withheld: map[[32]byte]bool{}}})
		path := TagPath("app", "1")
		gh.PutItem(path, "application/json", body)
		repo := gh.Ref()
		if err := check(repo, path, servefacts.Facts{}, gh); err != nil {
			t.Fatalf("a manifest with its row: %v", err)
		}
		gh2 := formattest.NewHost(f, formattest.HostOptions{Data: memTombstones{manifests: map[[32]byte]bool{sum: true}, withheld: map[[32]byte]bool{sum: true}}})
		gh2.PutItem(path, "application/json", body)
		if err := check(gh2.Ref(), path, servefacts.Facts{}, gh2); errs.KindOf(err) != errs.NotFound {
			t.Fatalf("a withheld manifest: %v", err)
		}
	})
}
