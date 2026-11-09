//go:build integration

// Package restoreversions tests the commands of steps 5 and 7 of the restore procedure (R-HA-025), `trove admin storage
// restore-versions` and `trove admin maintenance pause-deletions` / `resume-deletions`, by running the binary against the
// integration stack: an installation database, and the versioned blob bucket of the stack's SeaweedFS.
package restoreversions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var binary string

// TestMain builds the binary once for the package's tests.
func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "trove-restoreversions-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		binary = filepath.Join(dir, "trove")
		build := exec.Command("go", "build", "-o", binary, "github.com/guided-traffic/trove/cmd/trove")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
			return 1
		}
		return testenv.Main(m)
	}())
}

// run runs `trove admin <args>` with the stack's configuration and returns the exit code and the two streams.
func run(t *testing.T, s *testenv.Stack, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"admin"}, args...)...)
	cmd.Env = append(os.Environ(), s.Environ()...)
	cmd.Env = append(cmd.Env, "TROVE_LOG_LEVEL=warn")
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError) //nolint:errorlint // exec returns it unwrapped
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return code, o.String(), e.String()
}

// seedBlob stores an object and the blob and item rows that reference it, and returns the digest.
func seedBlob(t *testing.T, s *testenv.Stack, repo, path string, data []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(data)
	s.PutObject(t, sum[:], data)
	s.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, decode(repeat('00', 20), 'hex'), decode(repeat('00', 16), 'hex'), decode(repeat('00', 64), 'hex'), $2) ON CONFLICT DO NOTHING`,
		sum[:], len(data))
	s.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
		SELECT r.id, $2::text, '', 'file', $3, $4 FROM repositories r WHERE r.key = $1`, repo, path, sum[:], len(data))
	return sum[:]
}

// TestR_HA_025_RestoreVersionsBringsBackTheObjectsDeletedAfterT: three blobs have an object at t. The collector of the old
// installation deletes two of them after t (delete markers on the versioned bucket); the third is deleted before t. The command
// removes the markers of the two, leaves the third (its object was already gone at t) and reports it with exit code 5, a dry run
// changes nothing, and a second run finds nothing to do.
func TestR_HA_025_RestoreVersionsBringsBackTheObjectsDeletedAfterT(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "restorever"})
	s.Repository("npm-local", "local", "npm", "{}")
	early := seedBlob(t, s, "npm-local", "early/-/early-1.0.0.tgz", []byte("deleted before t"))
	late1 := seedBlob(t, s, "npm-local", "late1/-/late1-1.0.0.tgz", []byte("deleted after t, one"))
	late2 := seedBlob(t, s, "npm-local", "late2/-/late2-1.0.0.tgz", []byte("deleted after t, two"))
	intact := seedBlob(t, s, "npm-local", "intact/-/intact-1.0.0.tgz", []byte("never deleted"))
	// The store stamps versions to the second: keep the events apart by more than that.
	time.Sleep(2 * time.Second)
	s.DeleteObject(t, early)
	time.Sleep(2 * time.Second)
	at := time.Now().UTC().Truncate(time.Second)
	time.Sleep(2 * time.Second)
	s.DeleteObject(t, late1)
	s.DeleteObject(t, late2)
	tFlag := at.Format(time.RFC3339)

	code, out, errOut := run(t, s, "storage", "restore-versions", "--before", tFlag, "--dry-run")
	if code != 5 || !strings.Contains(out, "(dry run): 4 blobs checked, 3 without object, 2 restored, 0 delete markers removed, 1 still without object") {
		t.Fatalf("dry run: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, d := range [][]byte{early, late1, late2} {
		if s.ObjectExists(t, d) {
			t.Fatal("a dry run made an object current")
		}
	}

	code, out, errOut = run(t, s, "storage", "restore-versions", "--before", tFlag)
	if code != 5 {
		t.Fatalf("exit %d, want 5 for the blob that was deleted before t\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{
		fmt.Sprintf("blob %x: restored, 1 delete markers removed", late1),
		fmt.Sprintf("blob %x: restored, 1 delete markers removed", late2),
		fmt.Sprintf("blob %x: no delete marker younger than t", early),
		"3 without object, 2 restored, 2 delete markers removed, 1 still without object",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for name, d := range map[string][]byte{"late1": late1, "late2": late2, "intact": intact} {
		if !s.ObjectExists(t, d) {
			t.Errorf("the object of %s is not current after the command", name)
		}
	}
	if s.ObjectExists(t, early) {
		t.Error("the object that was deleted before t is current: its marker is older than t and must stay")
	}
	if n := s.Number(`SELECT count(*) FROM audit_event WHERE action = 'system.storage.versions_restored'`); n != 1 {
		t.Errorf("%d system.storage.versions_restored events, want 1 (the dry run writes none)", n)
	}

	// The remaining blob is the one finding of a second run, which removes nothing.
	code, out, _ = run(t, s, "storage", "restore-versions", "--before", tFlag, "--json")
	var rep struct {
		Missing, Restored, MarkersRemoved, Remaining int64
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != 5 || rep.Missing != 1 || rep.Restored != 0 || rep.MarkersRemoved != 0 || rep.Remaining != 1 {
		t.Errorf("second run: exit %d, %+v", code, rep)
	}

	// The credentials file replaces the credential of the blob store for this command only.
	dir := t.TempDir()
	o := testenv.S3Config(t)
	file := filepath.Join(dir, "creds.yaml")
	body := fmt.Sprintf("source: static\naccess_key_id_file: %s\nsecret_access_key_file: %s\n",
		testenv.WriteFile(t, dir, "id", o.AccessKey), testenv.WriteFile(t, dir, "secret", o.SecretKey))
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut = run(t, s, "storage", "restore-versions", "--before", tFlag, "--credentials-file", file); code != 5 || !strings.Contains(out, "1 without object") {
		t.Errorf("with a credentials file: exit %d\n%s\n%s", code, out, errOut)
	}
}

// TestR_HA_025_RestoreVersionsIsCleanWhenNothingIsMissing: with every object in place the command prints the totals and exits 0.
func TestR_HA_025_RestoreVersionsIsCleanWhenNothingIsMissing(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "restorever_ok"})
	s.Repository("npm-local", "local", "npm", "{}")
	seedBlob(t, s, "npm-local", "a/-/a-1.0.0.tgz", []byte("present"))
	code, out, errOut := run(t, s, "storage", "restore-versions", "--before", time.Now().UTC().Format(time.RFC3339))
	if code != 0 || !strings.Contains(out, "1 blobs checked, 0 without object, 0 restored") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
	if code, _, _ := run(t, s, "storage", "restore-versions"); code != 2 {
		t.Errorf("without --before: exit %d, want 2", code)
	}
}

// TestR_HA_025_ListVersionsSeesTheDeleteMarkerOfAKey: the store's listing of versions tells a delete marker from an object
// version, and the removal of the marker makes the object current again, which is what the command builds on.
func TestR_HA_025_ListVersionsSeesTheDeleteMarkerOfAKey(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "restorever_list"})
	sum := seedBlob2(t, s, []byte("versioned"))
	s.DeleteObject(t, sum)
	store, prefix := s.ObjectClient(t)
	c, ok := store.(*s3.Client)
	if !ok {
		t.Fatalf("the object client is a %T", store)
	}
	key := blob.Key(prefix, blob.Digest(sum))
	ctx := context.Background()
	vs, err := c.ListVersions(ctx, key)
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions = %+v, %v; want a delete marker and one object version", vs, err)
	}
	// The listing has no defined order, and a marker and a version written within one second carry the same time.
	var marker s3.ObjectVersion
	for _, v := range vs {
		if v.Marker {
			marker = v
		}
	}
	if marker.ID == "" || !marker.Latest {
		t.Fatalf("versions = %+v; want the delete marker to be the latest", vs)
	}
	if err := c.DeleteVersion(ctx, key, marker.ID); err != nil {
		t.Fatal(err)
	}
	if !s.ObjectExists(t, sum) {
		t.Error("the object is not current after its delete marker was removed")
	}
}

func seedBlob2(t *testing.T, s *testenv.Stack, data []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(data)
	s.PutObject(t, sum[:], data)
	return sum[:]
}

// TestR_HA_025_ResumeDeletionsClearsThePauseOfAPostRestore: pause-deletions sets the pause with its reason, a second call leaves
// it, resume-deletions clears it and says what it had been, a second resume changes nothing, and each change writes one audit
// event.
func TestR_HA_025_ResumeDeletionsClearsThePauseOfAPostRestore(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Name: "resumedel"})
	paused := func() int64 {
		return s.Number(`SELECT count(*) FROM system_state WHERE key = 'deletions_paused_at' AND value <> 'null'::jsonb`)
	}

	if code, out, errOut := run(t, s, "maintenance", "pause-deletions"); code != 2 {
		t.Fatalf("pause-deletions without a reason: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := run(t, s, "maintenance", "resume-deletions"); code != 0 || !strings.Contains(out, "was not set; nothing changed") || paused() != 0 {
		t.Fatalf("resume of a pause that is not set: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, errOut := run(t, s, "maintenance", "pause-deletions", "--reason", "restore drill"); code != 0 || !strings.Contains(out, "deletion pause set (restore drill)") || paused() != 1 {
		t.Fatalf("pause: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := run(t, s, "maintenance", "pause-deletions", "--reason", "another"); code != 0 || !strings.Contains(out, "already set") {
		t.Errorf("a second pause: exit %d\n%s", code, out)
	}
	if got := s.Text(`SELECT value #>> '{}' FROM system_state WHERE key = 'deletions_paused_reason'`); got != "restore drill" {
		t.Errorf("the reason of the pause is %q after a second pause, want the first", got)
	}
	code, out, errOut := run(t, s, "maintenance", "resume-deletions", "--json")
	var st struct {
		Set     bool
		At      string
		Reason  string
		Changed bool
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != 0 || st.Set || !st.Changed || st.Reason != "restore drill" || st.At == "" || paused() != 0 {
		t.Fatalf("resume: exit %d %+v\n%s", code, st, errOut)
	}
	if code, out, _ := run(t, s, "maintenance", "resume-deletions"); code != 0 || !strings.Contains(out, "nothing changed") {
		t.Errorf("a second resume: exit %d\n%s", code, out)
	}
	for action, want := range map[string]int64{"system.deletions_paused": 1, "system.deletions_resumed": 1} {
		if n := s.Number(`SELECT count(*) FROM audit_event WHERE action = $1`, action); n != want {
			t.Errorf("%d %s events, want %d", n, action, want)
		}
	}
}
