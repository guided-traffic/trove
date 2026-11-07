package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func dg(b byte) Digest {
	var d Digest
	for i := range d {
		d[i] = b
	}
	return d
}

func TestBackupID(t *testing.T) {
	t.Run("R-BAK-002 an ID is the UTC start time and 16 random bits", func(t *testing.T) {
		start := time.Date(2026, 10, 28, 2, 0, 0, 0, time.FixedZone("x", 3600))
		id, err := NewID(start, bytes.NewReader([]byte{0x7f, 0x3a}))
		if err != nil || id != "20261028T010000Z-7f3a" {
			t.Fatalf("NewID = %q, %v", id, err)
		}
		if !id.Time().Equal(start) {
			t.Fatalf("Time() = %v", id.Time())
		}
		if _, err := ParseID(string(id)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-BAK-002 lexical order is chronological order", func(t *testing.T) {
		a, _ := ParseID("20261027T020000Z-ffff")
		b, _ := ParseID("20261028T020000Z-0000")
		if !a.Less(b) || b.Less(a) {
			t.Fatal("order")
		}
	})
	t.Run("R-BAK-002 malformed IDs are refused", func(t *testing.T) {
		for _, s := range []string{"", "20261028T020000Z", "20261028T020000Z-7F3A", "20261328T020000Z-7f3a", "x/../y"} {
			if _, err := ParseID(s); err == nil {
				t.Errorf("ParseID(%q) succeeded", s)
			}
		}
	})
}

func TestLayout(t *testing.T) {
	l := Layout{Prefix: "trove"}
	d := dg(0xab)
	id := ID("20261028T020000Z-7f3a")
	t.Run("R-BAK-002 the keys are the documented layout", func(t *testing.T) {
		h := d.Hex()
		want := map[string]string{
			l.Marker():                  "trove/trove-backup-target.json",
			l.Pool(d):                   "trove/pool/sha256/ab/ab/" + h,
			l.Manifest(id):              "trove/backups/20261028T020000Z-7f3a/manifest.json",
			l.BlobList(id):              "trove/backups/20261028T020000Z-7f3a/blobs.idx.zst",
			l.Table(id, "items"):        "trove/backups/20261028T020000Z-7f3a/db/items.copy.zst",
			(Layout{}).Marker():         "trove-backup-target.json",
			(Layout{}).Manifest(id):     "backups/20261028T020000Z-7f3a/manifest.json",
			(Layout{}).Pool(dg(0x01)):   "pool/sha256/01/01/" + dg(0x01).Hex(),
			l.PoolPrefix() + "x":        "trove/pool/sha256/x",
			l.BackupsPrefix() + "y":     "trove/backups/y",
			l.Dir(id) + ManifestName:    l.Manifest(id),
			l.Dir(id) + TablesDir + "/": "trove/backups/20261028T020000Z-7f3a/db/",
		}
		for got, w := range want {
			if got != w {
				t.Errorf("got %q, want %q", got, w)
			}
		}
	})
	t.Run("R-BAK-017 ParsePool accepts the layout only", func(t *testing.T) {
		if got, ok := l.ParsePool(l.Pool(d)); !ok || got != d {
			t.Fatalf("ParsePool = %v, %v", got, ok)
		}
		for _, k := range []string{
			"trove/pool/sha256/junk", "trove/pool/sha256/ab/ab/" + strings.ToUpper(d.Hex()),
			"trove/pool/sha256/cd/ab/" + d.Hex(), "trove/pool/sha256/ab/cd/" + d.Hex(), "other/pool/sha256/ab/ab/" + d.Hex(),
			"trove/pool/sha256/ab/ab/" + d.Hex() + "/x",
		} {
			if _, ok := l.ParsePool(k); ok {
				t.Errorf("ParsePool(%q) accepted", k)
			}
		}
	})
	t.Run("R-BAK-016 ParseBackupKey splits a key into the ID and the inner path", func(t *testing.T) {
		got, inner, ok := l.ParseBackupKey(l.Table(id, "items"))
		if !ok || got != id || inner != "db/items.copy.zst" {
			t.Fatalf("ParseBackupKey = %v, %q, %v", got, inner, ok)
		}
		if _, _, ok := l.ParseBackupKey("trove/backups/notanid/manifest.json"); ok {
			t.Fatal("accepted a bad ID")
		}
	})
}

func TestMarker(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	m := NewMarker("inst", "inc1", now)
	t.Run("R-BAK-002 a marker round-trips and checks the lineage", func(t *testing.T) {
		raw, _ := m.Encode()
		got, err := DecodeMarker(raw)
		if err != nil || got.Check("inst", "inc1") != nil {
			t.Fatalf("DecodeMarker = %+v, %v", got, err)
		}
		if err := got.Check("inst", "inc2"); !errors.Is(err, ErrForeignTarget) || !strings.Contains(err.Error(), "inc1") {
			t.Fatalf("Check(other incarnation) = %v", err)
		}
		if err := got.Check("other", "inc1"); !errors.Is(err, ErrForeignTarget) {
			t.Fatalf("Check(other installation) = %v", err)
		}
	})
	t.Run("R-BAK-002 adoption is a compare-and-swap on the lineage the installation was restored from", func(t *testing.T) {
		if !m.CanAdopt("inst", "inc1") || m.CanAdopt("inst", "inc9") || m.CanAdopt("", "") {
			t.Fatal("CanAdopt")
		}
		a := m.Adopt("inst", "inc2", now.Add(time.Hour))
		if a.Incarnation != "inc2" || a.AdoptedAt == nil || a.CanAdopt("inst", "inc1") {
			t.Fatalf("Adopt = %+v", a)
		}
	})
	t.Run("R-BAK-003 a higher marker format is refused", func(t *testing.T) {
		_, err := DecodeMarker([]byte(`{"format":2,"installationId":"a","incarnation":"b"}`))
		var uf *UnknownFormatError
		if !errors.As(err, &uf) {
			t.Fatalf("err = %v", err)
		}
	})
}

func sampleManifest() *Manifest {
	prev := ID("20261027T020000Z-1c9e")
	at := time.Date(2026, 10, 28, 2, 0, 1, 87_000_000, time.UTC)
	return &Manifest{
		Format: 1, BackupID: "20261028T020000Z-7f3a", InstallationID: "0191c2d4", Incarnation: "0192f0a1",
		Trove:     TroveInfo{Version: "1.0.0", Revision: "3f2c1a"},
		Schema:    SchemaInfo{Step: 57, Steps: []SchemaStep{{Step: 1, Name: "00001_core", SHA256: strings.Repeat("a", 64)}}},
		Postgres:  PostgresRef{ServerVersion: "17.6", Isolation: "repeatable read"},
		StartedAt: at, SnapshotAt: at.Add(time.Second), CompletedAt: at.Add(time.Hour), PreviousBackupID: &prev,
		Source: Source{Storage: StorageRef{Bucket: "trove-blobs", Prefix: "trove"}, RevocationJournal: true},
		Tables: []TableFile{{
			Name: "items", Class: "data", File: "db/items.copy.zst",
			Columns: [][2]string{{"id", "bigint"}, {"path", "text"}}, Rows: 3, Bytes: 100, CompressedBytes: 40, SHA256: strings.Repeat("b", 64),
		}},
		Sequences:  []Sequence{{Name: "items_id_seq", LastValue: 9, IsCalled: true}},
		Blobs:      BlobsInfo{File: BlobListName, SHA256: strings.Repeat("c", 64), Referenced: 2, Damaged: []DamagedBlob{{SHA256: strings.Repeat("d", 64), Status: "missing"}}},
		Secrets:    SecretsInfo{KeyIDs: []string{"2026-10"}},
		Encryption: Encryption{SSE: "aws:kms"},
	}
}

func TestManifest(t *testing.T) {
	t.Run("R-BAK-003 a manifest round-trips and validates against the JSON Schema", func(t *testing.T) {
		raw, err := sampleManifest().Encode()
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeManifest(raw)
		if err != nil || got.BackupID != "20261028T020000Z-7f3a" || got.Tables[0].Columns[1][1] != "text" {
			t.Fatalf("DecodeManifest = %+v, %v", got, err)
		}
		c := jsonschema.NewCompiler()
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ManifestSchema))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource("manifest.schema.json", doc); err != nil {
			t.Fatal(err)
		}
		compiled, err := c.Compile("manifest.schema.json")
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(inst); err != nil {
			t.Fatalf("the schema rejects the manifest: %v", err)
		}
		// The schema must also reject a malformed manifest, or it proves nothing.
		var loose map[string]any
		_ = json.Unmarshal(raw, &loose)
		loose["backupId"] = "nope"
		inst, _ = jsonschema.UnmarshalJSON(bytes.NewReader(mustJSON(t, loose)))
		if compiled.Validate(inst) == nil {
			t.Fatal("the schema accepted a bad backupId")
		}
	})
	t.Run("R-BAK-003 a reader refuses a higher format and ignores unknown fields", func(t *testing.T) {
		raw, _ := sampleManifest().Encode()
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["futureField"] = []int{1}
		if _, err := DecodeManifest(mustJSON(t, m)); err != nil {
			t.Fatalf("unknown field: %v", err)
		}
		m["format"] = 2
		_, err := DecodeManifest(mustJSON(t, m))
		var uf *UnknownFormatError
		if !errors.As(err, &uf) || uf.Got != 2 || !strings.Contains(err.Error(), "format 2") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-BAK-003 Validate names what is missing", func(t *testing.T) {
		m := sampleManifest()
		m.Tables = append(m.Tables, m.Tables[0])
		m.Secrets.KeyIDs = nil
		m.Blobs.Damaged[0].Status = "gone"
		err := m.Validate()
		if err == nil {
			t.Fatal("no error")
		}
		for _, w := range []string{"listed twice", "secrets.keyIds", "status"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error lacks %q: %v", w, err)
			}
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func entries(bs ...byte) []BlobEntry {
	var out []BlobEntry
	for _, b := range bs {
		out = append(out, BlobEntry{Digest: dg(b), Size: int64(b)})
	}
	return out
}

func TestBlobList(t *testing.T) {
	t.Run("R-BAK-002 a blob list round-trips as sorted text lines", func(t *testing.T) {
		raw, err := EncodeBlobList(entries(1, 2, 200), 3)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewBlobListReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, err := ReadAll(r)
		if err != nil || len(got) != 3 || got[2].Size != 200 {
			t.Fatalf("ReadAll = %v, %v", got, err)
		}
	})
	t.Run("R-BAK-002 the writer refuses an entry that breaks the order", func(t *testing.T) {
		w, _ := NewBlobListWriter(io.Discard, 1)
		if err := w.Add(entries(5)[0]); err != nil {
			t.Fatal(err)
		}
		if w.Add(entries(5)[0]) == nil || w.Add(entries(4)[0]) == nil {
			t.Fatal("accepted a duplicate or a descending entry")
		}
	})
	t.Run("R-BAK-006 Diff lists every referenced entry and copies those the pool lacks", func(t *testing.T) {
		var list, cp []BlobEntry
		err := Diff(&EntryList{Entries: entries(1, 3, 5, 7)}, &EntryList{Entries: entries(2, 3, 7, 9)},
			func(e BlobEntry) error { list = append(list, e); return nil },
			func(e BlobEntry) error { cp = append(cp, e); return nil })
		if err != nil || len(list) != 4 || len(cp) != 2 || cp[0].Digest != dg(1) || cp[1].Digest != dg(5) {
			t.Fatalf("list=%v copy=%v err=%v", list, cp, err)
		}
	})
	t.Run("R-BAK-006 without a previous list everything is copied; the previous list minus damaged blobs is what the pool has", func(t *testing.T) {
		var cp []BlobEntry
		_ = Diff(&EntryList{Entries: entries(1, 2)}, &EntryList{}, nil, func(e BlobEntry) error { cp = append(cp, e); return nil })
		if len(cp) != 2 {
			t.Fatalf("copy = %v", cp)
		}
		cp = nil
		known := Without(&EntryList{Entries: entries(1, 2)}, map[string]bool{dg(2).Hex(): true})
		_ = Diff(&EntryList{Entries: entries(1, 2)}, known, nil, func(e BlobEntry) error { cp = append(cp, e); return nil })
		if len(cp) != 1 || cp[0].Digest != dg(2) {
			t.Fatalf("a damaged blob in the previous list must be copied again: %v", cp)
		}
	})
	t.Run("R-BAK-006 Diff refuses an unsorted referenced set", func(t *testing.T) {
		if err := Diff(&EntryList{Entries: entries(3, 1)}, &EntryList{}, nil, nil); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("R-BAK-017 Union is the sorted keep set without duplicates", func(t *testing.T) {
		got, err := ReadAll(Union(&EntryList{Entries: entries(1, 4)}, &EntryList{Entries: entries(1, 2, 9)}, &EntryList{}))
		if err != nil || len(got) != 4 || got[1].Digest != dg(2) || got[3].Digest != dg(9) {
			t.Fatalf("Union = %v, %v", got, err)
		}
	})
}

func TestSumWriter(t *testing.T) {
	t.Run("R-BAK-003 the SHA-256 and the size are computed while the file streams", func(t *testing.T) {
		var buf bytes.Buffer
		s := NewSumWriter(&buf)
		_, _ = s.Write([]byte("abc"))
		if s.Bytes() != 3 || s.SHA256() != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
			t.Fatalf("sum = %s, %d", s.SHA256(), s.Bytes())
		}
	})
}
