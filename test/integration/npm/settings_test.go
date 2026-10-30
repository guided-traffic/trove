//go:build integration

package npm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// settingsErrors are the errors[] of a 422 answer by pointer.
func settingsErrors(t *testing.T, a testenv.Answer) map[string]string {
	t.Helper()
	var p struct {
		Errors []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(a.Body, &p); err != nil {
		t.Fatalf("%d is no problem document: %v\n%s", a.Status, err, a.Body)
	}
	out := map[string]string{}
	for _, f := range p.Errors {
		out[f.Pointer] = f.Code
	}
	return out
}

// TestR_ARCH_008_FormatSettingsThroughTheAPI: the npm member of a repository document is
// accepted by the management API, validated against the schema the format registers, stored,
// read back, and read by npm from there (R-ARCH-008, R-REPO-004, R-NPM-016).
func TestR_ARCH_008_FormatSettingsThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	// The administrator is seeded before the replica starts: its grant index is built from what exists then.
	admin := e.st.User("rita")
	e.st.Bind("admin", admin)
	tok := e.st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	a := e.replica("web-a")
	post := func(body map[string]any) testenv.Answer {
		t.Helper()
		return e.st.Do(a, "POST", "/api/v1/repositories", tok, body)
	}

	t.Run("R-REPO-004 the service lists the schema of the npm settings", func(t *testing.T) {
		ans := e.st.Do(a, "GET", "/api/v1/formats", tok, nil)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		var list struct {
			Formats []struct {
				Name           string          `json:"name"`
				SettingsSchema json.RawMessage `json:"settingsSchema"`
			} `json:"formats"`
		}
		if err := json.Unmarshal(ans.Body, &list); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, f := range list.Formats {
			got[f.Name] = string(f.SettingsSchema)
		}
		if got["generic"] != "null" {
			t.Errorf("generic settings schema = %s", got["generic"])
		}
		for _, want := range []string{`"latestGuard"`, `"serveRemovedVersions"`, `"latestStrategy"`, `"distTagStrategy"`, `"additionalProperties":false`} {
			if !strings.Contains(got["npm"], want) {
				t.Errorf("the npm schema lacks %s: %s", want, got["npm"])
			}
		}
	})

	t.Run("R-REPO-004 a create with npm settings is stored and read back, and an update changes them", func(t *testing.T) {
		ans := post(map[string]any{"key": "npm-noguard", "class": "local", "format": "npm", "npm": map[string]any{"latestGuard": false}})
		if ans.Status != 201 {
			t.Fatalf("create %d %s", ans.Status, ans.Body)
		}
		read := e.st.Do(a, "GET", "/api/v1/repositories/npm-noguard", tok, nil)
		doc := read.JSON(t)
		if got, _ := json.Marshal(doc["npm"]); string(got) != `{"latestGuard":false}` {
			t.Fatalf("read back npm = %s (%s)", got, read.Body)
		}
		if stored := e.st.Text(`SELECT config #>> '{npm,latestGuard}' FROM repositories WHERE key = 'npm-noguard'`); stored != "false" {
			t.Fatalf("stored latestGuard = %q", stored)
		}
		put := e.st.Do(a, "PUT", "/api/v1/repositories/npm-noguard", tok, map[string]any{"key": "npm-noguard", "class": "local", "format": "npm", "npm": map[string]any{"latestGuard": true}}, "If-Match", read.Header.Get("ETag"))
		if put.Status != 200 {
			t.Fatalf("replace %d %s", put.Status, put.Body)
		}
		if got, _ := json.Marshal(put.JSON(t)["npm"]); string(got) != `{"latestGuard":true}` {
			t.Fatalf("replaced npm = %s", got)
		}
		if patched := e.st.Do(a, "PATCH", "/api/v1/repositories/npm-noguard", tok, map[string]any{"npm": map[string]any{"latestGuard": false}}, "If-Match", "*", "Content-Type", "application/merge-patch+json"); patched.Status != 200 {
			t.Fatalf("patch %d %s", patched.Status, patched.Body)
		}
		cleared := e.st.Do(a, "PATCH", "/api/v1/repositories/npm-noguard", tok, map[string]any{"npm": nil}, "If-Match", "*", "Content-Type", "application/merge-patch+json")
		if cleared.Status != 200 {
			t.Fatalf("clear %d %s", cleared.Status, cleared.Body)
		}
		if _, there := cleared.JSON(t)["npm"]; there {
			t.Fatalf("a cleared member is still there: %s", cleared.Body)
		}
	})

	t.Run("R-REPO-008 invalid settings answer 422 with the pointers of the fields", func(t *testing.T) {
		ans := post(map[string]any{
			"key": "npm-bad", "class": "virtual", "format": "npm",
			"npm": map[string]any{"latestStrategy": "oldest", "distTagStrategy": 7, "latestGuard": true, "unknown": 1},
		})
		if ans.Status != 422 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		got := settingsErrors(t, ans)
		want := map[string]string{
			"/npm/latestStrategy":  "invalid_value",
			"/npm/distTagStrategy": "invalid_value",
			"/npm/latestGuard":     "not_applicable",
			"/npm":                 "invalid_value",
		}
		for p, c := range want {
			if got[p] != c {
				t.Errorf("errors[%s] = %q, want %q (all: %v)", p, got[p], c, got)
			}
		}
		if e.st.Number(`SELECT count(*) FROM repositories WHERE key = 'npm-bad'`) != 0 {
			t.Error("a refused create wrote a row")
		}
	})

	t.Run("R-REPO-004 the npm member of a repository of another format answers not_applicable", func(t *testing.T) {
		ans := post(map[string]any{"key": "gen-npm", "class": "local", "format": "generic", "npm": map[string]any{"latestGuard": true}})
		if ans.Status != 422 || settingsErrors(t, ans)["/npm"] != "not_applicable" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})

	t.Run("R-NPM-016 a publish reads npm.latestGuard from the repository document", func(t *testing.T) {
		for repo, wantLatest := range map[string]string{"npm-guard-off": "1.5.0", "npm-guard-on": "2.0.0", "npm-guard-default": "2.0.0"} {
			doc := map[string]any{"key": repo, "class": "local", "format": "npm"}
			switch repo {
			case "npm-guard-off":
				doc["npm"] = map[string]any{"latestGuard": false}
			case "npm-guard-on":
				doc["npm"] = map[string]any{"latestGuard": true}
			}
			if ans := post(doc); ans.Status != 201 {
				t.Fatalf("create %s: %d %s", repo, ans.Status, ans.Body)
			}
			e.mustPublish(a, repo, tok, pkg{Name: "left-pad", Version: "2.0.0"})
			e.mustPublish(a, repo, tok, pkg{Name: "left-pad", Version: "1.5.0"})
			if got := e.tags(repo, "left-pad")["latest"]; got != wantLatest {
				t.Errorf("%s: latest = %q, want %q", repo, got, wantLatest)
			}
		}
	})
}
