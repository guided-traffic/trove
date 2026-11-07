//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/runstoretest"
	"github.com/guided-traffic/trove/internal/backup/verify"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The scenarios of R-BAK-029 that run without a cluster (scenario_*_test.go) compose the pieces of the lane the way the drill
// does: an installation changes while `create` runs, the backup is restored into a fresh installation, and the restored
// installation is judged by what it serves.

// realCanary replaces the placeholder canary row of newInstall by one the stack's key ring (key 2026-10, 32 bytes of 42, the key
// every destination stack loads) opens, which the restore checks before it loads anything (R-BAK-020 check 3).
func realCanary(t *testing.T, in *install) {
	t.Helper()
	file := testenv.WriteFile(t, t.TempDir(), "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)))
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{file}, ActiveKeyID: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ring.SealCanary("2026-10")
	if err != nil {
		t.Fatal(err)
	}
	in.exec(`UPDATE trove.secret_key_canary SET ciphertext = $1 WHERE key_id = '2026-10'`, sealed)
}

// sourceOf describes the backup a rig wrote, so that a destination can restore it.
func (in *install) sourceOf(t *testing.T, r *rig, id string) *source {
	t.Helper()
	m := manifestOf(t, r.tgt, r.cfg, id)
	return &source{t: t, layout: format.Layout{Prefix: r.cfg.Target.Prefix}, tgtCfg: r.cfg, id: format.ID(id), m: m}
}

// deepVerify is `trove backup verify --deep` of the newest backup of the rig's target and returns its exit code.
func deepVerify(t *testing.T, r *rig, ids ...string) int {
	t.Helper()
	v := &verify.Verifier{
		Store: r.tgt, Layout: format.Layout{Prefix: r.cfg.Target.Prefix}, Runs: &runstoretest.Mem{},
		Timing: backup.Timing{Lease: time.Minute, Heartbeat: time.Second, Stall: time.Hour, Finalize: time.Second},
		NewRunID: func() string {
			return fmt.Sprintf("00000000-0000-7000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
		},
	}
	opts := backup.VerifyOptions{Latest: len(ids) == 0, Deep: true}
	if len(ids) > 0 {
		opts.ID = format.ID(ids[0])
	}
	rep, err := v.Verify(context.Background(), opts)
	if err != nil {
		t.Logf("verify: %v %+v", err, rep)
	}
	return exit(err)
}

// downloadsMatch checks that every file item of the restored database, trashed or not, has an object in the restored blob store
// whose SHA-256 equals the digest of the item (R-BAK-029 scenario 1 and 2). It returns the number of items checked.
func downloadsMatch(t *testing.T, dst *destination) int {
	t.Helper()
	var digests []format.Digest
	dst.stack.Query(`SELECT DISTINCT blob_sha256 FROM items WHERE blob_sha256 IS NOT NULL`, func(rows pgx.Rows) error {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		var d format.Digest
		copy(d[:], b)
		digests = append(digests, d)
		return nil
	})
	for _, d := range digests {
		got := dst.blobObject(d)
		if got == nil {
			t.Errorf("blob %s: the restored blob store has no object", d.Hex())
			continue
		}
		if sum := sha256.Sum256(got); format.Digest(sum) != d {
			t.Errorf("blob %s: the object hashes to %x", d.Hex(), sum)
		}
	}
	return len(digests)
}

// sealStackCanary puts the canary row of the stack's key ring (key 2026-10, 32 bytes of 42, what every stack loads) into a stack
// whose replicas have not run: a replica seeds it at start-up (R-ARCH-021), and the restore opens it (R-BAK-020 check 3).
func sealStackCanary(t *testing.T, s *testenv.Stack) {
	t.Helper()
	file := testenv.WriteFile(t, t.TempDir(), "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)))
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{file}, ActiveKeyID: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ring.SealCanary("2026-10")
	if err != nil {
		t.Fatal(err)
	}
	s.Exec("INSERT INTO secret_key_canary (key_id, ciphertext) VALUES ($1, $2) ON CONFLICT DO NOTHING", "2026-10", sealed)
}

// installOnStack is an installation around a stack's database and blob prefix, for the create pipeline.
func installOnStack(t *testing.T, s *testenv.Stack) *install {
	t.Helper()
	loaded, err := config.Load(config.Options{Environ: append(s.Environ(), "TROVE_BACKUP_TARGET_BUCKET="+testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS")), Command: config.CommandBackup})
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Config
	client, err := s3.NewFromFactory(context.Background(), cfg.Storage.S3, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		t.Fatal(err)
	}
	in := &install{t: t, dbName: s.Database(), dbCfg: cfg.Database, s3cfg: cfg.Storage.S3, obj: client, client: client, prefix: cfg.Storage.S3.Prefix}
	in.dbCfg.Preflight.AllowSuperuser = true
	return in
}

// newBackupRig is a rig with a target of its own below the backup bucket.
func newBackupRig(t *testing.T, in *install) *rig {
	t.Helper()
	tc := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	tgt := newTarget(t, tc)
	cleanup(t, tgt, tc.Target.Prefix+"/")
	r := &rig{cfg: in.backupConfig(t), tgt: tgt}
	r.cfg.Target = tc.Target
	return r
}
