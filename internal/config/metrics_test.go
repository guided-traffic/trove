package config

import (
	"strings"
	"testing"
)

// R-OPS-007, Q-OPS-10: repository_label_include takes the selectors of an instance context; a bare key stops startup and
// names the entry.
func TestR_OPS_007_RepositoryLabelIncludeTakesInstanceSelectors(t *testing.T) {
	load := func(file string) (*Loaded, error) {
		return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(), Command: CommandMigrate})
	}
	t.Run("qualified names, patterns and classes are accepted", func(t *testing.T) {
		l, err := load("metrics:\n  repository_label_include: [\"web/npm-all\", \"*/nuget-*\", \"@any-remote\"]\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Metrics.RepositoryLabelInclude; len(got) != 3 || got[0] != "web/npm-all" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("a bare key is refused and named", func(t *testing.T) {
		_, err := load("metrics:\n  repository_label_include: [\"npm-all\"]\n")
		if err == nil || !strings.Contains(err.Error(), "metrics.repository_label_include") || !strings.Contains(err.Error(), `"npm-all"`) {
			t.Fatalf("err = %v", err)
		}
	})
}
