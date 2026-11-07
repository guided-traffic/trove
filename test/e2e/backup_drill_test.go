//go:build e2e

package e2e

// The restore drill of R-HA-052 and scenario 1, 3 and 9 of R-BAK-029 (E5.1, E5.11): packages are published, deleted and
// trashed, and garbage collection with a grace period of 0s runs every 10 seconds, while `trove backup create` runs as a Job
// made from the CronJob. The backup is restored into a second namespace (a fresh CloudNativePG cluster, a fresh bucket) with
// the restore Job of R-HA-060, and the restored installation is judged by what it serves.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// bakRun is a short random tag of a run, so that names do not meet those of an earlier run.
func bakRun() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// drillState is what the subtests of the drill hand on.
type drillState struct {
	adm      *api
	run      string
	backupID string
	load     bakStats
	rst      *restored
}

// restoredNPMVersions is `npm view <name> versions --json` of a client pod against the restored installation.
func restoredNPMVersions(t testing.TB, token, name string) []string {
	t.Helper()
	host := "trove-packages." + restoreNS + ".svc:8080"
	registry := "https://" + host + "/api/npm/" + e2eProject + "/" + bakNPM + "/"
	pod := npmClientPod(t, 0)
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "exec", pod, "--", "env", "HOME=/tmp/bakhome", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt",
		"npm11", "view", name, "versions", "--json", "--registry", registry, "--//"+host+"/api/npm/"+e2eProject+"/"+bakNPM+"/:_authToken="+token)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("npm view %s: %v: %s", name, err, tail(stderr, 600))
	}
	var many []string
	if json.Unmarshal(out, &many) != nil {
		var one string
		if err := json.Unmarshal(out, &one); err != nil {
			t.Fatalf("npm view %s: %s", name, out)
		}
		many = []string{one}
	}
	sort.Strings(many)
	return many
}

// blobObjectsMatch runs a Job in the restored namespace that reads the object of every digest from the restored blob bucket
// and compares its SHA-256 (the files in the trash can have no API: R-STOR-022 is P1). It fails the test on a missing or damaged
// object.
func blobObjectsMatch(t testing.TB, digests []string) {
	t.Helper()
	image := readVersionPin(t, "DEV_AWSCLI_IMAGE")
	var list strings.Builder
	for _, d := range digests {
		list.WriteString(d + "\n")
	}
	kr(t, "delete", "configmap", "blob-digests", "--ignore-not-found")
	apply := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", restoreNS, "create", "configmap", "blob-digests", "--from-literal=digests="+list.String())
	if b, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("configmap: %v: %s", err, b)
	}
	kr(t, "delete", "job", "blob-check", "--ignore-not-found")
	manifest := fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"blob-check"},"spec":{"backoffLimit":0,"template":{"spec":{
"restartPolicy":"Never","automountServiceAccountToken":false,
"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"runAsGroup":65532,"seccompProfile":{"type":"RuntimeDefault"}},
"containers":[{"name":"check","image":%q,"env":[{"name":"HOME","value":"/tmp"}],
"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}},
"command":["sh","-c",%q],
"volumeMounts":[{"name":"tmp","mountPath":"/tmp"},{"name":"s3","mountPath":"/secrets"},{"name":"tls","mountPath":"/tls"},{"name":"digests","mountPath":"/digests"}]}],
"volumes":[{"name":"tmp","emptyDir":{}},{"name":"s3","secret":{"secretName":"trove-s3"}},{"name":"tls","secret":{"secretName":"trove-s3-tls"}},{"name":"digests","configMap":{"name":"blob-digests"}}]}}}}`,
		image, `set -eu
export AWS_ACCESS_KEY_ID="$(cat /secrets/access-key-id)" AWS_SECRET_ACCESS_KEY="$(cat /secrets/secret-access-key)" AWS_DEFAULT_REGION=us-east-1 AWS_CA_BUNDLE=/tls/ca.crt
cat > /tmp/check.sh <<'EOS'
#!/bin/sh
# check.sh <file of digests>: reads each object of the restored bucket and compares its SHA-256 with its digest
n=0; bad=0
while read -r d; do
  [ -n "$d" ] || continue
  key="trove/sha256/$(echo "$d" | cut -c1-2)/$(echo "$d" | cut -c3-4)/$d"
  got="$(aws --endpoint-url https://trove-s3.trove.svc:9000 s3 cp "s3://trove-restore-blobs/$key" - 2>/dev/null | sha256sum | cut -d' ' -f1 || true)"
  n=$((n+1))
  if [ "$got" != "$d" ]; then echo "MISMATCH $d got $got"; bad=$((bad+1)); fi
done < "$1"
echo "$n $bad" > "$1.result"
EOS
chmod +x /tmp/check.sh
cd /tmp && split -n l/8 /digests/digests part.
for f in part.*; do /tmp/check.sh "/tmp/$f" & done
wait
cat /tmp/part.*.result | awk '{n+=$1; bad+=$2} END {print "checked " n " objects, " bad " bad"; exit bad > 0}'`)
	create := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", restoreNS, "create", "-f", "-")
	create.Stdin = strings.NewReader(manifest)
	if b, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create blob-check: %v: %s", err, b)
	}
	e := bakWaitJob(t, restoreNS, "blob-check", 10*time.Minute)
	logs := bakJobLogs(restoreNS, "blob-check")
	t.Logf("blob-check: %s\n%s", e.Condition, tail(logs, 600))
	if e.Condition != "Complete" {
		t.Fatalf("a restored object is missing or damaged:\n%s", tail(logs, 2000))
	}
}

// readVersionPin reads a pin of tools/versions.env.
func readVersionPin(t testing.TB, name string) string {
	t.Helper()
	raw, err := os.ReadFile(cfg.repo + "/tools/versions.env")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("%s is not pinned in tools/versions.env", name)
	return ""
}

func TestBackupDrill(t *testing.T) {
	useBackupProfile(t)
	st := &drillState{adm: bakAdmin(t), run: bakRun()}
	adm := st.adm
	token := adminToken(t)
	top := t // the forwards to the restored installation outlive the subtest that opens them

	t.Run("R-BAK-029 scenario 1: a backup during publishing, deleting, trashing and collecting", func(t *testing.T) {
		// A reference data set: a dozen files of 1 MiB, so that the copy of the blobs takes a while at the throttle below.
		for i := range 12 {
			body := make([]byte, 1<<20)
			_, _ = rand.Read(body)
			if r, err := retrying(func() (reply, error) { return bakPutFile(adm, fmt.Sprintf("ref-%s/%02d.bin", st.run, i), body) }); err != nil || r.Status != 201 {
				t.Fatalf("reference file %d: %v %s", i, err, r)
			}
		}
		for i := range 4 {
			r, err := retrying(func() (reply, error) { return bakPublishNPM(adm, fmt.Sprintf("bak-ref-%s-%d", st.run, i), "1.0.0") })
			if err != nil || r.Status != 201 {
				t.Fatalf("reference npm package %d: %v %s", i, err, r)
			}
			r, err = retrying(func() (reply, error) {
				return bakPushNuGet(adm, nupkgBytes(fmt.Sprintf("Bak.Ref%s%d", strings.ToUpper(st.run), i), "1.0.0", "reference"))
			})
			if err != nil || r.Status != 201 {
				t.Fatalf("reference NuGet package %d: %v %s", i, err, r)
			}
		}
		load := startBakLoad(adm, st.run)
		defer func() {
			if st.load.NPM == 0 { // the test failed before it stopped the load
				load.stop()
			}
		}()
		lastLog := time.Now()
		waitFor(t, "the load to publish, delete and collect", 2*time.Minute, time.Second, func() bool {
			s := load.snapshot()
			if time.Since(lastLog) > 15*time.Second {
				lastLog = time.Now()
				t.Logf("the load so far: %+v", s)
			}
			return s.NPM >= 5 && s.NuGet >= 5 && s.Deleted >= 1 && s.Trashed >= 1 && s.Collections >= 1
		})
		before := load.snapshot()

		zero := 0
		job := "bak-drill-" + st.run
		start := time.Now()
		bakCreateJob(t, bakJob{name: job, backoff: &zero, env: map[string]string{"TROVE_BACKUP_COPY_BYTES_PER_SECOND": "1MiB", "TROVE_BACKUP_COPY_CONCURRENCY": "1"}})
		end := bakWaitJob(t, cfg.namespace, job, 10*time.Minute)
		took := time.Since(start)
		after := load.snapshot()
		st.load = load.stop()
		logs := bakJobLogs(cfg.namespace, job)
		if end.Condition != "Complete" {
			t.Fatalf("the backup Job ended %s (exits %v):\n%s", end.Condition, end.Exits, tail(logs, 3000))
		}
		st.backupID = bakBackupID(logs)
		if st.backupID == "" {
			t.Fatalf("the Job's output names no backup:\n%s", tail(logs, 1500))
		}
		t.Logf("backup %s took %s; the load during the Job: %d npm, %d NuGet, %d files, %d deleted, %d trashed, %d collections (%d refused as running); %d errors",
			st.backupID, took.Round(time.Second), after.NPM-before.NPM, after.NuGet-before.NuGet, after.Files-before.Files, after.Deleted-before.Deleted,
			after.Trashed-before.Trashed, after.Collections-before.Collections, after.Throttled-before.Throttled, len(st.load.Errors))
		// The collection runs the load asked for are jobs of the kind gc. A run lasts delete_settle (30 s) beyond its work, so the requests every 10 s
		// start one run per 30 s and the others are refused as running (409); the runs that started while the Job did show that the collector met the hold.
		gcJobs := sql(t, fmt.Sprintf(`SELECT count(*) || ' gc jobs started, ' || count(*) FILTER (WHERE state = 'succeeded') || ' finished, ' ||
			coalesce(sum((result->>'deleted')::int), 0) || ' blobs deleted, ' || coalesce(sum((result->>'held')::int), 0) || ' candidates held'
			FROM trove.job WHERE kind = 'gc' AND created_at >= now() - interval '%d seconds'`, int(time.Since(start).Seconds())+5))
		t.Logf("garbage collection since the Job was created: %s", gcJobs)
		timings.set("gcDuringBackup", gcJobs)
		if after.NPM == before.NPM || after.NuGet == before.NuGet || after.Files == before.Files {
			t.Errorf("the load did not publish while the backup ran: before %+v, after %+v", before, after)
		}
		if len(st.load.Errors) > 0 {
			t.Errorf("the load saw errors: %v", st.load.Errors)
		}
		timings.set("backupCreate", map[string]any{"backupId": st.backupID, "seconds": took.Seconds(), "podExits": end.Exits, "loadDuring": map[string]int{
			"npm": after.NPM - before.NPM, "nuget": after.NuGet - before.NuGet, "files": after.Files - before.Files, "collections": after.Collections - before.Collections,
		}})
	})
	if st.backupID == "" {
		t.Fatal("no backup to restore")
	}

	t.Run("R-BAK-021 restore into a second namespace with the restore Job", func(t *testing.T) {
		start := time.Now()
		restoreNSScript(t, "infra")
		infra := time.Since(start)
		restoreNSScript(t, "install", st.backupID, "--set-json", `restore.extraArgs=["--json"]`)
		began := time.Now()
		end := bakWaitJob(t, restoreNS, "trove-restore", 20*time.Minute)
		took := time.Since(began)
		logs := bakJobLogs(restoreNS, "trove-restore")
		if end.Condition != "Complete" {
			t.Fatalf("the restore Job ended %s (exits %v):\n%s", end.Condition, end.Exits, tail(logs, 3000))
		}
		var rep struct {
			Phases map[string]float64
		}
		if m := regexp.MustCompile(`(?s)"phasesSeconds":\s*(\{.*?\})`).FindStringSubmatch(logs); m != nil {
			_ = json.Unmarshal([]byte(m[1]), &rep.Phases)
		}
		t.Logf("restore of %s: infra %s, Job %s, phases %v", st.backupID, infra.Round(time.Second), took.Round(time.Second), rep.Phases)
		timings.set("restore", map[string]any{"backupId": st.backupID, "infraSeconds": infra.Seconds(), "jobSeconds": took.Seconds(), "phasesSeconds": rep.Phases})

		serveStart := time.Now()
		restoreNSScript(t, "serve")
		timings.set("restoreServeSeconds", time.Since(serveStart).Seconds())
		st.rst = openRestored(top, token)
	})
	if st.rst == nil {
		t.Fatal("the restored installation is not up")
	}

	t.Run("R-BAK-029 scenario 1: every file returns bytes of its digest, npm view lists the restored versions", func(t *testing.T) {
		rows := krSQL(t, `SELECT r.key || '|' || i.path || '|' || encode(i.blob_sha256, 'hex') || '|' || (i.trashed_at IS NOT NULL)
			FROM trove.items i JOIN trove.repositories r ON r.id = i.repo_id WHERE i.kind = 'file' AND i.blob_sha256 IS NOT NULL ORDER BY 1`)
		var untrashed, trashed int
		digests := map[string]bool{}
		for _, row := range strings.Split(rows, "\n") {
			f := strings.Split(row, "|")
			if len(f) != 4 {
				continue
			}
			digests[f[2]] = true
			if f[3] == "true" {
				trashed++
				continue
			}
			untrashed++
			r, err := retrying(func() (reply, error) {
				return st.rst.pkg.send(http.MethodGet, "/files/"+e2eProject+"/"+f[0]+"/"+f[1], nil)
			})
			if err != nil || r.Status != 200 {
				t.Errorf("GET %s/%s: %v %s", f[0], f[1], err, r)
				continue
			}
			if sum := sha256.Sum256(r.Body); hex.EncodeToString(sum[:]) != f[2] {
				t.Errorf("GET %s/%s returns bytes of %x, the item records %s", f[0], f[1], sum, f[2])
			}
		}
		t.Logf("%d untrashed and %d trashed file items in the restored database, %d distinct blobs", untrashed, trashed, len(digests))
		if untrashed == 0 {
			t.Fatal("the restored installation holds no file item")
		}
		var all []string
		for d := range digests {
			all = append(all, d)
		}
		sort.Strings(all)
		blobObjectsMatch(t, all)

		// npm view lists exactly the versions of the restored database
		want := map[string][]string{}
		for _, row := range strings.Split(krSQL(t, `SELECT p.name || '|' || pv.version FROM trove.package_versions pv JOIN trove.packages p ON p.id = pv.package_id
			JOIN trove.repositories r ON r.id = p.repo_id WHERE r.key = '`+bakNPM+`' AND pv.trashed_at IS NULL ORDER BY 1`), "\n") {
			if n, v, ok := strings.Cut(row, "|"); ok {
				want[n] = append(want[n], v)
			}
		}
		if len(want) == 0 {
			t.Fatal("the restored database lists no npm version")
		}
		for name, versions := range want {
			sort.Strings(versions)
			if got := restoredNPMVersions(t, token, name); strings.Join(got, ",") != strings.Join(versions, ",") {
				t.Errorf("npm view %s lists %v, the restored database holds %v", name, got, versions)
			}
		}
		t.Logf("npm view agrees with the database for %d packages", len(want))

		// NuGet: the version list of the flat container is the database's
		nug := map[string][]string{}
		for _, row := range strings.Split(krSQL(t, `SELECT p.name || '|' || pv.version FROM trove.package_versions pv JOIN trove.packages p ON p.id = pv.package_id
			JOIN trove.repositories r ON r.id = p.repo_id WHERE r.key = '`+bakNuGet+`' AND pv.trashed_at IS NULL ORDER BY 1`), "\n") {
			if n, v, ok := strings.Cut(row, "|"); ok {
				nug[n] = append(nug[n], v)
			}
		}
		for id, versions := range nug {
			r := st.rst.pkg.get(nugetAddr(bakNuGet) + "/v3/flatcontainer/" + strings.ToLower(id) + "/index.json")
			var l struct {
				Versions []string `json:"versions"`
			}
			if r.Status != 200 || json.Unmarshal(r.Body, &l) != nil {
				t.Errorf("NuGet version list of %s: %s", id, r)
				continue
			}
			sort.Strings(l.Versions)
			sort.Strings(versions)
			if strings.Join(l.Versions, ",") != strings.ToLower(strings.Join(versions, ",")) {
				t.Errorf("NuGet %s lists %v, the restored database holds %v", id, l.Versions, versions)
			}
		}
		t.Logf("NuGet version lists agree with the database for %d packages", len(nug))
	})

	t.Run("R-BAK-019 verify --deep exits 0", func(t *testing.T) {
		zero := 0
		job := "bak-verify-" + st.run
		bakCreateJob(t, bakJob{name: job, backoff: &zero, args: []string{"backup", "verify", st.backupID, "--deep"}})
		end := bakWaitJob(t, cfg.namespace, job, 15*time.Minute)
		if end.Condition != "Complete" || len(end.Exits) != 1 || end.Exits[0] != 0 {
			t.Fatalf("verify --deep: %s, exits %v:\n%s", end.Condition, end.Exits, tail(bakJobLogs(cfg.namespace, job), 2000))
		}
	})

	t.Run("R-BAK-029 scenario 3: two Jobs from the CronJob at once, one succeeds and one exits 3", func(t *testing.T) {
		zero := 0
		a, b := "bak-overlap-a-"+st.run, "bak-overlap-b-"+st.run
		// created together; the slow copy keeps the first in its slot while the second starts
		env := map[string]string{"TROVE_BACKUP_COPY_BYTES_PER_SECOND": "1MiB"}
		bakCreateJob(t, bakJob{name: a, backoff: &zero, env: env})
		bakCreateJob(t, bakJob{name: b, backoff: &zero, env: env})
		ea, eb := bakWaitJob(t, cfg.namespace, a, 10*time.Minute), bakWaitJob(t, cfg.namespace, b, 10*time.Minute)
		t.Logf("Job a: %s %v; Job b: %s %v", ea.Condition, ea.Exits, eb.Condition, eb.Exits)
		ok := (ea.Condition == "Complete" && eb.Condition == "Failed" && len(eb.Exits) == 1 && eb.Exits[0] == 3) ||
			(eb.Condition == "Complete" && ea.Condition == "Failed" && len(ea.Exits) == 1 && ea.Exits[0] == 3)
		if !ok {
			t.Fatalf("want one Complete and one Failed with exit 3; a: %s\n%s\nb: %s\n%s", ea.Condition, tail(bakJobLogs(cfg.namespace, a), 800), eb.Condition, tail(bakJobLogs(cfg.namespace, b), 800))
		}
	})

	t.Run("R-BAK-029 scenario 9: the copy throughput of a full backup per mode", func(t *testing.T) {
		// A full backup into a fresh target per mode (server: CopyObject inside the store; stream: through the pod). The numbers are measured on the
		// data of the drill, not promised (Q-BAK-48).
		zero := 0
		modes := map[string]any{}
		for _, mode := range []struct{ name, setting string }{{"server", "always"}, {"stream", "never"}} {
			job := "bak-copy-" + mode.name + "-" + st.run
			bakCreateJob(t, bakJob{
				name: job, backoff: &zero, args: []string{"backup", "create", "--trigger", "schedule", "--no-prune", "--json"},
				env: map[string]string{"TROVE_BACKUP_TARGET_PREFIX": bakFreshPrefix(st.run, "copy-"+mode.name), "TROVE_BACKUP_COPY_SERVER_SIDE": mode.setting},
			})
			start := time.Now()
			end := bakWaitJob(t, cfg.namespace, job, 20*time.Minute)
			took := time.Since(start)
			logs := bakJobLogs(cfg.namespace, job)
			if end.Condition != "Complete" {
				t.Fatalf("the %s-mode backup ended %s:\n%s", mode.name, end.Condition, tail(logs, 1500))
			}
			var res struct {
				BlobsCopied     int64 `json:"blobsCopied"`
				BytesCopied     int64 `json:"bytesCopied"`
				DurationSeconds int64 `json:"durationSeconds"`
			}
			if m := regexp.MustCompile(`(?s)\{\s*"backupId".*?\n\}`).FindString(logs); m != "" {
				_ = json.Unmarshal([]byte(m), &res)
			}
			rate := 0.0
			if res.DurationSeconds > 0 {
				rate = float64(res.BytesCopied) / float64(res.DurationSeconds) / (1 << 20)
			}
			t.Logf("mode %s: %d blobs, %d bytes copied in a run of %d s (%.1f MiB/s), Job %s", mode.name, res.BlobsCopied, res.BytesCopied, res.DurationSeconds, rate, took.Round(time.Second))
			modes[mode.name] = map[string]any{"blobsCopied": res.BlobsCopied, "bytesCopied": res.BytesCopied, "runSeconds": res.DurationSeconds, "mibPerSecond": rate, "jobSeconds": took.Seconds()}
			if res.BlobsCopied == 0 {
				t.Errorf("the %s-mode backup into a fresh target copied no blob", mode.name)
			}
		}
		timings.set("copyThroughput", modes)
	})
}

var _ = http.StatusOK
