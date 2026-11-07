//go:build integration

package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
)

// TestR_BAK_006_AnIncrementalRunCopiesOnlyTheNewBlobs: after a first backup, one with 10 new blobs reports blobs.copied 10 and
// referenced equal to the source's count.
func TestR_BAK_006_AnIncrementalRunCopiesOnlyTheNewBlobs(t *testing.T) {
	in := newInstall(t)
	for i := 0; i < 3; i++ {
		in.mustUpload(in.repo, "old/"+string(rune('a'+i)), "old-content-"+string(rune('a'+i)))
	}
	r := in.newRig(t)
	first, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil || first.BlobsCopied != 3 {
		t.Fatalf("first %+v %v", first, err)
	}
	for i := 0; i < 10; i++ {
		in.mustUpload(in.repo, "new/"+string(rune('a'+i)), "new-content-"+string(rune('a'+i)))
	}
	second, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatal(err)
	}
	m := manifestOf(t, r.tgt, r.cfg, second.BackupID)
	want := in.number(`SELECT count(DISTINCT blob_sha256) FROM trove.items`)
	if m.Blobs.Copied != 10 || m.Blobs.Referenced != want || m.PreviousBackupID == nil || string(*m.PreviousBackupID) != first.BackupID {
		t.Fatalf("blobs %+v referenced want %d previous %v", m.Blobs, want, m.PreviousBackupID)
	}
}

// storeWithoutServerCopy is a target whose store rejects server-side copies, the case of a target credential without
// s3:GetObject on the source.
type storeWithoutServerCopy struct{ target.Store }

func (storeWithoutServerCopy) Copy(context.Context, target.ObjectRef, string) error {
	return target.ErrServerCopyUnavailable
}

// TestR_BAK_006_ServerSideCopyAndTheStreamingFallback: with one credential that reads both buckets the objects are copied inside
// the store and counted as mode server; a store that rejects the copy makes the run stream, and it completes.
func TestR_BAK_006_ServerSideCopyAndTheStreamingFallback(t *testing.T) {
	counter := func(m *backup.Metrics, mode string) float64 {
		var out dto.Metric
		if err := m.CopiedObjects.WithLabelValues(mode).Write(&out); err != nil {
			t.Fatal(err)
		}
		return out.GetCounter().GetValue()
	}
	t.Run("server", func(t *testing.T) {
		in := newInstall(t)
		in.mustUpload(in.repo, "a", "aaaa-content")
		in.mustUpload(in.repo, "b", "bbbb-content")
		r := in.newRig(t)
		r.metrics = backup.NewMetrics(observability.NewMetrics(meta.RoleBackup))
		if _, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true}); err != nil {
			t.Fatal(err)
		}
		if counter(r.metrics, "server") != 2 || counter(r.metrics, "stream") != 0 {
			t.Fatalf("server %v stream %v", counter(r.metrics, "server"), counter(r.metrics, "stream"))
		}
	})
	t.Run("stream after a rejected server-side copy", func(t *testing.T) {
		in := newInstall(t)
		in.mustUpload(in.repo, "a", "aaaa-content")
		in.mustUpload(in.repo, "b", "bbbb-content")
		r := in.newRig(t)
		r.tgt = storeWithoutServerCopy{r.tgt}
		r.metrics = backup.NewMetrics(observability.NewMetrics(meta.RoleBackup))
		res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
		if err != nil || res.BlobsCopied != 2 {
			t.Fatalf("%+v %v", res, err)
		}
		if counter(r.metrics, "server") != 0 || counter(r.metrics, "stream") != 2 {
			t.Fatalf("server %v stream %v", counter(r.metrics, "server"), counter(r.metrics, "stream"))
		}
	})
}

// TestR_BAK_006_ADamagedSourceBlobIsRecordedAndTheRunCompletesWithWarnings: the object of a referenced blob is removed from the
// bucket; the manifest lists the digest under blobs.damaged and the blob row is untouched.
func TestR_BAK_006_ADamagedSourceBlobIsRecordedAndTheRunCompletesWithWarnings(t *testing.T) {
	in := newInstall(t)
	in.mustUpload(in.repo, "good", "good-content")
	bad := in.mustUpload(in.repo, "bad", "bad-content")
	if err := in.obj.Delete(context.Background(), blobKey(in, bad)); err != nil {
		t.Fatal(err)
	}
	r := in.newRig(t)
	res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed_with_warnings" || res.DamagedBlobs != 1 {
		t.Fatalf("%+v", res)
	}
	m := manifestOf(t, r.tgt, r.cfg, res.BackupID)
	if len(m.Blobs.Damaged) != 1 || m.Blobs.Damaged[0].SHA256 != bad.Hex() || m.Blobs.Damaged[0].Status != "missing" {
		t.Fatalf("damaged = %+v", m.Blobs.Damaged)
	}
	if poolHas(t, r.tgt, r.cfg, fmtDigest(bad)) {
		t.Fatal("a damaged blob must not be in the pool")
	}
	if st, _ := in.blobState(bad); st != "live" {
		t.Fatalf("the blob row is %q", st)
	}
	if got := scalarText(in, `SELECT state FROM trove.backup_run`); got != "completed_with_warnings" {
		t.Fatalf("run state = %s", got)
	}
}

// TestR_BAK_008_ExcludeRemoteCaches: a blob that only a remote repository's cache references is left out of the pool and the blob
// list; a blob shared with a local repository is in; the database is exported in full.
func TestR_BAK_008_ExcludeRemoteCaches(t *testing.T) {
	in := newInstall(t)
	remote := in.newRepo("generic-remote", "remote")
	local := in.mustUpload(in.repo, "local.bin", "local-only")
	cached := in.mustUpload(remote, "cached.bin", "cache-only")
	shared := in.mustUpload(in.repo, "shared-l.bin", "shared-content")
	if _, err := in.upload(context.Background(), remote, "shared-r.bin", []byte("shared-content")); err != nil {
		t.Fatal(err)
	}
	r := in.newRig(t)
	res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true, ExcludeRemoteCaches: true})
	if err != nil {
		t.Fatal(err)
	}
	m := manifestOf(t, r.tgt, r.cfg, res.BackupID)
	if !m.Options.ExcludeRemoteCaches || m.Blobs.Referenced != 2 {
		t.Fatalf("options %+v blobs %+v", m.Options, m.Blobs)
	}
	for d, want := range map[[32]byte]bool{local: true, shared: true, cached: false} {
		if poolHas(t, r.tgt, r.cfg, fmtDigest(d)) != want {
			t.Fatalf("pool has %x = %v, want %v", d[:4], !want, want)
		}
	}
	if tf, _ := m.Table("items"); tf.Rows != in.number(`SELECT count(*) FROM trove.items`) {
		t.Fatalf("items exported = %d: the database is exported in full", tf.Rows)
	}
	if tf, _ := m.Table("blobs"); tf.Rows != 3 {
		t.Fatalf("blobs exported = %d, want the 3 referenced rows", tf.Rows)
	}
}

// TestR_BAK_005_APartitionedTableAndAnUnregisteredTable: a table created by hand is exported and listed under unregisteredTables.
func TestR_BAK_005_AnUnregisteredTableIsExportedAndListed(t *testing.T) {
	in := newInstall(t)
	in.exec(`CREATE TABLE trove.x_new (a int PRIMARY KEY)`)
	in.exec(`INSERT INTO trove.x_new VALUES (1), (2), (3)`)
	r := in.newRig(t)
	res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.UnregisteredTables) != 1 || res.UnregisteredTables[0] != "x_new" {
		t.Fatalf("unregistered = %v", res.UnregisteredTables)
	}
	if tf, ok := manifestOf(t, r.tgt, r.cfg, res.BackupID).Table("x_new"); !ok || tf.Rows != 3 {
		t.Fatalf("x_new = %+v", tf)
	}
}

func blobKey(in *install, d [32]byte) string {
	h := strings.ToLower(hex32(d))
	k := "sha256/" + h[0:2] + "/" + h[2:4] + "/" + h
	return in.prefix + "/" + k
}

func hex32(d [32]byte) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i, v := range d {
		b[2*i], b[2*i+1] = digits[v>>4], digits[v&0xf]
	}
	return string(b)
}

var _ = errors.New
