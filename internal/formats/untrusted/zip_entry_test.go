package untrusted_test

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// entrySpec is one entry of an in-memory archive: its body and whether it is deflated.
type entrySpec struct {
	name    string
	body    []byte
	deflate bool
}

func zipOf(t *testing.T, specs ...entrySpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, s := range specs {
		method := zip.Store
		if s.deflate {
			method = zip.Deflate
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: s.name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(s.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func entryByName(t *testing.T, z *untrusted.ZipReader, name string) untrusted.ZipEntry {
	t.Helper()
	for _, e := range z.Entries() {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no entry %q", name)
	return untrusted.ZipEntry{}
}

// R-ARCH-028, Q-VULN-116: CheckEntry judges the bounds of one entry from its declaration, reads nothing, and ReadEntry
// refuses exactly what it refuses and reads what it accepts.
func TestR_ARCH_028_ZipCheckEntry(t *testing.T) {
	const max = 64 << 10
	stored := bytes.Repeat([]byte("abcdefghij"), 4000) // 40,000 bytes, stored: ratio 1
	big := bytes.Repeat([]byte("abcdefghij"), 8000)    // 80,000 bytes, above max
	flat := bytes.Repeat([]byte{'a'}, 1<<20)           // deflates to about 1 KiB: ratio above 100:1 and slack
	data := zipOf(t,
		entrySpec{name: "ok.json", body: stored},
		entrySpec{name: "big.json", body: big},
		entrySpec{name: "flat.json", body: flat, deflate: true},
		entrySpec{name: "dir/"},
	)
	l := untrusted.Limits{Entries: 100, UncompressedBytes: 16 << 20, StreamBytes: 16 << 20, Ratio: 100, ManifestEntryBytes: 1 << 20}
	z, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(data), int64(len(data)), l)
	if err != nil {
		t.Fatal(err)
	}
	other := zipOf(t, entrySpec{name: "foreign.json", body: stored})
	zo, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(other), int64(len(other)), l)
	if err != nil {
		t.Fatal(err)
	}
	foreign := zo.Entries()[0]

	tests := []struct {
		name  string
		z     *untrusted.ZipReader
		entry untrusted.ZipEntry
		max   int64
		want  string // "", "large" or "refused"
	}{
		{"within both bounds", z, entryByName(t, z, "ok.json"), max, ""},
		{"size above the given bound", z, entryByName(t, z, "big.json"), max, "large"},
		{"the same entry under a larger bound", z, entryByName(t, z, "big.json"), 1 << 20, ""},
		{"a bound that is not positive falls back to the limit", z, entryByName(t, z, "big.json"), 0, ""},
		{"ratio above the limit", z, entryByName(t, z, "flat.json"), 4 << 20, "large"},
		{"a directory", z, entryByName(t, z, "dir/"), max, "refused"},
		{"an entry of another archive", z, foreign, max, "refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkErr := tt.z.CheckEntry(tt.entry, tt.max)
			got := ""
			switch {
			case untrusted.IsTooLarge(checkErr):
				got = "large"
			case untrusted.IsRefused(checkErr):
				got = "refused"
			case checkErr != nil:
				t.Fatalf("CheckEntry: %v", checkErr)
			}
			if got != tt.want {
				t.Fatalf("CheckEntry = %q (%v), want %q", got, checkErr, tt.want)
			}
			body, readErr := tt.z.ReadEntry(tt.entry, tt.max)
			if tt.want == "" {
				if readErr != nil || len(body) != int(tt.entry.Size) {
					t.Fatalf("ReadEntry of an accepted entry: %d bytes, %v", len(body), readErr)
				}
				return
			}
			if readErr == nil || untrusted.IsTooLarge(readErr) != untrusted.IsTooLarge(checkErr) || untrusted.IsRefused(readErr) != untrusted.IsRefused(checkErr) {
				t.Fatalf("ReadEntry = %v, CheckEntry = %v: they disagree", readErr, checkErr)
			}
		})
	}
}

// R-ARCH-028, Q-VULN-116: the bound of the whole archive is not a bound of one entry; CheckEntry accepts what only ReadEntry
// refuses, so a caller can tell the two apart.
func TestR_ARCH_028_ZipCheckEntryLeavesTheArchiveTotalToReadEntry(t *testing.T) {
	body := bytes.Repeat([]byte("abcdefghij"), 4000)
	data := zipOf(t, entrySpec{name: "a.json", body: body}, entrySpec{name: "b.json", body: body})
	l := untrusted.Limits{Entries: 10, UncompressedBytes: 60000, StreamBytes: 1 << 20, Ratio: 100, ManifestEntryBytes: 1 << 20}
	z, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(data), int64(len(data)), l)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range z.Entries() {
		if err := z.CheckEntry(e, 0); err != nil {
			t.Fatalf("CheckEntry(%s): %v", e.Name, err)
		}
	}
	if _, err := z.ReadEntry(z.Entries()[0], 0); err != nil {
		t.Fatal(err)
	}
	if _, err := z.ReadEntry(z.Entries()[1], 0); !untrusted.IsTooLarge(err) {
		t.Fatalf("the second entry crosses the total read: %v", err)
	}
}

// R-ARCH-028, Q-VULN-116: with DeclaredEntryCap an entry above the cap, which CheckEntry rejects, adds nothing to the declared total, so
// entries a caller rejects unread do not use up StreamBytes; without it the same archive is refused at open. What is read stays
// bounded by UncompressedBytes.
func TestR_ARCH_028_ZipDeclaredEntryCapKeepsRejectedEntriesOutOfTheTotal(t *testing.T) {
	big := bytes.Repeat([]byte("abcdefghij"), 200_000) // 2 MB, stored
	data := zipOf(t, entrySpec{name: "a.json", body: big}, entrySpec{name: "b.json", body: big}, entrySpec{name: "c.json", body: []byte("{}")})
	l := untrusted.Limits{Entries: 10, UncompressedBytes: 3 << 20, StreamBytes: 3 << 20, Ratio: 100, ManifestEntryBytes: 1 << 20}
	if _, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(data), int64(len(data)), l); !untrusted.IsTooLarge(err) {
		t.Fatalf("without the cap: %v, want too large at open", err)
	}
	// A total that two entries of cap + 1 bytes (or of the cap) would cross: only entries above the cap that count as nothing open.
	l.StreamBytes = 1 << 10
	l.DeclaredEntryCap = l.ManifestEntryBytes
	z, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(data), int64(len(data)), l)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range z.Entries()[:2] {
		if err := z.CheckEntry(e, 0); !untrusted.IsTooLarge(err) {
			t.Errorf("CheckEntry(%s) = %v, want too large", e.Name, err)
		}
	}
	if _, err := z.ReadEntry(z.Entries()[2], 0); err != nil {
		t.Errorf("the small entry: %v", err)
	}
	// An entry read with a larger bound is still counted at its full size against what may be read into memory.
	if _, err := z.ReadEntry(z.Entries()[0], 4<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := z.ReadEntry(z.Entries()[1], 4<<20); !untrusted.IsTooLarge(err) {
		t.Errorf("the second large read: %v, want the memory bound", err)
	}
}
