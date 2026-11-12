//go:build e2e

package e2e

// A request through a Bastion never reads by the level internal (R-BAS-008, Q-BAS-148, T-18): an internal user's token gets 404 for an
// internal repository through the Bastion and 200 at the main installation, while the public repository is read through the Bastion by
// that user and by a request without a credential.

import (
	"net/http"
	"testing"
	"time"
)

func TestBastion_R_BAS_008_NeverReadsByInternal(t *testing.T) {
	env := basFx(t)
	pubRepo, intRepo := "bas-npm-pub-"+env.run, "bas-npm-int-"+env.run
	createRepoAt(t, env.adm, map[string]any{"key": pubRepo, "class": "local", "format": "npm"}, map[string]any{"artifacts": "public", "wiki": "public"})
	createRepoAt(t, env.adm, map[string]any{"key": intRepo, "class": "local", "format": "npm"}, map[string]any{"artifacts": "internal", "wiki": "internal"})
	t.Cleanup(func() {
		deleteRepository(t, env.adm, pubRepo)
		deleteRepository(t, env.adm, intRepo)
	})
	ensurePolicy(t, env.adm, basPubSA+"-vis-"+env.run, []string{pubRepo, intRepo}, [2]any{"sa:" + basPubSA, []string{"read", "deploy", "delete"}})
	t.Cleanup(func() { env.adm.do(http.MethodDelete, "/api/v1/permission-policies/"+basPubSA+"-vis-"+env.run, nil) })
	time.Sleep(6 * time.Second) // the grant and the levels reach every replica of the main installation within the revocation bound
	pubPkg, intPkg := "bas"+env.run+"-vis-pub", "bas"+env.run+"-vis-int"
	env.publishNPM(t, pubRepo, pubPkg, "1.0.0", nil)
	env.publishNPM(t, intRepo, intPkg, "1.0.0", nil)
	tok := userToken(t, "bob")
	direct := func() *api { return newAPI(t).withToken(tok) }
	awaitStatusOnEveryReplica(t, "the internal user's read of the internal package at the main installation", direct, npmAddr(intRepo)+"/"+intPkg, 200)

	for _, site := range env.sites() {
		t.Run(site.variant, func(t *testing.T) {
			internalDoc, publicDoc := npmAddr(intRepo)+"/"+intPkg, npmAddr(pubRepo)+"/"+pubPkg
			waitFor(t, "the Bastion to read the public package for an internal user", 90*time.Second, 2*time.Second, func() bool {
				return site.api(t, tok).get(publicDoc).Status == 200
			})
			site.api(t, "").get(publicDoc).want(t, "a request without a credential reads the public package through the Bastion", 200)
			for range 3 { // the replicas of the Bastion and of the main installation differ from request to request
				got := site.api(t, tok).get(internalDoc)
				if got.Status != 404 {
					t.Fatalf("an internal user's token through %s for the internal package: %s, want 404 (a Bastion never reads by internal)", site.release, got)
				}
				direct().get(internalDoc).want(t, "the same token at the main installation", 200)
			}
			if got := site.api(t, "").get(internalDoc); got.Status != 401 && got.Status != 404 {
				t.Errorf("a request without a credential through %s for the internal package: %s, want 401 or 404", site.release, got)
			}
		})
	}
}
