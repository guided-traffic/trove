//go:build integration

package npm

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestR_NPM_038_ANameTheCallerMayNotReadIsNotForwarded: the audit forwards only names the caller may read on the
// forwarding remote, decided through the repository the client addressed (R-AUTHZ-016, Q-NPM-81). A name a grant's path
// pattern hides is withheld and counted with the names the remote never fetched, so the provider's answer and the notice
// tell the caller nothing about whether the remote proxied it; a caller without the pattern is answered for it.
func TestR_NPM_038_ANameTheCallerMayNotReadIsNotForwarded(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	excluder := r.st.User("excluder")
	r.st.Policy("p-excluder", []string{"read"}, []string{rRemote, rAll, rLocal}, excluder)
	r.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'lodash/**' FROM authz_policy WHERE name = 'p-excluder'`)
	tokX := r.st.Token(excluder, scope.Default())
	rep := r.start(1)[0]
	for _, n := range []string{"lodash", "left-pad"} {
		if a := r.view(rep, rRemote, n, r.tokA); a.Status != 200 {
			t.Fatalf("fetch %s: %d %s", n, a.Status, a.Body)
		}
	}
	r.stub.Reset()
	body := `{"lodash":["4.17.20"],"left-pad":["1.3.0"],"azure-orbit":["1.0.0"]}`
	for i, repo := range []string{rRemote, rAll} {
		a := r.postAudit(rep, repo, tokX, body, false)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", repo, a.Status, a.Body)
		}
		if got := askedOf(t, r.stub.Audits(), i); strings.Join(got, ",") != "left-pad" {
			t.Errorf("%s: the provider was asked about %v, want left-pad alone", repo, got)
		}
		if strings.Contains(string(a.Body), "lodash") {
			t.Errorf("%s: the answer names the hidden package: %s", repo, a.Body)
		}
		want := "trove audited 1 of 3 packages; 2 internal or not yet proxied names were not sent to the advisory provider"
		if got := a.Header.Get("npm-notice"); got != want {
			t.Errorf("%s: npm-notice %q, want %q", repo, got, want)
		}
	}
	a := r.postAudit(rep, rRemote, r.tokA, body, false)
	if got := askedOf(t, r.stub.Audits(), 2); a.Status != 200 || strings.Join(got, ",") != "left-pad,lodash" {
		t.Errorf("a caller without the pattern: %d, the provider was asked about %v", a.Status, got)
	}
}
