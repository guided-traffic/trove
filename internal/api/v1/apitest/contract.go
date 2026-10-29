package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Resource describes one collection for the contract tests of the API
// conventions (exit criterion E1.6): cursor pagination, ETag with If-Match,
// JSON merge patch and idempotency keys. The tests are generic, so the
// repository resource (R-API-022) is checked with the same suite as the test
// resource that proves the helpers: fill the fields and call [RunContract].
type Resource struct {
	// Name labels the subtests, for example "repositories".
	Name string
	// Do sends a request through the stack under test and returns the answer. A
	// test with several replicas spreads the calls over them; Do adds the
	// credential. The caller closes nothing: bodies are read here.
	Do func(*http.Request) (*http.Response, error)
	// Collection is the absolute URL of the collection, without a query.
	Collection string
	// Create returns the body that creates item n. Every n yields a distinct
	// key, and the key of one run does not collide with another run's.
	Create func(n int) map[string]any
	// Key returns the identifier a created resource is addressed by, from its
	// representation. The item URL is Collection + "/" + Key.
	Key func(created map[string]any) string
	// Patch is a merge patch that is valid for every created item and changes
	// PatchMember to PatchValue.
	Patch       map[string]any
	PatchMember string
	PatchValue  any
	// ClearMember is a member that null resets or removes; ClearedValue is what
	// the representation shows afterwards (nil for a member that becomes null).
	ClearMember  string
	ClearedValue any
	// ImmutableMember is a member whose change is refused with 422.
	ImmutableMember string
	ImmutableValue  any
	// OtherQuery is a query string (for example "sort=-name") that the list
	// accepts and that changes the sort or a filter; a cursor issued without it
	// must be refused with it.
	OtherQuery string
	// Put is true when the resource offers PUT of the whole representation.
	Put bool
	// RevisionMember is the member that grows with every write: "revision" unless
	// the resource names it otherwise (a policy calls it "version"). A write does
	// not count as changing the other members when only it and the update time
	// differ.
	RevisionMember string
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", a.status, err, a.body)
	}
	return m
}

func (r Resource) call(t testing.TB, method, url string, body any, hdr ...string) answer {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := r.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return answer{status: res.StatusCode, header: res.Header, body: b}
}

func wantProblem(t testing.TB, a answer, status int, code string) map[string]any {
	t.Helper()
	m := a.json(t)
	if a.status != status || m["code"] != code {
		t.Fatalf("answer %d %v, want %d %s\n%s", a.status, m["code"], status, code, a.body)
	}
	if !strings.HasPrefix(a.header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("Content-Type = %q, want application/problem+json", a.header.Get("Content-Type"))
	}
	return m
}

func revisionOf(t testing.TB, etag string) int64 {
	t.Helper()
	inner := strings.Trim(etag, `"`)
	num, _, _ := strings.Cut(inner, "-")
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		t.Fatalf("ETag %q is not a quoted revision", etag)
	}
	return n
}

// suite carries the counters the subtests share.
type suite struct {
	res Resource
	n   int
}

func (s *suite) next() int { s.n++; return 1000 + s.n }

// create makes one item and reads it back: its key, URL, representation and tag.
func (s *suite) create(t *testing.T) (key, url string, rep map[string]any, etag string) {
	t.Helper()
	a := s.res.call(t, "POST", s.res.Collection, s.res.Create(s.next()))
	if a.status != 201 {
		t.Fatalf("create answered %d\n%s", a.status, a.body)
	}
	key = s.res.Key(a.json(t))
	url = s.res.Collection + "/" + key
	g := s.res.call(t, "GET", url, nil)
	if g.status != 200 {
		t.Fatalf("GET %s answered %d\n%s", url, g.status, g.body)
	}
	return key, url, g.json(t), g.header.Get("ETag")
}

// RunContract runs the convention tests against the resource. Each subtest
// creates its own items.
func RunContract(t *testing.T, res Resource) {
	t.Helper()
	s := &suite{res: res}
	for _, c := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"R-API-004 a create answers 201 and Location is the URL of the resource", s.testCreate},
		{"R-API-003 an unknown member of a create answers 400 with its pointer", s.testUnknownMember},
		{"R-API-010 a traversal with limit=1 visits every item once, on any replica", s.testTraversal},
		{"R-API-010 a cursor is refused for other filters or another sort, and when it is not ours", s.testCursorRefused},
		{"R-API-012 GET answers a strong ETag and 304 for If-None-Match", s.testETag},
		{"R-API-012 R-API-013 PATCH needs If-Match; a stale tag is 412 with currentEtag; the right one succeeds", s.testIfMatch},
		{"R-API-013 merge patch: null clears, absent stays, immutable members and unknown members are refused", s.testMergePatch},
		{"R-API-014 an Idempotency-Key replays the outcome, byte for byte, on any replica", s.testIdempotency},
	} {
		t.Run(res.Name+" "+c.name, c.run)
	}
	if res.Put {
		t.Run(res.Name+" R-API-003 a GET body sent back with PUT and If-Match changes nothing", s.testPut)
	}
}

func (s *suite) testCreate(t *testing.T) {
	a := s.res.call(t, "POST", s.res.Collection, s.res.Create(s.next()))
	if a.status != 201 {
		t.Fatalf("%d\n%s", a.status, a.body)
	}
	loc := a.header.Get("Location")
	if loc == "" || s.res.call(t, "GET", loc, nil).status != 200 {
		t.Fatalf("Location %q does not name the resource", loc)
	}
}

func (s *suite) testUnknownMember(t *testing.T) {
	body := s.res.Create(s.next())
	body["upsteamUrl"] = "x"
	m := wantProblem(t, s.res.call(t, "POST", s.res.Collection, body), 400, "malformed_request")
	errs, _ := m["errors"].([]any)
	if len(errs) == 0 || errs[0].(map[string]any)["pointer"] != "/upsteamUrl" {
		t.Fatalf("errors = %v", m["errors"])
	}
}

func (s *suite) testTraversal(t *testing.T) {
	want := map[string]bool{}
	for range 4 {
		k, _, _, _ := s.create(t)
		want[k] = true
	}
	seen := map[string]int{}
	replicas := map[string]bool{}
	url := s.res.Collection + "?limit=1"
	for page := 0; ; page++ {
		if page > 100_000 {
			t.Fatal("the traversal does not end")
		}
		a := s.res.call(t, "GET", url, nil)
		if a.status != 200 {
			t.Fatalf("page %d answered %d\n%s", page, a.status, a.body)
		}
		m := a.json(t)
		items, _ := m["items"].([]any)
		if len(items) > 1 {
			t.Fatalf("page %d holds %d items for limit=1", page, len(items))
		}
		for _, it := range items {
			seen[s.res.Key(it.(map[string]any))]++
		}
		if r := a.header.Get("X-Trove-Replica"); r != "" {
			replicas[r] = true
		}
		cursor, _ := m["nextCursor"].(string)
		if m["nextCursor"] == nil {
			if a.header.Get("Link") != "" {
				t.Fatal("the last page has a Link header")
			}
			break
		}
		if link := a.header.Get("Link"); cursor == "" || !strings.Contains(link, `rel="next"`) || !strings.Contains(link, cursor) {
			t.Fatalf("page %d: nextCursor %q, Link %q", page, cursor, link)
		}
		url = s.res.Collection + "?limit=1&cursor=" + cursor
	}
	for k := range want {
		if seen[k] != 1 {
			t.Errorf("item %s was returned %d times", k, seen[k])
		}
	}
	for k, c := range seen {
		if c != 1 {
			t.Errorf("item %s was returned %d times", k, c)
		}
	}
	t.Logf("the traversal was answered by %d replica(s)", len(replicas))
}

func (s *suite) testCursorRefused(t *testing.T) {
	a := s.res.call(t, "GET", s.res.Collection+"?limit=1", nil)
	cursor, _ := a.json(t)["nextCursor"].(string)
	if cursor == "" {
		s.create(t)
		s.create(t)
		cursor, _ = s.res.call(t, "GET", s.res.Collection+"?limit=1", nil).json(t)["nextCursor"].(string)
	}
	if s.res.OtherQuery != "" {
		wantProblem(t, s.res.call(t, "GET", s.res.Collection+"?limit=1&cursor="+cursor+"&"+s.res.OtherQuery, nil), 400, "invalid_cursor")
	}
	wantProblem(t, s.res.call(t, "GET", s.res.Collection+"?cursor=bm90LWEtY3Vyc29y", nil), 400, "invalid_cursor")
	wantProblem(t, s.res.call(t, "GET", s.res.Collection+"?fromat=npm", nil), 400, "malformed_request")
}

func (s *suite) testETag(t *testing.T) {
	_, url, _, etag := s.create(t)
	if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
		t.Fatalf("ETag = %q", etag)
	}
	if a := s.res.call(t, "GET", url, nil, "If-None-Match", etag); a.status != 304 || len(a.body) != 0 {
		t.Fatalf("%d %q", a.status, a.body)
	}
	if a := s.res.call(t, "GET", url, nil, "If-None-Match", `"0"`); a.status != 200 {
		t.Fatalf("a stale If-None-Match answered %d", a.status)
	}
}

func (s *suite) testIfMatch(t *testing.T) {
	res := s.res
	_, url, _, etag := s.create(t)
	patch := func(extra ...string) answer {
		return res.call(t, "PATCH", url, res.Patch, append([]string{"Content-Type", "application/merge-patch+json"}, extra...)...)
	}
	wantProblem(t, patch(), 428, "precondition_required")
	if g := res.call(t, "GET", url, nil); g.header.Get("ETag") != etag {
		t.Fatal("a refused PATCH changed the resource")
	}
	m := wantProblem(t, patch("If-Match", `"0"`), 412, "precondition_failed")
	if m["currentEtag"] != etag {
		t.Fatalf("currentEtag = %v, want %s", m["currentEtag"], etag)
	}
	ok := patch("If-Match", etag)
	if ok.status != 200 {
		t.Fatalf("PATCH answered %d\n%s", ok.status, ok.body)
	}
	newTag := ok.header.Get("ETag")
	if revisionOf(t, newTag) <= revisionOf(t, etag) {
		t.Fatalf("ETag %s did not grow from %s", newTag, etag)
	}
	if got := ok.json(t)[res.PatchMember]; !equalJSON(got, res.PatchValue) {
		t.Fatalf("%s = %v, want %v", res.PatchMember, got, res.PatchValue)
	}
	// The loser of the race: the same tag again.
	wantProblem(t, patch("If-Match", etag), 412, "precondition_failed")
	// A blind overwrite is an explicit choice.
	if star := patch("If-Match", "*"); star.status != 200 {
		t.Fatalf("If-Match: * answered %d\n%s", star.status, star.body)
	}
	// Every replica serves the new tag at once (R-API-052).
	if g := res.call(t, "GET", url, nil); revisionOf(t, g.header.Get("ETag")) <= revisionOf(t, newTag) {
		t.Fatalf("a read after the write shows ETag %s, want above %s", g.header.Get("ETag"), newTag)
	}
}

func (s *suite) testMergePatch(t *testing.T) {
	res := s.res
	_, url, before, etag := s.create(t)
	patch := func(body any, tag string) answer {
		return res.call(t, "PATCH", url, body, "Content-Type", "application/merge-patch+json", "If-Match", tag)
	}
	if res.call(t, "PATCH", url, res.Patch, "Content-Type", "application/json", "If-Match", etag).status != 415 {
		t.Error("a merge patch with application/json must answer 415")
	}
	if res.ClearMember != "" {
		a := patch(map[string]any{res.ClearMember: nil}, etag)
		if a.status != 200 || !equalJSON(a.json(t)[res.ClearMember], res.ClearedValue) {
			t.Fatalf("clearing %s: %d %v\n%s", res.ClearMember, a.status, a.json(t)[res.ClearMember], a.body)
		}
		etag = a.header.Get("ETag")
		assertUnchanged(t, before, a.json(t), res.ClearMember, res.RevisionMember)
	}
	wantProblem(t, patch(map[string]any{"upsteamUrl": 1}, etag), 400, "malformed_request")
	if res.ImmutableMember == "" {
		return
	}
	m := wantProblem(t, patch(map[string]any{res.ImmutableMember: res.ImmutableValue}, etag), 422, "validation_failed")
	errs, _ := m["errors"].([]any)
	if len(errs) == 0 || errs[0].(map[string]any)["pointer"] != "/"+res.ImmutableMember {
		t.Fatalf("errors = %v", m["errors"])
	}
	// The stored value, sent back unchanged, is ignored.
	if a := patch(map[string]any{res.ImmutableMember: before[res.ImmutableMember]}, etag); a.status != 200 {
		t.Fatalf("an equal immutable member answered %d\n%s", a.status, a.body)
	}
}

// assertUnchanged fails when a member the patch did not mention changed.
func assertUnchanged(t *testing.T, before, after map[string]any, except, revision string) {
	t.Helper()
	for k, v := range before {
		if k == except || k == "revision" || k == revision || k == "updatedAt" || k == "etag" {
			continue
		}
		if !equalJSON(after[k], v) {
			t.Errorf("a member the patch did not mention changed: %s = %v, was %v", k, after[k], v)
		}
	}
}

func (s *suite) testPut(t *testing.T) {
	res := s.res
	_, url, _, etag := s.create(t)
	g := res.call(t, "GET", url, nil)
	wantProblem(t, res.call(t, "PUT", url, g.body), 428, "precondition_required")
	if p := res.call(t, "PUT", url, g.body, "If-Match", etag); p.status != 200 {
		t.Fatalf("PUT answered %d\n%s", p.status, p.body)
	}
	assertUnchanged(t, g.json(t), res.call(t, "GET", url, nil).json(t), "", res.RevisionMember)
}

func (s *suite) testIdempotency(t *testing.T) {
	res := s.res
	key := "contract-" + strconv.Itoa(s.next()) + "-" + strconv.Itoa(len(res.Name))
	body := res.Create(s.next())
	first := res.call(t, "POST", res.Collection, body, "Idempotency-Key", key)
	if first.status != 201 {
		t.Fatalf("first request: %d\n%s", first.status, first.body)
	}
	if first.header.Get("Idempotency-Replayed") != "" {
		t.Fatal("the first answer is marked replayed")
	}
	for range 3 {
		again := res.call(t, "POST", res.Collection, body, "Idempotency-Key", key)
		if again.status != 201 || again.header.Get("Idempotency-Replayed") != "true" ||
			!bytes.Equal(again.body, first.body) || again.header.Get("Location") != first.header.Get("Location") {
			t.Fatalf("replay: %d replayed=%q location=%q\n%s\nfirst:\n%s",
				again.status, again.header.Get("Idempotency-Replayed"), again.header.Get("Location"), again.body, first.body)
		}
	}
	wantProblem(t, res.call(t, "POST", res.Collection, res.Create(s.next()), "Idempotency-Key", key), 422, "idempotency_key_reused")
	if g := res.call(t, "GET", res.Collection+"/"+res.Key(first.json(t)), nil); g.status != 200 {
		t.Fatalf("the resource of the first request: %d", g.status)
	}
}

// equalJSON compares two decoded JSON values.
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
