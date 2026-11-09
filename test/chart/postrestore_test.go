//go:build chart

package chart_test

import (
	"path/filepath"
	"strings"
	"testing"
)

const restoredTo = "2026-10-28T09:15:00Z"

func postRestoreSets(extra ...string) []string {
	return append([]string{
		"postRestore.enabled=true", "postRestore.restoredTo=" + restoredTo,
		"api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0",
	}, extra...)
}

// postRestoreJob finds the hook Job, named after the restore point (Q-HA-161).
func postRestoreJob(docs []doc) doc {
	for _, d := range ofKind(docs, "Job") {
		if strings.HasPrefix(str(d, "metadata", "name"), "trove-post-restore-") {
			return d
		}
	}
	return nil
}

func postRestorePod(docs []doc) any {
	return at(postRestoreJob(docs), "spec", "template", "spec")
}

// TestR_HA_025_PostRestoreJob (R-HA-025 step 4, R-HA-030, R-HA-042): the hook Job that runs trove admin post-restore.
func TestR_HA_025_PostRestoreJob(t *testing.T) {
	t.Run("nothing is rendered without postRestore.enabled", func(t *testing.T) {
		_, docs := render(t, required())
		for _, c := range [][2]string{{"ServiceAccount", "trove-post-restore"}, {"NetworkPolicy", "trove-post-restore"}, {"ConfigMap", "trove-post-restore"}} {
			if find(docs, c[0], c[1]) != nil {
				t.Errorf("%s %s rendered with postRestore.enabled: false", c[0], c[1])
			}
		}
	})

	_, docs := render(t, required(), postRestoreSets()...)
	job := postRestoreJob(docs)
	if job == nil {
		t.Fatal("no Job trove-post-restore")
	}
	pod := postRestorePod(docs)

	t.Run("a post-install and post-upgrade hook that Helm waits for", func(t *testing.T) {
		if got := str(job, "metadata", "annotations", "helm.sh/hook"); got != "post-install,post-upgrade" {
			t.Errorf("hook = %q", got)
		}
		if got := str(job, "metadata", "annotations", "helm.sh/hook-delete-policy"); got != "before-hook-creation" {
			t.Errorf("hook-delete-policy = %q", got)
		}
		checkPodFailurePolicy(t, at(job, "spec", "podFailurePolicy"), "post-restore")
		if str(pod, "restartPolicy") != "Never" {
			t.Errorf("restartPolicy = %q", str(pod, "restartPolicy"))
		}
	})

	t.Run("the command, with the time of the restore", func(t *testing.T) {
		if got := strings.Join(containerArgs(pod), " "); got != "admin post-restore --restored-to "+restoredTo {
			t.Errorf("args = %q", got)
		}
		_, d := render(t, required(), postRestoreSets("postRestore.skipRevocationJournal=true")...)
		if got := strings.Join(containerArgs(postRestorePod(d)), " "); got != "admin post-restore --restored-to "+restoredTo+" --skip-revocation-journal" {
			t.Errorf("args = %q", got)
		}
	})

	t.Run("a ServiceAccount of its own, a policy that admits nothing, no disruption budget", func(t *testing.T) {
		if str(pod, "serviceAccountName") != "trove-post-restore" || at(pod, "automountServiceAccountToken") != false {
			t.Errorf("account = %v", str(pod, "serviceAccountName"))
		}
		if find(docs, "ServiceAccount", "trove-post-restore") == nil {
			t.Error("no ServiceAccount")
		}
		np := find(docs, "NetworkPolicy", "trove-post-restore")
		if np == nil {
			t.Fatal("no NetworkPolicy")
		}
		if rules, ok := at(np, "spec", "ingress").([]any); !ok || len(rules) != 0 || at(np, "spec", "egress") != nil {
			t.Errorf("policy = %v", at(np, "spec"))
		}
		lbl := podTemplateLabels(job)
		if !selects(at(np, "spec", "podSelector", "matchLabels"), lbl) {
			t.Error("the policy does not select the Job's pods")
		}
		for _, p := range ofKind(docs, "PodDisruptionBudget") {
			if selects(at(p, "spec", "selector", "matchLabels"), lbl) {
				t.Errorf("%s selects the Job's pods", str(p, "metadata", "name"))
			}
		}
		if lbl["trove/serve"] != "" {
			t.Error("the pod carries trove/serve")
		}
	})

	t.Run("the Secrets of its scope: the owner login, the blob store; no Valkey, no key ring, nothing of the target", func(t *testing.T) {
		_, d := render(t, required(), postRestoreSets(append(staticTarget(), "secrets.journalReader.existingSecret=trove-journal", "postRestore.journalCredentials.source=static")...)...)
		v := secretVolumes(postRestorePod(d))
		has := func(s string) bool {
			for _, x := range v {
				if x == s {
					return true
				}
			}
			return false
		}
		for _, want := range []string{"trove-db-owner", "trove-s3", "trove-journal"} {
			if !has(want) {
				t.Errorf("secrets = %v, want %s", v, want)
			}
		}
		for _, no := range []string{"trove-db-app", "trove-db-backup", "trove-valkey", "trove-encryption-keys", "trove-tls", "trove-backup-keys", "trove-backup-ca"} {
			if has(no) {
				t.Errorf("the post-restore Job mounts %s", no)
			}
		}
		// The journal credential reaches this Job and no other pod.
		for name, p := range podSpecs(d) {
			if !strings.HasPrefix(name, "trove-post-restore") && has2(secretVolumes(p), "trove-journal") {
				t.Errorf("%s mounts the journal credential", name)
			}
		}
		if has2(secretVolumes(cronJobPod(d)), "trove-journal") {
			t.Error("the backup pod mounts the journal credential")
		}
	})

	t.Run("journal credentials: runtime passes no file, default and static pass the file the chart renders", func(t *testing.T) {
		if hasMount(pod, "/etc/trove/post-restore") || find(docs, "ConfigMap", "trove-post-restore") != nil {
			t.Error("a credentials file with journalCredentials.source runtime")
		}
		_, d := render(t, required(), postRestoreSets("postRestore.journalCredentials.source=default")...)
		args := strings.Join(containerArgs(postRestorePod(d)), " ")
		if !strings.HasSuffix(args, "--credentials-file /etc/trove/post-restore/credentials.yaml") {
			t.Errorf("args = %q", args)
		}
		cm := str(find(d, "ConfigMap", "trove-post-restore"), "data", "credentials.yaml")
		if strings.TrimSpace(cm) != "source: default" {
			t.Errorf("credentials.yaml = %q", cm)
		}
		_, d = render(t, required(), postRestoreSets("postRestore.journalCredentials.source=static", "secrets.journalReader.existingSecret=trove-journal")...)
		cm = str(find(d, "ConfigMap", "trove-post-restore"), "data", "credentials.yaml")
		for _, want := range []string{"source: static", "access_key_id_file: /var/run/secrets/trove/journal-reader/access-key-id", "secret_access_key_file: /var/run/secrets/trove/journal-reader/secret-access-key"} {
			if !strings.Contains(cm, want) {
				t.Errorf("credentials.yaml lacks %q: %s", want, cm)
			}
		}
		if !hasMount(postRestorePod(d), "/etc/trove/post-restore") {
			t.Error("the credentials file is not mounted")
		}
	})

	t.Run("L3: one Job per restore point, annotated with it, and a repeat is announced", func(t *testing.T) {
		name := str(job, "metadata", "name")
		if len(name) != len("trove-post-restore-")+8 || str(job, "metadata", "annotations", "trove/restored-to") != restoredTo {
			t.Errorf("job %q, annotation %q", name, str(job, "metadata", "annotations", "trove/restored-to"))
		}
		_, d := render(t, required(), postRestoreSets()...)
		if str(postRestoreJob(d), "metadata", "name") != name {
			t.Error("the same restore point gives another name")
		}
		_, d = render(t, required(), "postRestore.enabled=true", "postRestore.restoredTo=2026-10-29T09:15:00Z", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0")
		if str(postRestoreJob(d), "metadata", "name") == name {
			t.Error("another restore point gives the same name")
		}
		notes := notesOf(t, required(), postRestoreSets()...)
		if !strings.Contains(notes, "postRestore.enabled is true") || !strings.Contains(notes, restoredTo) {
			t.Errorf("NOTES does not announce the hook:\n%s", notes)
		}
	})

	t.Run("H1: no replica serves the restored database before the clean-up", func(t *testing.T) {
		for _, v := range []string{"api", "packages", "worker"} {
			renderFails(t, required(), append(postRestoreSets(), v+".replicaCount=2"), "postRestore.enabled", v+".replicaCount")
		}
		renderFails(t, required(), append(postRestoreSets(), "packages.autoscaling.enabled=true"), "postRestore.enabled", "packages.autoscaling.enabled")
		renderFails(t, []string{filepath.Join(chartDir, "ci", "roles-pools.yaml")}, postRestoreSets("packages.autoscaling.enabled=false"), "postRestore.enabled", ".pools[")
		render(t, required(), postRestoreSets("highAvailability.enforce=true")...)
		// Without the hook the roles keep their minimums.
		renderFails(t, required(), []string{"api.replicaCount=0"}, "api.replicaCount")
	})

	t.Run("validation names the value", func(t *testing.T) {
		renderFails(t, required(), []string{"postRestore.enabled=true", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0"}, "postRestore.restoredTo")
		renderFails(t, required(), []string{"postRestore.enabled=true", "postRestore.restoredTo=yesterday", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0"}, "postRestore.restoredTo", "RFC 3339")
		renderFails(t, required(), postRestoreSets("postRestore.journalCredentials.source=static"), "postRestore.journalCredentials.source", "secrets.journalReader.existingSecret")
		renderFails(t, required(), postRestoreSets("postRestore.skipRevocationJournal=true", "postRestore.journalCredentials.source=default"), "postRestore.skipRevocationJournal", "journalCredentials")
		renderFails(t, required(), append(restoreSets(), "postRestore.enabled=true", "postRestore.restoredTo="+restoredTo), "postRestore.enabled", "restore.enabled")
		renderFails(t, required(), postRestoreSets("postRestore.terminationGracePeriodSeconds=5"), "terminationGracePeriodSeconds")
		render(t, required(), "postRestore.enabled=true", "postRestore.restoredTo=2026-10-28T11:15:00.5+02:00", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0")
	})

	t.Run("a typo in the values is refused", func(t *testing.T) {
		renderFails(t, required(), []string{"postRestore.restored_to=x"}, "restored_to")
	})
}

func has2(v map[string]string, secret string) bool {
	for _, s := range v {
		if s == secret {
			return true
		}
	}
	return false
}

// TestR_HA_025_HoldStopsEveryRole (R-HA-025 step 1, Q-HA-212): postRestore.hold is the supported way to stop every role before a platform restore.
func TestR_HA_025_HoldStopsEveryRole(t *testing.T) {
	hold := []string{"postRestore.hold=true", "api.replicaCount=0", "packages.replicaCount=0", "worker.replicaCount=0"}
	t.Run("zero replicas are accepted with the hold, even with the enforcement of the minimums on, and no Job is rendered", func(t *testing.T) {
		_, docs := render(t, required(), append(hold, "highAvailability.enforce=true", "backup.enabled=true", "config.backup.target.bucket=trove-backups")...)
		if postRestoreJob(docs) != nil {
			t.Error("the hold rendered the post-restore Job")
		}
		for _, d := range ofKind(docs, "CronJob") {
			if got := at(d, "spec", "suspend"); got != true {
				t.Errorf("the CronJob %s has suspend %v under the hold, want true", str(d, "metadata", "name"), got)
			}
		}
	})
	t.Run("a replica that runs is refused, and so is autoscaling", func(t *testing.T) {
		renderFails(t, required(), append(hold, "packages.replicaCount=2"), []string{"postRestore.hold", "packages.replicaCount"}...)
		renderFails(t, required(), append(hold, "packages.autoscaling.enabled=true"), []string{"postRestore.hold", "packages.autoscaling.enabled"}...)
	})
	t.Run("without the hold the minimums apply", func(t *testing.T) {
		renderFails(t, required(), []string{"api.replicaCount=0"}, "api.replicaCount")
	})
}
