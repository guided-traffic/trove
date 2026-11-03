//go:build integration

package bastion

import (
	"net/http"
	"testing"
)

// TestR_BAS_026_AReplicaStartedAfterARevocationDoesNotAcceptTheStoredAnswer: replica A stores the answer for a
// caller in Valkey, the caller's token is revoked, and replica B starts only then. B reads the feed history from
// before its start (R-BAS-026) before it uses any shared answer, so the answer A stored is unusable for it.
func TestR_BAS_026_AReplicaStartedAfterARevocationDoesNotAcceptTheStoredAnswer(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{Backend: "bucket", Valkey: true}).wait()
	u := w.newReader(npmAll, npmLocal, npmRemote)
	c := callOpts{token: u.token, addr: "198.51.100.191"}
	path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	warm(t, b, c, path)
	w.mustAPI("DELETE", "/api/v1/tokens/"+u.id, 204, nil)
	refused(t, b, path, c, http.StatusUnauthorized)

	second := b.addReplica()
	if got := second.do(t, "GET", path, c); got.Status != http.StatusUnauthorized {
		t.Fatalf("a replica started after the revocation serves the revoked token: %d", got.Status)
	}
}
