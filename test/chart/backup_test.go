//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/jobs"
)

// backupID is a backup ID of the form trove backup list prints (R-BAK-002).
const backupID = "20261028T020000Z-7f3a"

// restoreSets are the values of the operator's sequence of R-HA-060: every role at zero and the restore Job on.
func restoreSets(extra ...string) []string {
	return append([]string{
		"restore.enabled=true", "restore.backupId=" + backupID,
		"api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0",
	}, extra...)
}

// staticTarget are the values of a backup target with static keys, a login of its own for the backup and a CA of its own.
func staticTarget() []string {
	return []string{
		"config.backup.target.credentials.source=static",
		"secrets.backupTarget.existingSecret=trove-backup-keys",
		"secrets.backupTargetCA.existingSecret=trove-backup-ca",
		"secrets.databaseBackup.existingSecret=trove-db-backup",
		"secrets.databaseMigration.existingSecret=trove-db-owner",
		"config.storage.s3.credentials.source=static",
		"secrets.s3.existingSecret=trove-s3",
	}
}

func cronJobPod(docs []doc) any {
	return at(find(docs, "CronJob", "trove-backup"), "spec", "jobTemplate", "spec", "template", "spec")
}

func restorePod(docs []doc) any {
	return at(find(docs, "Job", "trove-restore"), "spec", "template", "spec")
}

// secretVolumes maps the volume names of a pod spec that come from a Secret to the Secret.
func secretVolumes(pod any) map[string]string {
	out := map[string]string{}
	for _, v := range list(pod, "volumes") {
		if s := str(v, "secret", "secretName"); s != "" {
			out[str(v, "name")] = s
		}
	}
	return out
}

func mountPaths(pod any) []string {
	var out []string
	for _, m := range list(pod, "containers", "0", "volumeMounts") {
		out = append(out, str(m, "mountPath"))
	}
	return out
}

func hasMount(pod any, prefix string) bool {
	for _, p := range mountPaths(pod) {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func envNames(pod any) []string {
	var out []string
	for _, e := range list(pod, "containers", "0", "env") {
		out = append(out, str(e, "name"))
	}
	return out
}

// podTemplateLabels returns the labels a Job's pods carry.
func podTemplateLabels(d doc) map[string]string {
	path := []string{"spec", "template", "metadata", "labels"}
	if str(d, "kind") == "CronJob" {
		path = []string{"spec", "jobTemplate", "spec", "template", "metadata", "labels"}
	}
	out := map[string]string{}
	if m, ok := at(d, path...).(map[string]any); ok {
		for k, v := range m {
			out[k], _ = v.(string)
		}
	}
	return out
}

// selects reports whether a selector with these matchLabels selects a pod with these labels.
func selects(matchLabels any, labels map[string]string) bool {
	m, ok := matchLabels.(map[string]any)
	if !ok || len(m) == 0 {
		return false
	}
	for k, v := range m {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// memoryBytes parses the Mi and Gi quantities the tests use.
func memoryBytes(t *testing.T, q string) int64 {
	t.Helper()
	for suffix, mult := range map[string]int64{"Mi": 1 << 20, "Gi": 1 << 30} {
		if n, ok := strings.CutSuffix(q, suffix); ok {
			v, err := strconv.ParseInt(n, 10, 64)
			if err != nil {
				t.Fatalf("quantity %q: %v", q, err)
			}
			return v * mult
		}
	}
	t.Fatalf("quantity %q is neither Mi nor Gi", q)
	return 0
}

func intList(v any) []int {
	var out []int
	for _, x := range v.([]any) {
		switch n := x.(type) {
		case int:
			out = append(out, n)
		case int64:
			out = append(out, int(n))
		case float64:
			out = append(out, int(n))
		}
	}
	return out
}

// TestR_HA_060_BackupCronJob (R-HA-060, R-BAK-012): the CronJob <fullname>-backup and every row of the table of what the
// commands need from it.
func TestR_HA_060_BackupCronJob(t *testing.T) {
	_, docs := render(t, required())
	cj := find(docs, "CronJob", "trove-backup")
	if cj == nil {
		t.Fatal("no CronJob trove-backup with backup.enabled (the default)")
	}
	pod := cronJobPod(docs)
	spec := at(cj, "spec")
	jobSpec := at(cj, "spec", "jobTemplate", "spec")

	t.Run("the fields of the table of R-HA-060", func(t *testing.T) {
		for path, want := range map[string]string{
			"schedule": "0 2 * * *", "timeZone": "Etc/UTC", "concurrencyPolicy": "Forbid",
		} {
			if got := str(spec, path); got != want {
				t.Errorf("%s = %q, want %q", path, got, want)
			}
		}
		for path, want := range map[string]int{"startingDeadlineSeconds": 3600, "successfulJobsHistoryLimit": 3, "failedJobsHistoryLimit": 3} {
			if got := at(spec, path); got != want {
				t.Errorf("%s = %v, want %d", path, got, want)
			}
		}
		if got := at(spec, "suspend"); got != false {
			t.Errorf("suspend = %v, want false", got)
		}
	})

	t.Run("R-BAK-012 room for retries and no wall-clock limit", func(t *testing.T) {
		if got := at(jobSpec, "backoffLimit"); got != 2 {
			t.Errorf("backoffLimit = %v, want 2", got)
		}
		if _, ok := jobSpec.(map[string]any)["activeDeadlineSeconds"]; ok {
			t.Error("activeDeadlineSeconds is rendered by default; a first backup of many hours would be killed")
		}
		_, docs := render(t, required(), "backup.activeDeadlineSeconds=7200", "backup.backoffLimit=0")
		js := at(find(docs, "CronJob", "trove-backup"), "spec", "jobTemplate", "spec")
		if at(js, "activeDeadlineSeconds") != 7200 || at(js, "backoffLimit") != 0 {
			t.Errorf("activeDeadlineSeconds/backoffLimit = %v/%v, want 7200/0", at(js, "activeDeadlineSeconds"), at(js, "backoffLimit"))
		}
	})

	t.Run("R-BAK-012 retries are decided by exit code: a disruption is ignored first, then 2, 3, 4 and 6 fail the Job", func(t *testing.T) {
		checkPodFailurePolicy(t, at(jobSpec, "podFailurePolicy"), "backup")
		if got := str(pod, "restartPolicy"); got != "Never" {
			t.Errorf("restartPolicy = %q, podFailurePolicy needs Never", got)
		}
	})

	t.Run("R-BAK-012 clean shutdown, scratch, memory and the autoscaler", func(t *testing.T) {
		if got := at(pod, "terminationGracePeriodSeconds"); got != 60 {
			t.Errorf("terminationGracePeriodSeconds = %v, want 60", got)
		}
		if got := str(at(cj, "spec", "jobTemplate", "spec", "template", "metadata"), "annotations", "cluster-autoscaler.kubernetes.io/safe-to-evict"); got != "false" {
			t.Errorf("safe-to-evict = %q, want false", got)
		}
		env := envByName(pod)
		if str(env["TMPDIR"], "value") != "/tmp" {
			t.Errorf("TMPDIR = %v, want /tmp", env["TMPDIR"])
		}
		var tmp doc
		for _, v := range list(pod, "volumes") {
			if str(v, "name") == "tmp" {
				tmp = v.(doc)
			}
		}
		if str(tmp, "emptyDir", "sizeLimit") != "256Mi" {
			t.Errorf("tmp volume = %v, want an emptyDir of 256Mi", tmp)
		}
		if limit := str(pod, "containers", "0", "resources", "limits", "memory"); memoryBytes(t, limit) < 512<<20 {
			t.Errorf("limits.memory = %s, R-BAK-012 asks for at least 512 MiB", limit)
		}
	})

	t.Run("the command and the pod: the trove image, the shared configuration, the contexts of R-HA-033, no listener and no probe", func(t *testing.T) {
		if got := strings.Join(containerArgs(pod), " "); got != "backup create --trigger schedule" {
			t.Errorf("args = %q", got)
		}
		if got := str(pod, "containers", "0", "name"); got != "backup" {
			t.Errorf("container name = %q", got)
		}
		if len(list(pod, "containers")) != 1 {
			t.Errorf("%d containers", len(list(pod, "containers")))
		}
		c := at(pod, "containers", "0")
		for _, k := range []string{"ports", "startupProbe", "livenessProbe", "readinessProbe"} {
			if at(c, k) != nil {
				t.Errorf("the backup container has %s; it has no listener for clients and no probes", k)
			}
		}
		if at(pod, "containers", "0", "securityContext", "readOnlyRootFilesystem") != true || at(pod, "securityContext", "runAsNonRoot") != true {
			t.Error("the pod does not run with the security contexts of R-HA-033")
		}
		if at(pod, "enableServiceLinks") != false {
			t.Error("enableServiceLinks is not false")
		}
		var cfg string
		for _, v := range list(pod, "volumes") {
			if str(v, "name") == "config" {
				cfg = str(v, "configMap", "name")
			}
		}
		if cfg != "trove-config" {
			t.Errorf("config volume = %q, want the shared ConfigMap trove-config", cfg)
		}
		if lbl := podTemplateLabels(cj); lbl["trove/serve"] != "" || lbl["trove/pool"] != "" {
			t.Errorf("the pods carry %v; trove/serve selects the serving pods (Services, Valkey policy) and a Job is none", lbl)
		}
	})

	t.Run("a ServiceAccount of its own without a token", func(t *testing.T) {
		if got := str(pod, "serviceAccountName"); got != "trove-backup" {
			t.Errorf("serviceAccountName = %q, want trove-backup", got)
		}
		if at(pod, "automountServiceAccountToken") != false {
			t.Error("automountServiceAccountToken is not false")
		}
		sa := find(docs, "ServiceAccount", "trove-backup")
		if sa == nil || at(sa, "automountServiceAccountToken") != false {
			t.Errorf("ServiceAccount trove-backup = %v", sa)
		}
		if find(docs, "ServiceAccount", "trove") == nil {
			t.Error("the replicas' ServiceAccount is gone")
		}
		_, docs := render(t, required(), "backup.serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::1:role/b", "backup.serviceAccount.name=backup-identity")
		if got := str(find(docs, "ServiceAccount", "backup-identity"), "metadata", "annotations", "eks.amazonaws.com/role-arn"); got != "arn:aws:iam::1:role/b" {
			t.Errorf("role annotation = %q", got)
		}
		if got := str(cronJobPod(docs), "serviceAccountName"); got != "backup-identity" {
			t.Errorf("serviceAccountName = %q", got)
		}
		_, docs = render(t, required(), "backup.serviceAccount.create=false")
		if find(docs, "ServiceAccount", "trove-backup") != nil || str(cronJobPod(docs), "serviceAccountName") != "default" {
			t.Error("create=false renders the account or does not fall back to default")
		}
	})

	t.Run("the values: schedule, time zone, adoption, suspension and the switch", func(t *testing.T) {
		_, docs := render(t, required(), "backup.schedule=@hourly", "backup.timeZone=Europe/Berlin", "backup.adoptTarget=true", "backup.suspend=true", "backup.startingDeadlineSeconds=60")
		cj := find(docs, "CronJob", "trove-backup")
		if str(cj, "spec", "schedule") != "@hourly" || str(cj, "spec", "timeZone") != "Europe/Berlin" || at(cj, "spec", "suspend") != true || at(cj, "spec", "startingDeadlineSeconds") != 60 {
			t.Errorf("spec = %v", at(cj, "spec"))
		}
		if got := strings.Join(containerArgs(cronJobPod(docs)), " "); got != "backup create --trigger schedule --adopt-target" {
			t.Errorf("args = %q", got)
		}
		_, docs = render(t, required(), "backup.enabled=false")
		for _, k := range []string{"CronJob", "NetworkPolicy/trove-backup", "ServiceAccount/trove-backup"} {
			kind, name, _ := strings.Cut(k, "/")
			if name == "" {
				if len(ofKind(docs, kind)) != 0 {
					t.Errorf("%s rendered with backup.enabled: false", kind)
				}
			} else if find(docs, kind, name) != nil {
				t.Errorf("%s rendered with backup.enabled: false", k)
			}
		}
		cfg := configOf(t, docs)
		if at(cfg, "backup", "manual_run") != nil {
			t.Error("backup.manual_run is rendered with backup.enabled: false; the UI would show a command for a CronJob that does not exist")
		}
	})

	t.Run("every value set renders the CronJob", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			if find(docs, "CronJob", "trove-backup") == nil {
				t.Error("no CronJob")
			}
		})
	})
}

// checkPodFailurePolicy asserts the rules of R-BAK-012: Ignore on DisruptionTarget first, then FailJob on 2, 3, 4, 6.
func checkPodFailurePolicy(t *testing.T, policy any, container string) {
	t.Helper()
	rules := list(policy, "rules")
	if len(rules) != 2 {
		t.Fatalf("podFailurePolicy rules = %v, want two", rules)
	}
	if str(rules[0], "action") != "Ignore" || str(rules[0], "onPodConditions", "0", "type") != "DisruptionTarget" {
		t.Errorf("first rule = %v, want Ignore on DisruptionTarget (an eviction must not consume backoffLimit)", rules[0])
	}
	if str(rules[1], "action") != "FailJob" || str(rules[1], "onExitCodes", "operator") != "In" || str(rules[1], "onExitCodes", "containerName") != container {
		t.Errorf("second rule = %v, want FailJob on exit codes of container %s", rules[1], container)
	}
	got := intList(at(rules[1], "onExitCodes", "values"))
	if len(got) != 4 || got[0] != 2 || got[1] != 3 || got[2] != 4 || got[3] != 6 {
		t.Errorf("exit codes = %v, want [2 3 4 6]; 75 and 1 are retried", got)
	}
}

// TestR_HA_060_RestoreJob (R-HA-060, R-BAK-012, Q-HA-18): the restore Job, no migration Job, a suspended CronJob.
func TestR_HA_060_RestoreJob(t *testing.T) {
	t.Run("nothing is rendered without restore.enabled", func(t *testing.T) {
		_, docs := render(t, required())
		if find(docs, "Job", "trove-restore") != nil || find(docs, "ServiceAccount", "trove-restore") != nil || find(docs, "NetworkPolicy", "trove-restore") != nil {
			t.Error("restore objects rendered with restore.enabled: false")
		}
	})

	_, docs := render(t, required(), restoreSets()...)
	job := find(docs, "Job", "trove-restore")
	if job == nil {
		t.Fatal("no Job trove-restore")
	}
	pod := restorePod(docs)

	t.Run("an ordinary Job and not a hook, with the failure handling of the backup Job", func(t *testing.T) {
		if at(job, "metadata", "annotations") != nil && str(job, "metadata", "annotations", "helm.sh/hook") != "" {
			t.Error("the restore Job is a Helm hook; Helm would wait for it")
		}
		if got := at(job, "spec", "backoffLimit"); got != 2 {
			t.Errorf("backoffLimit = %v, want 2", got)
		}
		if _, ok := at(job, "spec").(map[string]any)["activeDeadlineSeconds"]; ok {
			t.Error("activeDeadlineSeconds is rendered by default")
		}
		checkPodFailurePolicy(t, at(job, "spec", "podFailurePolicy"), "restore")
		if str(pod, "restartPolicy") != "Never" {
			t.Errorf("restartPolicy = %q", str(pod, "restartPolicy"))
		}
		if got := str(at(job, "spec", "template", "metadata"), "annotations", "cluster-autoscaler.kubernetes.io/safe-to-evict"); got != "false" {
			t.Errorf("safe-to-evict = %q", got)
		}
		if got := at(pod, "terminationGracePeriodSeconds"); got != 60 {
			t.Errorf("terminationGracePeriodSeconds = %v, want 60", got)
		}
		_, docs := render(t, required(), restoreSets("restore.backoffLimit=0", "restore.activeDeadlineSeconds=86400", "restore.terminationGracePeriodSeconds=120")...)
		j := find(docs, "Job", "trove-restore")
		if at(j, "spec", "backoffLimit") != 0 || at(j, "spec", "activeDeadlineSeconds") != 86400 || at(restorePod(docs), "terminationGracePeriodSeconds") != 120 {
			t.Errorf("spec = %v", at(j, "spec"))
		}
	})

	t.Run("the command resumes by itself: restore <id>, the journal flag and the extra arguments", func(t *testing.T) {
		if got := strings.Join(containerArgs(pod), " "); got != "backup restore "+backupID {
			t.Errorf("args = %q", got)
		}
		_, docs := render(t, required(), restoreSets("restore.skipRevocationJournal=true", "restore.extraArgs={--check}")...)
		if got := strings.Join(containerArgs(restorePod(docs)), " "); got != "backup restore "+backupID+" --skip-revocation-journal --check" {
			t.Errorf("args = %q", got)
		}
	})

	t.Run("a ServiceAccount of its own, neither the backup's nor the replicas'", func(t *testing.T) {
		if got := str(pod, "serviceAccountName"); got != "trove-restore" {
			t.Errorf("serviceAccountName = %q", got)
		}
		if find(docs, "ServiceAccount", "trove-restore") == nil || at(pod, "automountServiceAccountToken") != false {
			t.Error("no ServiceAccount trove-restore, or its token is mounted")
		}
		_, d2 := render(t, required(), restoreSets("restore.serviceAccount.name=restore-identity")...)
		if str(restorePod(d2), "serviceAccountName") != "restore-identity" {
			t.Error("restore.serviceAccount.name is ignored")
		}
	})

	t.Run("no migration Job: the restore applies the migrations itself", func(t *testing.T) {
		for _, c := range [][2]string{{"Job", "trove-migrate"}, {"ConfigMap", "trove-migrate"}, {"ServiceAccount", "trove-migrate"}} {
			if find(docs, c[0], c[1]) != nil {
				t.Errorf("%s %s rendered while restore.enabled", c[0], c[1])
			}
		}
		if hook := str(find(docs, "NetworkPolicy", "trove-migrate"), "metadata", "annotations", "helm.sh/hook"); hook != "" {
			t.Errorf("the migration policy is still a hook (%s) without the Job", hook)
		}
	})

	t.Run("the backup CronJob is suspended whatever backup.suspend says", func(t *testing.T) {
		if at(find(docs, "CronJob", "trove-backup"), "spec", "suspend") != true {
			t.Error("the CronJob is not suspended while restore.enabled")
		}
		_, d2 := render(t, required(), restoreSets("backup.suspend=false")...)
		if at(find(d2, "CronJob", "trove-backup"), "spec", "suspend") != true {
			t.Error("backup.suspend: false wins over restore.enabled")
		}
		_, d3 := render(t, required())
		if at(find(d3, "CronJob", "trove-backup"), "spec", "suspend") != false {
			t.Error("the CronJob is suspended without restore.enabled")
		}
	})

	t.Run("the roles are at zero and keep their Deployments", func(t *testing.T) {
		for _, n := range []string{"trove-api", "trove-packages", "trove-worker"} {
			if got := at(find(docs, "Deployment", n), "spec", "replicas"); got != 0 {
				t.Errorf("%s replicas = %v, want 0", n, got)
			}
		}
	})

	t.Run("it renders also with highAvailability.enforce set, as the sequence of R-HA-060 sets it to false", func(t *testing.T) {
		render(t, required(), restoreSets("highAvailability.enforce=false")...)
		render(t, required(), restoreSets("highAvailability.enforce=true")...)
	})

	t.Run("the sequence's other rules still hold while it restores: a pool, an endpoint pool and the bridge at zero", func(t *testing.T) {
		render(t, []string{filepath.Join(chartDir, "ci", "roles-pools.yaml")}, restoreSets("packages.autoscaling.enabled=false",
			"api.pools[0].replicaCount=0", "packages.pools[0].replicaCount=0", "worker.pools[0].replicaCount=0", "worker.pools[1].replicaCount=0",
			"endpoints.customers.pool.enabled=false")...)
	})
}

// TestR_HA_029_BackupValidation (R-HA-029): the rows of the table that name the backup and the restore.
func TestR_HA_029_BackupValidation(t *testing.T) {
	noBucket := func() []string {
		raw, err := os.ReadFile(filepath.Join(chartDir, "ci", "required-only.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var v doc
		if err := yaml.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		delete(at(v, "config").(map[string]any), "backup")
		out, err := yaml.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(t.TempDir(), "no-bucket.yaml")
		if err := os.WriteFile(f, out, 0o600); err != nil {
			t.Fatal(err)
		}
		return []string{f}
	}

	t.Run("backup.enabled without config.backup.target.bucket names both", func(t *testing.T) {
		renderFails(t, noBucket(), nil, "config.backup.target.bucket", "backup.enabled")
	})
	t.Run("backup.enabled: false needs no bucket", func(t *testing.T) {
		_, docs := render(t, noBucket(), "backup.enabled=false")
		if find(docs, "CronJob", "trove-backup") != nil {
			t.Error("CronJob rendered with backup.enabled: false")
		}
	})
	t.Run("restore.enabled needs the bucket the backup is read from", func(t *testing.T) {
		renderFails(t, noBucket(), restoreSets("backup.enabled=false"), "config.backup.target.bucket", "restore.enabled")
	})
	t.Run("restore.enabled without restore.backupId", func(t *testing.T) {
		renderFails(t, required(), []string{"restore.enabled=true", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0"}, "restore.backupId")
	})
	t.Run("restore.enabled with a replica of a role names restore.enabled and the value", func(t *testing.T) {
		for _, v := range []string{"api", "packages", "worker"} {
			sets := restoreSets(v + ".replicaCount=2")
			renderFails(t, required(), sets, "restore.enabled", v+".replicaCount")
		}
	})
	t.Run("restore.enabled with an autoscaler of a role names restore.enabled and the value", func(t *testing.T) {
		renderFails(t, required(), restoreSets("packages.autoscaling.enabled=true"), "restore.enabled", "packages.autoscaling.enabled")
	})
	t.Run("restore.enabled with a pool that has replicas names the pool", func(t *testing.T) {
		renderFails(t, []string{filepath.Join(chartDir, "ci", "roles-pools.yaml")}, restoreSets("packages.autoscaling.enabled=false"), "restore.enabled", ".pools[")
	})
	t.Run("restore.enabled with the bridge Deployment running names it", func(t *testing.T) {
		renderFails(t, required(), restoreSets("web.enabled=true", "web.replicaCount=2"), "restore.enabled", "web.replicaCount")
	})

	t.Run("the target must not be the blob store's bucket on the same endpoint", func(t *testing.T) {
		same := []string{"config.storage.s3.bucket=trove-blobs", "config.backup.target.bucket=trove-blobs"}
		renderFails(t, required(), same, "config.backup.target.bucket", "config.storage.s3.bucket", "allow_same_bucket")
		renderFails(t, required(), append([]string{"config.backup.target.prefix=trove/backups", "config.backup.target.allow_same_bucket=true"}, same...),
			"config.backup.target.prefix", "config.storage.s3.prefix", "trove/backups")
		renderFails(t, required(), append([]string{"config.backup.target.prefix=trove", "config.storage.s3.prefix=trove/blobs", "config.backup.target.allow_same_bucket=true"}, same...),
			"config.backup.target.prefix", "config.storage.s3.prefix")
		// Prefixes that do not contain each other pass with the flag, and another endpoint or bucket needs nothing.
		render(t, required(), append([]string{"config.backup.target.prefix=backups", "config.storage.s3.prefix=blobs", "config.backup.target.allow_same_bucket=true"}, same...)...)
		render(t, required(), "config.storage.s3.bucket=trove-blobs", "config.backup.target.bucket=trove-backups")
		render(t, required(), append([]string{"config.backup.target.endpoint=https://backups.example.com"}, same...)...)
	})
	t.Run("the endpoints are compared the way the binary compares them: no scheme means https", func(t *testing.T) {
		same := []string{"config.storage.s3.bucket=trove-blobs", "config.backup.target.bucket=trove-blobs"}
		renderFails(t, required(), append([]string{"config.storage.s3.endpoint=s3.example.com", "config.backup.target.endpoint=https://s3.example.com"}, same...), "config.backup.target.bucket")
		render(t, required(), append([]string{"config.storage.s3.endpoint=s3.example.com", "config.backup.target.endpoint=http://s3.example.com"}, same...)...)
	})

	t.Run("static credentials of the target need secrets.backupTarget.existingSecret", func(t *testing.T) {
		renderFails(t, required(), []string{"config.backup.target.credentials.source=static"}, "config.backup.target.credentials.source", "secrets.backupTarget.existingSecret")
		render(t, required(), "config.backup.target.credentials.source=static", "secrets.backupTarget.existingSecret=trove-backup-keys")
	})

	t.Run("a key the chart manages is refused inside config", func(t *testing.T) {
		for _, k := range []string{
			"backup.manual_run.namespace", "backup.manual_run.cronjob", "backup.target.ca_file",
			"backup.target.credentials.access_key_id_file", "backup.target.credentials.secret_access_key_file", "backup.target.credentials.session_token_file",
		} {
			named := "config." + k
			if strings.HasPrefix(k, "backup.manual_run") {
				named = "config.backup.manual_run" // the subtree is the managed key
			}
			renderFails(t, required(), []string{"config." + k + "=x"}, named, "managed by the chart")
		}
	})

	t.Run("the schedule is checked against the dialect of trove's cron parser", func(t *testing.T) {
		cases := []struct {
			expr string
			ok   bool
		}{
			{"0 2 * * *", true}, {"*/15 * * * *", true}, {"30 3 * * MON-FRI", true}, {"0 2 * * */7", true}, {"0 2 1 JAN *", true},
			{"@hourly", true}, {"@daily", true}, {"@WEEKLY", true},
			{"0 0 2 * * *", false}, {"0 0 2 * * * 2026", false}, {"0 2 * *", false}, {"0 2 * * 7", false}, {"0 2 * * 5-7", false},
			{"0 2 ? * *", false}, {"0 2 L * *", false}, {"0 2 15W * *", false}, {"0 2 * * 5#2", false},
			{"@every 1h", false}, {"@reboot", false}, {"@sometimes", false},
			{"TZ=Europe/Berlin 0 2 * * *", false}, {"CRON_TZ=UTC 0 2 * * *", false},
		}
		for _, c := range cases {
			t.Run(c.expr, func(t *testing.T) {
				// The chart and the parser the tasks of trove use agree: the table is the parser's answer.
				_, err := jobs.ParseSchedule(c.expr)
				if (err == nil) != c.ok {
					t.Fatalf("jobs.ParseSchedule(%q) = %v, the table says ok=%v", c.expr, err, c.ok)
				}
				f := filepath.Join(t.TempDir(), "schedule.yaml")
				body, _ := yaml.Marshal(doc{"backup": doc{"schedule": c.expr}})
				if err := os.WriteFile(f, body, 0o600); err != nil {
					t.Fatal(err)
				}
				if c.ok {
					_, docs := render(t, append(required(), f))
					if got := str(find(docs, "CronJob", "trove-backup"), "spec", "schedule"); got != c.expr {
						t.Errorf("schedule = %q", got)
					}
					return
				}
				renderFails(t, append(required(), f), nil, "backup.schedule", "not accepted")
			})
		}
	})
	t.Run("an empty schedule is refused by the schema", func(t *testing.T) {
		renderFails(t, required(), []string{"backup.schedule="}, "/backup/schedule")
	})
}

// TestR_HA_031_BackupValuesSchema (R-HA-031): values.schema.json declares the backup and restore values and refuses what is
// not one. The comparison of values.yaml with the specification's block is TestR_HA_031_ValuesReference.
func TestR_HA_031_BackupValuesSchema(t *testing.T) {
	for _, c := range []struct {
		name string
		set  string
		want string
	}{
		{"a typo in backup", "backup.shedule=x", "shedule"},
		{"a typo in restore", "restore.backup_id=x", "backup_id"},
		{"a grace period below 30 seconds of the backup", "backup.terminationGracePeriodSeconds=10", "terminationGracePeriodSeconds"},
		{"a grace period below 30 seconds of the restore", "restore.terminationGracePeriodSeconds=10", "terminationGracePeriodSeconds"},
		{"a negative backoff limit", "backup.backoffLimit=-1", "backoffLimit"},
		{"a typo in the secrets of the target", "secrets.backupTarget.existingsecret=x", "existingsecret"},
		{"a typo in the CA of the target", "secrets.backupTargetCA.file=x", "file"},
		{"an unknown credentials source of the target", "config.backup.target.credentials.source=file", "source"},
	} {
		t.Run(c.name, func(t *testing.T) { renderFails(t, required(), []string{c.set}, c.want) })
	}
	t.Run("the values of the specification render", func(t *testing.T) {
		_, docs := render(t, required(), append(staticTarget(),
			"backup.schedule=0 */6 * * *", "backup.tmpSizeLimit=512Mi", "backup.resources.limits.memory=2Gi", "restore.resources.limits.memory=4Gi",
			"backup.nodeSelector.disk=ssd", "backup.priorityClassName=low", "backup.podLabels.team=ops", "backup.podAnnotations.note=x", "backup.extraEnv[0].name=HTTPS_PROXY", "backup.extraEnv[0].value=http://proxy:3128")...)
		pod := cronJobPod(docs)
		if str(pod, "nodeSelector", "disk") != "ssd" || str(pod, "priorityClassName") != "low" || str(pod, "containers", "0", "resources", "limits", "memory") != "2Gi" {
			t.Errorf("placement or resources not passed: %v", pod)
		}
		if _, ok := envByName(pod)["HTTPS_PROXY"]; !ok {
			t.Error("backup.extraEnv is not rendered")
		}
		lbl := podTemplateLabels(find(docs, "CronJob", "trove-backup"))
		if lbl["team"] != "ops" {
			t.Errorf("labels = %v", lbl)
		}
	})
}

// TestR_HA_030_BackupSecrets (R-HA-030, R-HA-060, R-BAK-011, R-BAK-012): which Secret reaches which pod. The target's
// credentials reach the backup and restore pods and no replica; Valkey reaches neither Job; the key ring reaches the
// replicas and the restore Job; each Job gets the database login of its role.
func TestR_HA_030_BackupSecrets(t *testing.T) {
	_, docs := render(t, required(), staticTarget()...)
	backupPod := cronJobPod(docs)
	_, rdocs := render(t, required(), append(staticTarget(), restoreSets()...)...)
	restore := restorePod(rdocs)

	has := func(v map[string]string, secret string) bool {
		for _, s := range v {
			if s == secret {
				return true
			}
		}
		return false
	}

	t.Run("the backup pod: the backup login, the blob store's read credential and the target", func(t *testing.T) {
		v := secretVolumes(backupPod)
		for _, want := range []string{"trove-db-backup", "trove-s3", "trove-backup-keys", "trove-backup-ca"} {
			if !has(v, want) {
				t.Errorf("the backup pod mounts %v, want %s among them", v, want)
			}
		}
		for _, no := range []string{"trove-db-app", "trove-db-owner", "trove-valkey", "trove-encryption-keys", "trove-tls"} {
			if has(v, no) {
				t.Errorf("the backup pod mounts %s", no)
			}
		}
		if !hasMount(backupPod, "/var/run/secrets/trove/backup-target") || !hasMount(backupPod, "/var/run/secrets/trove/backup-target-ca") {
			t.Errorf("mounts = %v", mountPaths(backupPod))
		}
		for _, p := range []string{"valkey", "encryption", "oidc", "break-glass", "tls", "metrics"} {
			if hasMount(backupPod, "/var/run/secrets/trove/"+p) {
				t.Errorf("the backup pod mounts %s", p)
			}
		}
	})

	t.Run("the backup pod without a login of its own uses the runtime login", func(t *testing.T) {
		_, d := render(t, required())
		if v := secretVolumes(cronJobPod(d)); !has(v, "trove-db-app") {
			t.Errorf("volumes = %v, want trove-db-app", v)
		}
	})

	t.Run("the restore Job: the migration login, the blob store, the target and the key ring; no Valkey", func(t *testing.T) {
		v := secretVolumes(restore)
		for _, want := range []string{"trove-db-owner", "trove-s3", "trove-backup-keys", "trove-backup-ca", "trove-encryption-keys"} {
			if !has(v, want) {
				t.Errorf("the restore Job mounts %v, want %s among them", v, want)
			}
		}
		for _, no := range []string{"trove-db-app", "trove-db-backup", "trove-valkey", "trove-tls"} {
			if has(v, no) {
				t.Errorf("the restore Job mounts %s", no)
			}
		}
		if hasMount(restore, "/var/run/secrets/trove/valkey") {
			t.Error("the restore Job mounts a Valkey Secret")
		}
		_, d := render(t, required(), restoreSets()...)
		if v := secretVolumes(restorePod(d)); !has(v, "trove-db-app") || has(v, "trove-encryption-keys") == false {
			t.Errorf("without a migration login the restore Job uses the runtime login, and always the key ring: %v", v)
		}
	})

	t.Run("no replica and no migration Job mounts the target's credentials or CA", func(t *testing.T) {
		_, d := render(t, required(), staticTarget()...)
		for name, pod := range podSpecs(d) {
			v := secretVolumes(pod)
			if has(v, "trove-backup-keys") || has(v, "trove-backup-ca") || hasMount(pod, "/var/run/secrets/trove/backup-target") {
				t.Errorf("%s mounts the target's credentials or CA: %v", name, mountPaths(pod))
			}
			if name == "trove-migrate" && (has(v, "trove-db-backup") || has(v, "trove-s3")) {
				t.Errorf("the migration Job mounts %v", v)
			}
			if has(v, "trove-db-backup") {
				t.Errorf("%s mounts the backup's database login", name)
			}
		}
	})

	t.Run("no container of a replica carries the target's credentials in its environment, on any value set", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			for name, pod := range podSpecs(docs) {
				for _, e := range list(pod, "containers", "0", "env") {
					if strings.Contains(strings.ToLower(str(e, "name")), "backup") || strings.Contains(str(e, "valueFrom", "secretKeyRef", "name"), "backup") {
						t.Errorf("%s has the variable %v", name, e)
					}
				}
			}
		})
	})

	t.Run("the Jobs mount no secret as an environment variable either", func(t *testing.T) {
		for _, pod := range []any{backupPod, restore} {
			for _, e := range list(pod, "containers", "0", "env") {
				if at(e, "valueFrom", "secretKeyRef") != nil {
					t.Errorf("a Job reads a Secret into the variable %v", e)
				}
			}
			if at(pod, "containers", "0", "envFrom") != nil {
				t.Error("a Job has envFrom")
			}
		}
	})

	t.Run("the shared configuration names the files of the target, and the replicas have nothing under those paths", func(t *testing.T) {
		cfg := configOf(t, docs)
		creds := at(cfg, "backup", "target", "credentials")
		if str(creds, "access_key_id_file") != "/var/run/secrets/trove/backup-target/access-key-id" || str(creds, "secret_access_key_file") != "/var/run/secrets/trove/backup-target/secret-access-key" {
			t.Errorf("credentials = %v", creds)
		}
		if at(creds, "session_token_file") != nil {
			t.Errorf("session_token_file is set without secrets.backupTarget.sessionTokenKey: %v", creds)
		}
		if str(cfg, "backup", "target", "ca_file") != "/var/run/secrets/trove/backup-target-ca/ca.crt" {
			t.Errorf("ca_file = %v", at(cfg, "backup", "target", "ca_file"))
		}
		if str(cfg, "backup", "manual_run", "namespace") != "default" || str(cfg, "backup", "manual_run", "cronjob") != "trove-backup" {
			t.Errorf("manual_run = %v", at(cfg, "backup", "manual_run"))
		}
		_, d := render(t, required(), append(staticTarget(), "secrets.backupTarget.sessionTokenKey=session")...)
		if got := str(configOf(t, d), "backup", "target", "credentials", "session_token_file"); got != "/var/run/secrets/trove/backup-target/session-token" {
			t.Errorf("session_token_file = %q", got)
		}
		for _, v := range list(cronJobPod(d), "volumes") {
			if str(v, "secret", "secretName") == "trove-backup-keys" {
				items := list(v, "secret", "items")
				if len(items) != 3 {
					t.Errorf("items = %v, want the two keys and the session token", items)
				}
			}
		}
	})

	t.Run("workload identity renders no credential file and no mount", func(t *testing.T) {
		_, d := render(t, required())
		cfg := configOf(t, d)
		for _, k := range []string{"access_key_id_file", "secret_access_key_file", "session_token_file"} {
			if at(cfg, "backup", "target", "credentials", k) != nil {
				t.Errorf("target = %v", at(cfg, "backup", "target"))
			}
		}
		if at(cfg, "backup", "target", "ca_file") != nil {
			t.Errorf("target = %v", at(cfg, "backup", "target"))
		}
		if hasMount(cronJobPod(d), "/var/run/secrets/trove/backup-target") {
			t.Error("the backup pod mounts the target's Secret with source default")
		}
	})

	t.Run("every Secret is a directory mount without subPath, read-only, so a rotated Secret reaches the pod", func(t *testing.T) {
		for _, pod := range []any{backupPod, restore} {
			for _, m := range list(pod, "containers", "0", "volumeMounts") {
				if strings.HasPrefix(str(m, "mountPath"), "/var/run/secrets/") && (at(m, "subPath") != nil || at(m, "readOnly") != true) {
					t.Errorf("mount %v", m)
				}
			}
		}
	})

	t.Run("the environment of the Jobs is their own: the global extraEnv does not reach them (Q-HA-149)", func(t *testing.T) {
		_, d := render(t, required(), "extraEnv[0].name=FOR_REPLICAS", "extraEnv[0].value=1", "backup.extraEnv[0].name=FOR_BACKUP", "backup.extraEnv[0].value=1")
		if _, ok := envByName(cronJobPod(d))["FOR_REPLICAS"]; ok {
			t.Error("the global extraEnv reaches the backup pod")
		}
		if _, ok := envByName(cronJobPod(d))["FOR_BACKUP"]; !ok {
			t.Error("backup.extraEnv is missing")
		}
		if _, ok := envByName(at(find(d, "Deployment", "trove-api"), "spec", "template", "spec"))["FOR_REPLICAS"]; !ok {
			t.Error("the global extraEnv is gone from the replicas")
		}
	})

	t.Run("the pods know their name for the run record", func(t *testing.T) {
		for _, pod := range []any{backupPod, restore} {
			names := envNames(pod)
			for _, want := range []string{"TROVE_POD_NAME", "TROVE_POD_NAMESPACE", "TROVE_NODE_NAME", "TMPDIR"} {
				found := false
				for _, n := range names {
					found = found || n == want
				}
				if !found {
					t.Errorf("env %v lacks %s", names, want)
				}
			}
		}
	})
}

// TestR_HA_059_NoDisruptionBudgetForTheJobs (R-HA-059, R-HA-060, R-BAK-012): no PodDisruptionBudget selects a backup or restore
// pod, so a node drain never waits for them. The drain itself is the end-to-end suite's.
func TestR_HA_059_NoDisruptionBudgetForTheJobs(t *testing.T) {
	for _, sets := range [][]string{nil, restoreSets()} {
		_, docs := render(t, required(), sets...)
		pdbs := ofKind(docs, "PodDisruptionBudget")
		if len(pdbs) == 0 {
			t.Fatal("no PodDisruptionBudget rendered; the test would pass vacuously")
		}
		for _, d := range []doc{find(docs, "CronJob", "trove-backup"), find(docs, "Job", "trove-restore")} {
			if d == nil {
				continue
			}
			lbl := podTemplateLabels(d)
			if len(lbl) == 0 {
				t.Fatalf("%s has no pod labels", str(d, "metadata", "name"))
			}
			for _, p := range pdbs {
				if selects(at(p, "spec", "selector", "matchLabels"), lbl) {
					t.Errorf("%s selects the pods of %s", str(p, "metadata", "name"), str(d, "metadata", "name"))
				}
			}
		}
	}
}

// TestR_HA_042_JobsAdmitNothing (R-HA-042, ADR-154): the pods of the backup and the restore Job get a policy with
// ingress rules only and no rule: they open no port. No egress rule is rendered anywhere.
func TestR_HA_042_JobsAdmitNothing(t *testing.T) {
	check := func(t *testing.T, docs []doc, name string, d doc) {
		t.Helper()
		np := find(docs, "NetworkPolicy", name)
		if np == nil {
			t.Fatalf("no NetworkPolicy %s", name)
		}
		if types := list(np, "spec", "policyTypes"); len(types) != 1 || types[0] != "Ingress" {
			t.Errorf("policyTypes = %v, want [Ingress]", types)
		}
		if rules, ok := at(np, "spec", "ingress").([]any); !ok || len(rules) != 0 {
			t.Errorf("ingress = %v, want an explicit empty list: the Job opens no port", at(np, "spec", "ingress"))
		}
		if !selects(at(np, "spec", "podSelector", "matchLabels"), podTemplateLabels(d)) {
			t.Errorf("%s does not select the pods of the Job: %v", name, at(np, "spec", "podSelector"))
		}
		if at(np, "spec", "egress") != nil {
			t.Error("an egress rule is rendered")
		}
	}
	_, docs := render(t, required())
	check(t, docs, "trove-backup", find(docs, "CronJob", "trove-backup"))
	if find(docs, "NetworkPolicy", "trove-restore") != nil {
		t.Error("a policy for the restore Job without the Job")
	}
	_, rdocs := render(t, required(), restoreSets()...)
	check(t, rdocs, "trove-backup", find(rdocs, "CronJob", "trove-backup"))
	check(t, rdocs, "trove-restore", find(rdocs, "Job", "trove-restore"))

	t.Run("none of the other policies admits a Job's pods", func(t *testing.T) {
		for _, np := range ofKind(rdocs, "NetworkPolicy") {
			name := str(np, "metadata", "name")
			if name == "trove-backup" || name == "trove-restore" {
				continue
			}
			for _, d := range []doc{find(rdocs, "CronJob", "trove-backup"), find(rdocs, "Job", "trove-restore")} {
				if selects(at(np, "spec", "podSelector", "matchLabels"), podTemplateLabels(d)) {
					t.Errorf("%s selects the pods of %s", name, str(d, "metadata", "name"))
				}
			}
		}
	})
	t.Run("networkPolicy.enabled false renders none of them", func(t *testing.T) {
		_, d := render(t, required(), "networkPolicy.enabled=false")
		if n := len(ofKind(d, "NetworkPolicy")); n != 0 {
			t.Errorf("%d policies", n)
		}
	})
	t.Run("no value set renders an egress rule", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			for _, np := range ofKind(docs, "NetworkPolicy") {
				if at(np, "spec", "egress") != nil {
					t.Errorf("%s has egress", str(np, "metadata", "name"))
				}
			}
		})
	})
}

// TestR_BAK_012_ChartObjects (R-BAK-012): the acceptance criterion of the render, and the manual run: the Job created from the
// CronJob copies its arguments, so the run records the trigger of the schedule, and its pod name starts with the Job's.
func TestR_BAK_012_ChartObjects(t *testing.T) {
	t.Run("backup.enabled without a target bucket fails and names config.backup.target.bucket and backup.enabled", func(t *testing.T) {
		renderFails(t, required(), []string{"config.backup.target.bucket="}, "config.backup.target.bucket", "backup.enabled")
	})
	t.Run("the manual-run command of the UI names an object that exists", func(t *testing.T) {
		_, docs := renderRelease(t, "prod", required())
		var cfg doc
		if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "prod-trove-config"), "data", "trove.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		name := str(cfg, "backup", "manual_run", "cronjob")
		if name != "prod-trove-backup" || find(docs, "CronJob", name) == nil {
			t.Errorf("manual_run.cronjob = %q; the CronJob is %v", name, find(docs, "CronJob", name) != nil)
		}
		if str(cfg, "backup", "manual_run", "namespace") != "default" {
			t.Errorf("manual_run.namespace = %q", str(cfg, "backup", "manual_run", "namespace"))
		}
	})
	t.Run("the CronJob name stays within the 52 characters Kubernetes allows a CronJob, so the Jobs it creates get a valid name", func(t *testing.T) {
		render(t, required(), "fullnameOverride="+strings.Repeat("a", 45))
		renderFails(t, required(), []string{"fullnameOverride=" + strings.Repeat("a", 46)}, "fullnameOverride", "52")
		render(t, required(), "fullnameOverride="+strings.Repeat("a", 46), "backup.enabled=false")
	})
	t.Run("the memory limits of both Jobs are at least 512 MiB", func(t *testing.T) {
		_, docs := render(t, required(), restoreSets()...)
		for _, pod := range []any{cronJobPod(docs), restorePod(docs)} {
			if limit := str(pod, "containers", "0", "resources", "limits", "memory"); memoryBytes(t, limit) < 512<<20 {
				t.Errorf("limits.memory = %s", limit)
			}
		}
	})
}
