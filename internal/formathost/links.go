package formathost

import (
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// linkBuilder builds the absolute URLs of a response on the base URL the request's
// endpoint and host resolved (R-EP-003, R-ARCH-006, R-ARCH-007). It is the one place
// a format's links come from: nothing reads the Host header.
type linkBuilder struct {
	base string
	// template is the protocol base path of the request's format (catalog.Facts.ProtocolBasePath).
	template string
	key      qname.Repo
	// bound is true on a host whose root is bound to the repository (EndpointFacts.Bound).
	bound bool
}

var _ formats.LinkBuilder = linkBuilder{}

func newLinks(rq *formats.Request, template string) linkBuilder {
	return linkBuilder{
		base:     strings.TrimSuffix(httpserver.InfoFrom(rq.Context()).BaseURL, "/"),
		template: template, key: rq.Repo.Name, bound: rq.Endpoint.Bound,
	}
}

func (l linkBuilder) Base() string { return l.base }

func (l linkBuilder) Registry(serving qname.Repo) string {
	if l.bound {
		return l.base + strings.Replace(l.template, "{project}/{key}/", "", 1)
	}
	return l.base + qname.ProtocolBase(l.template, serving)
}

func (l linkBuilder) ClientName(name string) string {
	if l.bound {
		return name
	}
	return l.key.Project + "/" + l.key.Key + "/" + name
}

func (l linkBuilder) Repository() string { return l.Registry(l.key) }

// Path appends a canonical path, every segment escaped once.
func (l linkBuilder) Path(p string) string {
	if p == "" {
		return l.Repository()
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return l.Repository() + strings.Join(segs, "/")
}

func (l linkBuilder) File(repoKey qname.Repo, p string) string {
	return edge.Links{Base: l.base}.File(repoKey, p, false)
}
