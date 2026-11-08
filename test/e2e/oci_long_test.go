//go:build e2e

package e2e

// The long test of the OCI registry (R-OCI-011, R-HA-052; owner rule of 2026-11-01: drain, failover and soak run in the nightly workflow, on
// dispatch and locally, never in Test and Release): `docker push` of an image with a 300 MiB layer while the packages pod that receives its
// PATCH requests is drained. The set is deploy/dev/e2e.sh oci-long.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s3Multipart lists the multipart uploads of the blob bucket with the AWS CLI of the dev profile, run as a Job in the namespace of the release
// with the credentials of Secret trove-s3 (the pattern of the restore drill).
func s3Multipart(t *testing.T, name string) string {
	t.Helper()
	image := readVersionPin(t, "DEV_AWSCLI_IMAGE")
	kn(t, "delete", "job", name, "--ignore-not-found")
	manifest := fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":%q},"spec":{"backoffLimit":0,"template":{"spec":{
"restartPolicy":"Never","automountServiceAccountToken":false,
"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"runAsGroup":65532,"seccompProfile":{"type":"RuntimeDefault"}},
"containers":[{"name":"aws","image":%q,"env":[{"name":"HOME","value":"/tmp"}],
"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}},
"command":["sh","-c",%q],
"volumeMounts":[{"name":"tmp","mountPath":"/tmp"},{"name":"s3","mountPath":"/secrets"},{"name":"tls","mountPath":"/tls"}]}],
"volumes":[{"name":"tmp","emptyDir":{}},{"name":"s3","secret":{"secretName":"trove-s3"}},{"name":"tls","secret":{"secretName":"trove-s3-tls"}}]}}}}`,
		name, image, `set -eu
export AWS_ACCESS_KEY_ID="$(cat /secrets/access-key-id)" AWS_SECRET_ACCESS_KEY="$(cat /secrets/secret-access-key)" AWS_DEFAULT_REGION=us-east-1 AWS_CA_BUNDLE=/tls/ca.crt
aws --endpoint-url https://trove-s3.trove.svc:9000 s3api list-multipart-uploads --bucket trove-blobs --query 'length(Uploads || `+"`[]`"+`)' --output text`)
	create := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "create", "-f", "-")
	create.Stdin = strings.NewReader(manifest)
	if b, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create the Job %s: %v: %s", name, err, b)
	}
	defer func() {
		_, _ = kubectl("-n", cfg.namespace, "delete", "job", name, "--ignore-not-found", "--wait=false")
	}()
	e := bakWaitJob(t, cfg.namespace, name, 5*time.Minute)
	logs := bakJobLogs(cfg.namespace, name)
	if e.Condition != "Complete" {
		t.Fatalf("the Job %s ended %s:\n%s", name, e.Condition, tail(logs, 1500))
	}
	return strings.TrimSpace(logs)
}

// TestOCILong_R_OCI_011_DrainOfThePodThatReceivesAPush: the client pod's forwarder is pinned to one packages pod, so that the upload's PATCH
// requests reach it; once the session has received a few parts, the pod's node is drained (deploy/dev/drain.sh) and the forwarder is
// released to the edge. The push exits 0 (the client restarts the blob or resumes the session on another replica), the pulled image has the
// pushed digest, and after the session's expiry and a collection run the bucket holds no multipart upload of the abandoned session.
func TestOCILong_R_OCI_011_DrainOfThePodThatReceivesAPush(t *testing.T) {
	fx := ociFx(t)
	host := ociRegistryHost(t)
	key := "oci-long-" + fx.run
	fx.adm.do("POST", reposRoute, map[string]any{"key": key, "class": "local", "format": "oci", "requireAuthentication": true}).want(t, "create "+key, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ensurePolicy(t, fx.adm, "e2e-oci-long-"+fx.run, []string{key}, [2]any{"sa:" + ociPublisher, []string{"read", "deploy", "delete"}})
	ociReadableOnEveryReplica(t, fx.token, key)

	name := fx.name("long")
	ref := fx.image(t, key, name, "1")
	c := fx.client(t, 0, ociDocker, "long-push")
	c.login(host)
	ctx := c.buildContext("")
	ociClientExec(t, 0, ociDocker, "sh", "-c", `head -c 314572800 /dev/urandom >"$1/payload"`, "sh", c.dir()+"/"+ctx)
	c.exec("build", "--tag", ref, ctx)

	webs := packagesReplicas(t, 3)
	target := webs[0]
	ociPin(t, 0, target.IP)
	defer ociUnpin(t, 0)
	t.Logf("the push is pinned to %s (%s) on %s", target.Name, target.IP, target.Node)

	pushed := make(chan restoreResult, 1)
	go func() {
		out, err := c.try("push", ref)
		pushed <- restoreResult{out: out, err: err}
	}()

	received := func() string {
		return sql(t, `SELECT coalesce(max(s.received), 0) FROM trove.upload_sessions s JOIN trove.repositories r ON r.id = s.repo_id WHERE r.key = '`+key+`' AND s.state = 'open'`)
	}
	waitFor(t, "the upload session to have received 48 MiB", 5*time.Minute, time.Second, func() bool {
		var n int64
		_, _ = fmt.Sscan(received(), &n)
		return n >= 48<<20
	})
	avoidPrimary(t, target.Node)
	drained := make(chan error, 1)
	var drainOut []byte
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/drain.sh"), target.Node)
		cmd.Dir = cfg.repo
		var err error
		drainOut, err = cmd.CombinedOutput()
		drained <- err
	}()
	// the forwarder leaves the drained pod at once: the retries of the client reach the edge, and with it the other replicas
	ociUnpin(t, 0)

	var res restoreResult
	select {
	case res = <-pushed:
	case <-time.After(25 * time.Minute):
		t.Fatal("the push did not end within 25 minutes")
	}
	if err := <-drained; err != nil {
		t.Fatalf("drain.sh: %v\n%s", err, drainOut)
	}
	if res.err != nil {
		t.Fatalf("docker push exited with an error although the pod was drained: %v\n%s", res.err, tailLines(res.out, 30))
	}
	t.Logf("docker push survived the drain of %s:\n%s", target.Node, tailLines(res.out, 12))
	waitServingSettled(t)

	digest := fx.digestOf(t, key, name, "1")
	p := fx.client(t, 1, ociDocker, "long-pull")
	p.login(host)
	p.pull(ref)
	if !p.holds(ref, digest) {
		t.Fatalf("the pulled image is %q, want the digest %s of the push", p.digests(ref), digest)
	}

	// The session the drain cut stays open until it expires (storage.uploads.session_ttl, four hours): the clock of the test moves its last
	// activity back, the collection run (R-STOR-011 Expiry) aborts it, and the bucket keeps no part of it.
	if open := sql(t, `SELECT count(*) FROM trove.upload_sessions s JOIN trove.repositories r ON r.id = s.repo_id WHERE r.key = '`+key+`' AND s.state IN ('open', 'completing', 'assembled')`); open != "0" {
		t.Logf("%s abandoned session(s) of the drained push: aging them past the session TTL", open)
		sql(t, `UPDATE trove.upload_sessions SET last_activity_at = now() - interval '25 hours' WHERE state IN ('open', 'completing', 'assembled') AND repo_id = (SELECT id FROM trove.repositories WHERE key = '`+key+`')`)
	}
	waitFor(t, "a collection run to remove the multipart uploads of the abandoned session", 10*time.Minute, 15*time.Second, func() bool {
		r, err := fx.adm.send("POST", "/api/v1/system/tasks/gc/run", nil)
		if err != nil || (r.Status != 200 && r.Status != 202 && r.Status != 409) {
			return false
		}
		return s3Multipart(t, "oci-multipart-"+fx.run) == "0"
	})
}
