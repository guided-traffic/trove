package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
)

// fakeCache stands in for the invalidation service and the repository lookup.
type fakeCache struct {
	repos    map[string]repository.Stored
	calls    []cacheCall
	entries  []invalidation.Entry
	filter   invalidation.EntryFilter
	count    int64
	inner    CacheInvalidations // when set, Invalidate goes to it (the real service)
	entryErr error
}

type cacheCall struct {
	repo          repository.Repository
	prefix, scope string
}

func (f *fakeCache) Get(_ context.Context, key string) (repository.Stored, error) {
	st, ok := f.repos[key]
	if !ok {
		return repository.Stored{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository has the key "+key)
	}
	return st, nil
}

func (f *fakeCache) Invalidate(ctx context.Context, c repository.Caller, repo repository.Repository, prefix, scope string) (int64, error) {
	f.calls = append(f.calls, cacheCall{repo, prefix, scope})
	if f.inner != nil {
		return f.inner.Invalidate(ctx, c, repo, prefix, scope)
	}
	return f.count, nil
}

func (f *fakeCache) Entries(_ context.Context, _ int64, flt invalidation.EntryFilter) ([]invalidation.Entry, error) {
	f.filter = flt
	if f.entryErr != nil {
		return nil, f.entryErr
	}
	var out []invalidation.Entry
	for _, e := range f.entries {
		if flt.After != nil && e.Path <= flt.After.Path && (e.Path < flt.After.Path || e.Variant <= flt.After.Variant) {
			continue
		}
		out = append(out, e)
	}
	if len(out) > flt.Limit {
		out = out[:flt.Limit]
	}
	return out, nil
}

func storedRepo(id, key string, class repository.Class) repository.Stored {
	return repository.Stored{Repository: repository.Repository{ID: id, Key: key, Class: class, Format: "npm", Lifecycle: repository.Active}}
}

// noRunner is a runner that must not be reached: the refusals under test happen first.
type noRunner struct{ meta.Runner }

type noNotifier struct{}

func (noNotifier) Notify(meta.Tx, string, int, any) error { return nil }

// holdsAccess is an Access whose only question is repositories:read-config.
type holdsAccess struct{ config bool }

func (holdsAccess) Require(context.Context, string) error { return nil }

func (a holdsAccess) Holds(_ context.Context, permission string) (bool, error) {
	return a.config && permission == string(authz.RepositoriesRead), nil
}

// cacheDecider is a decision point with fixed answers.
type cacheDecider struct {
	authz.Decider
	authz.Authorizer
	manage bool // the caller holds manage
	hidden bool // the repository is hidden from the caller
}

func (d cacheDecider) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	return nil, nil
}

func (d cacheDecider) Know(_ context.Context, idx *authz.Index, _ authz.Subject, _ string) (*authz.Index, error) {
	return idx, nil
}

func (d cacheDecider) ExposedOn(_ *authz.Index, _ authz.Subject, keys []string) []string {
	if d.hidden {
		return nil
	}
	return keys
}

func (d cacheDecider) DecideOn(_ context.Context, _ *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	switch {
	case len(c.Actions) != 1 || c.Actions[0] != authz.Manage:
		return authz.Decision{Outcome: authz.DenyVisible, Reason: "wrong coordinate"}
	case d.hidden:
		return authz.Decision{Outcome: authz.DenyHidden}
	case !d.manage:
		return authz.Decision{Outcome: authz.DenyVisible, Reason: authz.ReasonNoGrant, Missing: authz.Manage, Target: c.Repository}
	}
	return authz.Decision{Outcome: authz.Allow}
}

func cacheSetup(t *testing.T, val *apitest.Validator, fc *fakeCache, access Access, dec authz.Decider) setup {
	t.Helper()
	s := newSetup(t, func(o *Options) {
		o.Access = access
		o.API.Config = apiConfigForTest()
		o.API.Config.MaxRequestBodyBytes = 1 << 20
		o.Cache = CacheServices{Invalidations: fc, Repositories: fc, Authz: dec}
	}, authed)
	s.handler = val.Handler(t, s.handler)
	return s
}

func (s setup) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	return w
}

func problem(t *testing.T, w *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != status || m["code"] != code {
		t.Fatalf("answer %d %v, want %d %s\n%s", w.Code, m["code"], status, code, w.Body.String())
	}
	return m
}

const invalidatePath = "/api/v1/repositories/npm-remote/cache/invalidate"

func testInvalidateRepositoryCache(t *testing.T, val *apitest.Validator) {
	remote := storedRepo("7", "npm-remote", repository.Remote)
	t.Run("R-API-024 R-OPS-021 the operation declares manage on the repository of the path", func(t *testing.T) {
		acc := &actionAccess{}
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote}, count: 12}
		s := cacheSetup(t, val, fc, acc, nil)
		w := s.do("POST", invalidatePath, `{"path":"@acme","scope":"all"}`, nil)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		c := acc.coordinate
		if c.Repository != "npm-remote" || len(c.Actions) != 1 || c.Actions[0] != authz.Manage || c.Kind != authz.KindWrite {
			t.Fatalf("coordinate = %+v", c)
		}
		var got map[string]int64
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["invalidated"] != 12 {
			t.Errorf("body = %s", w.Body)
		}
		if len(fc.calls) != 1 || fc.calls[0].prefix != "@acme" || fc.calls[0].scope != "all" || fc.calls[0].repo.ID != "7" {
			t.Errorf("calls = %+v", fc.calls)
		}
	})
	t.Run("R-API-024 without manage the answer is 403 and nothing is invalidated", func(t *testing.T) {
		acc := &actionAccess{err: errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")}
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote}}
		s := cacheSetup(t, val, fc, acc, nil)
		problem(t, s.do("POST", invalidatePath, `{"path":"@acme"}`, nil), 403, "forbidden")
		if len(fc.calls) != 0 {
			t.Errorf("the handler ran: %+v", fc.calls)
		}
	})
	t.Run("an absent path and scope reach the service as empty, which it reads as the whole repository and metadata", func(t *testing.T) {
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote}}
		s := cacheSetup(t, val, fc, &actionAccess{}, nil)
		if w := s.do("POST", invalidatePath, `{}`, nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if len(fc.calls) != 1 || fc.calls[0].prefix != "" || fc.calls[0].scope != "" {
			t.Errorf("calls = %+v", fc.calls)
		}
	})
	t.Run("R-API-024 a local repository answers 400 not_applicable", func(t *testing.T) {
		svc, err := invalidation.NewService(invalidation.ServiceOptions{Runner: noRunner{}, Notifier: noNotifier{}})
		if err != nil {
			t.Fatal(err)
		}
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": storedRepo("3", "npm-remote", repository.Local)}, inner: svc}
		s := cacheSetup(t, val, fc, &actionAccess{}, nil)
		m := problem(t, s.do("POST", invalidatePath, `{"path":"@acme"}`, nil), 400, "not_applicable")
		if !strings.Contains(m["detail"].(string), "local") {
			t.Errorf("detail = %v", m["detail"])
		}
	})
	t.Run("R-REPO-045 a prefix that could cover no path and an unknown scope are 422 with the field", func(t *testing.T) {
		svc, _ := invalidation.NewService(invalidation.ServiceOptions{Runner: noRunner{}, Notifier: noNotifier{}})
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote}, inner: svc}
		s := cacheSetup(t, val, fc, &actionAccess{}, nil)
		for body, pointer := range map[string]string{
			`{"path":"/@acme"}`: "/path", `{"path":"a//b"}`: "/path", `{"path":"@acme/"}`: "/path", `{"scope":"everything"}`: "/scope",
		} {
			m := problem(t, s.do("POST", invalidatePath, body, nil), 422, "validation_failed")
			list, _ := m["errors"].([]any)
			if len(list) != 1 || list[0].(map[string]any)["pointer"] != pointer {
				t.Errorf("%s: errors = %v", body, m["errors"])
			}
		}
	})
	t.Run("R-API-003 a malformed body, a foreign media type and an unknown member are refused", func(t *testing.T) {
		fc := &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote}}
		s := cacheSetup(t, val, fc, &actionAccess{}, nil)
		problem(t, s.do("POST", invalidatePath, `{"path":`, nil), 400, "malformed_request")
		problem(t, s.do("POST", invalidatePath, `{"paht":"x"}`, nil), 400, "malformed_request")
		problem(t, s.do("POST", invalidatePath, `{"path":1}`, nil), 400, "malformed_request")
		problem(t, s.do("POST", invalidatePath, `{}`, map[string]string{"Content-Type": "text/plain"}), 415, "unsupported_media_type")
		if len(fc.calls) != 0 {
			t.Errorf("calls = %+v", fc.calls)
		}
	})
	t.Run("an unknown repository is 404 and an unwired service is 503", func(t *testing.T) {
		fc := &fakeCache{}
		problem(t, cacheSetup(t, val, fc, &actionAccess{}, nil).do("POST", invalidatePath, `{}`, nil), 404, "repository_not_found")
		s := newSetup(t, func(o *Options) {
			o.Access = &actionAccess{}
			o.API.Config = apiConfigForTest()
			o.API.Config.MaxRequestBodyBytes = 1 << 20
		}, authed)
		problem(t, s.do("POST", invalidatePath, `{}`, nil), 503, "unavailable")
	})
}

func entryRows(n int) []invalidation.Entry {
	t0 := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	out := make([]invalidation.Entry, n)
	for i := range out {
		out[i] = invalidation.Entry{
			Path: "pkg-" + string(rune('a'+i)), Variant: "full", Kind: "metadata", UpstreamURL: "https://registry.example/pkg",
			FetchedAt: t0, ValidatedAt: t0, ExpiresAt: &t0, LastUsedAt: t0,
		}
	}
	return out
}

const entriesPath = "/api/v1/repositories/npm-remote/cache/entries"

func testListCacheEntries(t *testing.T, val *apitest.Validator) {
	remote := storedRepo("7", "npm-remote", repository.Remote)
	fc := func() *fakeCache {
		return &fakeCache{repos: map[string]repository.Stored{"npm-remote": remote, "local-one": storedRepo("2", "local-one", repository.Local)}, entries: entryRows(5)}
	}
	t.Run("R-REPO-032 a holder of manage lists the entries in pages that join without a gap or a repeat", func(t *testing.T) {
		f := fc()
		s := cacheSetup(t, val, f, holdsAccess{}, cacheDecider{manage: true})
		var seen []string
		url := entriesPath + "?limit=2"
		for range 5 {
			w := s.do("GET", url, "", nil)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var page struct {
				Items      []map[string]any `json:"items"`
				NextCursor *string          `json:"nextCursor"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &page)
			for _, it := range page.Items {
				seen = append(seen, it["path"].(string))
			}
			if page.NextCursor == nil {
				break
			}
			if w.Header().Get("Link") == "" {
				t.Error("no Link header on a page with a next page")
			}
			url = entriesPath + "?limit=2&cursor=" + *page.NextCursor
		}
		if strings.Join(seen, ",") != "pkg-a,pkg-b,pkg-c,pkg-d,pkg-e" {
			t.Errorf("walk = %v", seen)
		}
	})
	t.Run("R-REPO-032 the entry carries path, kind, fetched, validated, expires, gone, last used and the upstream URL", func(t *testing.T) {
		f := fc()
		gone := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
		f.entries = entryRows(1)
		f.entries[0].UpstreamGoneAt = &gone
		f.entries[0].ExpiresAt = nil
		f.entries[0].Kind = "artifact"
		s := cacheSetup(t, val, f, holdsAccess{}, cacheDecider{manage: true})
		w := s.do("GET", entriesPath+"?kind=artifact", "", nil)
		var page struct{ Items []map[string]any }
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		got := page.Items[0]
		for _, k := range []string{"path", "variant", "kind", "upstreamUrl", "fetchedAt", "validatedAt", "expiresAt", "upstreamGoneAt", "lastUsedAt"} {
			if _, ok := got[k]; !ok {
				t.Errorf("member %s is missing: %v", k, got)
			}
		}
		if got["expiresAt"] != nil || got["upstreamGoneAt"] != "2026-10-30T09:00:00Z" || got["kind"] != "artifact" {
			t.Errorf("entry = %v", got)
		}
		if f.filter.Kinds[0] != "artifact" {
			t.Errorf("kinds = %v", f.filter.Kinds)
		}
	})
	t.Run("R-REPO-032 metadata is the default kind and pathPrefix reaches the query", func(t *testing.T) {
		f := fc()
		s := cacheSetup(t, val, f, holdsAccess{}, cacheDecider{manage: true})
		if w := s.do("GET", entriesPath+"?pathPrefix=@acme", "", nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if f.filter.Prefix != "@acme" || len(f.filter.Kinds) != 1 || f.filter.Kinds[0] != "metadata" {
			t.Errorf("filter = %+v", f.filter)
		}
	})
	t.Run("R-REPO-032 a holder of repositories:read-config needs no manage", func(t *testing.T) {
		s := cacheSetup(t, val, fc(), holdsAccess{config: true}, cacheDecider{})
		if w := s.do("GET", entriesPath, "", nil); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-REPO-032 a caller with neither is answered 403, and 404 where the repository is hidden", func(t *testing.T) {
		s := cacheSetup(t, val, fc(), holdsAccess{}, cacheDecider{})
		problem(t, s.do("GET", entriesPath, "", nil), 403, "forbidden")
		s = cacheSetup(t, val, fc(), holdsAccess{}, cacheDecider{hidden: true})
		problem(t, s.do("GET", entriesPath, "", nil), 404, "not_found")
		s = cacheSetup(t, val, fc(), holdsAccess{config: true}, cacheDecider{hidden: true})
		problem(t, s.do("GET", entriesPath, "", nil), 404, "not_found")
	})
	t.Run("only a remote repository has entries", func(t *testing.T) {
		s := cacheSetup(t, val, fc(), holdsAccess{}, cacheDecider{manage: true})
		problem(t, s.do("GET", "/api/v1/repositories/local-one/cache/entries", "", nil), 400, "not_applicable")
	})
	t.Run("R-API-010 a bad kind, a bad prefix, an unknown parameter and a foreign cursor are 400", func(t *testing.T) {
		s := cacheSetup(t, val, fc(), holdsAccess{}, cacheDecider{manage: true})
		problem(t, s.do("GET", entriesPath+"?kind=all", "", nil), 400, "malformed_request")
		problem(t, s.do("GET", entriesPath+"?pathPrefix=a//b", "", nil), 400, "malformed_request")
		problem(t, s.do("GET", entriesPath+"?colour=red", "", nil), 400, "malformed_request")
		problem(t, s.do("GET", entriesPath+"?cursor=AAAA", "", nil), 400, "invalid_cursor")
		// A cursor of the right shape issued for another filter is refused as well.
		w := s.do("GET", entriesPath+"?limit=2", "", nil)
		var page struct{ NextCursor string }
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		problem(t, s.do("GET", entriesPath+"?limit=2&kind=artifact&cursor="+page.NextCursor, "", nil), 400, "invalid_cursor")
	})
	t.Run("an unwired service is 503 and a database failure is 500", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.Access, o.API.Config = allowAll{}, apiConfigForTest() }, authed)
		problem(t, s.do("GET", entriesPath, "", nil), 503, "unavailable")
		f := fc()
		f.entryErr = context.DeadlineExceeded
		s = cacheSetup(t, val, f, holdsAccess{}, cacheDecider{manage: true})
		if w := s.do("GET", entriesPath, "", nil); w.Code < 500 {
			t.Errorf("%d", w.Code)
		}
	})
	t.Run("an anonymous caller is 401 before the handler", func(t *testing.T) {
		f := fc()
		s := newSetup(t, func(o *Options) {
			o.Access, o.API.Config = denyAnonymous{}, apiConfigForTest()
			o.Cache = CacheServices{Invalidations: f, Repositories: f, Authz: cacheDecider{manage: true}}
		}, func(*http.Request) (httpserver.Principal, error) { return httpserver.Principal{Anonymous: true}, nil })
		problem(t, s.do("GET", entriesPath, "", nil), 401, "unauthenticated")
	})
}

// TestCacheAPI runs the cache operations through the router of the shared step with
// every answer validated against the document (R-API-002).
func TestCacheAPI(t *testing.T) {
	val := apitest.New(t, EmbeddedYAML())
	t.Run("invalidate", func(t *testing.T) { testInvalidateRepositoryCache(t, val) })
	t.Run("entries", func(t *testing.T) { testListCacheEntries(t, val) })
	t.Run("R-API-002 R-API-024 the answers of both cache operations were validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("invalidateRepositoryCache", "listCacheEntries"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}
