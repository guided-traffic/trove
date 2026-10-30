package formathost

import (
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// linkBuilder builds the absolute URLs of a response on the base URL the request's
// endpoint and host resolved (R-EP-003, R-ARCH-006, R-ARCH-007). It is the one place
// a format's links come from: nothing reads the Host header.
type linkBuilder struct {
	base   string
	format formats.ID
	key    string
}

var _ formats.LinkBuilder = linkBuilder{}

func newLinks(rq *formats.Request) linkBuilder {
	return linkBuilder{
		base:   strings.TrimSuffix(httpserver.InfoFrom(rq.Context()).BaseURL, "/"),
		format: formats.ID(rq.Repo.Format), key: rq.Repo.Key,
	}
}

func (l linkBuilder) Base() string { return l.base }

func (l linkBuilder) Registry(servingKey string) string {
	return l.base + "/api/" + string(l.format) + "/" + url.PathEscape(servingKey) + "/"
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

func (l linkBuilder) File(repoKey, p string) string {
	return edge.Links{Base: l.base}.File(repoKey, p, false)
}
