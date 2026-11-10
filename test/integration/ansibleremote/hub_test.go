//go:build integration

package ansibleremote

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

// The hub behind single sign-on against the stub's realm (R-ANS-019): the service itself is not reachable from the tests (H-842).

func TestR_ANS_019_AHubIsCalledWithTheAccessTokenItsRealmIssuedAndNeverWithTheOfflineToken(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	if err := e.stub.SetRealm(ansiblestub.Realm{Require: true, RefreshTokens: []string{offlineToken}, ClientID: "cloud-services", AccessTTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}

	const ver = "v3/collections/acme/base/versions/1.0.0/"
	if a := e.get(r, rHub1, ver); a.Status != http.StatusOK {
		t.Fatalf("version through the hub: %d %s", a.Status, a.Body)
	}
	if a := e.get(r, rHub1, "v3/artifacts/collections/acme-base-1.0.0.tar.gz"); a.Status != http.StatusOK {
		t.Fatalf("archive through the hub: %d", a.Status)
	}
	st := e.stub.RealmState()
	if st.Exchanges != 1 || st.Failed != 0 {
		t.Errorf("%d exchanges and %d refused for the requests of one token's lifetime, want 1 and 0", st.Exchanges, st.Failed)
	}
	// The upstream saw only requests the realm authorizes: the offline token was never sent to it.
	if st.Rejected != 0 || st.Accepted < 3 {
		t.Errorf("the upstream accepted %d and rejected %d requests; the root, the document and the archive carry the access token", st.Accepted, st.Rejected)
	}
	if got := e.stub.Counts().WithAuth(ansiblestub.GalaxyLoopbackHost, "Bearer"); got < 3 || e.stub.Counts().WithAuth(ansiblestub.GalaxyLoopbackHost, "") != 0 {
		t.Errorf("%d Bearer requests and %d without a credential at the upstream", got, e.stub.Counts().WithAuth(ansiblestub.GalaxyLoopbackHost, ""))
	}
	// The realm was asked in the form the grant is made in.
	if len(st.ClientIDs) != 1 || st.ClientIDs[0] != "cloud-services" {
		t.Errorf("client ids %v", st.ClientIDs)
	}
	// The access token is never served, logged or audited.
	for _, rep := range e.st.Replicas() {
		if strings.Contains(rep.Logs.String(), offlineToken) || strings.Contains(rep.Logs.String(), st.Issued[0]) {
			t.Errorf("the log of %s names a token", rep.Name)
		}
	}
}

func TestR_ANS_019_ARevokedAccessTokenIsExchangedAgainAndTheRequestSucceeds(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	if err := e.stub.SetRealm(ansiblestub.Realm{Require: true, RefreshTokens: []string{offlineToken}, AccessTTLSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(r, rHub2, "v3/collections/acme/base/versions/1.0.0/"); a.Status != http.StatusOK {
		t.Fatalf("first: %d %s", a.Status, a.Body)
	}
	// The access token expires at the realm; the next request of another document meets the 401, exchanges again and succeeds.
	if n := e.stub.ExpireAccessTokens(); n != 1 {
		t.Fatalf("%d tokens were valid, want 1", n)
	}
	if a := e.get(r, rHub2, "v3/collections/acme/base/versions/1.2.0/"); a.Status != http.StatusOK {
		t.Fatalf("after the expiry: %d %s", a.Status, a.Body)
	}
	st := e.stub.RealmState()
	if st.Exchanges != 2 || st.Rejected != 1 {
		t.Errorf("%d exchanges and %d rejected requests, want 2 and 1 (the 401 that asked for the second exchange)", st.Exchanges, st.Rejected)
	}
}

func TestR_ANS_019_AWrongRefreshTokenFailsTheRequestAndTheUpstreamSeesNothing(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	if err := e.stub.SetRealm(ansiblestub.Realm{Require: true, RefreshTokens: []string{"another-token"}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(r, rHub3, "v3/collections/acme/base/")
	if a.Status != http.StatusBadGateway || !strings.Contains(string(a.Body), "refused") {
		t.Fatalf("a refused credential: %d %s", a.Status, a.Body)
	}
	st := e.stub.RealmState()
	if st.Failed < 1 || st.Exchanges != 0 {
		t.Errorf("realm state %+v", st)
	}
	if n := e.stub.Counts().OnHost(ansiblestub.GalaxyLoopbackHost); n != 0 {
		t.Errorf("the upstream saw %d requests: nothing is sent without a token, and the credential is not sent in its place", n)
	}
	if strings.Contains(string(a.Body), offlineToken) {
		t.Error("the answer names the credential")
	}
}

func TestR_ANS_019_TheCredentialGoesOnlyToAHostThatMayHaveIt(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	if err := e.stub.SetRealm(ansiblestub.Realm{Require: true, RefreshTokens: []string{offlineToken}}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(r, rHubNoHost, "v3/collections/acme/base/"); a.Status != http.StatusBadGateway {
		t.Errorf("a realm that is no credential host: %d %s", a.Status, a.Body)
	}
	if n := e.stub.Counts().OnHost(ansiblestub.DefaultRealmHost); n != 0 {
		t.Errorf("the realm saw %d requests: the refresh token is not sent to a host the repository does not send its credential to", n)
	}
}
