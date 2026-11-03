//go:build integration

package vulnapi

import (
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestR_VULN_019_TheRepositoryFilterExpandsOnlyAVirtualRepositoryTheCallerReads (Q-VULN-71): a caller who reads npm-local but not
// the virtual npm-all gets no finding for repository=npm-all, whose member it reads, so the filter does not tell which members
// a virtual repository has; the member itself, and a virtual repository the caller reads, answer as before.
func TestR_VULN_019_TheRepositoryFilterExpandsOnlyAVirtualRepositoryTheCallerReads(t *testing.T) {
	e := newEnv(t)
	e.addFinding(finding{Repo: "npm-local", Package: "p-member", Severity: "high"})
	erin := e.st.User("erin")
	e.st.Policy("local-readers", []string{"read"}, []string{"npm-local"}, erin)
	tok := e.st.Token(erin, scope.Default())
	web2 := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}}) // loads the policy

	if got := e.list(web2, tok, q("repository", "npm-local")).packages(); len(got) != 1 {
		t.Fatalf("the member: %v", got)
	}
	if got := e.list(web2, tok, q("repository", "npm-all")).packages(); len(got) != 0 {
		t.Errorf("a virtual repository the caller cannot read expanded to its member: %v", got)
	}
	// alice reads the virtual repository and its member.
	if got := e.list(e.r, e.tokAlice, q("repository", "npm-all")).packages(); len(got) != 1 {
		t.Errorf("a virtual repository the caller reads: %v", got)
	}
	// the other two operations that take the repository filter.
	for _, path := range []string{"/api/v1/vulnerabilities/summary?repository=npm-all"} {
		a := e.get(web2, path, tok)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
	}
}
