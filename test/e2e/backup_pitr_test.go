//go:build e2e

package e2e

// The point-in-time restore drill of R-HA-025 and the platform layer of R-HA-023 (E5.1, set backup-pitr): data and a token are written, the
// time t is noted, the token is revoked and more is written and deleted after t, and then the steps of R-HA-025 are followed on the main
// installation of the dev cluster: stop the roles, recover PostgreSQL from the archive of the Barman Cloud plugin into a new CloudNativePG
// cluster up to t, point the release at it, run the post-restore Job (which replays the revocation journal, R-HA-054), roll the object
// versions of the blob bucket back to t, start the roles and resume deletions. The restored installation is judged by what it serves.
// docs/developer/e2e.md#the-point-in-time-restore-drill

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	pitrCluster   = "trove-db-pitr"
	pitrBlobsRoot = "trove"
)

// pitrState is what the steps of the drill hand on.
type pitrState struct {
	run         string
	adm         *api
	victim      createdToken
	keep, gone  pitrFile
	post        pitrFile
	pkg         string
	keyspace    string // system_state.keyspace_id of the source before the restore
	tPG, tRFC   string // t as a PostgreSQL timestamp and as RFC 3339
	postRestore string // the output of the post-restore Job
}

type pitrFile struct {
	path   string
	body   []byte
	digest string // hex SHA-256, which is also the name of the blob
}

func newPITRFile(path string) pitrFile {
	body := make([]byte, 512<<10)
	_, _ = rand.Read(body)
	sum := sha256.Sum256(body)
	return pitrFile{path: path, body: body, digest: hex.EncodeToString(sum[:])}
}

// key is the object key of the blob in the blob bucket (storage.s3.prefix "trove", the layout of the blob store).
func (f pitrFile) key() string {
	return fmt.Sprintf("%s/sha256/%s/%s/%s", pitrBlobsRoot, f.digest[:2], f.digest[2:4], f.digest)
}

// pitrSQL runs a statement as the superuser on the database of the new cluster.
func pitrSQL(t testing.TB, query string) string {
	t.Helper()
	out, err := kubectl("-n", cfg.namespace, "exec", pitrCluster+"-1", "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c", query)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// pitrReleaseValues is the output of release-values.sh for the profile backup: the helm arguments of the dev release.
func pitrReleaseValues(t testing.TB) []string {
	t.Helper()
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy", "dev", "release-values.sh"), "backup")
	cmd.Dir = cfg.repo
	cmd.Env = append(cmd.Environ(), "TROVE_IMAGE="+upImage())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("release-values.sh backup: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// pitrNoRoles are the arguments of R-HA-025 step 1: the hold (Q-HA-212), every role and every pool at zero, no autoscaling.
func pitrNoRoles() []string {
	args := []string{"--set", "postRestore.hold=true"}
	for _, role := range []string{"api", "packages", "worker"} {
		args = append(args, "--set", role+".replicaCount=0", "--set", role+".autoscaling.enabled=false")
	}
	return append(args, "--set-json", `worker.pools=[{"name":"vuln","queues":["vuln"],"replicaCount":0,"autoscaling":{"enabled":false}}]`)
}

// pitrNewDatabase is R-HA-025 step 3: the release names the new cluster, its credentials Secret and its CA.
func pitrNewDatabase() []string {
	return []string{
		"--set", "config.database.host=" + pitrCluster + "-rw." + cfg.namespace + ".svc",
		"--set", "secrets.database.existingSecret=" + pitrCluster + "-app",
		"--set", "secrets.databaseCA.existingSecret=" + pitrCluster + "-ca",
	}
}

// pitrHelm upgrades the release with the values of the dev profile and the extra arguments.
func pitrHelm(t testing.TB, label string, extra ...string) {
	t.Helper()
	args := append([]string{"upgrade", "trove", upChart(), "-n", cfg.namespace}, pitrReleaseValues(t)...)
	args = append(append(args, extra...), "--wait", "--timeout", "12m")
	if out, err := upHelm(t, label, 14*time.Minute, args...); err != nil {
		t.Fatalf("helm upgrade (%s): %v\n%s", label, err, tail(out, 3000))
	}
}

// pitrS3Job runs the script in the aws-cli image against the dev SeaweedFS with its login and CA, as a Job of the main namespace, and
// returns the log. It fails the test when the Job fails.
func pitrS3Job(t testing.TB, name, script string) string {
	t.Helper()
	image := readVersionPin(t, "DEV_AWSCLI_IMAGE")
	manifest := fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":%q},"spec":{"backoffLimit":0,"ttlSecondsAfterFinished":3600,"template":{"spec":{
"restartPolicy":"Never","automountServiceAccountToken":false,
"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"runAsGroup":65532,"seccompProfile":{"type":"RuntimeDefault"}},
"containers":[{"name":"s3","image":%q,"env":[{"name":"HOME","value":"/tmp"}],
"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}},
"command":["sh","-c",%q],
"volumeMounts":[{"name":"tmp","mountPath":"/tmp"},{"name":"s3","mountPath":"/secrets"},{"name":"tls","mountPath":"/tls"}]}],
"volumes":[{"name":"tmp","emptyDir":{}},{"name":"s3","secret":{"secretName":"trove-s3"}},{"name":"tls","secret":{"secretName":"trove-s3-tls"}}]}}}}`,
		name, image, `set -eu
export AWS_ACCESS_KEY_ID="$(cat /secrets/access-key-id)" AWS_SECRET_ACCESS_KEY="$(cat /secrets/secret-access-key)" AWS_DEFAULT_REGION=us-east-1 AWS_CA_BUNDLE=/tls/ca.crt
s3api() { aws --endpoint-url https://trove-s3.`+cfg.namespace+`.svc:9000 s3api "$@"; }
`+script)
	kn(t, "delete", "job", name, "--ignore-not-found")
	create := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "create", "-f", "-")
	create.Stdin = strings.NewReader(manifest)
	if b, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create %s: %v: %s", name, err, b)
	}
	e := bakWaitJob(t, cfg.namespace, name, 10*time.Minute)
	logs := bakJobLogs(cfg.namespace, name)
	t.Logf("%s: %s\n%s", name, e.Condition, tail(logs, 1500))
	if e.Condition != "Complete" {
		t.Fatalf("the Job %s ended %s:\n%s", name, e.Condition, tail(logs, 3000))
	}
	return logs
}

// pitrGet reads a file through the edge.
func pitrGet(a *api, path string) reply {
	return a.get(filesAddr(bakFiles) + "/" + path)
}

func TestBackupPITR_R_HA_025_R_HA_023_R_HA_054_PointInTimeRestoreOfThePlatformLayer(t *testing.T) {
	useBackupProfile(t)
	if got := sql(t, "SHOW archive_mode"); got != "on" {
		t.Fatalf("archive_mode of trove-db is %q: the drill needs the platform layer of R-HA-023, so start the cluster with TROVE_PITR=1 (make dev-up) or run the set backup-pitr", got)
	}
	st := &pitrState{run: bakRun(), adm: bakAdmin(t)}
	adm := st.adm
	_ = adminToken(t)

	// Whatever the drill leaves, the main installation goes back to the cluster trove-db.
	t.Cleanup(func() {
		pitrHelm(t, "pitr-cleanup")
		script(t, "pitr.sh", "down")
	})

	// step runs one stage of the drill; a stage that failed stops the drill, because the next one builds on its result.
	step := func(name string, f func(t *testing.T)) {
		t.Helper()
		if !t.Run(name, f) {
			t.Fatalf("the stage %q failed: the drill stops", name)
		}
	}

	step("R-HA-023 a base backup and the continuous WAL archive protect the database", func(t *testing.T) {
		if got := sql(t, "SELECT count(*) FROM pg_stat_archiver WHERE last_archived_wal IS NOT NULL AND (last_failed_time IS NULL OR last_archived_time > last_failed_time)"); got != "1" {
			t.Fatalf("the archiver of the primary has not shipped a segment, or its last attempt failed: %s", sql(t, "SELECT row_to_json(a) FROM pg_stat_archiver a"))
		}
		t.Logf("base backup %s", strings.TrimSpace(script(t, "pitr.sh", "backup")))
		st.keyspace = sql(t, "SELECT value #>> '{}' FROM trove.system_state WHERE key = 'keyspace_id'")
		if st.keyspace == "" {
			t.Fatal("system_state has no keyspace_id")
		}
	})

	step("R-HA-025 the state before t: files, an npm package and a token", func(t *testing.T) {
		st.keep, st.gone = newPITRFile("pitr-"+st.run+"/keep.bin"), newPITRFile("pitr-"+st.run+"/gone.bin")
		for _, f := range []pitrFile{st.keep, st.gone} {
			if r, err := retrying(func() (reply, error) { return bakPutFile(adm, f.path, f.body) }); err != nil || r.Status != 201 {
				t.Fatalf("put %s: %v %s", f.path, err, r)
			}
		}
		st.pkg = "bak-pitr-" + st.run
		if r, err := retrying(func() (reply, error) { return bakPublishNPM(adm, st.pkg, "1.0.0") }); err != nil || r.Status != 201 {
			t.Fatalf("publish %s@1.0.0: %v %s", st.pkg, err, r)
		}
		ensureSA(t, adm, "bak-pitr-"+st.run)
		st.victim = newSATokenWaiting(t, adm, "bak-pitr-"+st.run, 4*3600, map[string]any{"inherit": true})
		if r := adm.withToken(st.victim.Token).get("/api/v1/authz/me"); r.Status != 200 {
			t.Fatalf("the token must work before t: %s", r)
		}
	})

	step("R-HA-025 t is noted, then the token is revoked and more is written and deleted after t", func(t *testing.T) {
		time.Sleep(4 * time.Second)
		st.tPG = sql(t, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00'`)
		st.tRFC = strings.Replace(strings.TrimSuffix(st.tPG, "+00"), " ", "T", 1) + "Z"
		t.Logf("t = %s", st.tPG)
		time.Sleep(4 * time.Second)

		adm.do(http.MethodDelete, "/api/v1/tokens/"+st.victim.ID, nil).want(t, "revoke the token after t", 200, 204)
		st.post = newPITRFile("pitr-" + st.run + "/post.bin")
		if r, err := retrying(func() (reply, error) { return bakPutFile(adm, st.post.path, st.post.body) }); err != nil || r.Status != 201 {
			t.Fatalf("put %s after t: %v %s", st.post.path, err, r)
		}
		if r, err := retrying(func() (reply, error) { return bakPublishNPM(adm, st.pkg, "1.1.0") }); err != nil || r.Status != 201 {
			t.Fatalf("publish %s@1.1.0 after t: %v %s", st.pkg, err, r)
		}
		if r, err := retrying(func() (reply, error) { return adm.send(http.MethodDelete, filesAddr(bakFiles)+"/"+st.gone.path, nil) }); err != nil || (r.Status != 204 && r.Status != 200) {
			t.Fatalf("delete %s after t: %v %s", st.gone.path, err, r)
		}
		// The grace period of the profile is 0s: the collector deletes the blob, and the versioned bucket answers with a delete marker.
		waitFor(t, "garbage collection to delete the object of "+st.gone.path, 3*time.Minute, 10*time.Second, func() bool {
			_, _ = adm.send(http.MethodPost, "/api/v1/system/tasks/gc/run", nil)
			return sql(t, fmt.Sprintf("SELECT count(*) FROM trove.blobs WHERE sha256 = decode('%s', 'hex') AND object_deleted_at IS NULL", st.gone.digest)) == "0"
		})
		// The revocation reaches the journal in the blob bucket, which the platform restore does not touch.
		waitFor(t, "the revocation journal to be shipped", 2*time.Minute, 3*time.Second, func() bool {
			return sql(t, "SELECT count(*) FROM trove.revocation_outbox") == "0"
		})
		// A later segment with a commit after t ends the archive, so that the recovery finds its target inside it (R-HA-023 recovery point).
		t.Logf("archived up to %s", strings.TrimSpace(script(t, "pitr.sh", "archive-wal")))
	})

	step("R-HA-025 step 1: the roles and the backups are stopped", func(t *testing.T) {
		pitrHelm(t, "pitr-step1", pitrNoRoles()...)
		kn(t, "delete", "job", "-l", "app.kubernetes.io/component=backup", "--ignore-not-found")
		waitFor(t, "every replica to be gone", 5*time.Minute, 3*time.Second, func() bool { return len(livePods(t, selTrove)) == 0 })
	})

	step("R-HA-025 step 2: PostgreSQL is recovered from the archive into a new cluster up to t", func(t *testing.T) {
		script(t, "pitr.sh", "restore", st.tPG)
		// The new database is the one at t: the file and the package of before t are in it, what came after t is not, and the revocation of the token
		// is not either (the journal brings it back in step 4).
		if got := pitrSQL(t, fmt.Sprintf("SELECT count(*) FROM trove.blobs WHERE sha256 IN (decode('%s', 'hex'), decode('%s', 'hex'))", st.keep.digest, st.gone.digest)); got != "2" {
			t.Errorf("the recovered database holds %s of the two blobs written before t, want 2", got)
		}
		if got := pitrSQL(t, fmt.Sprintf("SELECT count(*) FROM trove.blobs WHERE sha256 = decode('%s', 'hex')", st.post.digest)); got != "0" {
			t.Errorf("the recovered database knows the blob written after t")
		}
		if got := pitrSQL(t, fmt.Sprintf("SELECT revoked_at IS NULL FROM trove.api_token WHERE id = '%s'", st.victim.ID)); got != "t" {
			t.Errorf("in the recovered database the token revoked after t has revoked_at set to %q before the post-restore", got)
		}
	})

	step("R-HA-025 steps 3 and 4 and R-HA-054: the release points at the new cluster, the post-restore Job replays the revocation journal", func(t *testing.T) {
		extra := append(pitrNoRoles(), pitrNewDatabase()...)
		extra = append(extra,
			"--set", "postRestore.enabled=true", "--set-string", "postRestore.restoredTo="+st.tRFC,
			"--set", "postRestore.journalCredentials.source=static", "--set", "secrets.journalReader.existingSecret=trove-s3",
			"--set-json", `postRestore.extraVolumes=[{"name":"dev-auth","secret":{"secretName":"trove-keycloak-tls","defaultMode":288,"items":[{"key":"ca.crt","path":"keycloak-ca.crt"}]}}]`,
			"--set-json", `postRestore.extraVolumeMounts=[{"name":"dev-auth","mountPath":"/var/run/secrets/trove-dev","readOnly":true}]`)
		pitrHelm(t, "pitr-step4", extra...)
		out, err := kubectl("-n", cfg.namespace, "get", "jobs", "-l", "app.kubernetes.io/component=post-restore", "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil || len(strings.Fields(out)) != 1 {
			t.Fatalf("the post-restore Job: %q %v", out, err)
		}
		job := strings.Fields(out)[0]
		if e := bakWaitJob(t, cfg.namespace, job, 5*time.Minute); e.Condition != "Complete" {
			t.Fatalf("the post-restore Job ended %s", e.Condition)
		}
		st.postRestore = bakJobLogs(cfg.namespace, job)
		t.Logf("trove admin post-restore:\n%s", tail(st.postRestore, 2500))
		if !strings.Contains(st.postRestore, "token.revoked") || !strings.Contains(st.postRestore, "applied") {
			t.Errorf("the post-restore output does not list token.revoked as applied")
		}
		// The check of step 4 sees the object the collector deleted after t: its row is in the restored database, its object has a delete marker.
		m := regexp.MustCompile(`storage verify: \d+ blobs checked, (\d+) without object`).FindStringSubmatch(st.postRestore)
		if m == nil {
			t.Errorf("the post-restore output has no storage verify line")
		} else if n, _ := strconv.Atoi(m[1]); n < 1 {
			t.Errorf("the storage verify of the post-restore finds %d blobs without object, want at least the one collected after t", n)
		}
		if got := pitrSQL(t, "SELECT value #>> '{}' FROM trove.system_state WHERE key = 'keyspace_id'"); got == "" || got == st.keyspace {
			t.Errorf("system_state.keyspace_id is %q after the post-restore, the source had %q: want a new one", got, st.keyspace)
		}
		if got := pitrSQL(t, "SELECT (value <> 'null'::jsonb)::text FROM trove.system_state WHERE key = 'deletions_paused_at'"); got != "true" {
			t.Errorf("the deletion pause is %q after the post-restore, want it set", got)
		}
		if got := pitrSQL(t, fmt.Sprintf("SELECT revoked_at IS NOT NULL FROM trove.api_token WHERE id = '%s'", st.victim.ID)); got != "t" {
			t.Errorf("the journal replay did not revoke the token in the new database")
		}
	})

	step("R-HA-025 step 5: the object versions of the blob bucket go back to t", func(t *testing.T) {
		// The runtime credential cannot do this in production (R-HA-023); the dev SeaweedFS has one login, so the Job uses it. Every delete marker
		// younger than t under the blob prefix is removed; the objects written after t stay and are orphans of the restored database.
		tEpoch := mustParseRFC3339(t, st.tRFC).Unix()
		logs := pitrS3Job(t, "pitr-restore-versions", fmt.Sprintf(`set -eu
bucket=trove-blobs
n=0
s3api list-object-versions --bucket "$bucket" --prefix %q/ --query 'DeleteMarkers[].[Key,VersionId,LastModified]' --output text > /tmp/markers
while read -r key vid lm; do
  [ -n "$key" ] && [ "$key" != None ] || continue
  if [ "$(date -d "$lm" +%%s)" -gt %d ]; then
    s3api delete-object --bucket "$bucket" --key "$key" --version-id "$vid" >/dev/null
    n=$((n + 1))
  fi
done < /tmp/markers
echo "removed $n delete markers younger than %s"
for key in %q %q; do
  s3api head-object --bucket "$bucket" --key "$key" >/dev/null && echo "present $key"
done`, pitrBlobsRoot, tEpoch, st.tRFC, st.keep.key(), st.gone.key()))
		if !strings.Contains(logs, "present "+st.gone.key()) || !strings.Contains(logs, "present "+st.keep.key()) {
			t.Errorf("the objects written before t are not both present after the roll back:\n%s", logs)
		}
		if strings.Contains(logs, "removed 0 delete markers") {
			t.Errorf("no delete marker was removed, but the collector deleted %s after t", st.gone.key())
		}
	})

	step("R-HA-025 step 6: the roles start on the new cluster", func(t *testing.T) {
		pitrHelm(t, "pitr-step6", pitrNewDatabase()...)
		for sel, want := range map[string]int{selAPI: 2, selPackages: 3} {
			if got := len(livePods(t, sel)); got < want {
				t.Errorf("%d pods of %s are live, want %d", got, sel, want)
			}
		}
		waitFor(t, "the administrator's token to work on the restored installation", 3*time.Minute, 3*time.Second, func() bool {
			return adm.get("/api/v1/authz/me").Status == 200
		})
	})

	step("R-HA-025 the restored installation serves exactly the state at t, byte for byte", func(t *testing.T) {
		for _, f := range []pitrFile{st.keep, st.gone} {
			r := pitrGet(adm, f.path)
			sum := sha256.Sum256(r.Body)
			if r.Status != 200 || hex.EncodeToString(sum[:]) != f.digest {
				t.Errorf("%s answers %d with %d bytes of digest %s, want 200 and %s", f.path, r.Status, len(r.Body), hex.EncodeToString(sum[:]), f.digest)
			}
		}
		if r := pitrGet(adm, st.post.path); r.Status != 404 {
			t.Errorf("%s, written after t, answers %s, want 404", st.post.path, r)
		}
		var doc struct {
			Versions map[string]json.RawMessage `json:"versions"`
		}
		adm.get(npmAddr(bakNPM)+"/"+st.pkg).want(t, "the packument", 200).JSON(t, &doc)
		if _, ok := doc.Versions["1.0.0"]; !ok || len(doc.Versions) != 1 {
			t.Errorf("the packument of %s lists %v, want only 1.0.0: 1.1.0 was published after t", st.pkg, pitrKeysOf(doc.Versions))
		}
	})

	step("R-HA-024 after the roll back of step 5 every referenced blob has its object", func(t *testing.T) {
		requireStorageVerified(t)
	})

	step("R-HA-054 the token revoked after t stays rejected", func(t *testing.T) {
		if r := adm.withToken(st.victim.Token).get("/api/v1/authz/me"); r.Status != 401 {
			t.Errorf("the token revoked after t answers %s on the restored installation, want 401", r)
		}
	})

	step("R-HA-025 step 7: deletions resume, and the object written after t was not deleted while they were paused", func(t *testing.T) {
		logs := pitrS3Job(t, "pitr-orphan-check", fmt.Sprintf(`s3api head-object --bucket trove-blobs --key %q >/dev/null && echo "present %s"`, st.post.key(), st.post.key()))
		if !strings.Contains(logs, "present "+st.post.key()) {
			t.Errorf("the object written after t was deleted while the deletion pause was set")
		}
		// There is no command that clears the pause yet (H-767); the row is the one the post-restore wrote.
		pitrSQL(t, "UPDATE trove.system_state SET value = 'null'::jsonb, updated_at = now() WHERE key IN ('deletions_paused_at', 'deletions_paused_reason')")
		if got := pitrSQL(t, "SELECT count(*) FROM trove.system_state WHERE key = 'deletions_paused_at' AND value <> 'null'::jsonb"); got != "0" {
			t.Errorf("the deletion pause is still set")
		}
		if r, err := adm.send(http.MethodPost, "/api/v1/system/tasks/gc/run", nil); err != nil || (r.Status != 202 && r.Status != 409) {
			t.Errorf("garbage collection after the resume: %v %s", err, r)
		}
	})
}

func pitrKeysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustParseRFC3339(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
