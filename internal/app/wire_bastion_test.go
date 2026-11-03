package app

import "testing"

// TestR_BAS_004_AReplicaWithoutIdentityBuildsNoBastionService: the registration service and the authenticator
// are built from the identity area and the database; a process that has neither (a worker, the shell profile)
// gets nil, which the management API answers with 503 and the recognition stage never calls.
func TestR_BAS_004_AReplicaWithoutIdentityBuildsNoBastionService(t *testing.T) {
	a := &App{parts: &wiring{}}
	if a.bastionRegistrations() != nil {
		t.Error("a registration service without a database")
	}
	if a.bastionAuthenticator() != nil {
		t.Error("an authenticator without a token service")
	}
}
