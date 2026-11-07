//go:build integration

// Package bastionsetup tests the client setup for a Bastion (R-BAS-007, R-API-030) against the
// integration stack: a replica started through the composition root reads the registrations and
// their last contacts from PostgreSQL and renders the snippets on the base URL a Bastion reported.
// The registrations and their contacts are written the way the statements of the Bastion files
// store them; the handlers that write them are the work of other packages.
package bastionsetup

import (
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// registration writes a Bastion principal and, when baseURL is not empty, the contact of one
// replica that reported it.
func registration(st *testenv.Stack, name, baseURL string) {
	id := st.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', $1) RETURNING id::text", name)
	st.Exec("INSERT INTO bastion (principal_id, description) VALUES ($1::uuid, $2)", id, name)
	if baseURL != "" {
		st.Exec("INSERT INTO bastion_contact (bastion_id, replica, contact_at, release, interface_version, base_url) "+
			"VALUES ($1::uuid, 'pod-a', now() - interval '5 minutes', '0.15.0', 1, $2)", id, baseURL)
		st.Exec("INSERT INTO bastion_contact (bastion_id, replica, contact_at, release, interface_version, base_url) "+
			"VALUES ($1::uuid, 'pod-b', now(), '0.15.0', 1, $2)", id, baseURL)
	}
}

// TestR_BAS_007_ClientSetupForABastionThroughTheStack: the acceptance case of R-BAS-007 against a
// real replica, a real database and the real chain: the registry URL is the base URL the Bastion
// reported at its most recent contact, npm's configure snippet sets replace-registry-host=always,
// and the answer has no publish snippet although the caller may deploy.
func TestR_BAS_007_ClientSetupForABastionThroughTheStack(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "bastionsetup"})
	alice := st.User("alice")
	st.Repository("npm-local", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Policy("writers", []string{"read", "deploy"}, []string{"npm-local"}, alice)
	tok := st.Token(alice, scope.Default())
	registration(st, "partners", "https://packages.example.com")
	registration(st, "fresh", "")
	registration(st, "stray", "https://stray.example.com")
	r := st.Start(t, testenv.ReplicaOptions{
		Name: "web1",
		Env:  []string{`TROVE_SERVER_ADDITIONAL_BASE_URLS=["https://packages.example.com"]`},
	})
	const path = "/api/v1/projects/it/repositories/npm-local/client-setup?client=npm&credentials=env"

	t.Run("the snippets are on the reported address", func(t *testing.T) {
		a := st.Do(r, "GET", path+"&bastion=partners", tok, nil)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		v := a.JSON(t)
		if v["registryUrl"] != "https://packages.example.com/api/npm/it/npm-local/" ||
			v["endpointPath"] != "//packages.example.com/api/npm/it/npm-local/" || v["publishTarget"] != nil {
			t.Fatalf("%v", v)
		}
		var configure string
		for _, s := range v["snippets"].([]any) {
			m := s.(map[string]any)
			if m["tab"] == "publish" {
				t.Errorf("a publish snippet: %v", m["id"])
			}
			if m["tab"] == "configure" {
				configure, _ = m["content"].(string)
			}
		}
		if !strings.Contains(configure, "replace-registry-host=always\n") ||
			!strings.Contains(configure, "//packages.example.com/api/npm/it/npm-local/:_authToken=${TROVE_TOKEN}") ||
			strings.Contains(configure, "trove.test") {
			t.Fatalf("%q", configure)
		}
	})
	t.Run("the setup of the installation itself is unchanged", func(t *testing.T) {
		a := st.Do(r, "GET", path, tok, nil)
		v := a.JSON(t)
		if a.Status != 200 || v["registryUrl"] != "http://trove.test/api/npm/it/npm-local/" || v["publishTarget"] != "it/npm-local" {
			t.Fatalf("%d %v", a.Status, v)
		}
	})
	t.Run("an address that is not listed, or not reported yet, answers 409", func(t *testing.T) {
		for _, name := range []string{"stray", "fresh"} {
			a := st.Do(r, "GET", path+"&bastion="+name, tok, nil)
			if a.Status != 409 || a.Code(t) != "bastion_base_url_unknown" {
				t.Errorf("%s: %d %s", name, a.Status, a.Body)
			}
		}
	})
	t.Run("a name that is no registration answers 404", func(t *testing.T) {
		if a := st.Do(r, "GET", path+"&bastion=nobody", tok, nil); a.Status != 404 {
			t.Errorf("%d %s", a.Status, a.Body)
		}
	})
}
