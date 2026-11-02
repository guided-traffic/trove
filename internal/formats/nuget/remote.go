package nuget

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// The remote class (R-NUGET-014, R-NUGET-015). The remote engine owns the request path, the
// freshness, the collapsing of concurrent fetches, the negative cache and the circuit
// (R-REPO-025 to R-REPO-031); this file is what NuGet adds to it: where the upstream's
// resources are (the service index), which request fetches a document, what a fetched document
// must be before it is stored, and what is recorded of it in the transaction that stores it.
// The documents a client sees are rendered from the stored ones (handlers_remote.go), and the
// packages are inspected before they are stored (remote_fill.go).

// documentVariant is the one variant of every metadata document a NuGet remote stores: the
// upstream's bytes as they came. The registration hive, the paging and the SemVer 2.0.0 filter
// are decided at serve time, never at fill time (R-NUGET-015). A virtual repository asks its
// remote members for the same bytes under the variants of its merged documents ([VariantFlat],
// [VariantRegistration]); they are the upstream's bytes too (Q-NUGET-67).
const documentVariant = "doc"

// storesVariant says whether a variant is one of the stored upstream documents.
func storesVariant(variant string) bool {
	return variant == documentVariant || variant == VariantFlat || variant == VariantRegistration
}

// Remote returns the format's remote adapter. It is a stable value, because Register records
// the instance it returns.
func (f *Format) Remote() formats.RemoteAdapter { return remoteAdapter{f: f} }

// remoteAdapter embeds the defaults of the contract: NuGet declares no artifact gate (NuGet v3
// announces no package digest, R-NUGET-015) and the pass-through query is the forwarded
// search's.
type remoteAdapter struct {
	formats.RemoteBase
	f *Format
}

var (
	_ formats.RemoteAdapter = remoteAdapter{}
	_ formats.Discoverer    = remoteAdapter{}
	_ formats.FillInspector = remoteAdapter{}
)

// TracksVersionRemoval is false: NuGet does not track the removal of a version, so the generic
// gone-package rule applies to an ID the upstream no longer has, and a version that left the
// upstream's lists keeps being served from the cache by its exact path (R-NUGET-015).
func (remoteAdapter) TracksVersionRemoval() bool { return false }

// kind is the document or file a canonical path of a remote repository addresses.
type docKind int

const (
	kindDiscovery docKind = iota + 1
	kindVersions
	kindRegIndex
	kindRegPage
	kindPackage
	kindSpec
)

// target is a canonical path taken apart.
type target struct {
	kind docKind
	// id is the lower-case ID of every kind but the discovery document, version the URL form of
	// a package file, lower and upper the bounds of a registration page.
	id, version, lower, upper string
}

// parseTarget resolves a canonical path of a remote repository. A path no document of the
// remote class lives at (symbol files, embedded files and the vulnerability documents are later
// work packages) is the 404 of a missing resource.
func parseTarget(p formats.Path) (target, error) {
	missing := errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	if p.Path == canonDiscovery+"/"+indexFile {
		return target{kind: kindDiscovery}, nil
	}
	segs := strings.Split(p.Path, "/")
	if len(segs) < 2 {
		return target{}, missing
	}
	name, ok := lowerID(segs[0])
	if !ok || name != segs[0] {
		return target{}, missing
	}
	t := target{id: name}
	switch {
	case len(segs) == 2 && segs[1] == indexFile:
		t.kind = kindVersions
	case segs[1] == canonRegistration && len(segs) == 3 && segs[2] == indexFile:
		t.kind = kindRegIndex
	case segs[1] == canonRegistration && len(segs) == 5 && segs[2] == registrationPageSeg && strings.HasSuffix(segs[4], jsonSuffix):
		lo, ok1 := urlForm(segs[3])
		hi, ok2 := urlForm(strings.TrimSuffix(segs[4], jsonSuffix))
		if !ok1 || !ok2 || lo != segs[3] || hi != strings.TrimSuffix(segs[4], jsonSuffix) {
			return target{}, missing
		}
		t.kind, t.lower, t.upper = kindRegPage, lo, hi
	case len(segs) == 3:
		v, ok := urlForm(segs[1])
		if !ok || v != segs[1] {
			return target{}, missing
		}
		t.version = v
		switch segs[2] {
		case NupkgFile(name, v):
			t.kind = kindPackage
		case name + nuspecSuffix:
			t.kind = kindSpec
		default:
			return target{}, missing
		}
	default:
		return target{}, missing
	}
	return t, nil
}

// RegistrationPagePath is the canonical path of a registration page of an ID: the page is
// keyed by its bounds, which the upstream's index names, so that a page whose range changed is
// another document and a fresh stored page is never served for a range it does not hold
// (Q-NUGET-51).
func RegistrationPagePath(lowerID, lower, upper string) string {
	return lowerID + "/" + canonRegistration + "/" + registrationPageSeg + "/" + lower + "/" + upper + jsonSuffix
}

// RegistrationIndexPath is the canonical path of the registration index of an ID in the cache of
// a remote repository (R-NUGET-015).
func RegistrationIndexPath(lowerID string) string {
	return lowerID + "/" + canonRegistration + "/" + indexFile
}

// DiscoveryDocument names the service index of the upstream: a metadata document of the
// repository like any other (R-NUGET-015), resolved before the first request that needs an
// address (Q-ARCH-268).
func (remoteAdapter) DiscoveryDocument() (formats.Path, string, bool) {
	return formats.Path{Path: canonDiscovery + "/" + indexFile}, documentVariant, true
}

// ParseDiscovery reads the stored service index. A feed that lacks a required resource is the
// 502 of the first request, naming the resource (Q-NUGET-14).
func (remoteAdapter) ParseDiscovery(_ context.Context, _ formats.RemoteContext, doc []byte) (any, error) {
	return remote.ParseDiscovery(doc)
}

// discovery returns what the engine resolved for the fetch.
func discovery(rc formats.RemoteContext) (*remote.Discovery, error) {
	d, ok := rc.Discovery.(*remote.Discovery)
	if !ok || d == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream addresses of the NuGet remote were requested before its service index was resolved.")
	}
	return d, nil
}

// UpstreamRequest maps a canonical path to the upstream request (R-NUGET-015): the service
// index is the remote's URL itself, every other address comes from the resource the index
// announced for it and the canonical path, never from a host a client named (R-REPO-043).
func (a remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	var u *url.URL
	if t.kind == kindDiscovery {
		c := *rc.Upstream
		c.User, c.Fragment, c.RawFragment = nil, "", ""
		u = &c
	} else {
		d, err := discovery(rc)
		if err != nil {
			return nil, err
		}
		if u, err = upstreamURL(d, t); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	if t.kind != kindPackage && t.kind != kindSpec {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

// upstreamURL is the address of a document or file below the resource that serves it.
func upstreamURL(d *remote.Discovery, t target) (*url.URL, error) {
	switch t.kind {
	case kindVersions:
		return remote.Join(d.Content, t.id, indexFile)
	case kindPackage:
		return remote.Join(d.Content, t.id, t.version, NupkgFile(t.id, t.version))
	case kindSpec:
		return remote.Join(d.Content, t.id, t.version, t.id+nuspecSuffix)
	case kindRegIndex:
		return remote.Join(d.Registration, t.id, indexFile)
	case kindRegPage:
		return remote.Join(d.Registration, t.id, registrationPageSeg, t.lower, t.upper+jsonSuffix)
	}
	return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// UpstreamQuery maps a pass-through search to the upstream request (R-NUGET-016): the search
// resource of the service index with the parameters the format cleaned. It needs the resolved
// service index; the road of Query hands the adapter none yet, which is where the work package
// that forwards the search extends it (Q-NUGET-55).
func (remoteAdapter) UpstreamQuery(ctx context.Context, rc formats.RemoteContext, q formats.Query) (*http.Request, error) {
	d, err := discovery(rc)
	if err != nil {
		return nil, err
	}
	u, ok := d.SearchURL(q.Params)
	if !ok {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream announces no search resource.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// AcceptResponse lets every 2xx response through: a media type that does not belong (an HTML
// page in place of a document) is refused by the engine's block list before a byte is stored
// (R-REPO-042), and a body that is no document by PrepareFill.
func (remoteAdapter) AcceptResponse(formats.Path, *http.Response) error { return nil }

// ExpectsType declares the media types of NuGet's resources, so that the engine does not block
// them even when the block list grows: a document is JSON, a package an octet stream or a ZIP,
// a .nuspec XML.
func (remoteAdapter) ExpectsType(p formats.Path, mediaType string) bool {
	t, err := parseTarget(p)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "application/json":
		return t.kind != kindPackage && t.kind != kindSpec
	case "application/octet-stream", "application/zip", "application/x-zip-compressed":
		return t.kind == kindPackage
	case "application/xml", "text/xml", "application/octet-stream+xml":
		return t.kind == kindSpec
	}
	return t.kind == kindSpec && strings.EqualFold(mediaType, "application/octet-stream")
}

// fillData is what PrepareFill hands OnFill.
type fillData struct{ kind docKind }

// PrepareFill checks a fetched metadata document before anything is stored, outside every
// transaction (R-ARCH-019): an upstream document that is not the document its path promises is
// the 502 nuget_invalid_upstream_document with the previous entry kept. It returns the URL-map
// rows of the addresses that are not the ones derived from the service index: the pages a
// registration index does not inline, and the package content addresses of its leaves
// (R-REPO-043).
func (a remoteAdapter) PrepareFill(_ context.Context, rc formats.RemoteContext, p formats.Path, variant string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	t, err := parseTarget(p)
	if err != nil || !storesVariant(variant) {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "NuGet fills metadata documents of the variants doc, flat and registration only.")
	}
	if size > MaxDocumentBytes {
		return formats.Prepared{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream document is larger than the limit of documents held in memory.")
	}
	doc, err := io.ReadAll(io.NewSectionReader(body, 0, size))
	if err != nil {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The fetched document cannot be read.", errs.WithCause(err))
	}
	out := formats.Prepared{Data: fillData{kind: t.kind}}
	switch t.kind {
	case kindDiscovery:
		_, err = remote.ParseDiscovery(doc)
	case kindVersions:
		_, err = remote.VersionList(doc, MaxDocumentBytes)
	case kindRegIndex:
		out.Mappings, err = a.indexMappings(rc, t, doc)
	case kindRegPage:
		out.Mappings, err = a.pageMappings(rc, t, doc)
	default:
		err = errs.E(errs.Internal, errs.CodeInternal, "NuGet fills metadata documents only.")
	}
	return out, err
}

func (a remoteAdapter) indexMappings(rc formats.RemoteContext, t target, doc []byte) ([]formats.URLMapping, error) {
	idx, err := remote.ParseIndex(doc, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	d, err := discovery(rc)
	if err != nil {
		return nil, err
	}
	var maps []formats.URLMapping
	for _, pg := range idx.Pages {
		if pg.Inline {
			maps = append(maps, packageMappings(d, t.id, pg.Leaves)...)
			continue
		}
		lo, ok1 := urlForm(pg.Lower)
		hi, ok2 := urlForm(pg.Upper)
		if !ok1 || !ok2 {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument,
				"A page of the upstream's registration index has bounds that are no versions.")
		}
		want, err := upstreamURL(d, target{kind: kindRegPage, id: t.id, lower: lo, upper: hi})
		if err != nil {
			return nil, err
		}
		got, err := remote.ContentURL(pg.ID)
		if err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument,
				"A page of the upstream's registration index names an address that cannot be used: "+err.Error())
		}
		if got.String() != want.String() {
			maps = append(maps, formats.URLMapping{Path: formats.Path{Path: RegistrationPagePath(t.id, lo, hi)}, Upstream: got.String()})
		}
	}
	return maps, nil
}

func (a remoteAdapter) pageMappings(rc formats.RemoteContext, t target, doc []byte) ([]formats.URLMapping, error) {
	leaves, err := remote.ParsePage(doc, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	d, err := discovery(rc)
	if err != nil {
		return nil, err
	}
	return packageMappings(d, t.id, leaves), nil
}

// packageMappings records the package content address a leaf names where it is not the address
// the package content resource derives (R-NUGET-015: the leaf's packageContent is used when the
// flat container does not serve the version where it should). A leaf that names no usable
// address is skipped: the derived address stays.
func packageMappings(d *remote.Discovery, lowerID string, leaves []remote.Leaf) []formats.URLMapping {
	var maps []formats.URLMapping
	for _, l := range leaves {
		v, ok := remote.Convert(l, lowerID)
		if !ok || l.PackageContent == "" {
			continue
		}
		got, err := remote.ContentURL(l.PackageContent)
		if err != nil {
			continue
		}
		want, err := upstreamURL(d, target{kind: kindPackage, id: lowerID, version: v.Row.LowerVersion})
		if err != nil || got.String() == want.String() {
			continue
		}
		maps = append(maps, formats.URLMapping{Path: formats.Path{Path: NupkgPath(lowerID, v.Row.LowerVersion)}, Upstream: got.String()})
	}
	return maps
}

// OnFill runs in the transaction that upserts the document's cache entry. NuGet keeps no rows
// of a metadata document: the documents are served from the stored bytes. What it does is the
// early eviction of the merged documents of virtual repositories that contain the package
// (R-REPO-020); nothing depends on the signal for correctness.
func (remoteAdapter) OnFill(_ context.Context, _ formats.Tx, ev formats.FillEvent) (formats.FillDecision, error) {
	t, err := parseTarget(ev.Path)
	if err != nil {
		return formats.FillByFetchTime, err
	}
	if t.kind == kindDiscovery {
		return formats.FillByFetchTime, nil
	}
	return formats.FillByFetchTime, ev.Packages.Notify(catalog.MetadataChangedArea, catalog.PackageChanged(ev.Repo.ID, t.id))
}

// Rewrite renders a stored upstream document the way the semver2 hive of the serving
// repository shows it, with the default paging: the registration index and page (inlined
// leaves only), and the version list. The request path renders with the hive and the paging of
// the request (handlers_remote.go), which this is one instance of; the service index is the
// repository's own and does not depend on the upstream's (R-NUGET-015).
func (a remoteAdapter) Rewrite(_ context.Context, _ formats.RemoteContext, p formats.Path, doc []byte, links formats.LinkBuilder) ([]byte, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	feed := registration.Feed(links.Repository() + FeedSegment + "/")
	switch t.kind {
	case kindDiscovery:
		return ServiceIndex(string(feed), false), nil
	case kindVersions:
		list, err := remote.VersionList(doc, MaxDocumentBytes)
		if err != nil {
			return nil, err
		}
		return found(renderVersionList(list, nil))
	case kindRegIndex, kindRegPage:
		var leaves []remote.Leaf
		if t.kind == kindRegIndex {
			idx, err := remote.ParseIndex(doc, MaxDocumentBytes)
			if err != nil {
				return nil, err
			}
			for _, pg := range idx.Pages {
				leaves = append(leaves, pg.Leaves...)
			}
		} else if leaves, err = remote.ParsePage(doc, MaxDocumentBytes); err != nil {
			return nil, err
		}
		return found(renderRegistration(registrationRender{
			feed: feed, hive: registration.SemVer2, lowerID: t.id, paging: registration.Paging{},
			versions: remote.Merge(leaves, t.id), kind: regKindOf(t.kind), lower: t.lower, upper: t.upper,
		}))
	}
	return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// found turns the answer of a renderer into the result of Rewrite: a document that does not
// exist is the 404 of a missing resource.
func found(body []byte, ok bool, err error) ([]byte, error) {
	if err == nil && !ok {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	return body, err
}

func regKindOf(k docKind) registrationKind {
	if k == kindRegPage {
		return kindPage
	}
	return kindIndex
}

// sameID reports whether the ID a package declares is the one asked for, under Unicode simple
// case folding (R-NUGET-003).
func sameID(declared, asked string) bool { return id.SameID(declared, asked) }

// sortedURLForms returns the version strings in URL form, deduplicated and ascending by
// precedence; a string that is no version is left out.
func sortedURLForms(list []string) []string {
	type entry struct {
		v   version.Version
		url string
	}
	seen := map[string]bool{}
	var es []entry
	for _, s := range list {
		v, err := version.Parse(s)
		if err != nil || seen[v.URLForm()] {
			continue
		}
		seen[v.URLForm()] = true
		es = append(es, entry{v, v.URLForm()})
	}
	sort.SliceStable(es, func(i, j int) bool { return version.Compare(es[i].v, es[j].v) < 0 })
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.url
	}
	return out
}
