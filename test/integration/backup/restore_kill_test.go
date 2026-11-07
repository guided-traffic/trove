//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
)

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// troveBinary builds cmd/trove once into .out of the repository.
func troveBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..", "..")
		dir := filepath.Join(root, ".out", "restore-it")
		if binErr = os.MkdirAll(dir, 0o755); binErr != nil {
			return
		}
		binPath = filepath.Join(dir, "trove")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/trove")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = errors.New(string(out))
		}
	})
	if binErr != nil {
		t.Fatalf("build trove: %v", binErr)
	}
	return binPath
}

// proc is a running trove process.
type proc struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   chan struct{}
}

// startTrove starts `trove backup <args>` against the destination with a pod name of its own.
func (d *destination) startTrove(t *testing.T, pod string, args ...string) *proc {
	t.Helper()
	p := &proc{done: make(chan struct{})}
	p.cmd = exec.Command(troveBinary(t), append([]string{"backup"}, args...)...)
	p.cmd.Env = append(append([]string{}, d.environ...), "TROVE_POD_NAME="+pod)
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

// wait returns the exit code of the process.
func (p *proc) wait(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(within):
		_ = p.cmd.Process.Kill()
		t.Fatalf("trove did not end within %s\nstdout:\n%s\nstderr:\n%s", within, p.stdout.String(), p.stderr.String())
	}
	return p.cmd.ProcessState.ExitCode()
}

// kill sends SIGKILL, as the kubelet does to a pod that outlives its grace period.
func (p *proc) kill(t *testing.T) {
	t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	<-p.done
}

// until polls cond every 10 ms; it fails the test when the process ended first or the time ran out.
func (p *proc) until(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("trove ended (exit %d) before %s\nstdout:\n%s\nstderr:\n%s", p.cmd.ProcessState.ExitCode(), what, p.stdout.String(), p.stderr.String())
		default:
		}
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.cmd.Process.Kill()
	t.Fatalf("timed out waiting for %s\nstderr:\n%s", what, p.stderr.String())
}

// objectsInBlobPrefix counts the blob objects the restore copied so far.
func (d *destination) objectsInBlobPrefix() int {
	count := 0
	token := ""
	c := d.blobClient()
	for {
		page, err := c.List(context.Background(), d.cfg.Storage.S3.Prefix+"/sha256/", blob.ListOptions{Token: token})
		if err != nil {
			return count
		}
		count += len(page.Objects)
		if page.Next == "" {
			return count
		}
		token = page.Next
	}
}

// resume runs the Job's next pod until it ends with something other than exit 75, the code a pod that waited for the lock of
// the killed one ends with.
func (d *destination) resume(t *testing.T, id string) int {
	t.Helper()
	for i := range 6 {
		p := d.startTrove(t, "restore-resume-"+string(rune('a'+i)), "restore", id, "--skip-revocation-journal")
		code := p.wait(t, 3*time.Minute)
		if code != backup.ExitTemporary {
			if code != 0 {
				t.Fatalf("resume ended with %d\nstdout:\n%s\nstderr:\n%s", code, p.stdout.String(), p.stderr.String())
			}
			return code
		}
		time.Sleep(time.Second)
	}
	t.Fatal("every resume ended with exit 75")
	return -1
}

func TestR_BAK_021_KillNine(t *testing.T) {
	src := newSourceRows(t, 300, 250000)
	tablesOf := exportedTables(src)
	ref := src.destination()
	if _, err := ref.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
		t.Fatal(err)
	}
	want := fingerprint(ref.stack, tablesOf)
	id := string(src.id)

	check := func(t *testing.T, dst *destination) {
		t.Helper()
		if got := fingerprint(dst.stack, tablesOf); got != want {
			t.Fatalf("the resumed restore differs from the uninterrupted one:\n%s\nwant\n%s", got, want)
		}
		if got := dst.stack.Number("SELECT rev_seq FROM packages WHERE name = 'pkg'"); got != 6+(1<<32) {
			t.Errorf("rev_seq = %d: the fix-ups did not run exactly once", got)
		}
		if n := dst.stack.Number("SELECT count(*) FROM system_state WHERE key = 'restore_in_progress'"); n != 0 {
			t.Error("the marker is still set")
		}
		if n := dst.stack.Number("SELECT count(*) FROM audit_event WHERE action = 'system.restored'"); n != 1 {
			t.Errorf("%d system.restored events", n)
		}
	}

	t.Run("R-BAK-021 kill -9 during step 2, the schema steps", func(t *testing.T) {
		dst := src.destination("TROVE_BACKUP_COPY_CONCURRENCY=1")
		p := dst.startTrove(t, "restore-first", "restore", id, "--skip-revocation-journal")
		p.until(t, "a few migration steps", time.Minute, func() bool {
			n, err := dst.stack.TryNumber("SELECT count(*) FROM schema_migration")
			return err == nil && n >= 8 && n < int64(src.m.Schema.Step)-8
		})
		p.kill(t)
		if got := dst.stack.Text("SELECT coalesce(obj_description('trove'::regnamespace, 'pg_namespace'), '')"); got != "trove-restore "+id {
			t.Fatalf("anchor = %q", got)
		}
		if n := dst.stack.Number("SELECT count(*) FROM schema_migration"); n >= int64(src.m.Schema.Step) {
			t.Fatalf("all %d steps applied before the kill", n)
		}
		dst.resume(t, id)
		check(t, dst)
	})

	t.Run("R-BAK-021 kill -9 during step 3, the blob copy", func(t *testing.T) {
		dst := src.destination("TROVE_BACKUP_COPY_CONCURRENCY=1")
		p := dst.startTrove(t, "restore-first", "restore", id, "--skip-revocation-journal")
		var seen int
		p.until(t, "part of the blobs copied", 2*time.Minute, func() bool {
			seen = dst.objectsInBlobPrefix()
			return seen >= 20 && seen < 200
		})
		p.kill(t)
		if got := dst.stack.Text("SELECT value ->> 'phase' FROM system_state WHERE key = 'restore_in_progress'"); got != "blobs" {
			t.Fatalf("phase = %q", got)
		}
		dst.resume(t, id)
		check(t, dst)
		if got := dst.objectsInBlobPrefix(); got != len(src.seed.blobs) {
			t.Errorf("%d blob objects, want %d", got, len(src.seed.blobs))
		}
	})

	t.Run("R-BAK-021 kill -9 during step 4, the load transaction", func(t *testing.T) {
		dst := src.destination()
		p := dst.startTrove(t, "restore-first", "restore", id, "--skip-revocation-journal")
		p.until(t, "the COPY of the load", 3*time.Minute, func() bool {
			n, err := dst.stack.TryNumber("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'active' AND query ILIKE 'COPY%audit%'")
			return err == nil && n > 0
		})
		p.kill(t)
		// The load is one transaction: nothing of it is visible and the marker still says blobs.
		time.Sleep(500 * time.Millisecond)
		if got := dst.stack.Text("SELECT value ->> 'phase' FROM system_state WHERE key = 'restore_in_progress'"); got != "blobs" {
			t.Fatalf("phase = %q", got)
		}
		if n := dst.stack.Number("SELECT count(*) FROM items"); n != 0 {
			t.Fatalf("%d items visible after the killed load", n)
		}
		dst.resume(t, id)
		check(t, dst)
	})
}

// TestR_BAK_020_CommandExitCodes runs the trove binary for the exit codes of a restore that does not start.
func TestR_BAK_020_CommandExitCodes(t *testing.T) {
	src := newSource(t, 4)
	id := string(src.id)

	t.Run("R-BAK-020 --check exits 0, a key ring without the backup's key ID exits 6 with the message on stderr", func(t *testing.T) {
		dst := src.destination()
		p := dst.startTrove(t, "restore-check", "restore", id, "--check", "--skip-revocation-journal")
		if code := p.wait(t, time.Minute); code != 0 {
			t.Fatalf("--check exit %d\n%s%s", code, p.stdout.String(), p.stderr.String())
		}
		if !strings.Contains(p.stdout.String(), "every precondition holds") {
			t.Errorf("stdout = %q", p.stdout.String())
		}
		if n := dst.relations(); n != 0 {
			t.Fatalf("--check created %d relations", n)
		}

		other := src.destination("TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\""+src.stack.KeyFile("2026-09.key", bytes.Repeat([]byte{1}, 32))+"\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-09")
		p = other.startTrove(t, "restore-keys", "restore", id, "--skip-revocation-journal")
		if code := p.wait(t, time.Minute); code != 6 {
			t.Fatalf("exit %d, want 6\n%s", code, p.stderr.String())
		}
		if !strings.Contains(p.stderr.String(), "key ring does not match backup: missing key 2026-10") {
			t.Errorf("stderr = %q", p.stderr.String())
		}
		if n := other.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}
	})

	t.Run("R-BAK-021 the whole command restores and a second pod after the first finished exits 6", func(t *testing.T) {
		dst := src.destination()
		p := dst.startTrove(t, "restore-one", "restore", id, "--skip-revocation-journal")
		if code := p.wait(t, 3*time.Minute); code != 0 {
			t.Fatalf("exit %d\n%s%s", code, p.stdout.String(), p.stderr.String())
		}
		if n := dst.stack.Number("SELECT count(*) FROM items"); n != 4 {
			t.Errorf("%d items", n)
		}
		p = dst.startTrove(t, "restore-two", "restore", id, "--skip-revocation-journal")
		if code := p.wait(t, time.Minute); code != 6 {
			t.Fatalf("second restore exit %d, want 6\n%s", code, p.stderr.String())
		}
		if !strings.Contains(p.stderr.String(), "check 4") {
			t.Errorf("stderr = %q", p.stderr.String())
		}
	})
}
