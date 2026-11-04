package config

import (
	"slices"
	"strings"
	"testing"
)

func TestR_ARCH_001_ProcessRoles(t *testing.T) {
	t.Run("R-ARCH-001 serve accepts api, packages, worker, web and all", func(t *testing.T) {
		for _, role := range []string{"api", "packages", "worker", "web", "all"} {
			if _, err := Load(Options{Environ: servableEnv("TROVE_PROCESS_ROLE=" + role), Command: CommandServe, ReadFile: fakeFS(nil)}); err != nil {
				t.Errorf("%s: %v", role, err)
			}
		}
	})
	t.Run("R-ARCH-001 a role outside the set is refused, naming the set", func(t *testing.T) {
		for _, role := range []string{"fetch", "bastion", "backup", "Web"} {
			_, err := Load(Options{Environ: servableEnv("TROVE_PROCESS_ROLE=" + role), Command: CommandServe, ReadFile: fakeFS(nil)})
			containsAll(t, issuesOf(t, err), "process.role must be one of api, packages, worker, web, all")
		}
	})
	t.Run("R-ARCH-001 the message of a missing role names the five roles", func(t *testing.T) {
		if RoleError != "process.role must be api, packages, worker, web or all" {
			t.Errorf("RoleError = %q", RoleError)
		}
	})
}

func TestR_EP_005_RoleFamilies(t *testing.T) {
	t.Run("R-EP-005 the families of the roles", func(t *testing.T) {
		for role, want := range map[string][]string{
			RoleAPI:      {SurfaceUI, SurfaceAPI, SurfaceCIExchange},
			RolePackages: {SurfacePackages, SurfaceFiles},
			RoleWeb:      Surfaces(),
			RoleAll:      Surfaces(),
			RoleWorker:   nil,
			"":           nil,
			"backup":     nil,
		} {
			if got := RoleFamilies(role); !slices.Equal(got, want) {
				t.Errorf("RoleFamilies(%q) = %v, want %v", role, got, want)
			}
		}
	})
	t.Run("R-EP-005 api and packages together serve every family exactly once", func(t *testing.T) {
		got := slices.Concat(RoleFamilies(RoleAPI), RoleFamilies(RolePackages))
		slices.Sort(got)
		want := slices.Clone(Surfaces())
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("api + packages = %v, want %v", got, want)
		}
	})
	t.Run("Q-ARCH-298 the effective surface is the surface in its own order, narrowed to the role", func(t *testing.T) {
		surface := []string{SurfacePackages, SurfaceFiles, SurfaceAPI}
		for role, want := range map[string][]string{
			RoleAPI:      {SurfaceAPI},
			RolePackages: {SurfacePackages, SurfaceFiles},
			RoleWeb:      surface,
			RoleWorker:   nil,
		} {
			if got := IntersectFamilies(surface, RoleFamilies(role)); !slices.Equal(got, want) {
				t.Errorf("%s: %v, want %v", role, got, want)
			}
		}
		if got := IntersectFamilies([]string{SurfacePackages}, RoleFamilies(RoleAPI)); len(got) != 0 {
			t.Errorf("a packages-only endpoint on api: %v", got)
		}
	})
	t.Run("R-ARCH-001 only a worker accepts no client", func(t *testing.T) {
		for role, want := range map[string]bool{RoleAPI: true, RolePackages: true, RoleWeb: true, RoleAll: true, RoleWorker: false, "": false} {
			if ServesClients(role) != want {
				t.Errorf("ServesClients(%q) = %v", role, !want)
			}
		}
	})
}

func TestR_HA_016_ExpectedServingReplicas(t *testing.T) {
	t.Run("Q-HA-77 the new key counts, the deprecated key is read while the new one is 0", func(t *testing.T) {
		for name, tc := range map[string]struct {
			p     DatabasePreflight
			n     int
			alias bool
		}{
			"new":        {DatabasePreflight{ExpectedServingReplicas: 6}, 6, false},
			"alias":      {DatabasePreflight{ExpectedWebReplicas: 4}, 4, true},
			"both equal": {DatabasePreflight{ExpectedServingReplicas: 4, ExpectedWebReplicas: 4}, 4, false},
			"new wins":   {DatabasePreflight{ExpectedServingReplicas: 6, ExpectedWebReplicas: 4}, 6, false},
			"none":       {DatabasePreflight{}, 0, false},
		} {
			if n, alias := tc.p.Serving(); n != tc.n || alias != tc.alias {
				t.Errorf("%s: Serving() = %d, %v; want %d, %v", name, n, alias, tc.n, tc.alias)
			}
		}
	})
	t.Run("Q-HA-77 both keys set to different values are refused naming the deprecated one", func(t *testing.T) {
		_, err := Load(Options{
			Environ: servableEnv("TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS=6", "TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS=4"),
			Command: CommandServe, ReadFile: fakeFS(nil),
		})
		containsAll(t, issuesOf(t, err), "database.preflight.expected_web_replicas", "expected_serving_replicas")
	})
	t.Run("Q-HA-77 the same value in both keys, and either key alone, load", func(t *testing.T) {
		for _, env := range [][]string{
			{"TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS=4", "TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS=4"},
			{"TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS=4"},
			{"TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS=4"},
		} {
			if _, err := Load(Options{Environ: servableEnv(env...), Command: CommandServe, ReadFile: fakeFS(nil)}); err != nil {
				t.Errorf("%v: %v", env, err)
			}
		}
	})
	t.Run("Q-HA-77 the key is a schema key with its environment variable, and the deprecated one stays", func(t *testing.T) {
		s := MustDescribe()
		for path, env := range map[string]string{
			"database.preflight.expected_serving_replicas": "TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS",
			"database.preflight.expected_web_replicas":     "TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS",
		} {
			if k, ok := s.ByPath(path); !ok || k.Env != env {
				t.Errorf("%s: env %q (found %v), want %q", path, k.Env, ok, env)
			}
		}
	})
}

func TestR_BAS_044_PackagesURL(t *testing.T) {
	withPackages := func(u string) string {
		return strings.Replace(bastionMinimal, "    url: https://trove.example.com\n", "    url: https://trove.example.com\n    packages_url: "+u+"\n", 1)
	}
	t.Run("R-BAS-044 packages_url defaults to empty, which means url", func(t *testing.T) {
		c := mustLoadBastion(t, bastionMinimal)
		if c.Bastion.Main.PackagesURL != "" {
			t.Errorf("default %q", c.Bastion.Main.PackagesURL)
		}
	})
	t.Run("R-BAS-044 an https packages_url loads", func(t *testing.T) {
		c := mustLoadBastion(t, withPackages("https://trove-packages.trove.svc:8080"))
		if got := c.Bastion.Main.PackagesURL; got != "https://trove-packages.trove.svc:8080" {
			t.Errorf("packages_url %q", got)
		}
	})
	t.Run("R-BAS-044 an http packages_url stops startup naming the key", func(t *testing.T) {
		msg := bastionIssues(t, withPackages("http://trove-packages.trove.svc:8080"))
		if !strings.Contains(msg, "bastion.main.packages_url") || !strings.Contains(msg, "https") {
			t.Errorf("error:\n%s", msg)
		}
	})
	t.Run("R-BAS-044 a packages_url with a path is refused naming the key", func(t *testing.T) {
		msg := bastionIssues(t, withPackages("https://trove-packages.trove.svc/api"))
		if !strings.Contains(msg, "bastion.main.packages_url") {
			t.Errorf("error:\n%s", msg)
		}
	})
}
