//go:build integration

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/idapi"
)

// The HTTP side of the identity tests: a replica serves the real management API
// handlers behind the real authentication stage and the real decision point, so a
// test drives the operations over HTTP the way a client does. The chain is the
// production one (internal/httpserver) without the rate-limit stage; the stage and
// the decision point read the replica's own caches, and the handlers read
// PostgreSQL (R-API-052).

const baseURL = "http://trove.test"

// bindAdmin gives a user the role admin. It runs before the replicas serve: the
// decision point builds its index from PostgreSQL when it starts.
func (e *env) bindAdmin(principalID string) {
	e.t.Helper()
	e.exec(`INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid) ON CONFLICT DO NOTHING`, principalID)
}

// takeLimiter adapts the cluster limiter in the take form to the API's interface,
// as the composition root does.
type takeLimiter struct{ l *cluster.Limiter }

func (t takeLimiter) Allow(ctx context.Context, bucket string) (bool, time.Duration) {
	v := t.l.Take(ctx, bucket)
	return v.Allowed, v.RetryAfter
}

// served is a replica with its HTTP listener.
type served struct {
	*replica
	srv *httptest.Server
}

// serve starts the decision point and the HTTP listener of a replica. val, when
// not nil, validates every answer against the document.
func (r *replica) serve(e *env, val *apitest.Validator) *served {
	r.t.Helper()
	var flow v1.FlowLimiter
	if r.flowStarts != nil {
		flow = takeLimiter{r.flowStarts}
	}
	h := idapi.Handler(r.t, idapi.Components{
		FlowStarts: flow,
		Name:       r.name, DB: r.db, Changes: r.changes, Stage: r.stage, Manager: r.manager, Tokens: r.tokens, Settings: r.settings,
		Recorder: r.recorder, Auth: idapi.AuthConfig(r.tokenCfg), BaseURL: baseURL, Validator: val, AcceptsSession: false,
	})
	s := &served{replica: r, srv: httptest.NewServer(h)}
	r.t.Cleanup(s.srv.Close)
	return s
}

// reply is one answer, read in full.
type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (rp reply) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rp.Body, &m); err != nil {
		t.Fatalf("body is not a JSON object (%d): %s", rp.Status, rp.Body)
	}
	return m
}

func (rp reply) code() string {
	var m map[string]any
	_ = json.Unmarshal(rp.Body, &m)
	c, _ := m["code"].(string)
	return c
}

// call sends a request to the replica. body is a string or a value to marshal;
// hdr sets headers ("-" as a value removes one). Authorization is "Bearer <bearer>"
// when bearer is not empty.
func (s *served) call(method, path, bearer string, body any, hdr ...string) reply {
	s.t.Helper()
	var rdr io.Reader
	ctype := "application/json"
	// The member and group changes are actions with a JSON body; every other PATCH is a merge patch.
	if method == http.MethodPatch && !strings.HasSuffix(path, "/members") && !strings.HasSuffix(path, "/groups") {
		ctype = "application/merge-patch+json"
	}
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.srv.URL+path, rdr)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "-" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// fixture is an installation with an administrator: a user bound to the role
// admin whose PAT carries admin: true, so a token-authenticated call is an
// administrator's.
type fixture struct {
	admin      principalUser
	adminToken string
}

type principalUser struct{ ID, Name string }

// newFixture creates the administrator before any replica serves.
func (e *env) newFixture(seed *replica) fixture {
	e.t.Helper()
	u := seed.user("root")
	e.bindAdmin(u.ID)
	c := seed.pat(u, func(r *token.CreateRequest) { r.Scope = []byte(`{"inherit":true,"admin":true}`) })
	return fixture{admin: principalUser{ID: u.ID, Name: u.Name}, adminToken: c.Secret}
}
