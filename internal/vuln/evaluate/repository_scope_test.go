package evaluate

import (
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
)

// R-VULN-010, R-VULN-015, Q-VULN-75: the repository-level part of the scope rule, which the download path reads from the
// snapshot. A local repository with auto is undecided (the name decides) and counts as covered.
func TestR_VULN_015_RepositoryScopeIsDecidedFromTheSettingsAndTheUpstream(t *testing.T) {
	public := map[string][]string{"npm": {"registry.npmjs.org"}}
	for _, tc := range []struct {
		name    string
		class   repository.Class
		mode    vuln.MatchPublic
		host    string
		scope   Scope
		decided bool
	}{
		{"remote always", repository.Remote, vuln.MatchAlways, "mirror.internal", ScopeMatched, true},
		{"remote never", repository.Remote, vuln.MatchNever, "registry.npmjs.org", ScopeNotCovered, true},
		{"remote auto public", repository.Remote, vuln.MatchAuto, "registry.npmjs.org", ScopeMatched, true},
		{"remote auto not public", repository.Remote, vuln.MatchAuto, "mirror.internal", ScopeNotCovered, true},
		{"remote auto no host", repository.Remote, vuln.MatchAuto, "", ScopeNotCovered, true},
		{"local always", repository.Local, vuln.MatchAlways, "", ScopeMatched, true},
		{"local never", repository.Local, vuln.MatchNever, "", ScopeInternal, true},
		{"local auto", repository.Local, vuln.MatchAuto, "", ScopeMatched, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rv := repoView{Format: "npm", Class: tc.class, Settings: vuln.Settings{MatchPublicAdvisories: tc.mode}, UpstreamHost: tc.host}
			sc, decided := repositoryScope(public, rv)
			if sc != tc.scope || decided != tc.decided {
				t.Errorf("repositoryScope = %v, %v; want %v, %v", sc, decided, tc.scope, tc.decided)
			}
		})
	}
}

// A repository the evaluation does not apply to is covered: the answer never withholds less.
func TestR_VULN_015_RepositoryCoveredDefaultsToCovered(t *testing.T) {
	if !RepositoryCovered(nil, repository.Repository{Key: "x", Format: "raw"}) {
		t.Error("a repository without the scanning is not covered")
	}
}
