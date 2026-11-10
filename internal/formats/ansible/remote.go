package ansible

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// The remote class (R-ANS-016 to R-ANS-018, R-ANS-020). The remote engine owns the request path, the freshness, the collapsing of
// concurrent fetches, the negative cache and the circuit (R-REPO-025 to R-REPO-031); this file is what the Galaxy adds to it: where
// the upstream's documents are (its API root), what a fetched document must be before it is stored, and what is recorded of it in the
// transaction that stores it. The documents a client sees are rendered from the stored ones (handlers_remote.go), the digest rule is
// remote_gate.go and the inspection of a fetched archive remote_fill.go.

// remoteAdapter embeds the defaults of the contract: no pass-through query (the Galaxy has no Volatile route a remote forwards).
type remoteAdapter struct {
	formats.RemoteBase
	f *Format
}

var (
	_ formats.RemoteAdapter = remoteAdapter{}
	_ formats.Discoverer    = remoteAdapter{}
	_ formats.FillInspector = remoteAdapter{}
)

// TracksVersionRemoval is false: a version the upstream drops from its list leaves the served list at the next revalidation and its
// cached archive stays downloadable by path (R-ANS-017, H-848).
func (remoteAdapter) TracksVersionRemoval() bool { return false }

// AcceptResponse lets every 2xx response through, except a role archive that declares more than ansible.role_archive_max_bytes
// (R-ANS-020, R-ANS-031); a body of unknown length is bounded as it is read. A media type that does not belong (an HTML page in place
// of a document) is refused by the engine's block list before a byte is stored (R-REPO-042), and a body that is no document by
// PrepareFill.
func (remoteAdapter) AcceptResponse(p formats.Path, resp *http.Response) error {
	t, err := parseTarget(p)
	if err != nil {
		return nil //nolint:nilerr // a path that is no document of the remote is not a role archive
	}
	if t.kind != kindRoleArchive {
		return nil
	}
	limit := roleArchiveLimit()
	if resp.ContentLength > limit {
		return roleArchiveTooLarge(limit)
	}
	resp.Body = &limitedBody{rc: resp.Body, left: limit, limit: limit}
	return nil
}

func roleArchiveTooLarge(limit int64) error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge,
		"The role archive is larger than ansible.role_archive_max_bytes ("+strconv.FormatInt(limit, 10)+" bytes).")
}

// limitedBody fails a read that goes past the limit with the error of an item that is too large.
type limitedBody struct {
	rc    io.ReadCloser
	left  int64
	limit int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte decides whether the body ended exactly at the limit.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, roleArchiveTooLarge(b.limit)
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// ExpectsType declares the media types of the Galaxy's resources, so that the engine does not block them even when the block list
// grows: a document is JSON, an archive a gzip stream or an octet stream.
func (remoteAdapter) ExpectsType(p formats.Path, mediaType string) bool {
	t, err := parseTarget(p)
	if err != nil {
		return false
	}
	archive := t.kind == kindArchive || t.kind == kindRoleArchive
	switch strings.ToLower(mediaType) {
	case "application/json":
		return !archive
	case "application/octet-stream", "application/gzip", "application/x-gzip", "application/x-tar", "application/x-compressed-tar", "binary/octet-stream":
		return archive
	}
	return false
}

// fillData is what PrepareFill hands OnFill.
type fillData struct {
	kind docKind
	pkg  string
	size int64
	// announced is the digest a version document announces.
	announced rows.Announced
	// dropped is the number of role versions the host rule removed from a role's version list.
	dropped int
}

// PrepareFill checks a fetched metadata document before anything is stored, outside every transaction (R-ARCH-019): an upstream
// document that is not the document its path promises is the 502 upstream_unavailable with the previous entry kept. It returns the
// URL-map rows of the archives the document names (R-REPO-043) and the digest a version document announces.
func (a remoteAdapter) PrepareFill(_ context.Context, rc formats.RemoteContext, p formats.Path, variant string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	t, err := parseTarget(p)
	if err != nil || variant == "" {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The Ansible remote fills metadata documents of the paths it stores only.")
	}
	if size > MaxDocumentBytes {
		return formats.Prepared{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream document is larger than the limit of documents held in memory.")
	}
	doc, err := io.ReadAll(io.NewSectionReader(body, 0, size))
	if err != nil {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The fetched document cannot be read.", errs.WithCause(err))
	}
	settings, _ := rc.Repo.Remote()
	hosts := make([]string, len(settings.ExtraHosts))
	for i, h := range settings.ExtraHosts {
		hosts[i] = h.Pattern
	}
	out := formats.Prepared{}
	data := fillData{kind: t.kind, pkg: t.pkg(), size: size}
	switch t.kind {
	case kindRoot:
		_, err = parseDiscovery(doc)
	case kindCollection:
		err = a.checkCollection(doc, t)
	case kindVersions:
		if _, ok := parseOffsetVariant(variant); !ok {
			err = errs.E(errs.Internal, errs.CodeInternal, "The version list of a collection is stored in pages with the variant offset=<n>.")
			break
		}
		_, err = parsePage(doc)
	case kindVersion:
		out.Mappings, data.announced, err = a.checkVersion(rc, hosts, doc, t)
	case kindRoleLookup:
		if _, _, ok := parseLookupVariant(variant); !ok {
			err = errs.E(errs.Internal, errs.CodeInternal, "The role lookup is stored with the variant owner=<owner>&name=<name>.")
			break
		}
		err = checkRoleLookup(doc)
	case kindRoleVersions:
		if _, ok := parsePageVariant(variant); !ok {
			err = errs.E(errs.Internal, errs.CodeInternal, "The versions of a role are stored in pages with the variant page=<n>.")
			break
		}
		out.Mappings, data.dropped, err = a.roleMappings(hosts, doc, t)
	default:
		err = errs.E(errs.Internal, errs.CodeInternal, "The Ansible remote fills metadata documents only.")
	}
	out.Data = data
	return out, err
}

func (remoteAdapter) checkCollection(doc []byte, t target) error {
	s, err := parseUpstreamCollection(doc)
	if err != nil {
		return err
	}
	if s.Namespace != t.ns || s.Name != t.name {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream answered the collection "+t.ns+"."+t.name+" with the document of "+s.Namespace+"."+s.Name+" (identity_mismatch).")
	}
	return nil
}

// checkVersion validates a version document before anything is stored (R-ANS-017): the identity in the document is the path's, the
// digest is 64 hexadecimal digits (ParseUpstreamVersion), and the download_url is an http or https URL on the upstream's host or an
// extra host. It returns the URL-map row of the archive and the digest the document announces.
func (remoteAdapter) checkVersion(rc formats.RemoteContext, hosts []string, doc []byte, t target) ([]formats.URLMapping, rows.Announced, error) {
	v, dl, err := galaxydoc.ParseUpstreamVersion(doc)
	if err != nil {
		return nil, rows.Announced{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent a version document that trove cannot use: "+err.Error()+".", errs.WithCause(err))
	}
	if !strings.EqualFold(v.Namespace, t.ns) || !strings.EqualFold(v.Name, t.name) || v.Version != t.version {
		return nil, rows.Announced{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream answered the version "+t.ns+"."+t.name+" "+t.version+" with the document of "+v.Namespace+"."+v.Name+" "+v.Version+" (identity_mismatch).")
	}
	ref, err := url.Parse(dl)
	if err != nil || (!ref.IsAbs() && !strings.HasPrefix(dl, "/")) {
		return nil, rows.Announced{}, invalidDocument("a version document", "its download_url is neither absolute nor a path from the root")
	}
	u := rc.Upstream.ResolveReference(ref)
	u.User, u.Fragment, u.RawFragment = nil, "", ""
	if !collectionHostAllowed(hosts, rc.Upstream, u) {
		return nil, rows.Announced{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The version document names its archive on the host "+u.Hostname()+", which is neither the upstream's nor in the extra hosts of the repository.")
	}
	maps := []formats.URLMapping{{Path: formats.Path{Path: ArtifactPath(t.ns, t.name, t.version)}, Upstream: u.String()}}
	return maps, announcedOf(t.ns, t.name, t.version, v.SHA256, v.Size, v.RequiresAnsible), nil
}

func checkRoleLookup(doc []byte) error {
	_, err := renderRoleLookup(doc, "https://trove.invalid/", "o", "n")
	return err
}

// roleMappings records the archive address of every version of a role's version list that passes the host rule (R-ANS-020): the path
// is built from the role id of the document's own path and the version name, so a document cannot map another role's path.
func (remoteAdapter) roleMappings(hosts []string, doc []byte, t target) ([]formats.URLMapping, int, error) {
	page, err := parseRoleVersions(doc, func(u string) bool { return roleHostAllowed(hosts, u) })
	if err != nil {
		return nil, 0, err
	}
	maps := make([]formats.URLMapping, 0, len(page.kept))
	for _, e := range page.kept {
		maps = append(maps, formats.URLMapping{Path: formats.Path{Path: RoleArchivePath(t.roleID, e.ref)}, Upstream: e.url})
	}
	return maps, page.dropped, nil
}

// OnFill runs in the transaction that upserts the document's cache entry. A version document records the digest it announces
// (RecordAnnounced: the first digest of a cached version stays, R-ANS-018); every fill of a package's document signals the early
// eviction of the merged documents that contain it (R-REPO-020). The records are written only when the fill is not a stale copy of
// the stored document: a stored entry that was fetched later stays, and the records describe it.
func (a remoteAdapter) OnFill(ctx context.Context, tx formats.Tx, ev formats.FillEvent) (formats.FillDecision, error) {
	data, ok := ev.Prepared.Data.(fillData)
	if !ok {
		return formats.FillByFetchTime, errs.E(errs.Internal, errs.CodeInternal, "The fill carries no checked document.")
	}
	if !ev.StoredAt.IsZero() && ev.StoredAt.After(ev.FetchedAt) {
		return formats.FillKeepStored, nil
	}
	rep := a.f.remoteMetricsOfPort(ev.Data)
	rep.observe(data)
	if data.kind == kindVersion {
		st, ok := ev.Data.(Store)
		if !ok {
			return formats.FillByFetchTime, errs.E(errs.Internal, errs.CodeInternal, "The Ansible data port is not bound.")
		}
		if _, err := st.RecordAnnounced(ctx, tx, ev.Repo.ID, []rows.Announced{data.announced}); err != nil {
			return formats.FillByFetchTime, err
		}
	}
	if data.kind == kindRoleVersions {
		rep.rolesDropped(ev.Repo.Name.String(), data.dropped)
	}
	if data.pkg == "" {
		return formats.FillByFetchTime, nil
	}
	return formats.FillByFetchTime, ev.Packages.Notify(catalog.MetadataChangedArea, catalog.PackageChanged(ev.Repo.ID, data.pkg))
}

// Rewrite renders a stored upstream document for one response of the serving repository (R-ANS-017): the documents carry the
// repository's addresses and nothing of the upstream's. The request path renders with the paging of the request (handlers_remote.go),
// which this is one instance of: a stored version-list page is shown as the page it is.
func (a remoteAdapter) Rewrite(_ context.Context, rc formats.RemoteContext, p formats.Path, doc []byte, links formats.LinkBuilder) ([]byte, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	repo := links.Repository()
	addr := galaxydoc.Addr{Repo: repo, Namespace: t.ns, Name: t.name}
	settings, _ := rc.Repo.Remote()
	hosts := make([]string, len(settings.ExtraHosts))
	for i, h := range settings.ExtraHosts {
		hosts[i] = h.Pattern
	}
	switch t.kind {
	case kindRoot:
		d, err := parseDiscovery(doc)
		if err != nil {
			return nil, err
		}
		return galaxydoc.RenderRoot(d.v1 != ""), nil
	case kindCollection:
		s, err := parseUpstreamCollection(doc)
		if err != nil {
			return nil, err
		}
		return galaxydoc.RenderCollection(s, addr), nil
	case kindVersions:
		page, err := parsePage(doc)
		if err != nil {
			return nil, err
		}
		offset := pageOffsetOf(doc, len(page.entries), page.count)
		return galaxydoc.RenderVersionList(page.entries, page.count, upstreamListLimit, offset, addr), nil
	case kindVersion:
		v, _, err := galaxydoc.ParseUpstreamVersion(doc)
		if err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent a version document that trove cannot use: "+err.Error()+".", errs.WithCause(err))
		}
		v.Filename = ArtifactFile(t.ns, t.name, t.version)
		return galaxydoc.RenderVersion(v, addr), nil
	case kindRoleLookup:
		return renderRoleLookup(doc, repo, "", "")
	case kindRoleVersions:
		return renderRoleVersions(doc, t.roleID, repo, func(u string) bool { return roleHostAllowed(hosts, u) }, repo)
	}
	return nil, missing()
}

// ArtifactFile is the file name of the archive of a version as the repository serves it.
func ArtifactFile(ns, name, ver string) string { return collection.FileName(ns, name, ver) }
