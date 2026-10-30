package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

type widget struct {
	Name   string            `json:"name"`
	Color  *string           `json:"color"`
	Tags   []string          `json:"tags"`
	Remote *widgetRemote     `json:"remote"`
	Labels map[string]string `json:"labels"`
	Rev    int64             `json:"revision"`
}

type widgetRemote struct {
	UpstreamURL string `json:"upstreamUrl"`
}

func apiCfg() config.API { return config.API{PageSizeDefault: 50, PageSizeMax: 500} }

func problemOf(t *testing.T, err error) (status int, code string, fields []errs.FieldError) {
	t.Helper()
	p, _ := errs.ProblemOf(err, "id", "/x")
	return p.Status, p.Code, p.Errors
}

func TestStrictDecoding(t *testing.T) {
	t.Run("R-API-003 an unknown member answers 400 with a pointer to it", func(t *testing.T) {
		var w widget
		err := StrictUnmarshal([]byte(`{"name":"a","remote":{"upsteamUrl":"x"}}`), &w)
		st, code, f := problemOf(t, err)
		if st != 400 || code != "malformed_request" || len(f) != 1 || f[0].Pointer != "/remote/upsteamUrl" {
			t.Fatalf("%d %s %+v", st, code, f)
		}
	})
	t.Run("R-API-003 a duplicate member answers 400 with a pointer to it", func(t *testing.T) {
		var w widget
		_, code, f := problemOf(t, StrictUnmarshal([]byte(`{"name":"a","name":"b"}`), &w))
		if code != "malformed_request" || f[0].Pointer != "/name" {
			t.Fatalf("%s %+v", code, f)
		}
	})
	t.Run("R-API-003 member names are compared exactly, unlike encoding/json", func(t *testing.T) {
		var w widget
		_, _, f := problemOf(t, StrictUnmarshal([]byte(`{"Name":"a"}`), &w))
		if len(f) != 1 || f[0].Pointer != "/Name" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("R-API-003 a wrong type names the member", func(t *testing.T) {
		var w widget
		_, _, f := problemOf(t, StrictUnmarshal([]byte(`{"remote":{"upstreamUrl":3}}`), &w))
		if len(f) != 1 || f[0].Pointer != "/remote/upstreamUrl" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("R-API-003 trailing data and syntax errors are rejected", func(t *testing.T) {
		var w widget
		for _, body := range []string{`{"name":"a"} {}`, `{"name":`, `[1]`, ``} {
			if err := StrictUnmarshal([]byte(body), &w); err == nil {
				t.Errorf("%q accepted", body)
			}
		}
	})
	t.Run("R-API-003 read-only members of a body obtained by GET are accepted", func(t *testing.T) {
		var w widget
		if err := StrictUnmarshal([]byte(`{"name":"a","revision":7,"color":null,"tags":[],"remote":null,"labels":{}}`), &w); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-003 R-API-017 content type, size and empty body", func(t *testing.T) {
		post := func(ct, body string) error {
			r := httptest.NewRequest("POST", "/x", strings.NewReader(body))
			if ct != "" {
				r.Header.Set("Content-Type", ct)
			}
			var w widget
			return DecodeJSON(httptest.NewRecorder(), r, &w, 16)
		}
		for _, c := range []struct {
			ct, body string
			status   int
		}{
			{"text/plain", `{}`, 415},
			{"", `{}`, 415},
			{"application/json; charset=latin1", `{}`, 415},
			{"application/json", strings.Repeat(" ", 40) + `{}`, 413},
			{"application/json", ``, 400},
			{"application/json; charset=utf-8", `{"name":"a"}`, 0},
		} {
			err := post(c.ct, c.body)
			if c.status == 0 {
				if err != nil {
					t.Errorf("%q: %v", c.ct, err)
				}
				continue
			}
			if st, _, _ := problemOf(t, err); st != c.status {
				t.Errorf("%q %q: status %d, want %d", c.ct, c.body, st, c.status)
			}
		}
	})
}

func TestMergePatch(t *testing.T) {
	// The examples of RFC 7396 appendix A.
	for _, c := range []struct{ orig, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`["a","b"]`, `["c","d"]`, `["c","d"]`},
		{`{"a":"b"}`, `["c"]`, `["c"]`},
		{`{"a":"foo"}`, `null`, `null`},
		{`{"a":"foo"}`, `"bar"`, `"bar"`},
		{`{"e":null}`, `{"a":1}`, `{"a":1,"e":null}`},
		{`[1,2]`, `{"a":"b","c":null}`, `{"a":"b"}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
	} {
		t.Run("R-API-013 RFC 7396 "+c.patch, func(t *testing.T) {
			got, err := MergePatch([]byte(c.orig), []byte(c.patch))
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			_ = json.Unmarshal(got, &g)
			_ = json.Unmarshal([]byte(c.want), &w)
			gb, _ := json.Marshal(g)
			wb, _ := json.Marshal(w)
			if string(gb) != string(wb) {
				t.Fatalf("got %s, want %s", gb, wb)
			}
		})
	}

	cur := widget{Name: "n", Color: ptr("red"), Tags: []string{"a"}, Remote: &widgetRemote{UpstreamURL: "https://u"}, Rev: 3}
	t.Run("R-API-013 null removes a member, an absent one stays, an array is replaced", func(t *testing.T) {
		got, err := ApplyMergePatch(cur, []byte(`{"color":null,"tags":["b","c"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if got.Color != nil || len(got.Tags) != 2 || got.Remote == nil || got.Remote.UpstreamURL != "https://u" || got.Name != "n" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("R-API-013 a nested member is merged", func(t *testing.T) {
		got, err := ApplyMergePatch(cur, []byte(`{"remote":{"upstreamUrl":"https://v"}}`))
		if err != nil || got.Remote.UpstreamURL != "https://v" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-API-003 R-API-013 an unknown member of the patch names its pointer", func(t *testing.T) {
		_, err := ApplyMergePatch(cur, []byte(`{"remote":{"upsteamUrl":"x"}}`))
		if _, _, f := problemOf(t, err); len(f) != 1 || f[0].Pointer != "/remote/upsteamUrl" {
			t.Fatalf("%v %+v", err, f)
		}
	})
	t.Run("R-API-013 an immutable member that differs answers 422 with its pointer, an equal one is ignored", func(t *testing.T) {
		same, _ := ApplyMergePatch(cur, []byte(`{"name":"n","color":"blue"}`))
		if err := CheckImmutable(cur, same, "name"); err != nil {
			t.Fatal(err)
		}
		changed, _ := ApplyMergePatch(cur, []byte(`{"name":"other"}`))
		st, code, f := problemOf(t, CheckImmutable(cur, changed, "name"))
		if st != 422 || code != "validation_failed" || f[0].Pointer != "/name" {
			t.Fatalf("%d %s %+v", st, code, f)
		}
	})
	t.Run("R-API-013 the patch body must be an object with the merge-patch media type", func(t *testing.T) {
		read := func(ct, body string) error {
			r := httptest.NewRequest("PATCH", "/x", strings.NewReader(body))
			r.Header.Set("Content-Type", ct)
			_, err := ReadMergePatch(httptest.NewRecorder(), r, 1024)
			return err
		}
		if st, _, _ := problemOf(t, read("application/json", `{}`)); st != 415 {
			t.Errorf("application/json: %d", st)
		}
		if st, _, _ := problemOf(t, read(MediaMergePatch, `[1]`)); st != 400 {
			t.Errorf("array patch: %d", st)
		}
		if err := read(MediaMergePatch, `{"a":null}`); err != nil {
			t.Error(err)
		}
	})
}

func TestETag(t *testing.T) {
	req := func(h ...string) *http.Request {
		r := httptest.NewRequest("PATCH", "/x", nil)
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Add(h[i], h[i+1])
		}
		return r
	}
	t.Run("R-API-012 the tag is the quoted revision, with the view where the representation depends on it", func(t *testing.T) {
		if ETag(4, "") != `"4"` || ETag(4, "config") != `"4-config"` {
			t.Fatal(ETag(4, ""), ETag(4, "config"))
		}
	})
	t.Run("R-API-012 without If-Match a write answers 428 precondition_required", func(t *testing.T) {
		p, err := ParseIfMatch(req())
		if err != nil {
			t.Fatal(err)
		}
		st, code, _ := problemOf(t, p.Require())
		if st != 428 || code != "precondition_required" {
			t.Fatalf("%d %s", st, code)
		}
	})
	t.Run("R-API-012 If-Match carries one strong tag or *", func(t *testing.T) {
		p, _ := ParseIfMatch(req("If-Match", `"4"`))
		if rev, ok := p.Revision(""); !ok || rev != 4 || !p.Matches(4, "") || p.Matches(5, "") {
			t.Fatalf("%+v", p)
		}
		star, _ := ParseIfMatch(req("If-Match", "*"))
		if _, ok := star.Revision(""); ok || !star.Matches(99, "") {
			t.Fatalf("%+v", star)
		}
		for _, bad := range []string{`W/"4"`, `"4", "5"`, `4`, `""x`} {
			if _, err := ParseIfMatch(req("If-Match", bad)); err == nil {
				t.Errorf("%q accepted", bad)
			}
		}
	})
	t.Run("R-API-012 a tag of another view or a garbled revision matches nothing", func(t *testing.T) {
		p, _ := ParseIfMatch(req("If-Match", `"4-config"`))
		if rev, ok := p.Revision("config"); !ok || rev != 4 {
			t.Fatal(rev, ok)
		}
		if rev, ok := p.Revision("summary"); !ok || rev != 0 {
			t.Fatal("a tag of another view must not select a revision", rev, ok)
		}
		if rev, ok := p.Revision(""); !ok || rev != 0 {
			t.Fatal("a tag with a view on a resource without one", rev, ok)
		}
	})
	t.Run("R-API-012 a mismatch is 412 with currentEtag", func(t *testing.T) {
		rec := httptest.NewRecorder()
		errs.Write(rec, httptest.NewRequest("PATCH", "/x", nil), Failed(5), "id", nil)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if rec.Code != 412 || m["currentEtag"] != `"5"` || m["code"] != "precondition_failed" {
			t.Fatalf("%d %v", rec.Code, m)
		}
	})
	t.Run("R-API-012 If-None-Match answers 304 on a match", func(t *testing.T) {
		for _, c := range []struct {
			h    string
			want bool
		}{{`"4"`, true}, {`W/"4"`, true}, {`"3", "4"`, true}, {`*`, true}, {`"5"`, false}} {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/x", nil)
			r.Header.Set("If-None-Match", c.h)
			if got := NotModified(rec, r, `"4"`); got != c.want || (got && (rec.Code != 304 || rec.Header().Get("ETag") != `"4"`)) {
				t.Errorf("%q: %v %d", c.h, got, rec.Code)
			}
		}
	})
}

func TestPaging(t *testing.T) {
	spec := ListSpec{Filters: []string{"format", "q"}, Sorts: []string{"name", "createdAt"}, DefaultSort: "name"}
	parse := func(rawQuery string) (ListQuery, error) {
		return ParseList(httptest.NewRequest("GET", "/api/v1/things?"+rawQuery, nil), spec, apiCfg())
	}
	t.Run("R-API-010 limit defaults to 50 and is clamped to the maximum", func(t *testing.T) {
		q, err := parse("")
		if err != nil || q.Limit != 50 || q.FetchLimit() != 51 || q.Sort != "name" || q.Desc {
			t.Fatalf("%+v %v", q, err)
		}
		if q, _ := parse("limit=9999"); q.Limit != 500 {
			t.Fatal(q.Limit)
		}
		for _, bad := range []string{"limit=0", "limit=x", "limit="} {
			if _, err := parse(bad); err == nil {
				t.Errorf("%s accepted", bad)
			}
		}
	})
	t.Run("R-API-011 an unknown parameter or sort field answers 400 naming it", func(t *testing.T) {
		_, err := parse("fromat=npm")
		if st, code, _ := problemOf(t, err); st != 400 || code != "malformed_request" || !strings.Contains(err.Error(), "fromat") {
			t.Fatalf("%v", err)
		}
		if _, err := parse("sort=bogus"); err == nil || !strings.Contains(err.Error(), "bogus") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-API-011 filters may repeat and sort has a direction", func(t *testing.T) {
		q, err := parse("format=npm&format=nuget&sort=-createdAt")
		if err != nil || len(q.Filter["format"]) != 2 || q.Sort != "createdAt" || !q.Desc {
			t.Fatalf("%+v %v", q, err)
		}
	})
	t.Run("R-API-010 the cursor is opaque and carries the sort key of the last item", func(t *testing.T) {
		q, _ := parse("limit=2")
		rows := []string{"a", "b", "c"}
		p := NewPage(q, rows, func(s string) []string { return []string{s, "id-" + s} })
		if len(p.Items) != 2 || p.NextCursor == nil {
			t.Fatalf("%+v", p)
		}
		if strings.ContainsAny(*p.NextCursor, "{}\"=+/ ") {
			t.Fatalf("cursor is not base64url: %q", *p.NextCursor)
		}
		q2, err := parse("limit=2&cursor=" + *p.NextCursor)
		if err != nil || len(q2.After) != 2 || q2.After[0] != "b" || q2.After[1] != "id-b" {
			t.Fatalf("%+v %v", q2, err)
		}
	})
	t.Run("R-API-010 the last page has no cursor and empty lists are []", func(t *testing.T) {
		q, _ := parse("limit=5")
		p := NewPage(q, []string(nil), func(s string) []string { return nil })
		b, _ := json.Marshal(p)
		if string(b) != `{"items":[],"nextCursor":null}` {
			t.Fatalf("%s", b)
		}
	})
	t.Run("R-API-010 a cursor for another sort or other filters answers 400 invalid_cursor", func(t *testing.T) {
		q, _ := parse("limit=1")
		p := NewPage(q, []string{"a", "b"}, func(s string) []string { return []string{s, s} })
		for _, other := range []string{"sort=-name", "sort=createdAt", "format=npm", "q=x"} {
			_, err := parse("cursor=" + *p.NextCursor + "&" + other)
			if st, code, _ := problemOf(t, err); st != 400 || code != "invalid_cursor" {
				t.Errorf("%s: %v", other, err)
			}
		}
		for _, bad := range []string{"%%%", "bm90LWEtY3Vyc29y", "", "djIuZXlKcklqcGJYWDA"} {
			if _, err := parse("cursor=" + bad); err == nil {
				t.Errorf("cursor %q accepted", bad)
			}
		}
	})
	t.Run("R-API-010 the fingerprint ignores filter order and unused filters", func(t *testing.T) {
		a, _ := parse("format=npm&format=nuget&limit=1")
		b, _ := parse("limit=7&format=nuget&format=npm")
		// The cursor carries the fingerprint, so equal cursors for one key mean equal fingerprints.
		if a.EncodeCursor([]string{"k"}) != b.EncodeCursor([]string{"k"}) {
			t.Fatal("OR values in another order must give the same fingerprint")
		}
		wider := ListSpec{Filters: []string{"format", "q", "newFilter"}, Sorts: spec.Sorts, DefaultSort: "name"}
		c, _ := ParseList(httptest.NewRequest("GET", "/x?format=npm&format=nuget", nil), wider, apiCfg())
		if a.EncodeCursor([]string{"k"}) != c.EncodeCursor([]string{"k"}) {
			t.Fatal("a release that adds an optional filter must not change the fingerprint of a request that does not use it")
		}
	})
	t.Run("R-API-010 Link names the next page on the public base URL", func(t *testing.T) {
		q, _ := parse("limit=1&format=npm")
		p := NewPage(q, []string{"a", "b"}, func(s string) []string { return []string{s, s} })
		rec := httptest.NewRecorder()
		WritePage(rec, httptest.NewRequest("GET", "/api/v1/things?limit=1&format=npm", nil), p)
		link := rec.Header().Get("Link")
		if !strings.HasPrefix(link, "</api/v1/things?") || !strings.HasSuffix(link, `>; rel="next"`) || !strings.Contains(link, "format=npm") || !strings.Contains(link, "cursor=") {
			t.Fatalf("Link = %q", link)
		}
		rec = httptest.NewRecorder()
		WritePage(rec, httptest.NewRequest("GET", "/api/v1/things", nil), NewPage(q, []string{"a"}, func(s string) []string { return nil }))
		if rec.Header().Get("Link") != "" {
			t.Fatal("the last page has a Link header")
		}
	})
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := CORS([]string{"https://portal.example.com"})(ok)
	do := func(method, origin string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/system/info", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	t.Run("R-API-018 a preflight from an unlisted origin gets no Access-Control-Allow-Origin", func(t *testing.T) {
		rec := do("OPTIONS", "https://unlisted.example", "Access-Control-Request-Method", "GET")
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("allowed")
		}
	})
	t.Run("R-API-018 a listed origin's preflight is answered, credentials are never allowed", func(t *testing.T) {
		rec := do("OPTIONS", "https://portal.example.com", "Access-Control-Request-Method", "PATCH")
		if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "https://portal.example.com" ||
			!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") ||
			!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Trove-Authz-Min-Epoch") {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credentials allowed")
		}
	})
	t.Run("R-API-018 the actual response of a request with Authorization exposes the headers", func(t *testing.T) {
		rec := do("GET", "https://portal.example.com", "Authorization", "Bearer x")
		if rec.Header().Get("Access-Control-Allow-Origin") != "https://portal.example.com" ||
			!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "RateLimit-Policy") ||
			rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%v", rec.Header())
		}
		if rec := do("GET", "https://portal.example.com"); rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("a request without Authorization gets CORS headers")
		}
	})
	t.Run("R-API-018 CORS is off by default: no Access-Control header at all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("OPTIONS", "/api/v1/system/info", nil)
		r.Header.Set("Origin", "https://portal.example.com")
		r.Header.Set("Access-Control-Request-Method", "GET")
		CORS(nil)(ok).ServeHTTP(rec, r)
		for k := range rec.Header() {
			if strings.HasPrefix(k, "Access-Control") {
				t.Fatalf("header %s", k)
			}
		}
	})
}
