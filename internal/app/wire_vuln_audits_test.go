package app

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

type fakeClaims map[string][]repository.ClaimMatch

func (f fakeClaims) ClaimsFor(_ context.Context, _, name string) ([]repository.ClaimMatch, error) {
	return f[name], nil
}

type fakeRepos map[string]repository.Repository

func (f fakeRepos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	if r, ok := f[key]; ok {
		return r, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "gone")
}
func (f fakeRepos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

// R-VULN-010, R-VULN-017: a name is internal when a claim of a local repository of the format covers it. The setting
// matchPublicAdvisories: always of the holder lifts that, which the integration tier of the client audits covers with a real
// snapshot (test/integration/vulnaudit).
func TestInternalNameRuleCountsTheClaimsOfLocalRepositoriesOfTheFormat(t *testing.T) {
	claims := fakeClaims{
		"@acme/ui":   {{Repository: "npm-local"}},
		"mirrored":   {{Repository: "npm-remote"}},
		"other":      {{Repository: "nuget-local"}},
		"gone":       {{Repository: "deleted"}},
		"two-claims": {{Repository: "deleted"}, {Repository: "npm-local"}},
	}
	repos := fakeRepos{
		"npm-local":   {Key: "npm-local", Class: repository.Local, Format: "npm"},
		"npm-remote":  {Key: "npm-remote", Class: repository.Remote, Format: "npm"},
		"nuget-local": {Key: "nuget-local", Class: repository.Local, Format: "nuget"},
	}
	rule := internalNameRule(claims, repos)
	for name, want := range map[string]bool{
		"@acme/ui": true, "mirrored": false, "other": false, "gone": false, "two-claims": true, "unclaimed": false,
	} {
		got, err := rule(context.Background(), "npm", name)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v, want %v", name, got, err, want)
		}
	}
	failing := internalNameRule(fakeClaims{}, errReader{})
	if _, err := failing(context.Background(), "npm", "x"); err != nil {
		t.Errorf("a name without claims reads no repository: %v", err)
	}
	broken := internalNameRule(fakeClaims{"x": {{Repository: "k"}}}, errReader{})
	if _, err := broken(context.Background(), "npm", "x"); err == nil {
		t.Error("a reader that fails is not an answer")
	}
}

type errReader struct{ fakeRepos }

func (errReader) ByKey(context.Context, string) (repository.Repository, error) {
	return repository.Repository{}, errors.New("snapshot unavailable")
}
