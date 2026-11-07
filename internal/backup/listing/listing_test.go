package listing_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
)

func setup(t *testing.T) (*targettest.Mem, format.Layout, *listing.Lister) {
	t.Helper()
	mem := targettest.New()
	l := format.Layout{Prefix: "trove"}
	return mem, l, &listing.Lister{Store: mem, Layout: l}
}

func TestR_BAK_018_ListsCompleteBackupsNewestFirst(t *testing.T) {
	mem, l, lister := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}, KeyIDs: []string{"old"}})
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"a", "b"}, Damaged: []string{"d"}, KeyIDs: []string{"new"}})
	out, err := lister.List(context.Background(), backup.ListOptions{})
	if err != nil || len(out) != 2 {
		t.Fatalf("err %v, %d entries", err, len(out))
	}
	n, o := out[0], out[1]
	if n.BackupID != fixture.ID(2) || o.BackupID != fixture.ID(1) {
		t.Fatalf("order: %s, %s", n.BackupID, o.BackupID)
	}
	if !n.Complete || !n.Damaged || o.Damaged || n.SchemaStep != 46 || n.BlobCount != 3 || n.TroveVersion != "0.1.0" || n.Incarnation != "inc-1" {
		t.Fatalf("newest = %+v", n)
	}
	if n.DatabaseBytes == 0 || n.BlobsCopied != 2 || n.Options == nil || n.Encryption == nil || n.Encryption.SSE != "AES256" {
		t.Fatalf("newest = %+v", n)
	}
	// R-BAK-028: after a rotation the older backup shows the old key ID and the newer the new one.
	if strings.Join(n.KeyIDs, ",") != "new" || strings.Join(o.KeyIDs, ",") != "old" {
		t.Fatalf("key IDs: %v %v", n.KeyIDs, o.KeyIDs)
	}
}

// R-BAK-002 criterion: a directory without a manifest is absent from list and present with --all as incomplete.
func TestR_BAK_018_IncompleteDirectoriesOnlyWithAll(t *testing.T) {
	mem, l, lister := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1)})
	mem.Remove(l.Manifest(fixture.ID(1)))
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(2)})
	out, _ := lister.List(context.Background(), backup.ListOptions{})
	if len(out) != 1 || out[0].BackupID != fixture.ID(2) {
		t.Fatalf("default list = %+v", out)
	}
	out, _ = lister.List(context.Background(), backup.ListOptions{All: true})
	if len(out) != 2 || out[1].BackupID != fixture.ID(1) || out[1].Complete || out[1].TroveVersion != "" {
		t.Fatalf("--all list = %+v", out)
	}
}

func TestR_BAK_018_JSONShape(t *testing.T) {
	mem, l, lister := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(2), Incomplete: true})
	out, _ := lister.List(context.Background(), backup.ListOptions{All: true})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) != 2 {
		t.Fatalf("%s: %v", raw, err)
	}
	if arr[1]["backupId"] != string(fixture.ID(1)) || arr[1]["schemaStep"] != float64(46) || arr[1]["incarnation"] != "inc-1" {
		t.Fatalf("complete entry: %v", arr[1])
	}
	if _, ok := arr[0]["troveVersion"]; ok || arr[0]["complete"] != false {
		t.Fatalf("incomplete entry carries manifest fields: %v", arr[0])
	}
}

func TestR_BAK_018_ListWritesNothing(t *testing.T) {
	mem, l, lister := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1)})
	before := len(mem.Keys())
	if _, err := lister.List(context.Background(), backup.ListOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"put", "writer", "delete", "copy"} {
		if mem.Calls[c] > before { // fixture writes use Set, which is not counted
			t.Fatalf("list issued %s", c)
		}
	}
	if len(mem.Keys()) != before {
		t.Fatal("list changed the target")
	}
}

func TestR_BAK_018_ManifestOfAHigherFormatIsExit6AndAStrayKeyIsIgnored(t *testing.T) {
	mem, l, lister := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1)})
	mem.Set(l.BackupsPrefix()+"stray.txt", []byte("x"))
	if out, err := lister.List(context.Background(), backup.ListOptions{}); err != nil || len(out) != 1 {
		t.Fatalf("stray key: %v %d", err, len(out))
	}
	raw, _ := mem.Data(l.Manifest(fixture.ID(1)))
	mem.Set(l.Manifest(fixture.ID(1)), []byte(strings.Replace(string(raw), `"format": 1`, `"format": 2`, 1)))
	_, err := lister.List(context.Background(), backup.ListOptions{})
	if backup.ExitCode(err) != backup.ExitPrecondition {
		t.Fatalf("exit %d err %v", backup.ExitCode(err), err)
	}
}

func TestR_BAK_018_ManifestNamingAnotherBackupIsRefused(t *testing.T) {
	mem, l, _ := setup(t)
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(1)})
	fixture.Put(t, mem, l, fixture.Spec{ID: fixture.ID(2)})
	raw, _ := mem.Data(l.Manifest(fixture.ID(2)))
	mem.Set(l.Manifest(fixture.ID(1)), raw)
	if _, err := listing.Scan(context.Background(), mem, l); err == nil || errors.Is(err, backup.ErrFindings) {
		t.Fatalf("err = %v", err)
	}
}
