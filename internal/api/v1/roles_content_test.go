package v1

import (
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/packages/view"
)

// TestR_OPS_004_SummaryPerRoleOfTheCut: replicas.roles has one entry per role of Q-OPS-161, api and packages included, and
// the registry's pool is a member of the replica description (R-UI-038, Q-UI-107).
func TestR_OPS_004_SummaryPerRoleOfTheCut(t *testing.T) {
	pool := "vuln"
	reps := []gen.Replica{
		replica("trove-api-1", "api", "n1", gen.Ready, false),
		replica("trove-api-2", "api", "n2", gen.Ready, false, "valkey"),
		replica("trove-packages-1", "packages", "n1", gen.Ready, false),
		replica("trove-packages-2", "packages", "n1", gen.Starting, false),
		replica("trove-worker-1", "worker", "n3", gen.Ready, false),
		replica("trove-worker-vuln-1", "worker", "n4", gen.Ready, false),
		replica("trove-web-1", "web", "n5", gen.Ready, false),
		replica("trove-packages-gone", "packages", "n6", gen.Ready, true),
	}
	reps[5].Pool = &pool
	sum := Summarise(reps)
	want := map[string]gen.RoleSummary{
		"api":      {Live: 2, Ready: 2, Degraded: 1, Nodes: 2},
		"packages": {Live: 2, Ready: 1, Degraded: 0, Nodes: 1},
		"worker":   {Live: 2, Ready: 2, Degraded: 0, Nodes: 2},
		"web":      {Live: 1, Ready: 1, Degraded: 0, Nodes: 1},
	}
	if len(sum.Roles) != len(want) {
		t.Fatalf("roles = %v", sum.Roles)
	}
	for role, w := range want {
		if sum.Roles[role] != w {
			t.Errorf("%s = %+v, want %+v", role, sum.Roles[role], w)
		}
	}
	if sum.Live != 7 {
		t.Errorf("live = %d, want 7 (a lost replica is not live)", sum.Live)
	}
}

// TestR_API_029_PackageFileContentURL: the file of a version carries the relative URL of its content route, built on the
// repository that holds it and the path of its item, next to the protocol URL (R-API-025, Q-UI-106).
func TestR_API_029_PackageFileContentURL(t *testing.T) {
	d := view.VersionDetail{
		Format: "npm", Name: "@team-a/ui", Version: "1.4.0",
		Files: []view.File{
			{Path: "@team-a/ui/-/ui-1.4.0.tgz", SizeBytes: 10, SHA256: "ab", Repo: "npm-local"},
			{Path: "dir with space/a#b.bin", SizeBytes: 1, SHA256: "cd", Repo: "gen eric"},
		},
	}
	out := versionOf("https://trove.example.com", d)
	for i, want := range []string{
		"/api/v1/repositories/npm-local/content/@team-a/ui/-/ui-1.4.0.tgz",
		"/api/v1/repositories/gen%20eric/content/dir%20with%20space/a%23b.bin",
	} {
		if got := out.Files[i].ContentUrl; got != want {
			t.Errorf("file %d: contentUrl %q, want %q", i, got, want)
		}
	}
	if out.Files[0].DownloadUrl == "" || out.Files[0].DownloadUrl == out.Files[0].ContentUrl {
		t.Errorf("downloadUrl %q must stay the protocol URL", out.Files[0].DownloadUrl)
	}
}

// TestR_OPS_004_RingsOfEveryRole: the key ring of a replica of every role counts in the compatibility rule (Q-OPS-161).
func TestR_OPS_004_RingsOfEveryRole(t *testing.T) {
	for _, role := range []string{"api", "packages", "worker", "web", "all"} {
		if !opensSecrets(role) {
			t.Errorf("a %s replica is left out of the key ring rule", role)
		}
	}
	if opensSecrets("backup") || opensSecrets("") {
		t.Error("a role that never registers counts")
	}
}
