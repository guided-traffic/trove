//go:build integration

package authz

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/test/integration/idapi"
)

// ownAPI serves the management API on the two replicas of an ownWorld: the real
// v1 handlers on the production chain (test/integration/idapi), each with the
// replica's own engine and ownership service, and a stand-in authentication that
// names the caller in X-Test-Person. Every answer is validated against the
// document.
type ownAPI struct {
	w      *ownWorld
	val    *apitest.Validator
	a, b   *httptest.Server
	people map[string]authz.Subject
}

func newOwnAPI(t *testing.T) *ownAPI {
	t.Helper()
	w := newOwnWorld(t)
	api := &ownAPI{w: w, val: apitest.New(t, v1.EmbeddedYAML()), people: map[string]authz.Subject{}}
	for _, s := range []authz.Subject{w.alice, w.dave, w.mallory, w.lead, w.root, w.carol, w.bot} {
		api.people[s.Name] = s
	}
	mapping := w.e.ciMapping("gitlab-internal/acme-publish")
	api.people["ci"] = authz.Subject{Kind: mapping.Kind, ID: mapping.ID, Name: "ci", Mappings: []string{"ci:" + mapping.Name}}
	serve := func(r *replica, own *authz.Ownership) *httptest.Server {
		h := idapi.Handler(t, idapi.Components{
			Name: r.name, DB: r.db, Changes: r.changes, Recorder: r.rec, BaseURL: "http://trove.test", Log: quiet,
			Engine: r.engine, Validator: api.val, Authenticate: api.authenticate,
			Options: func(eng *authz.Engine, o *v1.Options) { o.Authz = v1.AuthzServices{Engine: eng, Owners: own} },
		})
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv
	}
	api.a, api.b = serve(w.a, w.oa), serve(w.b, w.ob)
	return api
}

// authenticate turns the X-Test-Person header into the principal the stage would
// hand over: a bearer token of that person, with its groups and CI mappings.
func (api *ownAPI) authenticate(req *http.Request) (httpserver.Principal, error) {
	name := req.Header.Get("X-Test-Person")
	if name == "" {
		return httpserver.AnonymousPrincipal(), nil
	}
	s, ok := api.people[name]
	if !ok {
		return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "unknown test person", errs.WithChallenge("Bearer"))
	}
	p := httpserver.Principal{
		Kind: httpserver.PrincipalKind(s.Kind), ID: s.ID, Name: s.Name, Mappings: s.Mappings,
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "00000000-0000-4000-8000-000000000001"},
	}
	for _, g := range s.Groups {
		p.Groups = append(p.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	return p, nil
}

type ownAnswer struct {
	status int
	header http.Header
	body   []byte
}

func (a ownAnswer) code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(a.body, &p)
	return p.Code
}

// do sends one request; path is sent as written, so an encoded "/" stays encoded.
func (api *ownAPI) do(t *testing.T, srv *httptest.Server, person, method, path, body string, hdr ...string) ownAnswer {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if person != "" {
		req.Header.Set("X-Test-Person", person)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return ownAnswer{status: res.StatusCode, header: res.Header, body: b}
}

func want(t *testing.T, a ownAnswer, status int, code string) {
	t.Helper()
	if a.status != status || (code != "" && a.code() != code) {
		t.Fatalf("status %d code %q, want %d %q: %s", a.status, a.code(), status, code, a.body)
	}
}

const ownersOf = "/api/v1/repositories/npm-internal/packages/"

// TestR_AUTHZ_019_OwnershipOperationsOverHTTP drives the four operations of
// R-AUTHZ-019 through the management API: the package name as one encoded path
// segment, pre-assignment with its Location, 409 already_exists and an
// Idempotency-Key replay, 403 for refused writes whatever exists, 403
// ci_token_restricted, 422 unknown_principal, 409 last_owner and release, the
// list by owner with its cursor, and the owners read on the other replica at
// once (they are read from PostgreSQL, no index carries them).
func TestR_AUTHZ_019_OwnershipOperationsOverHTTP(t *testing.T) {
	api := newOwnAPI(t)
	notYet := ownersOf + "@acme%2Fnot-yet/owners"

	t.Run("R-AUTHZ-019 an unclaimed name reads back unclaimed, with no owners", func(t *testing.T) {
		got := api.do(t, api.a, "lead", "GET", notYet, "")
		want(t, got, 200, "")
		var po struct {
			Package, State string
			Owners         []any
		}
		if err := json.Unmarshal(got.body, &po); err != nil || po.Package != "@acme/not-yet" || po.State != "unclaimed" || len(po.Owners) != 0 {
			t.Fatalf("%s", got.body)
		}
	})
	t.Run("R-AUTHZ-019 a holder of manage pre-assigns the name; Location shows it on the other replica", func(t *testing.T) {
		got := api.do(t, api.a, "lead", "POST", notYet, `{"owner":"group:team-web"}`)
		want(t, got, 201, "")
		var o struct{ Owner, Reason, AddedBy string }
		if err := json.Unmarshal(got.body, &o); err != nil || o.Owner != "group:team-web" || o.Reason != "pre-assigned" || o.AddedBy != "user:lead" {
			t.Fatalf("%s", got.body)
		}
		loc := got.header.Get("Location")
		if loc != "http://trove.test"+notYet {
			t.Fatalf("Location %q", loc)
		}
		read := api.do(t, api.b, "dave", "GET", strings.TrimPrefix(loc, "http://trove.test"), "")
		want(t, read, 200, "")
		if !strings.Contains(string(read.body), `"state":"claimed"`) || !strings.Contains(string(read.body), `"group:team-web"`) {
			t.Fatalf("%s", read.body)
		}
	})
	t.Run("R-AUTHZ-019 an owner added twice is 409 already_exists; an Idempotency-Key replays the first answer", func(t *testing.T) {
		want(t, api.do(t, api.b, "lead", "POST", notYet, `{"owner":"group:team-web"}`), 409, errs.CodeAlreadyExists)
		first := api.do(t, api.a, "lead", "POST", notYet, `{"owner":"sa:ci-bot"}`, "Idempotency-Key", "own-1")
		want(t, first, 201, "")
		again := api.do(t, api.a, "lead", "POST", notYet, `{"owner":"sa:ci-bot"}`, "Idempotency-Key", "own-1")
		want(t, again, 201, "")
		if again.header.Get("Idempotency-Replayed") != "true" || string(again.body) != string(first.body) {
			t.Fatalf("replay %v %s", again.header, again.body)
		}
	})
	t.Run("R-AUTHZ-016 a refused owner change is 403 whether or not the repository or name exists", func(t *testing.T) {
		for _, path := range []string{notYet, ownersOf + "@nope%2Fnothing/owners", "/api/v1/repositories/ghost/packages/x/owners"} {
			want(t, api.do(t, api.a, "carol", "POST", path, `{"owner":"user:carol"}`), 403, errs.CodeForbidden)
		}
		want(t, api.do(t, api.a, "carol", "DELETE", notYet+"/group:team-web", ""), 403, errs.CodeForbidden)
		want(t, api.do(t, api.a, "", "POST", notYet, `{"owner":"user:carol"}`), 401, "")
	})
	t.Run("R-AUTHZ-019 a CI token never changes owners", func(t *testing.T) {
		want(t, api.do(t, api.a, "ci", "POST", notYet, `{"owner":"group:team-mobile"}`), 403, errs.CodeCITokenRestricted)
		want(t, api.do(t, api.a, "ci", "DELETE", notYet+"/group:team-web", ""), 403, errs.CodeCITokenRestricted)
	})
	t.Run("R-AUTHZ-019 an owner reference that names no owner identity is 422 unknown_principal", func(t *testing.T) {
		for _, owner := range []string{"user:nobody", "anonymous", "ci:gitlab-internal:project:4712"} {
			want(t, api.do(t, api.a, "lead", "POST", notYet, `{"owner":"`+owner+`"}`), 422, errs.CodeValidation)
		}
		want(t, api.do(t, api.a, "lead", "POST", notYet, `{"owner":"group:team-web","extra":1}`), 400, "")
	})
	t.Run("R-AUTHZ-019 removing the only owner is 409 last_owner; release=true releases the name", func(t *testing.T) {
		solo := ownersOf + "@acme%2Fsolo/owners"
		want(t, api.do(t, api.a, "lead", "POST", solo, `{"owner":"user:lead"}`), 201, "")
		want(t, api.do(t, api.a, "lead", "DELETE", solo+"/user:lead", ""), 409, errs.CodeLastOwner)
		want(t, api.do(t, api.b, "lead", "DELETE", solo+"/user:lead?release=true", ""), 204, "")
		got := api.do(t, api.a, "lead", "GET", solo, "")
		if !strings.Contains(string(got.body), `"state":"released"`) {
			t.Fatalf("%s", got.body)
		}
		want(t, api.do(t, api.a, "lead", "DELETE", solo+"/user:lead", ""), 404, "")
	})
	t.Run("R-AUTHZ-019 an owner reference with a slash is one encoded segment", func(t *testing.T) {
		mapped := ownersOf + "@acme%2Fmapped/owners"
		want(t, api.do(t, api.a, "lead", "POST", mapped, `{"owner":"ci:gitlab-internal/acme-publish"}`), 201, "")
		want(t, api.do(t, api.a, "lead", "POST", mapped, `{"owner":"group:team-web"}`), 201, "")
		want(t, api.do(t, api.a, "lead", "DELETE", mapped+"/ci:gitlab-internal%2Facme-publish", ""), 204, "")
	})
	t.Run("R-AUTHZ-019 a dot-dot inside the encoded name stays one name, never a path", func(t *testing.T) {
		// The registered npm format refuses "@acme/../x" as a name, so the answer is
		// the 404 of a name that does not exist; it never describes another package
		// that a cleaned path would address.
		got := api.do(t, api.a, "lead", "GET", ownersOf+"@acme%2F..%2Fx/owners", "")
		want(t, got, 404, errs.CodeNotFound)
		if strings.Contains(string(got.body), `"package":`) {
			t.Fatalf("%s", got.body)
		}
	})
	t.Run("R-AUTHZ-016 reading the owners needs read: 404 for a caller without it", func(t *testing.T) {
		want(t, api.do(t, api.a, "carol", "GET", notYet, ""), 404, errs.CodeNotFound)
		want(t, api.do(t, api.a, "root", "GET", "/api/v1/repositories/npmjs/packages/left-pad/owners", ""), 404, errs.CodeNotFound)
	})
	t.Run("R-AUTHZ-019 R-API-010 the names an identity owns, page by page", func(t *testing.T) {
		var names []string
		path := "/api/v1/package-owners?owner=group:team-web&limit=1"
		for range 10 {
			got := api.do(t, api.b, "dave", "GET", path, "")
			want(t, got, 200, "")
			var page struct {
				Items []struct {
					Repository, Package string
				}
				NextCursor *string
			}
			if err := json.Unmarshal(got.body, &page); err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				names = append(names, it.Repository+"/"+it.Package)
			}
			if page.NextCursor == nil {
				break
			}
			path = "/api/v1/package-owners?owner=group:team-web&limit=1&cursor=" + *page.NextCursor
		}
		if strings.Join(names, ",") != "npm-internal/@acme/mapped,npm-internal/@acme/not-yet" {
			t.Fatalf("names %v", names)
		}
		want(t, api.do(t, api.b, "mallory", "GET", "/api/v1/package-owners?owner=group:team-web", ""), 403, errs.CodeForbidden)
		want(t, api.do(t, api.b, "root", "GET", "/api/v1/package-owners?owner=group:team-web", ""), 200, "")
		want(t, api.do(t, api.b, "dave", "GET", "/api/v1/package-owners", ""), 400, "")
	})
	t.Run("R-API-002 every ownership operation was reached and answered as documented", func(t *testing.T) {
		if missing := api.val.Uncovered("getPackageOwners", "addPackageOwner", "removePackageOwner", "listOwnedPackages"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}
