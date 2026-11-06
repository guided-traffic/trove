//go:build e2e

package e2e

// A partner's project service account with npm (R-AUTHZ-026, R-AUTHZ-025, R-NPM-043; beta phase 5a): a system administrator
// creates the project with both switches on and a project administrator; the project administrator, with a session and
// no system role, creates the project's repositories, its service account, its policies and the account's token; the
// partner publishes with that token into the project's local repository and installs through the project's virtual
// repository, which also serves the remote member; what lies outside the project is a 404; revoking the token by the
// project administrator ends the access with 401.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNPMProjectPartner(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)
	p := newPartnerProject(t, fx.adm, "partner-npm")

	// what lies outside the project: a package in npm-local, which the administrator's fixture created and the project
	// holds no grant on
	outside := fx.pkgName("projout-"+fx.run, "outside", false)
	fx.clientOn(t, "npm11", 0, npmLocalKey, "projout").publish(outside, "1.0.0")

	local := p.repository(map[string]any{"class": "local", "format": "npm", "requireAuthentication": true}, "local")
	remote := p.repository(map[string]any{"class": "remote", "format": "npm", "remote": map[string]any{"url": "https://" + fx.stubHost + "/"}}, "remote")
	virtual := p.repository(map[string]any{
		"class": "virtual", "format": "npm", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{p.bare(local), p.bare(remote)}, "deployTarget": p.bare(local)},
	}, "all")
	p.serviceAccount()
	// a virtual repository accepts read and manage only (R-AUTHZ-009): the publish through it is authorized on its deployment
	// target, so deploy is granted on the local repository and read on the other two
	p.policy("write", []string{local}, "read", "deploy")
	p.policy("read", []string{remote, virtual}, "read")
	tok := p.token()
	if !strings.HasPrefix(tok.Token, "trv_sat_") {
		t.Fatalf("the token of a service account starts with %q, want trv_sat_", tok.Token[:min(8, len(tok.Token))])
	}
	awaitReadableOnEveryReplica(t, tok.Token, local, remote, virtual)

	name := fx.pkgName("proj-"+fx.run, "partner", false)

	t.Run("R-AUTHZ-026 the partner publishes into the project's local repository", func(t *testing.T) {
		pub := fx.clientOn(t, "npm11", 0, local, "projpub")
		pub.token = tok.Token
		pub.publish(name, "1.0.0")
		r := fx.adm.withToken(tok.Token).get(npmAddr(local)+"/"+url.PathEscape(name)).want(t, "the package document in the project's repository", 200)
		if !strings.Contains(string(r.Body), `"1.0.0"`) {
			t.Fatalf("the document of %s in %s does not list 1.0.0:\n%s", name, local, r)
		}
	})

	t.Run("R-AUTHZ-026 the partner installs through the project's virtual repository", func(t *testing.T) {
		for i, row := range []string{"npm11", "pnpm11"} {
			t.Run(row, func(t *testing.T) {
				c := fx.clientOn(t, row, i+1, virtual, "projinst")
				c.token = tok.Token
				d := c.project("consume", nil)
				c.configure(d)
				// the local member's package, and a package the remote member fetches from the test upstream
				c.mustInstall(d, name)
				c.mustInstall(d, "@cobalt/eager-lantern")
				if v := c.read(d + "/node_modules/" + name + "/package.json"); !strings.Contains(v, `"version": "1.0.0"`) {
					t.Fatalf("%s installed %s without version 1.0.0:\n%s", row, name, v)
				}
			})
		}
	})

	t.Run("R-AUTHZ-026 what lies outside the project is a 404 for the partner", func(t *testing.T) {
		api := fx.adm.withToken(tok.Token)
		api.get(npmAddr(npmLocalKey)+"/"+url.PathEscape(outside)).want(t, "a package of a repository outside the project", 404)
		c := fx.clientOn(t, "npm11", 2, npmLocalKey, "projout404")
		c.token = tok.Token
		d := c.project("consume", nil)
		c.configure(d)
		r := c.run(d, c.addCmd(outside)...)
		c.expectRefusal(t, "the install of a package outside the project", r, "E404", 30*time.Second)
		// the project's own repositories and the partner's read-only rights: nothing is writable but the local repository
		push := api.do(http.MethodPut, npmAddr(remote)+"/"+url.PathEscape(name), map[string]any{"name": name}).want(t, "a publish into the remote member", 403)
		t.Logf("a publish into the project's remote repository answers %s", push)
	})

	t.Run("R-AUTHZ-026 R-AUTH-020 the project administrator revokes the token and the next install is a 401", func(t *testing.T) {
		p.revoke(tok)
		c := fx.clientOn(t, "npm11", 1, virtual, "projrevoked") // a home of its own: nothing cached
		c.token = tok.Token
		d := c.project("consume", nil)
		c.configure(d)
		r := c.run(d, c.addCmd(name)...)
		c.expectRefusal(t, "the install with the revoked token", r, "401", 30*time.Second)
	})
}
