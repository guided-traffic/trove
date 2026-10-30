// Package testformat is the conformance kit's own test format: a small, complete
// package format (routes, hooks, a data port, a remote and a virtual adapter,
// setup templates) that passes all ten checks of R-ARCH-010, and a set of named
// defects, each of which breaks exactly the rule one check exists for. The kit's
// negative tests run the defective variants through formattest.Evaluate and assert
// that the check the spec names fails (the acceptance criteria of R-ARCH-010).
//
// A package is a name ("left-pad", "@scope/name"); a version is stored as
// "<name>/-/<base>-<version>.tgz", a gzip-compressed tar archive whose
// package/package.json holds the name and the version.
package testformat

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "testfmt"

// Defect names one way the test format breaks the contract.
type Defect string

// The defects, with the check each one is meant to fail.
const (
	// AmbiguousRoutes registers a second route for scoped names that the first
	// route's matcher accepts as well: check 1.
	AmbiguousRoutes Defect = "ambiguous-routes"
	// CleaningMatch makes Match clean the path itself instead of refusing a path
	// that is not clean: check 2.
	CleaningMatch Defect = "cleaning-match"
	// ContentMetadata classifies a package document as content: check 3.
	ContentMetadata Defect = "content-metadata"
	// NondeterministicRewrite lets Rewrite put a clock reading into the document:
	// check 4.
	NondeterministicRewrite Defect = "nondeterministic-rewrite"
	// UpstreamLeak lets Rewrite keep a link to the upstream: check 4.
	UpstreamLeak Defect = "upstream-leak"
	// MergeLastWins lets the highest (tier, position) part win a version: check 5.
	MergeLastWins Defect = "merge-last-wins"
	// InspectAccepts makes Inspect accept every input: check 6.
	InspectAccepts Defect = "inspect-accepts"
	// InspectWrongKind makes Inspect answer 400 for what is a bound crossed: check 6.
	InspectWrongKind Defect = "inspect-wrong-kind"
	// InspectHeap makes Inspect hold 640 MiB before it refuses: check 6.
	InspectHeap Defect = "inspect-heap"
	// TarballVolatile registers the tarball route with kind Volatile instead of
	// Download: check 7.
	TarballVolatile Defect = "tarball-volatile"
	// TemplateCredential puts a credential option into a setup template: check 8.
	TemplateCredential Defect = "template-credential" //nolint:gosec // G101: the name of a defect, not a credential
	// NoInstallTemplate leaves the client without an install template: check 8.
	NoInstallTemplate Defect = "no-install-template"
	// TagWithoutRevision lets the tag route write the tag to the format's own table
	// and not raise the package's revision: check 9.
	TagWithoutRevision Defect = "tag-without-revision"
	// NamesCaseSensitive lets the name rule keep the case of a spelling: check 10.
	NamesCaseSensitive Defect = "names-case-sensitive"
	// WrongDocumentPath lets PackageDocumentPath answer a path no GET route
	// resolves: check 10.
	WrongDocumentPath Defect = "wrong-document-path"
)

// Option adjusts the format; it is not a defect.
type Option string

// StripHeaders makes every content handler delete the content headers before and
// after it writes, as a careless handler would. The core puts them back
// (R-ARCH-007), so the variant passes check 7.
const StripHeaders Option = "strip-headers"

// Format is the test format.
type Format struct {
	formats.Base
	flags map[string]bool
}

var _ formats.Format = Format{}

// New returns the test format with the given defects and options.
func New(flags ...string) Format {
	f := Format{flags: map[string]bool{}}
	for _, x := range flags {
		f.flags[x] = true
	}
	return f
}

// With returns New with Defect or Option values.
func With[T ~string](flags ...T) Format {
	s := make([]string, len(flags))
	for i, x := range flags {
		s[i] = string(x)
	}
	return New(s...)
}

func (f Format) has(x string) bool { return f.flags[x] }

// Store is the data port of the format: the tags it keeps beside the generic rows.
type Store interface {
	SetTag(pkg, tag, version string)
	Tags(pkg string) map[string]string
}

// MemStore is the in-memory twin of the port.
type MemStore struct {
	mu   sync.Mutex
	tags map[string]map[string]string
}

var _ Store = (*MemStore)(nil)

// SetTag implements Store.
func (m *MemStore) SetTag(pkg, tag, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tags == nil {
		m.tags = map[string]map[string]string{}
	}
	if m.tags[pkg] == nil {
		m.tags[pkg] = map[string]string{}
	}
	m.tags[pkg][tag] = version
}

// Tags implements Store.
func (m *MemStore) Tags(pkg string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.tags[pkg] {
		out[k] = v
	}
	return out
}

var nameRule = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

type names struct{ caseSensitive bool }

func (n names) Normalize(name string) (string, bool) {
	if !n.caseSensitive {
		name = strings.ToLower(name)
	}
	return name, nameRule.MatchString(strings.ToLower(name))
}

// Descriptor implements formats.Format.
func (f Format) Descriptor() formats.Descriptor {
	ttl := 300
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: ID, DisplayName: "Test format", Classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual},
			Layout: "testfmt", ProtocolBasePath: "/api/testfmt/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
			DefaultMetadataTTLSeconds: &ttl, OwnershipDefault: catalog.OwnershipEnforce,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{caseSensitive: f.has(string(NamesCaseSensitive))},
		Clients:          []formats.Client{{ID: "tf", DisplayName: "tf", Versions: []string{"1"}}},
		PropertyPrefix:   "tf.",
		MaxDocumentBytes: 4 << 20,
		DataPort:         reflect.TypeFor[Store](),
	}
}

// splitName splits an escaped path into the package name and the rest. A scoped
// name is "@scope/name" in two segments or in one with an encoded slash.
func splitName(escaped string) (pkg string, rest []string, ok bool) {
	var segs []string
	for s := range strings.SplitSeq(strings.Trim(escaped, "/"), "/") {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" {
			return "", nil, false
		}
		segs = append(segs, d)
	}
	if len(segs) == 0 || segs[0] == "-" {
		return "", nil, false
	}
	if strings.HasPrefix(segs[0], "@") {
		if strings.Contains(segs[0], "/") {
			pkg, rest = segs[0], segs[1:]
		} else if len(segs) >= 2 {
			pkg, rest = segs[0]+"/"+segs[1], segs[2:]
		} else {
			return "", nil, false
		}
	} else {
		pkg, rest = segs[0], segs[1:]
	}
	if !nameRule.MatchString(strings.ToLower(pkg)) {
		return "", nil, false
	}
	return pkg, rest, true
}

// matcher builds a route matcher over splitName: the rest of the path must be the
// given literal segments, then (when param is not empty) one more segment.
func matcher(lit []string, param string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		pkg, rest, ok := splitName(escaped)
		if !ok || len(rest) != len(lit)+btoi(param != "") {
			return nil, false
		}
		for i, l := range lit {
			if rest[i] != l {
				return nil, false
			}
		}
		p := formats.Params{"package": pkg}
		if param != "" {
			p[param] = rest[len(lit)]
		}
		return p, true
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func exact(want ...string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		segs := strings.Split(strings.Trim(escaped, "/"), "/")
		if len(segs) != len(want) {
			return nil, false
		}
		for i := range want {
			if d, err := url.PathUnescape(segs[i]); err != nil || d != want[i] {
				return nil, false
			}
		}
		return formats.Params{}, true
	}
}

// Routes implements formats.Format.
func (f Format) Routes() []formats.Route {
	tarballKind := formats.Download
	if f.has(string(TarballVolatile)) {
		tarballKind = formats.Volatile
	}
	rs := []formats.Route{
		{
			Name: "tf.doc.get", Method: http.MethodGet, Template: "/{package}", Examples: []string{"left-pad", "@scope/name", "@scope%2Fname"},
			Action: formats.ActionRead, Kind: formats.Metadata, Match: matcher(nil, ""), Handler: f.docGet,
		},
		{
			Name: "tf.tarball.get", Method: http.MethodGet, Template: "/{package}/-/{file}",
			Examples: []string{"left-pad/-/left-pad-1.0.0.tgz", "@scope/name/-/name-1.0.0.tgz"},
			Action:   formats.ActionRead, Kind: tarballKind, Match: matcher([]string{"-"}, "file"), Handler: f.tarballGet,
		},
		{
			Name: "tf.tarball.put", Method: http.MethodPut, Template: "/{package}/-/{file}",
			Examples: []string{"left-pad/-/left-pad-1.0.0.tgz", "@scope/name/-/name-1.0.0.tgz"},
			Action:   formats.ActionDeploy, Kind: formats.Upload, Match: matcher([]string{"-"}, "file"), Handler: f.tarballPut,
		},
		{
			Name: "tf.tarball.delete", Method: http.MethodDelete, Template: "/{package}/-/{file}",
			Examples: []string{"left-pad/-/left-pad-1.0.0.tgz"},
			Action:   formats.ActionDelete, Kind: formats.Delete, Match: matcher([]string{"-"}, "file"), Handler: f.tarballDelete,
		},
		{
			Name: "tf.tag.put", Method: http.MethodPut, Template: "/{package}/tags/{tag}", Examples: []string{"left-pad/tags/latest"},
			Action: formats.ActionDeploy, Kind: formats.Admin, Match: matcher([]string{"tags"}, "tag"), Handler: f.tagPut,
		},
		{
			Name: "tf.search.get", Method: http.MethodGet, Template: "/-/search", Examples: []string{"-/search"},
			Action: formats.ActionRead, Kind: formats.Volatile, Match: exact("-", "search"), Handler: f.search,
		},
		{
			Name: "tf.all.get", Method: http.MethodGet, Template: "/-/all", Examples: []string{"-/all"},
			Action: formats.ActionRead, Kind: formats.Computed, Match: exact("-", "all"), Handler: f.all,
		},
	}
	if f.has(string(AmbiguousRoutes)) {
		rs = append(rs, formats.Route{
			Name: "tf.scoped.get", Method: http.MethodGet, Template: "/{scope}/{name}", Examples: []string{"@scope/name"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: f.docGet,
		})
	}
	return rs
}

// Match implements formats.Format.
func (f Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	if f.has(string(CleaningMatch)) {
		escapedPath = path.Clean("/" + escapedPath)
	}
	return formats.MatchRoutes(f.Routes(), method, escapedPath)
}

// Classify implements formats.Format.
func (f Format) Classify(p formats.Path) formats.PathInfo {
	if p.Folder || p.Path == "" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	switch p.Path {
	case "-/search":
		return formats.PathInfo{Kind: formats.PathVolatile}
	case "-/all":
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	pkg, rest, ok := splitName(p.Path)
	if !ok {
		return formats.PathInfo{Kind: formats.Content}
	}
	norm, _ := f.Descriptor().Names.Normalize(pkg)
	switch {
	case len(rest) == 0 && f.has(string(ContentMetadata)):
		return formats.PathInfo{Kind: formats.Content, Package: norm}
	case len(rest) == 0:
		return formats.PathInfo{Kind: formats.PathMetadata, Package: norm}
	case len(rest) == 2 && rest[0] == "-":
		return formats.PathInfo{Kind: formats.Content, Package: norm, Version: versionOf(norm, rest[1])}
	}
	return formats.PathInfo{Kind: formats.PathComputed, Package: norm}
}

// versionOf reads the version out of "<base>-<version>.tgz".
func versionOf(pkg, file string) string {
	base := pkg[strings.LastIndex(pkg, "/")+1:]
	v, ok := strings.CutPrefix(strings.TrimSuffix(file, ".tgz"), base+"-")
	if !ok || !strings.HasSuffix(file, ".tgz") {
		return ""
	}
	return v
}

// PackageDocumentPath implements formats.Format.
func (f Format) PackageDocumentPath(name string) (formats.Path, bool) {
	if f.has(string(WrongDocumentPath)) {
		return formats.Path{Path: name + "/index.json"}, true
	}
	return formats.Path{Path: name}, true
}

// SetupTemplates implements formats.Format.
func (f Format) SetupTemplates() []formats.SetupTemplate {
	configure := "registry={{.RegistryURL}}\n"
	if f.has(string(TemplateCredential)) {
		configure += "//{{.Repository}}/:_authToken=changeme\n"
	}
	ts := []formats.SetupTemplate{
		{ID: "tf.rc.configure", Client: "tf", Section: "configure", Lang: "ini", File: ".tfrc", Template: configure},
		{ID: "tf.publish", Client: "tf", Section: "publish", Lang: "shell", Template: "tf publish --registry {{.RegistryURL}}\n"},
	}
	if !f.has(string(NoInstallTemplate)) {
		ts = append(ts, formats.SetupTemplate{ID: "tf.install", Client: "tf", Section: "install", Lang: "shell", Template: "tf install {{.Scope}}/name\n"})
	}
	return ts
}

// manifest is package/package.json of an upload.
type manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Inspect implements formats.Format: the defects of the Inspect check, then the
// inspection of the PUT route.
func (f Format) Inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	switch {
	case f.has(string(InspectAccepts)):
		return &formats.Inspection{Package: "x", Version: "1"}, nil
	case f.has(string(InspectWrongKind)):
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The upload is refused.")
	case f.has(string(InspectHeap)):
		hold := make([]byte, 640<<20)
		for i := 0; i < len(hold); i += 4096 {
			hold[i] = 1
		}
		time.Sleep(100 * time.Millisecond)
		runtime.KeepAlive(hold)
		return nil, untrusted.TooLarge("The upload is too large.")
	}
	return f.inspect(ctx, in)
}

// inspect reads the manifest of the upload with the readers of
// internal/formats/untrusted, by the extension of the path: .tgz is a tar.gz
// archive, .zip a zip archive, .json and .xml a document.
func (f Format) inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	ctx, cancel := in.Limits.Context(ctx)
	defer cancel()
	var m manifest
	switch path.Ext(in.Path.Path) {
	case ".tgz":
		body, err := in.Body.Open()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		tr := untrusted.NewTarGz(ctx, body, in.Limits)
		found := false
		for {
			e, err := tr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			if e.Name == "package/package.json" {
				data, err := tr.ReadEntry(0)
				if err != nil {
					return nil, err
				}
				if err := untrusted.UnmarshalJSON(data, in.Limits, &m); err != nil {
					return nil, err
				}
				found = true
			}
		}
		if !found {
			return nil, untrusted.Refused("The archive has no package/package.json.")
		}
	case ".zip":
		ra, err := in.Body.ReaderAt()
		if err != nil {
			return nil, err
		}
		z, err := untrusted.NewZip(ctx, ra, in.Body.Size(), in.Limits)
		if err != nil {
			return nil, err
		}
		for _, e := range z.Entries() {
			if e.Name != "package.json" {
				continue
			}
			data, err := z.ReadEntry(e, 0)
			if err != nil {
				return nil, err
			}
			if err := untrusted.UnmarshalJSON(data, in.Limits, &m); err != nil {
				return nil, err
			}
		}
	case ".json":
		body, err := in.Body.Open()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		if err := untrusted.DecodeJSON(body, in.Limits, &m); err != nil {
			return nil, err
		}
	case ".xml":
		body, err := in.Body.Open()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		var x struct {
			Name    string `xml:"metadata>id"`
			Version string `xml:"metadata>version"`
		}
		if err := untrusted.DecodeXML(body, in.Limits, &x); err != nil {
			return nil, err
		}
		m = manifest{Name: x.Name, Version: x.Version}
	default:
		return nil, untrusted.Refused("The file is not a package of this format.")
	}
	norm, ok := f.Descriptor().Names.Normalize(m.Name)
	if !ok || m.Version == "" {
		return nil, untrusted.Refused("The manifest names no package and version.")
	}
	return &formats.Inspection{Package: norm, Version: m.Version, Properties: map[string]string{"tf.name": m.Name}, Payload: m}, nil
}

// OnCommit implements formats.Format: the rows of a published version.
func (f Format) OnCommit(ctx context.Context, _ formats.Tx, c formats.CommitEvent) error {
	if c.Inspection == nil {
		return nil
	}
	pkg, created, err := c.Packages.EnsureLocked(ctx, c.Repo, c.Inspection.Package, c.Inspection.Package)
	if err != nil {
		return err
	}
	if _, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{Version: c.Inspection.Version, LeadItem: &c.Item}); err != nil {
		return err
	}
	if err := c.Packages.SetProperties(ctx, c.Item, c.Inspection.Properties); err != nil {
		return err
	}
	if created {
		c.Data.(Store).SetTag(pkg.Name, "latest", c.Inspection.Version)
	}
	return c.Packages.Notify("tf.package", pkg.Name)
}

// OnItemsRemoved implements formats.Format: a removed item takes its version with
// it.
func (f Format) OnItemsRemoved(ctx context.Context, _ formats.Tx, e formats.ItemsEvent) error {
	for _, it := range e.Items {
		if it.Version != 0 {
			if err := e.Packages.TrashOrDeleteVersion(ctx, it.Version, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// OnItemsRestored implements formats.Format: a restored item changes its
// package's document, so the revision moves.
func (f Format) OnItemsRestored(ctx context.Context, _ formats.Tx, e formats.ItemsEvent) error {
	for _, it := range e.Items {
		pkg, _, ok := splitName(it.Item.Path)
		if !ok {
			continue
		}
		norm, _ := f.Descriptor().Names.Normalize(pkg)
		ref, _, err := e.Packages.EnsureLocked(ctx, e.Repo, norm, norm)
		if err != nil {
			return err
		}
		if _, _, err := e.Packages.BumpRevision(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}

// Remote implements formats.Format.
func (f Format) Remote() formats.RemoteAdapter { return remoteAdapter{f} }

// Virtual implements formats.Format.
func (f Format) Virtual() formats.VirtualAdapter { return virtualAdapter{f} }

// Tarball builds an upload: a tar.gz whose package/package.json names the package
// and its version.
func Tarball(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body, _ := json.Marshal(manifest{Name: name, Version: version})
	_ = tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// document renders the package document of a local repository from its rows.
func (f Format) document(ctx context.Context, rq *formats.Request, name string) ([]byte, bool, error) {
	rows, rev, err := rq.Host.Packages().Versions(ctx, rq.RepoRef(), name)
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	ln := rq.Host.Links(rq)
	base := name[strings.LastIndex(name, "/")+1:]
	versions := map[string]any{}
	for _, v := range rows {
		file := base + "-" + v.Version + ".tgz"
		versions[v.Version] = map[string]any{"dist": map[string]string{"tarball": ln.Path(name + "/-/" + file)}}
	}
	doc := map[string]any{"name": name, "rev": rev, "versions": versions}
	if st, ok := rq.Host.FormatData(ID).(Store); ok {
		doc["dist-tags"] = st.Tags(name)
	}
	b, err := json.Marshal(doc)
	return b, true, err
}

func (f Format) strip(w http.ResponseWriter) {
	if f.has(string(StripHeaders)) {
		for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Cross-Origin-Resource-Policy", "Content-Disposition"} {
			w.Header().Del(h)
		}
	}
}

func (f Format) norm(p string) string {
	n, _ := f.Descriptor().Names.Normalize(p)
	return n
}

func (f Format) docGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := f.norm(rq.Params.Get("package"))
	if name == "" {
		pkg, _, _ := splitName(rq.Escaped)
		name = f.norm(pkg)
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	doc, found, err := f.document(ctx, rq, name)
	if err != nil {
		return err
	}
	if !found {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The package does not exist.")
	}
	f.strip(w)
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(doc)
	f.strip(w)
	return err
}

func (f Format) tarballGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := f.norm(rq.Params.Get("package"))
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	it, ok, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), rq.Path)
	if err != nil {
		return err
	}
	if !ok {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The file does not exist.")
	}
	f.strip(w)
	err = rq.Host.Items().Serve(ctx, rq, w, it, formats.ServeStored)
	f.strip(w)
	return err
}

func (f Format) tarballPut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	host := rq.Host
	sp, err := host.Ingest().Spool(ctx, rq, rq.Body, formats.IngestOptions{})
	if err != nil {
		return err
	}
	defer host.Ingest().Close(sp)
	d := f.Descriptor()
	lim := untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: d.MaxDocumentBytes}).Tighten(d.Limits)
	ins, err := f.inspect(ctx, formats.InspectInput{Path: rq.Path, MediaType: "application/gzip", Body: sp, Limits: lim})
	if err != nil {
		return err
	}
	t := formats.Target{Package: ins.Package, Version: ins.Version, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionDeploy}}
	if err := host.Authorize(ctx, rq, t); err != nil {
		return err
	}
	if _, err := host.Ingest().Commit(ctx, rq, sp, formats.Commit{Path: rq.Path, MediaType: "application/gzip", CreateOnly: true, Inspection: ins}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	_, err = w.Write([]byte(`{"ok":true}`))
	return err
}

func (f Format) tarballDelete(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := f.norm(rq.Params.Get("package"))
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionDelete}}); err != nil {
		return err
	}
	h, ok := rq.Host.(interface {
		Remove(context.Context, *formats.Request, string, ...string) error
	})
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The host removes no items.")
	}
	if err := h.Remove(ctx, rq, "delete", rq.Path.Path); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (f Format) tagPut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, tag := f.norm(rq.Params.Get("package")), rq.Params.Get("tag")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	var version string
	if err := untrusted.DecodeJSON(rq.Body, untrusted.Limits{DocumentBytes: 4 << 10}, &version); err != nil {
		return err
	}
	st := rq.Host.FormatData(ID).(Store)
	err := rq.Host.Write(ctx, rq, func(_ formats.Tx, wr formats.PackageWriter) error {
		pkg, _, err := wr.EnsureLocked(ctx, rq.RepoRef(), name, name)
		if err != nil {
			return err
		}
		if err := wr.Claim(ctx, formats.Target{Package: name}, formats.OwnershipWrite{}); err != nil {
			return err
		}
		st.SetTag(name, tag, version)
		if f.has(string(TagWithoutRevision)) {
			return nil // the tag is a row of the format's own table: nothing raised the revision
		}
		_, _, err = wr.BumpRevision(ctx, pkg)
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func (f Format) search(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{"objects":[],"total":0}`))
	return err
}

func (f Format) all(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	f.strip(w)
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{"format":"testfmt"}`))
	f.strip(w)
	return err
}

// remoteAdapter rewrites the documents of an upstream.
type remoteAdapter struct{ f Format }

func (remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(rc.Upstream.String(), "/")+"/"+p.Path, nil)
}

func (remoteAdapter) AcceptResponse(_ formats.Path, resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream answered with an error.")
	}
	return nil
}

func (remoteAdapter) PrepareFill(_ context.Context, _ formats.RemoteContext, _ formats.Path, _ string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	var doc map[string]any
	if err := untrusted.DecodeJSON(io.NewSectionReader(body, 0, size), untrusted.Limits{}, &doc); err != nil {
		return formats.Prepared{}, err
	}
	return formats.Prepared{}, nil
}

func (remoteAdapter) OnFill(context.Context, formats.Tx, formats.FillEvent) (bool, error) {
	return false, nil
}

func (remoteAdapter) TracksVersionRemoval() bool { return false }

// Rewrite points every download link of the document at this registry. The
// output is JSON with sorted keys, so two runs agree, and a document that was
// rewritten already is left as it is.
func (a remoteAdapter) Rewrite(_ context.Context, _ formats.RemoteContext, _ formats.Path, doc []byte, ln formats.LinkBuilder) ([]byte, error) {
	var d struct {
		Name     string                    `json:"name"`
		Versions map[string]map[string]any `json:"versions"`
	}
	if err := untrusted.UnmarshalJSON(doc, untrusted.Limits{DocumentBytes: 4 << 20}, &d); err != nil {
		return nil, err
	}
	out := map[string]any{"name": d.Name}
	vs := map[string]any{}
	keys := make([]string, 0, len(d.Versions))
	for v := range d.Versions {
		keys = append(keys, v)
	}
	sort.Strings(keys)
	for _, v := range keys {
		entry := d.Versions[v]
		dist, _ := entry["dist"].(map[string]any)
		tarball, _ := dist["tarball"].(string)
		if u, err := url.Parse(tarball); err == nil && tarball != "" && (!a.f.has(string(UpstreamLeak)) || !strings.HasSuffix(v, "-legacy")) {
			dist["tarball"] = ln.Path(d.Name + "/-/" + path.Base(u.Path))
		}
		vs[v] = entry
	}
	out["versions"] = vs
	if a.f.has(string(NondeterministicRewrite)) {
		out["rewrittenAt"] = time.Now().UnixNano()
	}
	return json.Marshal(out)
}

// virtualAdapter merges the documents of the members.
type virtualAdapter struct{ f Format }

func (virtualAdapter) Strategy(formats.Path) formats.Strategy { return formats.Merge }

// Merge takes every version from the lowest (tier, position) part that holds it.
func (a virtualAdapter) Merge(_ context.Context, _ *formats.Request, _ formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	sorted := append([]formats.MemberDocument(nil), parts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Tier != sorted[j].Tier {
			return sorted[i].Tier < sorted[j].Tier
		}
		return sorted[i].Position < sorted[j].Position
	})
	if a.f.has(string(MergeLastWins)) {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	versions := map[string]any{}
	for _, p := range sorted {
		var d struct {
			Versions map[string]any `json:"versions"`
		}
		if err := untrusted.UnmarshalJSON(p.Doc, untrusted.Limits{DocumentBytes: 4 << 20}, &d); err != nil {
			return nil, err
		}
		for v, e := range d.Versions {
			if _, ok := versions[v]; !ok {
				versions[v] = e
			}
		}
	}
	return json.Marshal(map[string]any{"versions": versions})
}

func (a virtualAdapter) LocalDocument(ctx context.Context, rq *formats.Request, _ formats.RepoRef, p formats.Path, _ string) ([]byte, formats.RevSeq, bool, error) {
	doc, found, err := a.f.document(ctx, rq, a.f.norm(p.Path))
	return doc, 0, found, err
}
