//go:build integration

package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The system banner (R-OPS-025, R-UI-033) and the read-only maintenance mode (R-OPS-024) across three web replicas behind a round-robin
// Service and a worker, on the real composition root. Valkey is stopped (the replicas point at a Sentinel that never answers), so what
// the tests observe is carried by PostgreSQL alone.

const settingsPaths = "/api/v1/system/banner"

// stateEnv starts the installation: an npm and a generic repository (rows exist before the first replica starts, so every grant index
// has them), three web replicas and a worker.
func stateEnv(t *testing.T) (*env, *webCluster) {
	t.Helper()
	e := newEnv(t, false)
	e.stack.Repository("gen-state", "local", "generic", "{}")
	e.stack.Repository("npm-state", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	webs := []*node{e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{}), e.newNode("web-3", nodeOpts{})}
	e.newNode("worker-1", nodeOpts{role: "worker"})
	return e, e.cluster(webs...)
}

// everyReplica runs fn once per replica of the cluster, so that a check cannot be satisfied by the one replica that took the write.
func everyReplica(c *webCluster, fn func(i int)) {
	for i := range c.nodes {
		fn(i)
	}
}

func TestR_OPS_025_SystemBanner(t *testing.T) {
	e, c := stateEnv(t)
	admin, alice := e.admin.header(), e.alice.header()

	t.Run("R-OPS-025 a banner that was never set is disabled, and anonymous callers may read it", func(t *testing.T) {
		a := send(t, c, "GET", settingsPaths, nil)
		if a.Status != 200 || a.JSON(t)["enabled"] != false {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-OPS-025 only an administrator sets the banner", func(t *testing.T) {
		body := map[string]any{"enabled": true, "message": "x"}
		if a := send(t, c, "PUT", settingsPaths, body); a.Status != 401 {
			t.Errorf("anonymous: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "PUT", settingsPaths, body, alice...); a.Status != 403 {
			t.Errorf("alice: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "PUT", settingsPaths, body, e.reader.header()...); a.Status != 403 {
			t.Errorf("a reader with settings:read only: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-OPS-025 an invalid banner names the offending members", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"unknown severity":     {"enabled": true, "severity": "loud", "message": "x"},
			"enabled without text": {"enabled": true, "message": "  "},
			"title over 120":       {"enabled": false, "title": strings.Repeat("t", 121)},
			"message over 2000":    {"enabled": false, "message": strings.Repeat("m", 2001)},
			"member the API lacks": {"enabled": false, "colour": "red"},
			"enabled is required":  {"message": "x"},
		} {
			a := send(t, c, "PUT", settingsPaths, body, admin...)
			if a.Status != 400 && a.Status != 422 {
				t.Errorf("%s: %d %s", name, a.Status, a.Body)
			}
		}
		a := send(t, c, "PUT", settingsPaths, map[string]any{"enabled": true, "severity": "loud", "message": "x"}, admin...)
		errs, _ := a.JSON(t)["errors"].([]any)
		if a.Status != 422 || len(errs) != 1 || errs[0].(map[string]any)["pointer"] != "/severity" {
			t.Errorf("severity: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-OPS-025 a banner with a javascript link is stored as typed and every replica returns it, with Valkey stopped", func(t *testing.T) {
		text := "see [status](javascript:alert(1))"
		a := send(t, c, "PUT", settingsPaths, map[string]any{"enabled": true, "severity": "warning", "title": "Notice", "message": text}, admin...)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		waitFor(t, "every replica to return the banner", 2*time.Second, func() bool {
			ok := true
			everyReplica(c, func(int) {
				b := send(t, c, "GET", settingsPaths, nil).JSON(t)
				ok = ok && b["enabled"] == true && b["message"] == text && b["severity"] == "warning" && b["title"] == "Notice"
			})
			return ok
		})
	})

	t.Run("R-OPS-025 the change is audited with the members that changed, and an unchanged banner writes nothing", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'system.banner_changed'`)
		if before != 1 {
			t.Fatalf("%d banner events after one change", before)
		}
		if a := send(t, c, "PUT", settingsPaths, map[string]any{"enabled": true, "severity": "warning", "title": "Notice", "message": "see [status](javascript:alert(1))"}, admin...); a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'system.banner_changed'`); n != before {
			t.Errorf("an unchanged banner wrote an event: %d", n)
		}
		a := send(t, c, "PUT", settingsPaths, map[string]any{"enabled": false, "severity": "warning", "title": "Notice", "message": "see [status](javascript:alert(1))"}, admin...)
		if a.Status != 200 || a.JSON(t)["enabled"] != false {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if got := e.stack.Text(`SELECT details->>'changes' FROM audit_event WHERE action = 'system.banner_changed' ORDER BY occurred_at DESC LIMIT 1`); got != `["enabled"]` {
			t.Errorf("changes %s", got)
		}
		b := send(t, c, "GET", settingsPaths, nil).JSON(t)
		if b["enabled"] != false || b["message"] != "see [status](javascript:alert(1))" {
			t.Errorf("a disabled banner keeps its text: %v", b)
		}
	})
}

func TestR_OPS_024_ReadOnlyMaintenanceMode(t *testing.T) {
	e, c := stateEnv(t)
	admin, alice := e.admin.header(), e.alice.header()
	const path = "/api/v1/system/maintenance"
	mode := func() string {
		return send(t, c, "GET", "/api/v1/system/info", nil, admin...).JSON(t)["maintenanceMode"].(string)
	}

	t.Run("R-OPS-024 the mode is normal at first, and any authenticated caller reads it", func(t *testing.T) {
		if a := send(t, c, "GET", path, nil); a.Status != 401 {
			t.Errorf("anonymous: %d %s", a.Status, a.Body)
		}
		a := send(t, c, "GET", path, nil, alice...)
		if a.Status != 200 || a.JSON(t)["mode"] != "normal" || a.JSON(t)["cacheFill"] != true {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-OPS-024 only an administrator switches the mode, and a bad request names the member", func(t *testing.T) {
		if a := send(t, c, "PUT", path, map[string]any{"mode": "read_only"}, alice...); a.Status != 403 {
			t.Errorf("alice: %d %s", a.Status, a.Body)
		}
		for name, body := range map[string]map[string]any{
			"unknown mode":     {"mode": "paused"},
			"retry over a day": {"mode": "read_only", "retryAfterSeconds": 86401},
			"retry zero":       {"mode": "read_only", "retryAfterSeconds": 0},
			"message over 500": {"mode": "read_only", "message": strings.Repeat("m", 501)},
			"mode is required": {"message": "x"},
		} {
			if a := send(t, c, "PUT", path, body, admin...); a.Status != 400 && a.Status != 422 {
				t.Errorf("%s: %d %s", name, a.Status, a.Body)
			}
		}
		if mode() != "normal" {
			t.Fatal("a refused request changed the mode")
		}
	})

	filesPut := func(path string) answer {
		return send(t, c, "PUT", "/files/it/gen-state/"+path, "payload", append([]string{"Content-Type", "application/octet-stream"}, admin...)...)
	}
	// An npm unpublish of a package that does not exist answers 404 in normal mode: it is a write route that reaches the store.
	npmWrite := func() answer {
		return send(t, c, "DELETE", "/api/npm/it/npm-state/left-pad/-rev/1-abc", nil, admin...)
	}
	create := func(name string) answer {
		return send(t, c, "POST", "/api/v1/projects", map[string]any{"key": name, "displayName": name}, admin...)
	}

	if a := filesPut("before.txt"); a.Status != 201 {
		t.Fatalf("a deploy in normal mode: %d %s", a.Status, a.Body)
	}
	if a := npmWrite(); a.Status == 503 {
		t.Fatalf("an npm write in normal mode: %d %s", a.Status, a.Body)
	}

	t.Run("R-OPS-024 read_only mode is in force on every replica within 2 s, with Valkey stopped", func(t *testing.T) {
		a := send(t, c, "PUT", path, map[string]any{"mode": "read_only", "message": "Storage migration until 14:00 UTC", "retryAfterSeconds": 120}, admin...)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		got := a.JSON(t)
		if got["mode"] != "read_only" || got["message"] != "Storage migration until 14:00 UTC" || got["retryAfterSeconds"] != float64(120) || got["by"] == nil || got["since"] == nil {
			t.Fatalf("%v", got)
		}
		waitFor(t, "every replica to refuse a write", 2*time.Second, func() bool {
			ok := true
			everyReplica(c, func(int) { ok = ok && create("probe-"+randHex(4)).Status == 503 })
			return ok
		})
	})

	t.Run("R-OPS-024 every kind of write is refused with 503, Retry-After and the message", func(t *testing.T) {
		cases := map[string]answer{
			"a file deploy":            filesPut("during.txt"),
			"a file delete":            send(t, c, "DELETE", "/files/it/gen-state/before.txt", nil, admin...),
			"an npm unpublish":         npmWrite(),
			"a management write":       create("during"),
			"another management write": send(t, c, "POST", "/api/v1/groups", map[string]any{"name": "during-group"}, admin...),
			"a repository delete":      send(t, c, "DELETE", "/api/v1/projects/it/repositories/gen-state", nil, admin...),
			"a banner write":           send(t, c, "PUT", settingsPaths, map[string]any{"enabled": false}, admin...),
			"a task run":               send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, admin...),
		}
		for name, a := range cases {
			if a.Status != 503 {
				t.Errorf("%s: %d %s", name, a.Status, a.Body)
				continue
			}
			if a.Header.Get("Retry-After") != "120" {
				t.Errorf("%s: Retry-After %q", name, a.Header.Get("Retry-After"))
			}
			if !strings.Contains(string(a.Body), "Storage migration until 14:00 UTC") {
				t.Errorf("%s: the answer does not carry the message: %s", name, a.Body)
			}
		}
		if n := e.scalar(`SELECT count(*) FROM items WHERE path = 'before.txt'`); n != 1 {
			t.Errorf("a refused delete removed the file: %d", n)
		}
	})

	t.Run("R-OPS-024 a caller who may not write is told 403, not the mode", func(t *testing.T) {
		if a := send(t, c, "POST", "/api/v1/projects", map[string]any{"key": "x1", "displayName": "x"}, alice...); a.Status != 403 {
			t.Errorf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-OPS-024 reads, sign-in credentials and the switch itself keep working", func(t *testing.T) {
		if a := send(t, c, "GET", "/files/it/gen-state/before.txt", nil, admin...); a.Status != 200 || string(a.Body) != "payload" {
			t.Errorf("a download: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "GET", "/api/v1/projects", nil, admin...); a.Status != 200 {
			t.Errorf("a management read: %d %s", a.Status, a.Body)
		}
		a := send(t, c, "POST", "/api/v1/tokens", map[string]any{"description": "during maintenance", "ttlSeconds": 3600}, admin...)
		if a.Status != 201 {
			t.Fatalf("a token created in maintenance: %d %s", a.Status, a.Body)
		}
		id, _ := a.JSON(t)["id"].(string)
		if r := send(t, c, "DELETE", "/api/v1/tokens/"+id, nil, admin...); r.Status != 204 {
			t.Errorf("a token revoked in maintenance: %d %s", r.Status, r.Body)
		}
		if got := mode(); got != "read_only" {
			t.Errorf("the info shows %q", got)
		}
		if info := send(t, c, "GET", "/api/v1/system/info", nil, admin...); info.Status != 200 {
			t.Errorf("%d", info.Status)
		}
	})

	t.Run("R-OPS-024 setting the mode again changes nothing, keeps the time and writes no event", func(t *testing.T) {
		first := send(t, c, "GET", path, nil, alice...).JSON(t)
		events := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'system.maintenance_mode_changed'`)
		a := send(t, c, "PUT", path, map[string]any{"mode": "read_only", "message": "Storage migration until 14:00 UTC", "retryAfterSeconds": 120}, admin...)
		if a.Status != 200 || a.JSON(t)["since"] != first["since"] || a.JSON(t)["by"] != first["by"] {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'system.maintenance_mode_changed'`); n != events {
			t.Errorf("a repeat wrote an event: %d", n)
		}
	})

	t.Run("R-OPS-024 the scheduler enqueues no mutating task while the mode is on", func(t *testing.T) {
		// The precondition is forced: the garbage collection the scheduler enqueued when the worker started is removed, so that a
		// scheduler that ignored the mode would insert it again within a tick.
		e.exec(`DELETE FROM job WHERE kind IN ('gc', 'gc-full') AND task IS NOT NULL`)
		time.Sleep(3 * time.Second) // several scheduler ticks
		if n := e.scalar(`SELECT count(*) FROM job WHERE kind IN ('gc', 'gc-full') AND task IS NOT NULL`); n != 0 {
			t.Errorf("%d scheduled garbage collections in read-only mode", n)
		}
	})

	t.Run("R-OPS-024 normal mode lifts the refusal on every replica within 2 s and is audited", func(t *testing.T) {
		a := send(t, c, "PUT", path, map[string]any{"mode": "normal"}, admin...)
		if a.Status != 200 || a.JSON(t)["mode"] != "normal" || a.JSON(t)["since"] != nil {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		waitFor(t, "every replica to accept a write", 2*time.Second, func() bool {
			ok := true
			everyReplica(c, func(int) { ok = ok && filesPut("after-"+randHex(4)+".txt").Status == 201 })
			return ok
		})
		if npmWrite().Status == 503 || create("after").Status != 201 {
			t.Error("a write is still refused")
		}
		// The positive control of the scheduler check: with the mode off, the slot that was held back is enqueued.
		waitFor(t, "the scheduler to enqueue the garbage collection again", 30*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM job WHERE kind IN ('gc', 'gc-full') AND task IS NOT NULL`) > 0
		})
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'system.maintenance_mode_changed'`); n != 2 {
			t.Errorf("%d events, want one for on and one for off", n)
		}
		if got := e.stack.Text(`SELECT details->>'from' || '>' || (details->>'to') FROM audit_event WHERE action = 'system.maintenance_mode_changed' ORDER BY occurred_at DESC LIMIT 1`); got != "read_only>normal" {
			t.Errorf("the last event %q", got)
		}
	})

	t.Run("R-OPS-024 a refusal is a 503 problem the document knows", func(t *testing.T) {
		if a := send(t, c, "PUT", path, map[string]any{"mode": "read_only"}, admin...); a.Status != http.StatusOK {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		defer send(t, c, "PUT", path, map[string]any{"mode": "normal"}, admin...)
		waitFor(t, "every replica to refuse a write", 2*time.Second, func() bool {
			ok := true
			everyReplica(c, func(int) { ok = ok && create("probe-"+randHex(4)).Status == 503 })
			return ok
		})
		a := create("probe2")
		if a.Status != 503 || a.Code(t) != "unavailable" || a.Header.Get("Retry-After") != "300" {
			t.Errorf("%d %s %q", a.Status, a.Body, a.Header.Get("Retry-After"))
		}
	})
}
