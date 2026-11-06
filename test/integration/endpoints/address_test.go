//go:build integration

package endpoints

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// TestR_REPO_001_ARepositoryIsAddressedByProjectAndKey runs the four-segment address through
// the real chain, the dispatcher and the files handler: the same key in two projects is two
// repositories, one segment is no address, a "%" in a named segment is none either, and a
// repository of a project the caller cannot see answers exactly like one that does not exist
// (R-REPO-001, R-AUTHZ-016, R-ARCH-005).
func TestR_REPO_001_ARepositoryIsAddressedByProjectAndKey(t *testing.T) {
	e := newEnv(t, false)
	e.stack.Repository("ops/generic-local", "local", "generic", "{}")
	r := e.start("a")
	put := func(path, content string) {
		t.Helper()
		if a := e.send(r.def, "trove.test", "PUT", path, e.opsPAT, []byte(content), "Content-Type", "application/octet-stream"); a.Status != 201 {
			t.Fatalf("PUT %s: %d %s", path, a.Status, a.Body)
		}
	}
	get := func(tok, path string) answer { return e.send(r.def, "trove.test", "GET", path, tok, nil) }

	t.Run("R-REPO-001 the same key in two projects is two repositories", func(t *testing.T) {
		put("/files/ops/generic-local/shared.txt", "from ops")
		put("/files/it/generic-local/shared.txt", "from it")
		for path, want := range map[string]string{
			"/files/ops/generic-local/shared.txt": "from ops",
			"/files/it/generic-local/shared.txt":  "from it",
		} {
			if a := get(e.opsPAT, path); a.Status != 200 || string(a.Body) != want {
				t.Errorf("%s: %d %q, want %q", path, a.Status, a.Body, want)
			}
		}
		// A file of one project is not in the other.
		put("/files/ops/generic-local/only-ops.txt", "x")
		if a := get(e.opsPAT, "/files/it/generic-local/only-ops.txt"); a.Status != 404 {
			t.Errorf("a file of ops in it: %d", a.Status)
		}
	})

	t.Run("R-REPO-001 one segment and an escape in a named segment are no address", func(t *testing.T) {
		for _, path := range []string{
			"/files/ops", "/files/ops/", "/files/generic-local/shared.txt",
			"/api/npm/ops", "/api/npm/ops/", "/api/nuget/ops",
			"/files/o%70s/generic-local/shared.txt", "/files/ops/generic-loca%6C/shared.txt", "/files/ops%2Fgeneric-local/shared.txt",
			"/api/npm/o%70s/generic-local/lodash",
		} {
			if a := get(e.opsPAT, path); a.Status != 404 {
				t.Errorf("%s: %d %s", path, a.Status, a.Body)
			}
		}
	})

	t.Run("R-AUTHZ-016 a project the caller cannot see answers like a repository that does not exist", func(t *testing.T) {
		// alice holds no grant anywhere: ops/generic-local exists, nowhere/generic-local and
		// it/ghost-key do not, and the three answers are the same.
		normal := func(a answer) map[string]any {
			m := a.JSON(t)
			delete(m, "instance")
			delete(m, "requestId")
			return m
		}
		hidden := get(e.alicePAT, "/files/ops/generic-local/shared.txt")
		if hidden.Status != 404 {
			t.Fatalf("hidden project: %d %s", hidden.Status, hidden.Body)
		}
		for _, path := range []string{"/files/nowhere/generic-local/shared.txt", "/files/it/ghost-key/shared.txt", "/files/ops/ghost-key/shared.txt"} {
			a := get(e.alicePAT, path)
			if a.Status != hidden.Status || !reflect.DeepEqual(normal(a), normal(hidden)) {
				t.Errorf("%s: %d %v, the hidden project answers %d %v", path, a.Status, normal(a), hidden.Status, normal(hidden))
			}
		}
		// The same holds on the protocol routes: nothing tells a hidden repository from an absent one.
		hiddenNPM := get(e.alicePAT, "/api/npm/ops/generic-local/lodash")
		for _, path := range []string{"/api/npm/nowhere/generic-local/lodash", "/api/npm/ops/ghost-key/lodash"} {
			a := get(e.alicePAT, path)
			if a.Status != hiddenNPM.Status || fmt.Sprint(normalNPM(a)) != fmt.Sprint(normalNPM(hiddenNPM)) {
				t.Errorf("%s: %d %s, the hidden project answers %d %s", path, a.Status, a.Body, hiddenNPM.Status, hiddenNPM.Body)
			}
		}
	})
}

// normalNPM is an npm error body without what differs per request.
func normalNPM(a answer) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(a.Body, &m)
	delete(m, "requestId")
	delete(m, "instance")
	return m
}
