package oci

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// upstreamTags is an upstream's tags list as a registry pages it: the tags after `last`, at most n, in byte order.
func upstreamTags(all []string, q formats.Query) *formats.QueryResult {
	sorted := append([]string(nil), all...)
	sort.Strings(sorted)
	n, _ := strconv.Atoi(q.Params.Get("n"))
	var page []string
	for _, t := range sorted {
		if t > q.Params.Get("last") && (n == 0 || len(page) < n) {
			page = append(page, t)
		}
	}
	return &formats.QueryResult{ContentType: "application/json", Body: []byte(`{"name":"x","tags":["` + strings.Join(page, `","`) + `"]}`)}
}

type pagingFixture struct {
	*fixture
	passed []formats.Query
}

// pagingFixture serves a name "cut" whose stored list is the first three tags of a, b, c, d, e, f and the upstream has the rest, and a
// name "whole" with four tags. hidden are tags the caller may not read.
func newPaging(t *testing.T, hidden ...string) *pagingFixture {
	t.Helper()
	pf := &pagingFixture{}
	m := &formattest.MemRemote{Docs: map[string][]byte{
		TaglistPath("whole"): []byte(`{"name":"whole","tags":["a","b","c","d"]}`),
		TaglistPath("cut"):   []byte(`{"name":"cut","tags":["a","b","c"],"x-trove-truncated":true}`),
	}}
	m.Queries = func(_ context.Context, _ *formats.Request, q formats.Query) (*formats.QueryResult, error) {
		pf.passed = append(pf.passed, q)
		return upstreamTags([]string{"a", "b", "c", "d", "e", "f"}, q), nil
	}
	tw := newTwin()
	h := formattest.NewHost(New(), formattest.HostOptions{
		Class: formats.Remote, Remote: m, Data: tw, Resolver: proceedResolver{},
		Allow: func(tg formats.Target) error {
			for _, hid := range hidden {
				if strings.HasSuffix(tg.Path, "/_tags/"+hid) {
					return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
				}
			}
			return nil
		},
	})
	tw.host = h
	pf.fixture = &fixture{t: t, f: New(), h: h, twin: tw}
	return pf
}

func (pf *pagingFixture) list(path string) (tags []string, link string) {
	pf.t.Helper()
	resp := pf.getq(path)
	pf.wantStatus(resp, http.StatusOK, "")
	var p struct {
		Tags []string `json:"tags"`
	}
	if err := jsonDecode(resp.Body.Bytes(), &p); err != nil {
		pf.t.Fatalf("%s: %v %s", path, err, resp.Body)
	}
	return p.Tags, resp.Header().Get("Link")
}

func lastOf(link string) string {
	i := strings.Index(link, "last=")
	if i < 0 {
		return ""
	}
	v := link[i+5:]
	if j := strings.IndexAny(v, "&>"); j >= 0 {
		v = v[:j]
	}
	u, _ := url.QueryUnescape(v)
	return u
}

// Mechanism of R-OCI-024, not a proof of its criteria: it checks a window, a bound or a spool detail of the tags list; the
// criteria are the first five tags with a Link to trove and the referrers pass-through.
func TestALinkFollowsEveryPageThatIsNotTheEndOfTheList(t *testing.T) {
	pf := newPaging(t)
	for _, c := range []struct {
		path, tags, next string
	}{
		// The stored list was cut: a page of exactly the stored tags has a Link, because the upstream may have more.
		{"cut/tags/list?n=3", "a,b,c", "c"},
		{"cut/tags/list?n=2", "a,b", "b"},
		{"cut/tags/list?n=1000", "a,b,c,d,e,f", ""},
		{"cut/tags/list", "a,b,c,d,e,f", ""},
		{"cut/tags/list?n=4", "a,b,c,d", "d"},
		{"cut/tags/list?n=2&last=b", "c,d", "d"},
		{"cut/tags/list?n=2&last=e", "f", ""},
		{"cut/tags/list?n=5&last=f", "", ""},
		// A whole list has a Link while tags follow and none at its end.
		{"whole/tags/list?n=4", "a,b,c,d", ""},
		{"whole/tags/list?n=3", "a,b,c", "c"},
	} {
		tags, link := pf.list(c.path)
		if strings.Join(tags, ",") != c.tags || lastOf(link) != c.next || (c.next == "") != (link == "") {
			t.Errorf("%s: tags %v link %q, want %s and next %q", c.path, tags, link, c.tags, c.next)
		}
	}
	// Following the Links of a one-tag page walks the whole list once.
	var walked []string
	path := "cut/tags/list?n=1"
	for i := 0; i < 20; i++ {
		tags, link := pf.list(path)
		walked = append(walked, tags...)
		if link == "" {
			break
		}
		path = "cut/tags/list?n=1&last=" + lastOf(link)
	}
	if strings.Join(walked, ",") != "a,b,c,d,e,f" {
		t.Errorf("walked %v", walked)
	}
}

// Mechanism of R-OCI-024, not a proof of its criteria: it checks a window, a bound or a spool detail of the tags list; the
// criteria are the first five tags with a Link to trove and the referrers pass-through.
func TestATagTheCallerMayNotReadIsInNoPageAndNoLink(t *testing.T) {
	pf := newPaging(t, "b", "e")
	for _, path := range []string{"whole/tags/list?n=1", "cut/tags/list?n=1"} {
		var walked []string
		p := path
		for i := 0; i < 20; i++ {
			tags, link := pf.list(p)
			walked = append(walked, tags...)
			if strings.Contains(link, "last=b") || strings.Contains(link, "last=e") {
				t.Fatalf("%s: a Link names a hidden tag: %q", p, link)
			}
			if link == "" {
				break
			}
			p = strings.Split(path, "?")[0] + "?n=1&last=" + lastOf(link)
		}
		if strings.Contains(strings.Join(walked, ","), "b") || strings.Contains(strings.Join(walked, ","), "e") {
			t.Errorf("%s: walked %v", path, walked)
		}
	}
	// The page is filled from the readable tags: a hidden tag at the position n does not shorten it.
	tags, link := pf.list("whole/tags/list?n=2")
	if strings.Join(tags, ",") != "a,c" || lastOf(link) != "c" {
		t.Errorf("n=2 = %v link %q", tags, link)
	}
	tags, link = pf.list("cut/tags/list?n=4")
	if strings.Join(tags, ",") != "a,c,d,f" || link != "" && lastOf(link) != "f" {
		t.Errorf("cut n=4 = %v link %q", tags, link)
	}
}

// Mechanism of R-OCI-024, not a proof of its criteria: it checks a window, a bound or a spool detail of the tags list; the
// criteria are the first five tags with a Link to trove and the referrers pass-through.
func TestOfflineTheStoredWindowAnswersAndReferrersAre404(t *testing.T) {
	m := &formattest.MemRemote{Docs: map[string][]byte{
		TaglistPath("cut"): []byte(`{"name":"cut","tags":["a","b","c"],"x-trove-truncated":true}`),
	}} // no Queries: the engine answers formats.ErrOffline, as it does for an offline repository and a caller who may not fill
	tw := newTwin()
	h := formattest.NewHost(New(), formattest.HostOptions{Class: formats.Remote, Remote: m, Data: tw, Resolver: proceedResolver{}})
	tw.host = h
	pf := &pagingFixture{fixture: &fixture{t: t, f: New(), h: h, twin: tw}}
	tags, link := pf.list("cut/tags/list?n=2")
	if strings.Join(tags, ",") != "a,b" || lastOf(link) != "b" {
		t.Errorf("window inside = %v link %q", tags, link)
	}
	tags, link = pf.list("cut/tags/list?n=2&last=b")
	if strings.Join(tags, ",") != "c" || link != "" {
		t.Errorf("window at the end of the stored part = %v link %q", tags, link)
	}
	tags, link = pf.list("cut/tags/list?n=5&last=c")
	if len(tags) != 0 || link != "" {
		t.Errorf("window beyond = %v link %q", tags, link)
	}
	pf.wantStatus(pf.get("app/referrers/"+sha([]byte("x"))), http.StatusNotFound, "NAME_UNKNOWN")

	// The circuit open is the same answer.
	m.Queries = func(context.Context, *formats.Request, formats.Query) (*formats.QueryResult, error) {
		return nil, errs.E(errs.Unavailable, errs.CodeUpstreamAssumedOffline, "open", errs.WithRetryAfter(30))
	}
	tags, _ = pf.list("cut/tags/list?n=5&last=c")
	if len(tags) != 0 {
		t.Errorf("circuit open: %v", tags)
	}
	pf.wantStatus(pf.get("app/referrers/"+sha([]byte("x"))), http.StatusNotFound, "NAME_UNKNOWN")
}
