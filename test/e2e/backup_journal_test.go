//go:build e2e

package e2e

// E5.12 and R-BAK-024 (scenario 10 of R-BAK-029): what a restore brings back and what it withholds. State is changed in the
// installation after a backup's snapshot, the revocation journal ships it to the blob bucket, and the backup is restored into the
// second namespace: the restore Job replays the journal.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// npmUnpublish is `npm unpublish name@version` of a client pod against the main installation.
func npmClientRun(t testing.TB, registryHost, token string, args ...string) (string, error) {
	t.Helper()
	pod := npmClientPod(t, 0)
	base := "/api/npm/" + e2eProject + "/" + bakNPM + "/"
	full := append([]string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", pod, "--", "env", "HOME=/tmp/bakhome", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt", "npm11"}, args...)
	full = append(full, "--registry", "https://"+registryHost+base, "--//"+registryHost+base+":_authToken="+token)
	out, err := exec.Command("kubectl", full...).CombinedOutput()
	return string(out), err
}

func TestBackupJournal(t *testing.T) {
	useBackupProfile(t)
	adm := bakAdmin(t)
	token := adminToken(t)
	run := bakRun()

	// ── before the snapshot ──────────────────────────────────────────────────
	ensureSA(t, adm, "bak-victim-"+run)
	victim := newSATokenWaiting(t, adm, "bak-victim-"+run, 4*3600, map[string]any{"inherit": true})
	if r := adm.withToken(victim.Token).get("/api/v1/authz/me"); r.Status != 200 {
		t.Fatalf("the token must work before the snapshot: %s", r)
	}
	alice := newBrowser(t)
	alice.oidcLogin("alice", secretValue(t, "trove-keycloak", "alice-password"))
	unp := "bak-unp-" + run
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if r, err := retrying(func() (reply, error) { return bakPublishNPM(adm, unp, v) }); err != nil || r.Status != 201 {
			t.Fatalf("publish %s@%s: %v %s", unp, v, err, r)
		}
	}

	// Three rules of the endpoint `customers` (R-EP-020): one that is narrowed in a way the comparator proves (a pattern leaves the include list), one narrowed in
	// a way it cannot prove (a glob replaced by a narrower glob: patterns are compared as strings, Q-EP-79), and one that is deleted.
	narrow, glob, gone := "bak-narrow-"+run, "bak-glob-"+run, "bak-gone-"+run
	for name, include := range map[string][]string{narrow: {"releases/**", "internal/**"}, glob: {"releases/**"}, gone: {"releases/**"}} {
		adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
			"name": name, "repository": qn(bakFiles), "paths": map[string]any{"include": include},
		}).want(t, "create the exposure rule "+name, 201)
	}
	t.Cleanup(func() {
		for _, name := range []string{narrow, glob, gone} {
			adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/"+name, nil, "If-Match", "*")
		}
	})

	// ── the backup ───────────────────────────────────────────────────────────
	zero := 0
	job := "bak-journal-" + run
	bakCreateJob(t, bakJob{name: job, backoff: &zero})
	end := bakWaitJob(t, cfg.namespace, job, 10*time.Minute)
	if end.Condition != "Complete" {
		t.Fatalf("the backup Job ended %s:\n%s", end.Condition, tail(bakJobLogs(cfg.namespace, job), 2000))
	}
	backupID := bakBackupID(bakJobLogs(cfg.namespace, job))
	t.Logf("backup %s", backupID)

	// ── after the snapshot ───────────────────────────────────────────────────
	adm.do(http.MethodDelete, "/api/v1/tokens/"+victim.ID, nil).want(t, "revoke the token", 200, 204)
	adm.do(http.MethodPost, "/api/v1/users/alice/disable", map[string]any{"revokeTokens": true}).want(t, "disable alice", 200, 204)
	if out, err := npmClientRun(t, "localhost:"+edgePort(t), token, "unpublish", unp+"@1.1.0", "--force"); err != nil {
		t.Fatalf("npm unpublish: %v: %s", err, tail(out, 600))
	}
	reserved := "Bak.Res" + strings.ToUpper(run)
	bytesA := nupkgBytes(reserved, "1.0.0", "the journaled build")
	if r, err := retrying(func() (reply, error) { return bakPushNuGet(adm, bytesA) }); err != nil || r.Status != 201 {
		t.Fatalf("push %s: %v %s", reserved, err, r)
	}
	adm.do(http.MethodDelete, nugetAddr(bakNuGet)+"/v3/package/"+reserved+"/1.0.0", nil).want(t, "delete the NuGet version", 200, 204)
	for name, include := range map[string]string{narrow: `["releases/**"]`, glob: `["releases/2.0/**"]`} {
		g := adm.get("/api/v1/endpoints/customers/exposure-rules/"+name).want(t, "read the rule", 200)
		adm.do(http.MethodPatch, "/api/v1/endpoints/customers/exposure-rules/"+name, `{"paths":{"include":`+include+`}}`,
			"Content-Type", "application/merge-patch+json", "If-Match", g.Header.Get("ETag")).want(t, "narrow the rule "+name, 200)
	}
	g := adm.get("/api/v1/endpoints/customers/exposure-rules/"+gone).want(t, "read the rule", 200)
	adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/"+gone, nil, "If-Match", g.Header.Get("ETag")).want(t, "delete the rule", 204)
	// the shipper writes the outbox to the bucket every ship_interval (5 s); the journal is complete when no row is pending
	time.Sleep(25 * time.Second)

	// ── the restore ──────────────────────────────────────────────────────────
	restoreNSScript(t, "infra")
	restoreNSScript(t, "install", backupID)
	re := bakWaitJob(t, restoreNS, "trove-restore", 20*time.Minute)
	logs := bakJobLogs(restoreNS, "trove-restore")
	if re.Condition != "Complete" {
		t.Fatalf("the restore Job ended %s:\n%s", re.Condition, tail(logs, 3000))
	}
	t.Logf("the restore's replay:\n%s", tail(logs, 1800))
	// what the restore left, before any replica or worker runs on it
	usage := func() string {
		return krSQL(t, `SELECT (SELECT blobs FROM trove.storage_usage_totals) || ' totals, ' || (SELECT count(*) FROM trove.storage_usage_deltas) || ' deltas, ' ||
			(SELECT count(*) FROM trove.blobs WHERE state = 'live') || ' live, ' || (SELECT count(*) FROM trove.blobs) || ' rows'`)
	}
	usageBefore := usage()
	usageFields := strings.Fields(usageBefore)
	t.Logf("storage usage after the restore, before the final upgrade: %s", usageBefore)
	restoreNSScript(t, "serve")
	t.Logf("storage usage after the final upgrade: %s", usage())
	rst := openRestored(t, token)

	t.Run("R-BAK-024 a token revoked after the snapshot is rejected after the restore and the output lists it as applied", func(t *testing.T) {
		if !strings.Contains(logs, "token.revoked") || !strings.Contains(logs, "applied") {
			t.Errorf("the restore's output does not list token.revoked as applied:\n%s", tail(logs, 1500))
		}
		if r := rst.api.withToken(victim.Token).get("/api/v1/authz/me"); r.Status != 401 {
			t.Errorf("the revoked token answers %s on the restored installation, want 401", r)
		}
		if r := rst.api.get("/api/v1/authz/me"); r.Status != 200 {
			t.Errorf("the administrator's token of the snapshot must still work: %s", r)
		}
	})

	t.Run("R-BAK-024 a user disabled after the snapshot stays disabled", func(t *testing.T) {
		var u struct {
			Status string `json:"status"`
		}
		rst.api.get("/api/v1/users/alice").want(t, "alice", 200).JSON(t, &u)
		if u.Status != "disabled" {
			t.Errorf("alice is %q on the restored installation, want disabled", u.Status)
		}
	})

	t.Run("R-NPM-047 an npm version unpublished after the snapshot is withheld until trove admin npm reinstate", func(t *testing.T) {
		got := restoredNPMVersions(t, token, unp)
		if strings.Join(got, ",") != "1.0.0" {
			t.Fatalf("npm view lists %v, want only 1.0.0: 1.1.0 was unpublished after the snapshot", got)
		}
		out, err := kubectl("-n", restoreNS, "exec", "deploy/trove-worker", "--", "trove", "admin", "npm", "reinstate", e2eProject+"/"+bakNPM, unp+"@1.1.0")
		if err != nil {
			t.Fatalf("reinstate: %v", err)
		}
		t.Logf("trove admin npm reinstate: %s", strings.TrimSpace(out))
		waitFor(t, "npm view to list the reinstated version", 2*time.Minute, 3*time.Second, func() bool {
			return strings.Join(restoredNPMVersions(t, token, unp), ",") == "1.0.0,1.1.0"
		})
	})

	t.Run("R-NUGET-033 a version number lost by the restore is reserved for its journaled bytes", func(t *testing.T) {
		other := nupkgBytes(reserved, "1.0.0", "another build")
		push := func(b []byte) reply {
			rep, err := retrying(func() (reply, error) { return bakPushNuGet(rst.pkg, b) })
			if err != nil {
				t.Fatal(err)
			}
			return rep
		}
		if rep := push(other); rep.Status != 409 && rep.Status != 403 && rep.Status != 400 {
			t.Errorf("another build of the reserved number answers %s, want a refusal", rep)
		} else {
			t.Logf("another build: %s", rep)
		}
		if rep := push(bytesA); rep.Status != 201 {
			t.Errorf("the journaled bytes answer %s, want 201", rep)
		}
	})

	t.Run("R-EP-020 an exposure rule narrowed or deleted after the snapshot stays narrowed or deleted, a narrowing the comparator cannot prove disables the rule", func(t *testing.T) {
		row := func(name string) string {
			return krSQL(t, `SELECT (paths->'include')::text || ' enabled=' || enabled FROM trove.endpoint_exposure_rule WHERE endpoint = 'customers' AND name = '`+name+`'`)
		}
		if got := row(narrow); got != `["releases/**"] enabled=true` {
			t.Errorf("the provably narrowed rule is %s on the restored installation, want [\"releases/**\"] enabled=true", got)
		}
		if got := row(glob); !strings.HasSuffix(got, "enabled=false") {
			t.Errorf("the rule narrowed by a glob the comparator cannot compare is %s, want it disabled", got)
		}
		if got := krSQL(t, `SELECT count(*) FROM trove.endpoint_exposure_rule WHERE endpoint = 'customers' AND name = '`+gone+`'`); got != "0" {
			t.Errorf("the deleted rule exists %s times on the restored installation", got)
		}
	})

	t.Run("R-STOR-047 the restore recounts the usage, and the overview equals the blobs table once the roles have started", func(t *testing.T) {
		// before any replica ran, the recount of the restore equals the table
		if usageBefore != fmt.Sprintf("%s totals, 0 deltas, %s live, %s rows", usageFields[0], usageFields[0], usageFields[0]) {
			t.Errorf("after the restore the usage counters differ from the blobs table: %s", usageBefore)
		}
		// the replicas that start publish the blobs of the UI bundle (ui_assets is a transient table), so the counters move; the overview
		// follows the table once the usage fold has run
		var o struct {
			Storage struct {
				Blobs int64 `json:"blobs"`
				Bytes int64 `json:"bytes"`
			} `json:"storage"`
		}
		var got, want string
		waitFor(t, "the overview to equal the blobs table", 3*time.Minute, 3*time.Second, func() bool {
			want = krSQL(t, `SELECT count(*) || '|' || coalesce(sum(size), 0) FROM trove.blobs WHERE state = 'live'`)
			r := rst.api.get("/api/v1/system/overview")
			if r.Status != 200 || json.Unmarshal(r.Body, &o) != nil {
				return false
			}
			got = fmt.Sprintf("%d|%d", o.Storage.Blobs, o.Storage.Bytes)
			return got == want
		})
		t.Logf("the overview says %s, the blobs table %s", got, want)
	})

	t.Run("R-API-050 the restored installation shows its origin, the source shows the run and the stored backups", func(t *testing.T) {
		var s struct {
			RestoredFrom *struct {
				BackupID string `json:"backupId"`
			} `json:"restoredFrom"`
		}
		rst.api.get("/api/v1/system/backups/summary").want(t, "summary", 200).JSON(t, &s)
		if s.RestoredFrom == nil || s.RestoredFrom.BackupID != backupID {
			b, _ := json.Marshal(s)
			t.Errorf("restoredFrom is not the backup %s: %s", backupID, b)
		}
		// backup_run is a transient table (R-BAK-004): the restored installation starts with no run; the run and the stored backups are the source's
		var src struct {
			StoredBackups *struct {
				Items []struct {
					BackupID string `json:"backupId"`
				} `json:"items"`
			} `json:"storedBackups"`
		}
		// the summary is one evaluation per replica and 30 seconds (Q-BAK-35)
		waitFor(t, "the source's summary to list the backup", 3*time.Minute, 5*time.Second, func() bool {
			src.StoredBackups = nil
			if r := adm.get("/api/v1/system/backups/summary"); r.Status != 200 || json.Unmarshal(r.Body, &src) != nil || src.StoredBackups == nil {
				return false
			}
			for _, b := range src.StoredBackups.Items {
				if b.BackupID == backupID {
					return true
				}
			}
			return false
		})
		var runs struct {
			Items []struct{ ID, Operation, State string } `json:"items"`
		}
		adm.get("/api/v1/system/backups?operation=create").want(t, "runs of the source", 200).JSON(t, &runs)
		if len(runs.Items) == 0 {
			t.Error("the source lists no backup run")
		}
	})
}
