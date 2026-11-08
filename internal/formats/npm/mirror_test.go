package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// Contract tests of the npm format's part of the mirror mode: R-REPO-050 (the catalog fact), R-REPO-051 (discovery, the plan),
// R-REPO-052 (the announced size) and R-REPO-054 (a rate-limit answer is returned as it came). The IDs stay out of the test
// names while the engine that makes these requirements true is missing, so that the coverage evidence does not count them
// (Q-ROAD-5); the tests carry them in comments.

// fakeMirrorUpstream is the discover.Upstream the engine lends a format, with the answers a test gives it: the
// replication service by path (the answer is a function of the request's decoded query), the stored documents by name.
type fakeMirrorUpstream struct {
	base  *url.URL
	gets  []*url.URL
	serve map[string]func(q url.Values) (int, string)
	docs  map[string]string
	// getErr and docErr, when set, are what the next calls return.
	getErr, docErr error
}

func newFakeMirrorUpstream(base string) *fakeMirrorUpstream {
	u, err := url.Parse(base)
	if err != nil {
		panic(err)
	}
	return &fakeMirrorUpstream{base: u, serve: map[string]func(url.Values) (int, string){}, docs: map[string]string{}}
}

func (f *fakeMirrorUpstream) Base() *url.URL            { return f.base }
func (f *fakeMirrorUpstream) Settings() json.RawMessage { return nil }

func (f *fakeMirrorUpstream) Get(_ context.Context, u *url.URL, _ string, _ int64) (*discover.Response, error) {
	f.gets = append(f.gets, u)
	if f.getErr != nil {
		return nil, f.getErr
	}
	h, ok := f.serve[u.Path]
	if !ok {
		return &discover.Response{Status: http.StatusNotFound}, nil
	}
	status, body := h(u.Query())
	return &discover.Response{Status: status, Body: []byte(body)}, nil
}

// Artifact implements discover.Upstream: the fake holds no artifacts.
func (f *fakeMirrorUpstream) Artifact(context.Context, string, int64) ([]byte, bool, error) {
	return nil, false, nil
}

func (f *fakeMirrorUpstream) Document(_ context.Context, path, variant string) ([]byte, bool, error) {
	if variant != "full" {
		return nil, false, fmt.Errorf("variant %q", variant)
	}
	if f.docErr != nil {
		return nil, false, f.docErr
	}
	d, ok := f.docs[path]
	return []byte(d), ok, nil
}

// queries returns "path?decoded query" of every request, with the query in the order it was sent.
func (f *fakeMirrorUpstream) queries() []string {
	var out []string
	for _, u := range f.gets {
		raw, err := url.QueryUnescape(u.RawQuery)
		if err != nil {
			raw = u.RawQuery
		}
		s := u.Host + u.Path
		if raw != "" {
			s += "?" + raw
		}
		out = append(out, s)
	}
	return out
}

// rateLimit is an upstream's rate-limit answer, as internal/proxy/upstream returns it.
type rateLimit struct{}

func (rateLimit) Error() string                     { return "rate limited" }
func (rateLimit) RetryAfter() (time.Duration, bool) { return 7 * time.Second, true }
func (rateLimit) RateLimitReason() string           { return "too_many_requests" }

const publicRegistryURL = "https://registry.npmjs.org/"

func mirrorLister() discover.Lister { return New().Remote().(discover.Lister) }

// admits returns a Scope check that admits the names with one of the prefixes and records every name it is asked about.
func admits(asked *[]string, prefixes ...string) func(string) bool {
	return func(name string) bool {
		if asked != nil {
			*asked = append(*asked, name)
		}
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
}

func allDocsRows(ids ...string) string {
	var rows []string
	for _, id := range ids {
		b, _ := json.Marshal(id)
		rows = append(rows, `{"id":`+string(b)+`,"key":`+string(b)+`,"value":{"rev":"1-x"}}`)
	}
	return `{"total_rows":99,"offset":0,"rows":[` + strings.Join(rows, ",") + `]}`
}

func cursorOf(t *testing.T, c discover.Cursor) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c, &m); err != nil {
		t.Fatalf("cursor %q: %v", c, err)
	}
	return m
}

func TestMirrorMethodsAndHosts(t *testing.T) {
	l := mirrorLister()
	pub, other := newFakeMirrorUpstream(publicRegistryURL), newFakeMirrorUpstream("https://npm.partner.example/")
	got, err := l.MirrorMethods(context.Background(), pub)
	if err != nil || !reflect.DeepEqual(got, []discover.Method{discover.MethodFeed, discover.MethodNames}) {
		t.Fatalf("public registry: %v, %v", got, err)
	}
	got, err = l.MirrorMethods(context.Background(), other)
	if err != nil || !reflect.DeepEqual(got, []discover.Method{discover.MethodNames}) {
		t.Fatalf("another registry: %v, %v", got, err)
	}
	for _, m := range got {
		if m == discover.MethodListing {
			t.Fatal("npm offers no listing (Q-NPM-70)")
		}
	}
	// Q-NPM-72: the replication host belongs to the public registry only.
	for base, want := range map[string][]string{
		"https://registry.npmjs.org/":     {"replicate.npmjs.com"},
		"https://registry.npmjs.org":      {"replicate.npmjs.com"},
		"https://registry.npmjs.org:443/": {"replicate.npmjs.com"},
		"http://registry.npmjs.org/":      nil,
		"https://registry.npmjs.org/sub/": nil,
		"https://registry.npmjs.org/?":    nil, // Q-NPM-75: a query is not the well-known upstream
		"https://registry.npmjs.org/?x=1": nil,
		"https://registry.npmjs.org:8443": nil,
		"https://npm.partner.example/":    nil,
		"https://replicate.npmjs.com/":    nil,
	} {
		u, _ := url.Parse(base)
		if got := l.MirrorHosts(u); !reflect.DeepEqual(got, want) {
			t.Errorf("MirrorHosts(%s) = %v, want %v", base, got, want)
		}
	}
	// A method the upstream does not offer answers ErrMethodUnavailable.
	for _, m := range []discover.Method{discover.MethodListing, discover.MethodFeed} {
		_, err := l.MirrorNames(context.Background(), other, discover.PageRequest{Method: m, Kind: discover.PassFull})
		if !errors.Is(err, discover.ErrMethodUnavailable) {
			t.Errorf("%s on a private registry: %v", m, err)
		}
	}
	if len(other.gets) != 0 {
		t.Fatalf("requests sent for an unavailable method: %v", other.queries())
	}
}

// R-REPO-051: the exact request of the npm.md acceptance criteria, one key range per literal prefix.
func TestMirrorFullPassKeyRange(t *testing.T) {
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"db_name":"registry","update_seq":41592}` }
	up.serve["/registry/_all_docs"] = func(url.Values) (int, string) {
		return 200, allDocsRows("@acme/a", "@acme/b", "@acme/Bad Name/x", "@acme/c")
	}
	var asked []string
	sc := discover.Scope{Include: []string{"@acme/*"}, Admits: admits(&asked, "@acme/a", "@acme/c")}
	page, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{
		Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"replicate.npmjs.com/",
		"replicate.npmjs.com/registry/_all_docs?startkey=\"@acme/\"&endkey=\"@acme/￰\"&limit=10000",
	}
	if got := up.queries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests\n got %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(page.Names, []string{"@acme/a", "@acme/c"}) || !page.Done || len(page.Removed) != 0 {
		t.Fatalf("page %+v", page)
	}
	// The pass ends with the root's update_seq as the position of the next incremental pass.
	if got := string(page.Cursor); got != `{"seq":"41592"}` {
		t.Fatalf("final cursor %s", got)
	}
	for _, n := range asked {
		if n == "@acme/Bad Name/x" {
			t.Fatal("a name that is none was offered to the scope's check")
		}
	}
}

func TestMirrorFullPassWholeKeySpace(t *testing.T) {
	for name, include := range map[string][]string{
		"without include patterns":   nil,
		"a pattern without a prefix": {"@acme/*", "*"},
	} {
		t.Run(name, func(t *testing.T) {
			up := newFakeMirrorUpstream(publicRegistryURL)
			up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":"7-abc"}` }
			up.serve["/registry/_all_docs"] = func(url.Values) (int, string) { return 200, allDocsRows("left-pad", "zod") }
			page, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{
				Method: discover.MethodFeed, Kind: discover.PassFull,
				Scope: discover.Scope{Include: include, Admits: func(string) bool { return true }},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := up.queries()[1]; got != "replicate.npmjs.com/registry/_all_docs?limit=10000" {
				t.Fatalf("request %q", got)
			}
			if !page.Done || len(page.Names) != 2 || string(page.Cursor) != `{"seq":"7-abc"}` {
				t.Fatalf("page %+v cursor %s", page, page.Cursor)
			}
		})
	}
}

// pagedRows serves _all_docs over ids: the rows from startkey (inclusive) on, at most limit of them, as CouchDB does.
func pagedRows(ids []string) func(url.Values) (int, string) {
	return func(q url.Values) (int, string) {
		start := 0
		if sk := q.Get("startkey"); sk != "" {
			var s string
			_ = json.Unmarshal([]byte(sk), &s)
			for start < len(ids) && ids[start] < s {
				start++
			}
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		end := min(start+limit, len(ids))
		if ek := q.Get("endkey"); ek != "" {
			var s string
			_ = json.Unmarshal([]byte(ek), &s)
			for i := start; i < end; i++ {
				if ids[i] > s {
					end = i
					break
				}
			}
		}
		return 200, allDocsRows(ids[start:end]...)
	}
}

// R-REPO-051: paged by startkey after the last id, limit 10000.
func TestMirrorFullPassPagesAfterTheLastId(t *testing.T) {
	var ids []string
	for i := 0; i < 10002; i++ {
		ids = append(ids, fmt.Sprintf("p%05d", i))
	}
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":900}` }
	up.serve["/registry/_all_docs"] = pagedRows(ids)
	sc := discover.Scope{Admits: func(string) bool { return true }}
	l := mirrorLister()

	p1, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Limit: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Names) != 10000 || p1.Done {
		t.Fatalf("first page: %d names, done %v", len(p1.Names), p1.Done)
	}
	c := cursorOf(t, p1.Cursor)
	if c["seq"] != "900" || c["after"] != "p09999" || c["range"] != float64(0) {
		t.Fatalf("cursor %s", p1.Cursor)
	}

	p2, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Cursor: p1.Cursor, Limit: 20000})
	if err != nil {
		t.Fatal(err)
	}
	// The second request starts at the last id, which the service returns again and the page leaves out.
	if got := up.queries()[2]; got != "replicate.npmjs.com/registry/_all_docs?startkey=\"p09999\"&limit=10000" {
		t.Fatalf("second request %q", got)
	}
	if !reflect.DeepEqual(p2.Names, []string{"p10000", "p10001"}) || !p2.Done || string(p2.Cursor) != `{"seq":"900"}` {
		t.Fatalf("second page %+v cursor %s", p2, p2.Cursor)
	}
	if len(up.gets) != 3 {
		t.Fatalf("%d requests: the root is read once per pass: %v", len(up.gets), up.queries())
	}
}

func TestMirrorFullPassTruncatesToTheLimit(t *testing.T) {
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":5}` }
	up.serve["/registry/_all_docs"] = pagedRows([]string{"a", "b", "c", "d"})
	sc := discover.Scope{Admits: func(string) bool { return true }}
	l := mirrorLister()
	p1, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p1.Names, []string{"a", "b", "c"}) || p1.Done || cursorOf(t, p1.Cursor)["after"] != "c" {
		t.Fatalf("page %+v cursor %s", p1, p1.Cursor)
	}
	p2, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Cursor: p1.Cursor, Limit: 3})
	if err != nil || !reflect.DeepEqual(p2.Names, []string{"d"}) || !p2.Done {
		t.Fatalf("page %+v, %v", p2, err)
	}
}

func TestMirrorFullPassOneRangePerPrefix(t *testing.T) {
	ids := []string{"@acme/a", "@acme/b", "@zeta/q", "left-pad", "left-pad-extra", "lodash", "zod"}
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":12}` }
	up.serve["/registry/_all_docs"] = pagedRows(ids)
	// "@acme/a" lies inside the range of "@acme/*"; "left-pad" and "left-*" are one range; an escaped wildcard is literal.
	sc := discover.Scope{
		Include: []string{"@acme/*", "@acme/a", "left-*", "left-pad", `lod\*ash`},
		Admits:  func(string) bool { return true },
	}
	l := mirrorLister()
	var names []string
	var cur discover.Cursor
	for i := 0; i < 5; i++ {
		page, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc, Cursor: cur, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, page.Names...)
		if page.Done {
			if string(page.Cursor) != `{"seq":"12"}` {
				t.Fatalf("final cursor %s", page.Cursor)
			}
			want := []string{
				"replicate.npmjs.com/",
				"replicate.npmjs.com/registry/_all_docs?startkey=\"@acme/\"&endkey=\"@acme/￰\"&limit=10000",
				"replicate.npmjs.com/registry/_all_docs?startkey=\"left-\"&endkey=\"left-￰\"&limit=10000",
				"replicate.npmjs.com/registry/_all_docs?startkey=\"lod*ash\"&endkey=\"lod*ash￰\"&limit=10000",
			}
			if got := up.queries(); !reflect.DeepEqual(got, want) {
				t.Fatalf("requests\n got %q\nwant %q", got, want)
			}
			if want := []string{"@acme/a", "@acme/b", "left-pad", "left-pad-extra"}; !reflect.DeepEqual(names, want) {
				t.Fatalf("names %v, want %v", names, want)
			}
			return
		}
		if r := cursorOf(t, page.Cursor)["range"]; r != float64(i+1) && r != float64(i) {
			t.Fatalf("page %d: cursor %s", i, page.Cursor)
		}
		cur = page.Cursor
	}
	t.Fatal("the pass does not end")
}

// R-REPO-051: _changes?since=<stored>&limit=10000, a change marked deleted is a removed name.
func TestMirrorIncrementalPassReadsChanges(t *testing.T) {
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/registry/_changes"] = func(url.Values) (int, string) {
		return 200, `{"results":[
			{"seq":41593,"id":"@acme/a","changes":[{"rev":"2-a"}]},
			{"seq":41594,"id":"@acme/b","changes":[{"rev":"3-b"}],"deleted":true},
			{"seq":41595,"id":"@other/x","changes":[{"rev":"1-x"}]},
			{"seq":41596,"id":"@acme/a","changes":[{"rev":"3-a"}]},
			{"seq":41597,"id":"_design/app","changes":[{"rev":"1-d"}]}
		],"last_seq":41597,"pending":0}`
	}
	sc := discover.Scope{Include: []string{"@acme/*"}, Admits: admits(nil, "@acme/")}
	page, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{
		Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"41592"}`), Scope: sc, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := up.queries(); !reflect.DeepEqual(got, []string{"replicate.npmjs.com/registry/_changes?since=41592&limit=10000"}) {
		t.Fatalf("requests %q", got)
	}
	if !reflect.DeepEqual(page.Names, []string{"@acme/a"}) || !reflect.DeepEqual(page.Removed, []string{"@acme/b"}) || !page.Done {
		t.Fatalf("page %+v", page)
	}
	if string(page.Cursor) != `{"seq":"41597"}` {
		t.Fatalf("cursor %s", page.Cursor)
	}
}

func TestMirrorIncrementalPassPagesAndStringSequences(t *testing.T) {
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/registry/_changes"] = func(q url.Values) (int, string) {
		if q.Get("since") == "10-g1AA" {
			return 200, `{"results":[{"seq":"11-g1BB","id":"a"},{"seq":"12-g1CC","id":"b","deleted":true},{"seq":"13-g1DD","id":"c"}],"last_seq":"13-g1DD"}`
		}
		return 200, `{"results":[],"last_seq":"13-g1DD"}`
	}
	sc := discover.Scope{Admits: func(string) bool { return true }}
	l := mirrorLister()
	p1, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"10-g1AA"}`), Scope: sc, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Two changes fit: the cursor is the sequence of the last one used, and the pass goes on.
	if !reflect.DeepEqual(p1.Names, []string{"a"}) || !reflect.DeepEqual(p1.Removed, []string{"b"}) || p1.Done || string(p1.Cursor) != `{"seq":"12-g1CC"}` {
		t.Fatalf("page %+v cursor %s", p1, p1.Cursor)
	}
	p2, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"10-g1AA"}`), Cursor: p1.Cursor, Scope: sc, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Names)+len(p2.Removed) != 0 || !p2.Done || string(p2.Cursor) != `{"seq":"13-g1DD"}` {
		t.Fatalf("page %+v cursor %s", p2, p2.Cursor)
	}
	if got := up.queries()[1]; got != "replicate.npmjs.com/registry/_changes?since=12-g1CC&limit=10000" {
		t.Fatalf("second request %q", got)
	}
	// An incremental pass without a position is an error, not a request.
	before := len(up.gets)
	if _, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Scope: sc}); err == nil || len(up.gets) != before {
		t.Fatalf("err %v, %d requests", err, len(up.gets)-before)
	}
}

func TestMirrorNamesMethodOnAPrivateUpstream(t *testing.T) {
	up := newFakeMirrorUpstream("https://npm.partner.example/")
	var asked []string
	sc := discover.Scope{
		Include: []string{"@acme/a", "@acme/*", "left-pad", `weird\*name`, "@other/x", "@acme/a"},
		Admits:  admits(&asked, "@acme/", "left-pad", "weird"),
	}
	page, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodNames, Kind: discover.PassFull, Scope: sc, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@acme/a", "left-pad", "weird*name"}; !reflect.DeepEqual(page.Names, want) || !page.Done || page.Cursor != nil {
		t.Fatalf("page %+v", page)
	}
	if len(up.gets) != 0 {
		t.Fatalf("the method names sends no request: %v", up.queries())
	}
	// Paged by the last name.
	p1, _ := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodNames, Kind: discover.PassFull, Scope: sc, Limit: 2})
	if !reflect.DeepEqual(p1.Names, []string{"@acme/a", "left-pad"}) || p1.Done {
		t.Fatalf("page %+v", p1)
	}
	p2, _ := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodNames, Kind: discover.PassFull, Scope: sc, Cursor: p1.Cursor, Limit: 2})
	if !reflect.DeepEqual(p2.Names, []string{"weird*name"}) || !p2.Done {
		t.Fatalf("page %+v", p2)
	}
}

// R-REPO-054, R-REPO-051: the contract rule for a rate-limit answer.
func TestMirrorRateLimitIsReturnedUnchanged(t *testing.T) {
	// R-REPO-051 rule of the contract: a rate-limit error of the upstream reaches the engine as it came, and nothing more
	// is sent in that call.
	l := mirrorLister()
	sc := discover.Scope{Admits: func(string) bool { return true }}
	t.Run("on the root", func(t *testing.T) {
		up := newFakeMirrorUpstream(publicRegistryURL)
		up.getErr = rateLimit{}
		_, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc})
		if !errors.Is(err, rateLimit{}) {
			t.Fatalf("err %v", err)
		}
		if _, ok := discover.AsRateLimited(err); !ok || len(up.gets) != 1 {
			t.Fatalf("%d requests", len(up.gets))
		}
	})
	t.Run("on the changes", func(t *testing.T) {
		up := newFakeMirrorUpstream(publicRegistryURL)
		up.getErr = rateLimit{}
		_, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"1"}`), Scope: sc})
		if _, ok := discover.AsRateLimited(err); !ok || len(up.gets) != 1 {
			t.Fatalf("err %v, %d requests", err, len(up.gets))
		}
	})
	t.Run("on the second request of the first page", func(t *testing.T) {
		up := newFakeMirrorUpstream(publicRegistryURL)
		up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":1}` }
		up.serve["/registry/_all_docs"] = func(url.Values) (int, string) { return 200, allDocsRows("a") }
		// The first request passes, the second is limited.
		limited := &limitAfter{fakeMirrorUpstream: up, ok: 1}
		_, err := l.MirrorNames(context.Background(), limited, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc})
		if _, ok := discover.AsRateLimited(err); !ok || len(up.gets) != 2 {
			t.Fatalf("err %v, %d requests", err, len(up.gets))
		}
	})
	t.Run("on the document of a plan, wrapped errors included", func(t *testing.T) {
		up := newFakeMirrorUpstream(publicRegistryURL)
		up.docErr = fmt.Errorf("upstream: %w", rateLimit{})
		_, err := l.MirrorPlan(context.Background(), up, "left-pad")
		if _, ok := discover.AsRateLimited(err); !ok || !errors.Is(err, up.docErr) {
			t.Fatalf("err %v", err)
		}
	})
}

// limitAfter lets ok requests through and answers the next ones with a rate limit.
type limitAfter struct {
	*fakeMirrorUpstream
	ok int
}

func (l *limitAfter) Get(ctx context.Context, u *url.URL, accept string, max int64) (*discover.Response, error) {
	if len(l.gets) >= l.ok {
		l.gets = append(l.gets, u)
		return nil, rateLimit{}
	}
	return l.fakeMirrorUpstream.Get(ctx, u, accept, max)
}

func TestMirrorReplicationServiceAnswers(t *testing.T) {
	sc := discover.Scope{Admits: func(string) bool { return true }}
	for status, wantUnavailable := range map[int]bool{404: true, 410: true, 501: true, 500: false, 502: false, 401: false} {
		up := newFakeMirrorUpstream(publicRegistryURL)
		up.serve["/"] = func(url.Values) (int, string) { return status, "" }
		_, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc})
		if err == nil || errors.Is(err, discover.ErrMethodUnavailable) != wantUnavailable {
			t.Errorf("status %d: %v", status, err)
		}
	}
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"db_name":"registry"}` }
	if _, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc}); err == nil {
		t.Error("a root without update_seq was accepted")
	}
}

func TestMirrorRequestsGoOnlyToTheReplicationHost(t *testing.T) {
	// The format sends no request of its own and none to another host than the ones it declares (Q-NPM-72): every Get of a
	// pass is to MirrorHosts(Base()).
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":3}` }
	up.serve["/registry/_all_docs"] = func(url.Values) (int, string) { return 200, allDocsRows("a") }
	up.serve["/registry/_changes"] = func(url.Values) (int, string) { return 200, `{"results":[],"last_seq":3}` }
	sc := discover.Scope{Admits: func(string) bool { return true }}
	l := mirrorLister()
	_, _ = l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc})
	_, _ = l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"3"}`), Scope: sc})
	hosts := l.MirrorHosts(up.Base())
	for _, u := range up.gets {
		if u.Scheme != "https" || len(hosts) != 1 || u.Host != hosts[0] || u.User != nil {
			t.Errorf("request to %s", u)
		}
	}
}

func TestMirrorNamesAreAdmittedOnly(t *testing.T) {
	// Every name returned was checked by Scope.Admits, whatever the method returns it by.
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":3}` }
	up.serve["/registry/_all_docs"] = func(url.Values) (int, string) { return 200, allDocsRows("@acme/a", "@evil/a", "plain") }
	up.serve["/registry/_changes"] = func(url.Values) (int, string) {
		return 200, `{"results":[{"seq":4,"id":"@evil/a","deleted":true},{"seq":5,"id":"@acme/b"}],"last_seq":5}`
	}
	var asked []string
	sc := discover.Scope{Include: []string{"@acme/*"}, Admits: admits(&asked, "@acme/")}
	l := mirrorLister()
	p1, _ := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: sc})
	p2, _ := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassIncremental, Since: discover.Cursor(`{"seq":"3"}`), Scope: sc})
	all := append(append(append(append([]string{}, p1.Names...), p1.Removed...), p2.Names...), p2.Removed...)
	for _, n := range all {
		if !strings.HasPrefix(n, "@acme/") {
			t.Errorf("a refused name was returned: %s", n)
		}
	}
	if !reflect.DeepEqual(p1.Names, []string{"@acme/a"}) || len(p2.Removed) != 0 || !reflect.DeepEqual(p2.Names, []string{"@acme/b"}) {
		t.Fatalf("pages %+v %+v", p1, p2)
	}
	if len(asked) == 0 {
		t.Fatal("the scope was never asked")
	}
}

func TestMirrorCursorStaysBounded(t *testing.T) {
	long := "@acme/" + strings.Repeat("x", 300)
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.serve["/"] = func(url.Values) (int, string) { return 200, `{"update_seq":3}` }
	up.serve["/registry/_all_docs"] = pagedRows([]string{"@acme/a", long})
	c, err := mirrorCursor{Seq: "1", After: strings.Repeat("y", discover.MaxCursorBytes), Range: intp(0)}.encode()
	if err == nil || c != nil {
		t.Fatalf("a cursor over %d bytes was encoded", discover.MaxCursorBytes)
	}
	page, err := mirrorLister().MirrorNames(context.Background(), up, discover.PageRequest{
		Method: discover.MethodFeed, Kind: discover.PassFull, Scope: discover.Scope{Admits: func(string) bool { return true }}, Limit: 1,
	})
	if err != nil || len(page.Cursor) > discover.MaxCursorBytes {
		t.Fatalf("%v, cursor %d bytes", err, len(page.Cursor))
	}
}

// versionJSON is a version entry of a package document.
func versionJSON(pkg, version string) string {
	base := pkg[strings.LastIndex(pkg, "/")+1:]
	return fmt.Sprintf(`{"name":%q,"version":%q,"dist":{"tarball":"https://registry.npmjs.org/%s/-/%s-%s.tgz","integrity":"sha512-AAAA","unpackedSize":123456}}`,
		pkg, version, pkg, base, version)
}

func packageDoc(pkg, latest, attachments string, versions ...string) string {
	var vs []string
	for _, v := range versions {
		vs = append(vs, fmt.Sprintf("%q:%s", v, versionJSON(pkg, v)))
	}
	doc := fmt.Sprintf(`{"_id":%q,"name":%q,"dist-tags":{"latest":%q},"versions":{%s}`, pkg, pkg, latest, strings.Join(vs, ","))
	if attachments != "" {
		doc += `,"_attachments":` + attachments
	}
	return doc + `}`
}

func TestMirrorPlan(t *testing.T) {
	// Newest first by semver precedence (not by text, not by the document's order), the tarball's canonical path, the
	// prerelease flag by the pre-release part, the latest dist-tag's version pinned, the size only from a real tarball size.
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.docs["@acme/a"] = packageDoc("@acme/a", "1.1.0",
		`{"a-1.2.0.tgz":{"content_type":"application/octet-stream","length":2048,"stub":true},"a-1.0.0.tgz":{"length":"0"}}`,
		"1.0.0", "1.10.0", "1.2.0", "2.0.0-rc.1", "1.1.0", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.1+build.5", "not-a-version", "v1.0.0")
	plan, err := mirrorLister().MirrorPlan(context.Background(), up, "@acme/a")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "@acme/a" {
		t.Fatalf("name %q", plan.Name)
	}
	type row struct {
		v    string
		pre  bool
		pin  bool
		size int64
	}
	var got []row
	for _, it := range plan.Items {
		if want := "@acme/a/-/a-" + it.Version + ".tgz"; it.Path != want {
			t.Errorf("path %q, want %q", it.Path, want)
		}
		got = append(got, row{it.Version, it.Prerelease, it.Pinned, it.Size})
	}
	want := []row{
		{"2.0.0-rc.1", true, false, 0},
		{"1.10.0", false, false, 0},
		{"1.2.0", false, false, 2048},
		{"1.1.0", false, true, 0},
		{"1.0.0", false, false, 0},
		{"1.0.0-beta.11", true, false, 0},
		{"1.0.0-beta.2", true, false, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan\n got %v\nwant %v", got, want)
	}
}

func TestMirrorPlanUsesNoUnpackedSize(t *testing.T) {
	// R-REPO-052: the announced size is the tarball's. dist.unpackedSize is not, so a document without a tarball size
	// announces none.
	up := newFakeMirrorUpstream(publicRegistryURL)
	up.docs["left-pad"] = packageDoc("left-pad", "1.3.0", "", "1.3.0")
	plan, err := mirrorLister().MirrorPlan(context.Background(), up, "left-pad")
	if err != nil || len(plan.Items) != 1 || plan.Items[0].Size != 0 || plan.Items[0].Path != "left-pad/-/left-pad-1.3.0.tgz" {
		t.Fatalf("%+v, %v", plan, err)
	}
}

func TestMirrorPlanOfAnAbsentPackageIsEmpty(t *testing.T) {
	up := newFakeMirrorUpstream(publicRegistryURL)
	plan, err := mirrorLister().MirrorPlan(context.Background(), up, "@acme/gone")
	if err != nil || plan.Name != "@acme/gone" || len(plan.Items) != 0 {
		t.Fatalf("%+v, %v", plan, err)
	}
	// A package without versions (all unpublished) is empty as well.
	up.docs["@acme/none"] = `{"name":"@acme/none","versions":{},"time":{"unpublished":{"time":"2026-01-01T00:00:00.000Z"}}}`
	if plan, err = mirrorLister().MirrorPlan(context.Background(), up, "@acme/none"); err != nil || len(plan.Items) != 0 {
		t.Fatalf("%+v, %v", plan, err)
	}
	// A document that is no package document is an error of the item, not an empty plan.
	up.docs["@acme/bad"] = `[1]`
	if _, err = mirrorLister().MirrorPlan(context.Background(), up, "@acme/bad"); err == nil {
		t.Fatal("a document that is no object was planned")
	}
	if _, err = mirrorLister().MirrorPlan(context.Background(), up, "../x"); err == nil {
		t.Fatal("a name that is none was planned")
	}
	// The plan asks the stored document by the canonical name and nothing else of the upstream.
	if len(up.gets) != 0 {
		t.Fatalf("requests of the plan: %v", up.queries())
	}
}

func TestMirrorListerIsRegisteredWithTheCatalog(t *testing.T) {
	// R-REPO-050: the catalog fact Mirror follows the lister, so that mode mirror is accepted for npm.
	if !formats.CatalogEntry(New()).Mirror {
		t.Fatal("the catalog fact Mirror of npm is false")
	}
	if formats.CapabilitiesOf(New()).Remote.Mirror == nil {
		t.Fatal("the capabilities of npm carry no lister")
	}
}
