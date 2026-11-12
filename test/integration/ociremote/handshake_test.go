//go:build integration

package ociremote

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ocistub"
)

// testResult is the part of an upstream test's answer the login is read from.
type testResult struct {
	Reachable     bool  `json:"reachable"`
	Authenticated *bool `json:"authenticated"`
	TokenService  *struct {
		Host               string `json:"host"`
		Allowed            bool   `json:"allowed"`
		ReceivesCredential bool   `json:"receivesCredential"`
	} `json:"tokenService"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *renv) upstreamTest(t *testing.T, path string, body any) testResult {
	t.Helper()
	a := e.st.Do(e.st.Pick(), http.MethodPost, path, e.admin, body)
	if a.Status != 200 {
		t.Fatalf("%s: %d %s", path, a.Status, a.Body)
	}
	var r testResult
	if err := json.Unmarshal(a.Body, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// tokenRequests are the requests of the counting stub's realm.
func tokenRequests(c ocistub.Counts) []ocistub.Entry {
	var out []ocistub.Entry
	for _, en := range c.Entries {
		if en.Kind == ocistub.KindToken {
			out = append(out, en)
		}
	}
	return out
}

// TestR_REPO_064_TheUpstreamTestLogsInAtTheCountingRegistrysRealm: against the counting OCI stub with its realm on a second host,
// the saved test of a repository whose realm receives the credential runs one login without a scope and reads the credential as
// accepted; a repository that does not list the realm gets foreign_host_denied and the realm sees nothing; an anonymous one names
// the realm without asking it; and an unsaved test with a wrong password is refused by the realm.
func TestR_REPO_064_TheUpstreamTestLogsInAtTheCountingRegistrysRealm(t *testing.T) {
	e := newEnv(t, options{})
	e.start(1)
	saved := func(key string) testResult {
		return e.upstreamTest(t, "/api/v1/projects/it/repositories/"+key+"/upstream-test", nil)
	}

	t.Run("R-REPO-064 the stored credential is accepted by one login without a scope", func(t *testing.T) {
		r := saved(rBasic)
		if !r.Reachable || r.Authenticated == nil || !*r.Authenticated || r.Error != nil || r.TokenService == nil ||
			r.TokenService.Host != ocistub.LoopbackRealmHost || !r.TokenService.Allowed || !r.TokenService.ReceivesCredential {
			t.Fatalf("result = %+v", r)
		}
		got := tokenRequests(e.stub.Counts())
		if len(got) != 1 || got[0].Count != 1 || got[0].Auth != "basic" || strings.Contains(got[0].Query, "scope") || !strings.Contains(got[0].Query, "service=") {
			t.Fatalf("the realm saw %+v", got)
		}
	})
	t.Run("R-REPO-064 a realm the repository does not list is named and not asked", func(t *testing.T) {
		before := len(tokenRequests(e.stub.Counts()))
		r := saved(rNoHost)
		if !r.Reachable || r.Authenticated != nil || r.Error == nil || r.Error.Code != "foreign_host_denied" ||
			!strings.Contains(r.Error.Message, ocistub.LoopbackRealmHost) || r.TokenService == nil || r.TokenService.Allowed {
			t.Fatalf("result = %+v", r)
		}
		if n := len(tokenRequests(e.stub.Counts())); n != before {
			t.Fatalf("the realm was asked: %d entries", n)
		}
	})
	t.Run("R-REPO-064 without a credential the realm is named and not asked", func(t *testing.T) {
		before := tokenRequests(e.stub.Counts())
		r := saved(rAnon)
		if r.Authenticated != nil || r.Error != nil || r.TokenService == nil || !r.TokenService.Allowed || r.TokenService.ReceivesCredential {
			t.Fatalf("result = %+v", r)
		}
		if after := tokenRequests(e.stub.Counts()); len(after) != len(before) {
			t.Fatalf("the realm was asked: %+v", after)
		}
	})
	t.Run("R-REPO-064 an unsaved test with a wrong password is refused by the realm", func(t *testing.T) {
		r := e.upstreamTest(t, "/api/v1/projects/it/upstream-tests", map[string]any{"format": "oci", "remote": map[string]any{
			"url": e.run.URL, "authMode": "basic", "username": ocistub.DefaultUsername, "password": map[string]any{"value": "wrong"},
			"outboundProxy": map[string]any{"mode": "direct"},
			"extraHosts":    []any{map[string]any{"pattern": ocistub.LoopbackRealmHost, "sendCredentials": true}},
		}})
		if r.Authenticated == nil || *r.Authenticated || r.Error != nil {
			t.Fatalf("result = %+v", r)
		}
	})
}
