//go:build chart

package chart_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The login secrets of the chart: secrets.breakGlass (R-AUTH-014) and secrets.oidc[] (R-AUTH-004),
// mounted into web pods only (R-AUTH-042), as a directory so that a rotated Secret reaches the pod
// (Q-AUTH-14).

// loginSets is the minimum a login render needs: a provider of config.auth.oidc.providers for the
// secrets.oidc entry.
func loginSets(extra ...string) []string {
	return append([]string{
		"config.auth.oidc.providers.corp.issuer=https://idp.example.com",
		"config.auth.oidc.providers.corp.client_id=trove",
		"config.auth.oidc.providers.corp.display_name=Corp",
	}, extra...)
}

func volumeNamed(pod any, name string) any {
	for _, v := range list(pod, "volumes") {
		if str(v, "name") == name {
			return v
		}
	}
	return nil
}

func TestLoginSecrets(t *testing.T) {
	t.Run("R-HA-030 R-AUTH-014 secrets.breakGlass mounts the password into web pods as a directory and renders password_file", func(t *testing.T) {
		_, docs := render(t, required(), "secrets.breakGlass.existingSecret=trove-break-glass")
		cfg := configOf(t, docs)
		if got := str(cfg, "auth", "break_glass", "password_file"); got != "/var/run/secrets/trove/break-glass/password" {
			t.Errorf("auth.break_glass.password_file = %q", got)
		}
		if at(cfg, "auth", "break_glass", "enabled") != nil {
			t.Errorf("auth.break_glass.enabled = %v, want the trove default (not set by the chart)", at(cfg, "auth", "break_glass", "enabled"))
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		vol := secretVolume(pod, "trove-break-glass")
		if vol == nil {
			t.Fatal("trove-web does not mount trove-break-glass")
		}
		m := mountOf(pod, vol)
		if str(m, "mountPath") != "/var/run/secrets/trove/break-glass" || at(m, "readOnly") != true || at(m, "subPath") != nil {
			t.Errorf("mount = %v, want a read-only directory mount without subPath", m)
		}
		items := list(vol, "secret", "items")
		if len(items) != 1 || str(items[0], "key") != "password" || str(items[0], "path") != "password" {
			t.Errorf("items = %v", items)
		}
		if at(vol, "secret", "defaultMode") != 288 {
			t.Errorf("defaultMode = %v, want 0440", at(vol, "secret", "defaultMode"))
		}
	})

	t.Run("R-AUTH-042 the break-glass Secret is not mounted into worker pods or the migration Job", func(t *testing.T) {
		_, docs := render(t, required(), "secrets.breakGlass.existingSecret=trove-break-glass")
		for _, pod := range []any{
			at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"),
			at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"),
		} {
			if secretVolume(pod, "trove-break-glass") != nil || volumeNamed(pod, "secret-break-glass") != nil {
				t.Error("a pod that is not web mounts the break-glass Secret")
			}
		}
	})

	t.Run("R-AUTH-042 pool pods serve the login and mount the login secrets", func(t *testing.T) {
		_, docs := render(t, []string{filepath.Join(chartDir, "ci", "endpoints-pool.yaml")}, "secrets.breakGlass.existingSecret=trove-break-glass")
		found := 0
		for _, d := range ofKind(docs, "Deployment") {
			n := str(d, "metadata", "name")
			if strings.HasPrefix(n, "trove-web-ep-") {
				found++
				if secretVolume(at(d, "spec", "template", "spec"), "trove-break-glass") == nil {
					t.Errorf("%s does not mount the break-glass Secret", n)
				}
			}
		}
		if found == 0 {
			t.Fatal("no endpoint pool Deployment in ci/endpoints-pool.yaml")
		}
	})

	t.Run("R-HA-030 R-AUTH-014 without secrets.breakGlass nothing is mounted and the account is off", func(t *testing.T) {
		_, docs := render(t, required())
		cfg := configOf(t, docs)
		if at(cfg, "auth", "break_glass", "enabled") != false {
			t.Errorf("auth.break_glass.enabled = %v, want false", at(cfg, "auth", "break_glass", "enabled"))
		}
		if at(cfg, "auth", "break_glass", "password_file") != nil {
			t.Error("auth.break_glass.password_file rendered without secrets.breakGlass")
		}
		for _, role := range []string{"web", "worker"} {
			if volumeNamed(at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec"), "secret-break-glass") != nil {
				t.Errorf("trove-%s mounts a break-glass volume without secrets.breakGlass", role)
			}
		}
	})

	t.Run("R-HA-030 a different key of the break-glass Secret is projected as password", func(t *testing.T) {
		_, docs := render(t, required(), "secrets.breakGlass.existingSecret=bg", "secrets.breakGlass.key=hash")
		vol := secretVolume(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"), "bg")
		items := list(vol, "secret", "items")
		if len(items) != 1 || str(items[0], "key") != "hash" || str(items[0], "path") != "password" {
			t.Errorf("items = %v", items)
		}
	})

	t.Run("R-HA-029 auth.break_glass.password_file is chart-managed", func(t *testing.T) {
		renderFails(t, required(), []string{"config.auth.break_glass.password_file=/x"}, "config.auth.break_glass.password_file", "managed")
	})

	t.Run("R-HA-030 R-AUTH-004 secrets.oidc mounts the client secret into web pods and renders client_secret_file of the provider", func(t *testing.T) {
		_, docs := render(t, required(), loginSets("secrets.oidc[0].providerId=corp", "secrets.oidc[0].existingSecret=trove-oidc-corp")...)
		cfg := configOf(t, docs)
		if got := str(cfg, "auth", "oidc", "providers", "corp", "client_secret_file"); got != "/var/run/secrets/trove/oidc-corp/client-secret" {
			t.Errorf("client_secret_file = %q", got)
		}
		if got := str(cfg, "auth", "oidc", "providers", "corp", "client_id"); got != "trove" {
			t.Errorf("the provider's own keys were lost: client_id = %q", got)
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		vol := secretVolume(pod, "trove-oidc-corp")
		if vol == nil {
			t.Fatal("trove-web does not mount trove-oidc-corp")
		}
		m := mountOf(pod, vol)
		if str(m, "mountPath") != "/var/run/secrets/trove/oidc-corp" || at(m, "readOnly") != true || at(m, "subPath") != nil {
			t.Errorf("mount = %v", m)
		}
		items := list(vol, "secret", "items")
		if len(items) != 1 || str(items[0], "key") != "client-secret" || str(items[0], "path") != "client-secret" {
			t.Errorf("items = %v", items)
		}
		for _, pod := range []any{
			at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"),
			at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"),
		} {
			if secretVolume(pod, "trove-oidc-corp") != nil {
				t.Error("R-AUTH-042: a pod that is not web mounts the client secret")
			}
		}
	})

	t.Run("R-HA-030 secrets.oidc takes a key and several providers", func(t *testing.T) {
		_, docs := render(t, required(), loginSets(
			"config.auth.oidc.providers.second.issuer=https://idp2.example.com",
			"config.auth.oidc.providers.second.client_id=trove",
			"config.auth.oidc.providers.second.display_name=Second",
			"secrets.oidc[0].providerId=corp", "secrets.oidc[0].existingSecret=a", "secrets.oidc[0].key=secret",
			"secrets.oidc[1].providerId=second", "secrets.oidc[1].existingSecret=b")...)
		cfg := configOf(t, docs)
		for id, path := range map[string]string{"corp": "oidc-corp", "second": "oidc-second"} {
			if got := str(cfg, "auth", "oidc", "providers", id, "client_secret_file"); got != "/var/run/secrets/trove/"+path+"/client-secret" {
				t.Errorf("provider %s client_secret_file = %q", id, got)
			}
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		if got := str(list(secretVolume(pod, "a"), "secret", "items")[0], "key"); got != "secret" {
			t.Errorf("key of provider corp = %q", got)
		}
	})

	t.Run("R-HA-029 secrets.oidc is validated: provider ID, Secret, a provider to bind and the managed key", func(t *testing.T) {
		renderFails(t, required(), loginSets("secrets.oidc[0].providerId=Corp", "secrets.oidc[0].existingSecret=x"), "providerId")
		renderFails(t, required(), loginSets("secrets.oidc[0].providerId=corp"), "existingSecret")
		renderFails(t, required(), loginSets("secrets.oidc[0].providerId=other", "secrets.oidc[0].existingSecret=x"), "secrets.oidc[0].providerId", "config.auth.oidc.providers")
		renderFails(t, required(), loginSets("secrets.oidc[0].providerId=corp", "secrets.oidc[0].existingSecret=x",
			"secrets.oidc[1].providerId=corp", "secrets.oidc[1].existingSecret=y"), "two entries", "corp")
		renderFails(t, required(), loginSets("secrets.oidc[0].providerId=corp", "secrets.oidc[0].existingSecret=x",
			"config.auth.oidc.providers.corp.client_secret_file=/x"), "config.auth.oidc.providers.corp.client_secret_file", "managed")
	})

	t.Run("R-HA-030 without secrets.oidc no client secret is rendered", func(t *testing.T) {
		_, docs := render(t, required(), loginSets()...)
		if at(configOf(t, docs), "auth", "oidc", "providers", "corp", "client_secret_file") != nil {
			t.Error("client_secret_file rendered without secrets.oidc")
		}
	})

	t.Run("R-OPS-028 the rendered login configuration passes the binary", func(t *testing.T) {
		bin := trove(t)
		_, docs := render(t, required(), loginSets("secrets.oidc[0].providerId=corp", "secrets.oidc[0].existingSecret=x", "secrets.breakGlass.existingSecret=bg")...)
		cfgPath := filepath.Join(t.TempDir(), "trove.yaml")
		if err := os.WriteFile(cfgPath, []byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), 0o600); err != nil {
			t.Fatal(err)
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		cmd := exec.Command(bin, "admin", "config", "validate", "--config", cfgPath, "--format", "json")
		cmd.Env = podEnviron(pod)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("trove admin config validate: %v\n%s", err, out)
		}
	})
}
