//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// processEnv is the configuration of `trove backup create` as the environment of a Job pod: the installation's database, its
// blob store and the rig's target.
func (in *install) processEnv(t *testing.T, r *rig, extra ...string) []string {
	t.Helper()
	d, s, b := in.dbCfg, in.s3cfg, r.cfg.Target
	env := append(os.Environ(),
		"TROVE_DATABASE_HOST="+d.Host, "TROVE_DATABASE_PORT="+strconv.Itoa(d.Port), "TROVE_DATABASE_NAME="+d.Name,
		"TROVE_DATABASE_USERNAME_FILE="+d.UsernameFile, "TROVE_DATABASE_PASSWORD_FILE="+d.PasswordFile,
		"TROVE_DATABASE_SSL_ROOT_CERT_FILE="+d.SSLRootCertFile, "TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s1:26379\"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=trove",
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"/keys/2026-10.key\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_SERVER_OPS_LISTEN=127.0.0.1:0", "TROVE_LOG_LEVEL=warn",
		"TROVE_STORAGE_S3_ENDPOINT="+s.Endpoint, "TROVE_STORAGE_S3_REGION="+s.Region, "TROVE_STORAGE_S3_BUCKET="+s.Bucket,
		"TROVE_STORAGE_S3_PREFIX="+s.Prefix, "TROVE_STORAGE_S3_FORCE_PATH_STYLE=true", "TROVE_STORAGE_S3_CA_FILE="+s.CAFile,
		"TROVE_STORAGE_S3_CREDENTIALS_SOURCE=static", "TROVE_STORAGE_S3_CREDENTIALS_ACCESS_KEY_ID_FILE="+s.Credentials.AccessKeyIDFile,
		"TROVE_STORAGE_S3_CREDENTIALS_SECRET_ACCESS_KEY_FILE="+s.Credentials.SecretAccessKeyFile,
		"TROVE_BACKUP_TARGET_ENDPOINT="+b.Endpoint, "TROVE_BACKUP_TARGET_REGION="+b.Region, "TROVE_BACKUP_TARGET_BUCKET="+b.Bucket,
		"TROVE_BACKUP_TARGET_PREFIX="+b.Prefix, "TROVE_BACKUP_TARGET_FORCE_PATH_STYLE=true", "TROVE_BACKUP_TARGET_CA_FILE="+b.CAFile,
		"TROVE_BACKUP_TARGET_CREDENTIALS_SOURCE=static", "TROVE_BACKUP_TARGET_CREDENTIALS_ACCESS_KEY_ID_FILE="+b.Credentials.AccessKeyIDFile,
		"TROVE_BACKUP_TARGET_CREDENTIALS_SECRET_ACCESS_KEY_FILE="+b.Credentials.SecretAccessKeyFile,
		"TROVE_BACKUP_TARGET_SSE_MODE="+b.SSE.Mode,
		"TROVE_BACKUP_LEASE_DURATION="+r.cfg.LeaseDuration.String(), "TROVE_BACKUP_HEARTBEAT_INTERVAL="+max(r.cfg.HeartbeatInterval, time.Second).String(),
		"TROVE_BACKUP_FINALIZE_TIMEOUT="+r.cfg.FinalizeTimeout.String(), "TROVE_BACKUP_DATABASE_LOCK_TIMEOUT=5s", "TROVE_BACKUP_DATABASE_MIGRATION_WAIT=5s",
		"TROVE_POD_NAME=it-bak-proc",
	)
	return append(env, extra...)
}

// runTrove runs `trove backup create` to its end and returns the exit code, standard output and standard error.
func runTrove(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(troveBinary(t), append([]string{"backup", "create"}, args...)...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), errb.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), errb.String()
	}
	t.Fatal(err)
	return -1, "", ""
}

// randomBlob is incompressible content, so that a rate cap makes a copy last.
func randomBlob(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return string(b)
}

// TestR_BAK_013_TheCommandExitCodes runs the real binary against the stack: success is 0 with the result as JSON, a target of
// another installation is 4, a target in the blob store's bucket is 2.
func TestR_BAK_013_TheCommandExitCodes(t *testing.T) {
	in := newInstall(t)
	in.mustUpload(in.repo, "a", "alpha-content")
	r := in.newRig(t)
	env := in.processEnv(t, r)

	code, stdout, stderr := runTrove(t, env, "--trigger", "schedule", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var res backup.Result
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.Outcome != "completed" || res.BlobsCopied != 1 {
		t.Fatalf("result %q: %v", stdout, err)
	}
	m := manifestOf(t, r.tgt, r.cfg, res.BackupID)
	if got := scalarText(in, `SELECT trigger FROM trove.backup_run`); m.Blobs.Referenced != 1 || got != "schedule" {
		t.Fatalf("trigger %s blobs %+v", got, m.Blobs)
	}
	if got := in.number(`SELECT count(*) FROM trove.audit_event WHERE action = 'system.backup.completed'`); got != 1 {
		t.Fatalf("completed events = %d", got)
	}

	t.Run("4 the target belongs to another installation", func(t *testing.T) {
		marker, _ := format.NewMarker("someone-else", "other-incarnation", time.Now()).Encode()
		if _, err := r.tgt.Put(context.Background(), format.Layout{Prefix: r.cfg.Target.Prefix}.Marker(), marker, target.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runTrove(t, env)
		if code != 4 || !strings.Contains(stderr, "someone-else") {
			t.Fatalf("exit %d: %s", code, stderr)
		}
	})
	t.Run("2 the target in the bucket of the blob store", func(t *testing.T) {
		code, _, stderr := runTrove(t, append(env, "TROVE_BACKUP_TARGET_BUCKET="+in.s3cfg.Bucket, "TROVE_BACKUP_TARGET_ENDPOINT="+in.s3cfg.Endpoint))
		if code != 2 || !strings.Contains(stderr, "backup.target.bucket") {
			t.Fatalf("exit %d: %s", code, stderr)
		}
	})
}

// TestR_BAK_010_AKilledRunIsTakenOverAfterItsLease: two processes at once - the second exits 3 naming the first; the first is
// killed with SIGKILL during copying, and the next run waits for the lease, abandons the old row (its pins go with it) and
// completes.
func TestR_BAK_010_AKilledRunIsTakenOverAfterItsLease(t *testing.T) {
	in := newInstall(t)
	for _, n := range []string{"a", "b", "c"} {
		in.mustUpload(in.repo, n, randomBlob(300<<10))
	}
	r := in.newRig(t)
	r.cfg.LeaseDuration = 6 * time.Second
	r.cfg.HeartbeatInterval = time.Second
	r.cfg.FinalizeTimeout = time.Second
	slow := in.processEnv(t, r, "TROVE_BACKUP_COPY_SERVER_SIDE=never", "TROVE_BACKUP_COPY_BYTES_PER_SECOND=64KiB")

	cmd := exec.Command(troveBinary(t), "backup", "create", "--no-prune")
	cmd.Env = slow
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = cmd.Process.Kill(); <-exited }()

	defer func() {
		if t.Failed() {
			t.Logf("first process stderr: %s", errb.String())
		}
	}()
	testenv.WaitFor(t, "the first run is copying with pins left", 60*time.Second, func() bool {
		var n int64
		_ = in.admin.QueryRow(context.Background(), `SELECT count(*) FROM trove.backup_run r JOIN trove.backup_blob_pin p ON p.run_id = r.id WHERE r.state = 'copying'`).Scan(&n)
		return n > 0
	})

	code, _, stderr := runTrove(t, in.processEnv(t, r), "--no-prune")
	if code != 3 || !strings.Contains(stderr, "another backup run is active") {
		t.Fatalf("second run: exit %d: %s", code, stderr)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-exited
	oldID := scalarText(in, `SELECT id::text FROM trove.backup_run WHERE state = 'copying'`)
	if hold := scalarText(in, `SELECT hold FROM trove.backup_run WHERE id = $1::uuid`, oldID); hold != "pinned" {
		t.Fatalf("hold of the killed run = %s", hold)
	}

	// A heartbeat younger than 3 x heartbeat_interval counts as alive (R-BAK-010), so a retry pod that starts sooner than that
	// after the crash would exit 3; the Job controller's backoff is longer.
	time.Sleep(3500 * time.Millisecond)
	start := time.Now()
	code, stdout, stderr := runTrove(t, in.processEnv(t, r), "--no-prune", "--json")
	if code != 0 {
		t.Fatalf("retry: exit %d: %s", code, stderr)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("the retry did not wait for the lease (%s)", time.Since(start))
	}
	if st := scalarText(in, `SELECT state FROM trove.backup_run WHERE id = $1::uuid`, oldID); st != "abandoned" {
		t.Fatalf("the killed run is %s", st)
	}
	if n := in.number(`SELECT count(*) FROM trove.backup_blob_pin`); n != 0 {
		t.Fatalf("pins left: %d", n)
	}
	var res backup.Result
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.BlobCount != 3 {
		t.Fatalf("%q %v", stdout, err)
	}
	if got := len(listManifests(t, r)); got != 1 {
		t.Fatalf("the target holds %d manifests, want exactly the retry's", got)
	}
}
