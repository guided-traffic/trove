package cluster

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// TestR_OPS_003_RoleValues: the registry's vocabulary is the --role name (Q-OPS-161), and only the roles that fill client
// misses share a remote repository's fetch budget (R-REPO-040, Q-ARCH-297).
func TestR_OPS_003_RoleValues(t *testing.T) {
	for role, fills := range map[string]bool{config.RoleAPI: false, config.RolePackages: true, config.RoleWorker: false, config.RoleWeb: true, config.RoleAll: true} {
		if !config.ValidRole(role) {
			t.Errorf("%q is not a valid role", role)
		}
		if FillsClientMisses(role) != fills {
			t.Errorf("FillsClientMisses(%q) = %v, want %v", role, !fills, fills)
		}
	}
	for _, role := range []string{"", "fetch", "backup", "WEB", "api "} {
		if config.ValidRole(role) || FillsClientMisses(role) {
			t.Errorf("%q is accepted", role)
		}
	}
}

func TestR_OPS_003_RegistryRefusesAnUnknownRole(t *testing.T) {
	id := ReplicaIdentity{UID: "8a0b", Name: "p", Role: "fetch"}
	if _, err := NewRegistry(RegistryOptions{DB: nopDB{}, Identity: id}); err == nil {
		t.Fatal("the role fetch registered")
	}
	for _, role := range []string{config.RoleAPI, config.RolePackages, config.RoleWorker, config.RoleWeb, config.RoleAll} {
		id.Role = role
		if _, err := NewRegistry(RegistryOptions{DB: nopDB{}, Identity: id}); err != nil {
			t.Errorf("role %s: %v", role, err)
		}
	}
}

func TestR_REPO_040_FillCountIsAtLeastOne(t *testing.T) {
	if _, err := NewFillCount(nil, time.Second, time.Second); err == nil {
		t.Error("a count without a connection")
	}
	c, err := NewFillCount(nopDB{}, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.Get() != 1 {
		t.Errorf("n = %d before the first read, want 1", c.Get())
	}
	if err := c.Refresh(t.Context()); err == nil {
		t.Error("a failed read is not reported")
	}
	if c.Get() != 1 {
		t.Errorf("a failed read changed n to %d", c.Get())
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Errorf("stop of a count that never started: %v", err)
	}
}
