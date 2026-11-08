package oci

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// One request of a client costs a bounded number of upstream pages, however many tags the caller may not read (Q-OCI-128).
func TestR_OCI_024_APageEndsEarlyWithALinkAfterTheAllowedUpstreamPages(t *testing.T) {
	var asked int
	var all []string
	for c := 'b'; c <= 'z'; c++ {
		all = append(all, string(c))
	}
	m := &formattest.MemRemote{Docs: map[string][]byte{
		TaglistPath("cut"): []byte(`{"name":"cut","tags":["a"],"x-trove-truncated":true}`),
	}}
	m.Queries = func(_ context.Context, _ *formats.Request, q formats.Query) (*formats.QueryResult, error) {
		asked++
		// The upstream cuts its pages at one tag.
		q.Params.Set("n", "1")
		return upstreamTags(all, q), nil
	}
	tw := newTwin()
	h := formattest.NewHost(New(), formattest.HostOptions{
		Class: formats.Remote, Remote: m, Data: tw, Resolver: proceedResolver{},
		Allow: func(tg formats.Target) error {
			if strings.Contains(tg.Path, "/_tags/") && !strings.HasSuffix(tg.Path, "/_tags/a") {
				return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
			}
			return nil
		},
	})
	tw.host = h
	pf := &pagingFixture{fixture: &fixture{t: t, f: New(), h: h, twin: tw}}
	tags, link := pf.list("cut/tags/list?n=5")
	if strings.Join(tags, ",") != "a" || lastOf(link) != "a" {
		t.Errorf("tags %v link %q: the page ends early with what could be listed", tags, link)
	}
	if asked != maxPassPages {
		t.Errorf("%d upstream pages for one request, want %d", asked, maxPassPages)
	}
	// With nothing readable in the pages asked for, the list cannot go on without naming a tag the caller may not read.
	asked = 0
	resp := pf.getq("cut/tags/list?n=5&last=a")
	if resp.Code == http.StatusOK || asked != maxPassPages {
		t.Errorf("nothing readable: status %d after %d upstream pages", resp.Code, asked)
	}
}
