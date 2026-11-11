package formattest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// hasWiki reports whether the format has wiki content (R-AUTHZ-034): its catalog facts say so, or its fixture seeds wiki samples.
func (r *run) hasWiki() bool { return r.d.Wiki || len(r.fx.WikiSamples) > 0 }

// wiki is check 15 (R-AUTHZ-034, R-ARCH-010): the wiki of a repository is a part of its own, and a caller who reads the artifacts
// and not the wiki gets none of it.
//
//   - A route declared Wiki serves only wiki content, so it is a Metadata or Computed route with the action read, and only a
//     format whose facts say it has wiki content declares one.
//   - A format with wiki content has wiki samples in its fixture, the texts its content cases seed.
//   - Every content case is served again with the kit host answering as for a caller who reads the artifacts and not the wiki
//     (HostOptions.WikiDenied): a Wiki route is refused by the dispatcher's coarse check before its handler runs, so the kit
//     serves none; every other answer holds no byte of any sample, in its body or its headers.
//
// A format without wiki content declares no Wiki route and has nothing more to judge.
func (r *run) wiki() {
	for _, rt := range r.f.Routes() {
		if !rt.Wiki {
			continue
		}
		if !r.hasWiki() {
			r.failf("%s: the route %q is declared Wiki and the format's facts say it has no wiki content (Facts.Wiki)", CheckWiki, rt.Name)
		}
		if (rt.Kind != formats.Metadata && rt.Kind != formats.Computed) || rt.Action != formats.ActionRead {
			r.failf("%s: the route %q is declared Wiki and is a %s route with the action %q: a wiki route is a Metadata or Computed read", CheckWiki, rt.Name, rt.Kind, rt.Action)
		}
	}
	if !r.hasWiki() {
		return
	}
	if len(r.fx.WikiSamples) == 0 {
		r.failf("%s: the format has wiki content (Facts.Wiki) and the fixture has no WikiSamples, the README texts its content cases seed", CheckWiki)
		return
	}
	if len(r.fx.Content) == 0 {
		r.failf("%s: the fixture has no content case to serve for a caller who may not read the wiki", CheckWiki)
		return
	}
	served := 0
	for _, c := range r.fx.Content {
		var o HostOptions
		if r.fx.Host != nil {
			o = r.fx.Host()
		}
		o.WikiDenied = true
		h := NewHost(r.f, o)
		if c.Seed != nil {
			if err := c.Seed(h); err != nil {
				r.failf("content case %q: the seed fails: %v", c.Name, err)
				continue
			}
		}
		method := c.Method
		if method == "" {
			method = http.MethodGet
		}
		rq, err := h.NewRequest(context.Background(), method, c.Path, bytes.NewReader(c.Body))
		if err != nil {
			continue // checks 1 and 7 report a case no route takes
		}
		if rq.Route.Wiki {
			continue // refused by the coarse check before the handler runs
		}
		resp := h.ServeRequest(rq, false)
		served++
		for _, s := range r.fx.WikiSamples {
			if s == "" {
				continue
			}
			if strings.Contains(resp.Body.String(), s) || strings.Contains(fmt.Sprint(resp.Header()), s) {
				r.failf("%s: content case %q (route %q) answered a caller who may not read the wiki with the wiki text %q: a route that serves wiki content is declared Wiki, and a document that embeds it asks Host.WikiReadable first", CheckWiki, c.Name, rq.Route.Name, s)
				break
			}
		}
	}
	if served == 0 {
		r.failf("%s: no content case was served for a caller who may not read the wiki, so the check judged nothing", CheckWiki)
	}
}
