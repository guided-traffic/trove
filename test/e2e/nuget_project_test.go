//go:build e2e

package e2e

// A partner's project service account with NuGet (R-AUTHZ-026, R-AUTH-020, R-NUGET-012; beta phase 5a): the same scenario as
// TestNPMProjectPartner. The project Owner, with a session and no system role, creates a local, a remote and a
// virtual NuGet repository, the project's service account, its policies and its token; the partner pushes into the
// project's local feed with `dotnet nuget push` and restores through the virtual feed, which also serves the remote member;
// a feed outside the project refuses the restore; revoking the token ends the access with 401.

import (
	"net/http"
	"strings"
	"testing"
)

func TestNuGetProjectPartner(t *testing.T) {
	fx := nugetFx(t)
	p := newPartnerProject(t, fx.adm, "partner-nuget")
	apiHost, contentHost := nugetStubHosts(t)

	// what lies outside the project: a package in nuget-e2e, which the administrator's fixture created
	outside := fx.pkgID("ProjOutside")
	pubOut := fx.client(t, 0, "proj-outside-push")
	nugetAuth := nugetFeed(t, "localhost", nugetAuthKey)
	pubOut.config(nugetAuth, true)
	pubOut.mustPush("push outside the project", "dotnet", "nuget", "push", pubOut.place(outside, "1.0.0", fx.nugetPack(t, outside, "1.0.0", "outside the project")),
		"--source", nugetAuth, "--api-key", fx.token)

	local := p.repository(map[string]any{"class": "local", "format": "nuget"}, "local")
	remote := p.repository(map[string]any{
		"class": "remote", "format": "nuget",
		"remote": map[string]any{
			"url":        "https://" + apiHost + "/v3/index.json",
			"extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}},
		},
	}, "remote")
	virtual := p.repository(map[string]any{
		"class": "virtual", "format": "nuget",
		"virtual": map[string]any{"members": []string{p.bare(local), p.bare(remote)}, "deployTarget": p.bare(local)},
	}, "all")
	p.serviceAccount()
	p.policy("write", []string{local}, "read", "deploy")
	p.policy("read", []string{remote, virtual}, "read")
	tok := p.token()
	if !strings.HasPrefix(tok.Token, "trv_sat_") {
		t.Fatalf("the token of a service account starts with %q, want trv_sat_", tok.Token[:min(8, len(tok.Token))])
	}
	nugetReadableOnEveryReplica(t, tok.Token, local, remote, virtual)

	id := fx.pkgID("ProjPartner")
	localFeed, virtualFeed := nugetFeed(t, "localhost", local), nugetFeed(t, "localhost", virtual)

	t.Run("R-AUTHZ-026 R-NUGET-012 the partner pushes into the project's local feed", func(t *testing.T) {
		c := fx.client(t, 0, "proj-push")
		c.token = tok.Token
		c.config(localFeed, true)
		c.mustPush("push with the project token", "dotnet", "nuget", "push", c.place(id, "1.0.0", fx.nugetPack(t, id, "1.0.0", "pushed by the partner")),
			"--source", localFeed, "--api-key", tok.Token)
		if got := fx.flatVersions(t, local, id); len(got) != 1 || got[0] != "1.0.0" {
			t.Fatalf("flat container versions in the project's repository = %v, want [1.0.0]", got)
		}
	})

	t.Run("R-AUTHZ-026 R-NUGET-014 dotnet restore through the project's virtual feed", func(t *testing.T) {
		c := fx.client(t, 1, "proj-restore")
		c.token = tok.Token
		c.config(virtualFeed, true)
		c.exec("dotnet", "restore", c.consumer("p", id, "1.0.0"), "--no-cache")
		if !c.hasPackage(id, "1.0.0") {
			t.Fatalf("the restore did not extract %s 1.0.0", id)
		}
		// a package the remote member fetches from the test upstream
		r := fx.client(t, 2, "proj-restore-remote")
		r.token = tok.Token
		r.config(virtualFeed, true)
		r.exec("dotnet", "restore", r.consumer("p", nugetDepRoot, "1.0.0"), "--no-cache")
		if !r.hasPackage(nugetDepRoot, "1.0.0") {
			t.Fatalf("the restore did not extract %s 1.0.0 from the remote member", nugetDepRoot)
		}
	})

	t.Run("R-AUTHZ-026 a feed outside the project refuses the partner", func(t *testing.T) {
		api := fx.adm.withToken(tok.Token)
		api.get(nugetAddr(nugetAuthKey)+"/v3/flatcontainer/"+strings.ToLower(outside)+"/index.json").want(t, "a package of a repository outside the project", 404)
		c := fx.client(t, 0, "proj-outside-restore")
		c.token = tok.Token
		c.config(nugetAuth, true)
		out, err := c.try("dotnet", "restore", c.consumer("p", outside, "1.0.0"), "--no-cache")
		if err == nil || c.hasPackage(outside, "1.0.0") {
			t.Fatalf("the restore of a package outside the project succeeded:\n%s", out)
		}
		t.Logf("the restore from a feed outside the project failed as expected:\n%s", tailLines(out, 6))
		if !strings.Contains(out, "404") && !strings.Contains(out, "NU1101") && !strings.Contains(out, "NU1301") {
			t.Fatalf("the restore failed without a 404, NU1101 or NU1301:\n%s", out)
		}
		// nothing but the project's local repository takes a push
		push := api.do(http.MethodPut, nugetAddr(nugetAuthKey)+"/v3/package/", nil).want(t, "a push into a repository outside the project", 403)
		t.Logf("a push into a repository outside the project answers %s", push)
	})

	t.Run("R-AUTHZ-026 R-AUTH-020 the project Owner revokes the token and the next restore is a 401", func(t *testing.T) {
		p.revoke(tok)
		c := fx.client(t, 1, "proj-revoked")
		c.token = tok.Token
		c.config(virtualFeed, true)
		c.mustRefuse("restore with the revoked token", "401", "dotnet", "restore", c.consumer("p", id, "1.0.0"), "--no-cache")
	})
}
