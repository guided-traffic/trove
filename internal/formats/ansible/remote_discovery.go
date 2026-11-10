package ansible

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// discovery is what the stored discovery document of an upstream parsed to (R-ANS-016): the path prefixes of its v3 and v1 APIs,
// each relative to the API root, with their trailing slash; v1 is "" when the upstream serves collections only.
type discovery struct{ v3, v1 string }

// DiscoveryDocument names the API root of the upstream: a metadata document of the repository like any other, stored, revalidated
// and collapsed by the engine, resolved before the first request that needs an address (R-ANS-016).
func (remoteAdapter) DiscoveryDocument() (formats.Path, string, bool) {
	return formats.Path{Path: RootDocument}, documentVariant, true
}

// ParseDiscovery reads the stored API root.
func (remoteAdapter) ParseDiscovery(_ context.Context, _ formats.RemoteContext, doc []byte) (any, error) {
	return parseDiscovery(doc)
}

// parseDiscovery reads {"available_versions": {"v3": "v3/", "v1": "v1/"}}. The v3 API is required, v1 optional. A prefix that is
// absolute, has a scheme, leaves the API root or holds a query is refused (R-ANS-016): every upstream address is built from the
// repository's URL and a relative prefix, so a document cannot send trove elsewhere.
func parseDiscovery(doc []byte) (*discovery, error) {
	var d struct {
		Available map[string]string `json:"available_versions"`
	}
	if len(doc) > MaxDocumentBytes || json.Unmarshal(doc, &d) != nil || d.Available == nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream did not answer a Galaxy API root (a JSON object with available_versions). The API root of a Galaxy usually ends in /api/.")
	}
	out := &discovery{}
	for key, dst := range map[string]*string{"v3": &out.v3, "v1": &out.v1} {
		raw, ok := d.Available[key]
		if !ok {
			continue
		}
		prefix, err := cleanPrefix(raw)
		if err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's API root names the "+key+" API at "+strconv.Quote(raw)+", which is not a path below the API root.")
		}
		*dst = prefix
	}
	if out.v3 == "" {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's API root lists no v3 API, which collections need.")
	}
	return out, nil
}

var errBadPrefix = errors.New("not a relative path")

func cleanPrefix(raw string) (string, error) {
	if raw == "" || len(raw) > 128 || strings.ContainsAny(raw, "?#\\ \t\r\n") || strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") {
		return "", errBadPrefix
	}
	for _, seg := range strings.Split(strings.TrimSuffix(raw, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errBadPrefix
		}
	}
	return strings.TrimSuffix(raw, "/") + "/", nil
}

// discoveryOf returns what the engine resolved for the fetch.
func discoveryOf(rc formats.RemoteContext) (*discovery, error) {
	d, ok := rc.Discovery.(*discovery)
	if !ok || d == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream addresses of the Ansible remote were requested before its API root was resolved.")
	}
	return d, nil
}

// UpstreamRequest maps a canonical path to the upstream request (R-ANS-016, R-ANS-017): the API root is the remote's URL itself,
// every other address is the URL, a prefix the root announced and the Galaxy path. The address of an archive that the version
// document named is the URL map's (R-REPO-043); the address built here is the default the map replaces.
func (remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, variant string) (*http.Request, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	var u *url.URL
	if t.kind == kindRoot {
		c := *rc.Upstream
		c.User, c.Fragment, c.RawFragment = nil, "", ""
		u = &c
	} else {
		d, err := discoveryOf(rc)
		if err != nil {
			return nil, err
		}
		if u, err = upstreamURL(rc.Upstream, d, t, variant); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	if t.kind != kindArchive && t.kind != kindRoleArchive {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

// upstreamURL is the address of a document or file below the API root.
func upstreamURL(root *url.URL, d *discovery, t target, variant string) (*url.URL, error) {
	base := *root
	base.User, base.Fragment, base.RawFragment, base.RawQuery = nil, "", "", ""
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	build := func(prefix, rest, query string) (*url.URL, error) {
		raw := base.String() + prefix + rest
		if query != "" {
			raw += "?" + query
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream address does not build.", errs.WithCause(err))
		}
		return u, nil
	}
	coll := func(t target) string { return "collections/" + escapeSeg(t.ns) + "/" + escapeSeg(t.name) + "/" }
	switch t.kind {
	case kindCollection:
		return build(d.v3, coll(t), "")
	case kindVersions:
		offset, ok := parseOffsetVariant(variant)
		if !ok {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The version list of a collection is stored in pages with the variant offset=<n>.")
		}
		return build(d.v3, coll(t)+"versions/", "limit="+strconv.Itoa(upstreamListLimit)+"&offset="+strconv.Itoa(offset))
	case kindVersion:
		return build(d.v3, coll(t)+"versions/"+escapeSeg(t.version)+"/", "")
	case kindArchive:
		return build(d.v3, "artifacts/collections/"+escapeSeg(t.file), "")
	}
	if d.v1 == "" {
		return nil, missing()
	}
	switch t.kind {
	case kindRoleLookup:
		owner, name, ok := parseLookupVariant(variant)
		if !ok {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The role lookup is stored with the variant owner=<owner>&name=<name>.")
		}
		return build(d.v1, "roles/", "owner__username="+url.QueryEscape(owner)+"&name="+url.QueryEscape(name))
	case kindRoleVersions:
		page, ok := parsePageVariant(variant)
		if !ok {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The versions of a role are stored in pages with the variant page=<n>.")
		}
		return build(d.v1, "roles/"+t.roleID+"/versions/", "page_size="+strconv.Itoa(roleVersionsPageSize)+"&page="+strconv.Itoa(page))
	case kindRoleArchive:
		return build(d.v1, "roles/archives/"+t.roleID+"/"+escapeSeg(t.roleRef)+collection.ArchiveSuffix, "")
	}
	return nil, missing()
}

// announcedOf is the row a version document announces (R-ANS-018); the repository id is the data port's argument.
func announcedOf(ns, name, ver string, sha [32]byte, size int64, requires string) rows.Announced {
	return rows.Announced{Name: ns + "." + name, Version: ver, SHA256: sha, Size: size, RequiresAnsible: requires}
}
