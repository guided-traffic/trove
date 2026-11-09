package index

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

func entry(suite, comp, arch, pkg, ver string) rows.IndexEntry {
	return rows.IndexEntry{
		Suite: suite, Component: comp, Architecture: arch, Package: pkg, Version: ver,
		Control:  "Package: " + pkg + "\nVersion: " + ver + "\nArchitecture: " + arch + "\nDescription: x\n long\n .\n text\n",
		Filename: "pool/" + pkg[:1] + "/" + pkg + "/" + pkg + "_" + ver + "_" + arch + ".deb", Size: 1234,
		MD5: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64),
	}
}

func paragraphs(t *testing.T, b []byte) []control.Paragraph {
	t.Helper()
	r := control.NewReader(bytes.NewReader(b), false)
	var out []control.Paragraph
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
}

// R-DEB-011: a paragraph is the uploaded control data followed by Filename, Size, MD5sum and SHA256, ordered by name, dpkg version and
// architecture, and a field of the upload that trove computes is replaced.
func TestR_DEB_011_PackagesOrderAndFields(t *testing.T) {
	a := entry("stable", "main", "amd64", "hello", "2.10-3")
	a.Control += "Size: 1\nFilename: elsewhere\n"
	got, err := Packages([]rows.IndexEntry{entry("stable", "main", "amd64", "zlib", "1.0"), entry("stable", "main", "amd64", "hello", "2.9-1"), a})
	if err != nil {
		t.Fatal(err)
	}
	ps := paragraphs(t, got)
	if len(ps) != 3 || ps[0].Value("Version") != "2.9-1" || ps[1].Value("Version") != "2.10-3" || ps[2].Value("Package") != "zlib" {
		t.Fatalf("order: %s", got)
	}
	names := []string{}
	for _, f := range ps[1].Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names[len(names)-4:], ",") != "Filename,Size,MD5sum,SHA256" || ps[1].Value("Size") != "1234" || ps[1].Value("Filename") == "elsewhere" {
		t.Errorf("computed fields: %v", names)
	}
	if ps[1].Value("Description") != "x\nlong\n.\ntext" {
		t.Errorf("description = %q", ps[1].Value("Description"))
	}
	again, _ := Packages([]rows.IndexEntry{a, entry("stable", "main", "amd64", "hello", "2.9-1"), entry("stable", "main", "amd64", "zlib", "1.0")})
	if !bytes.Equal(got, again) {
		t.Error("equal rows in another order give other bytes")
	}
}

// R-DEB-011: gzip carries no name and no time; both compressions round-trip and are reproducible.
func TestR_DEB_011_CompressionIsReproducible(t *testing.T) {
	data := bytes.Repeat([]byte("Package: x\n\n"), 100)
	g := Gzip(data)
	if !bytes.Equal(g, Gzip(data)) || g[3] != 0 || !bytes.Equal(g[4:8], []byte{0, 0, 0, 0}) {
		t.Error("gzip is not reproducible or carries a name or a time")
	}
	zr, err := gzip.NewReader(bytes.NewReader(g))
	if err != nil {
		t.Fatal(err)
	}
	if back, _ := io.ReadAll(zr); !bytes.Equal(back, data) {
		t.Error("gzip round trip")
	}
	x1, err := XZ(data)
	if err != nil {
		t.Fatal(err)
	}
	x2, _ := XZ(data)
	xr, err := xz.NewReader(bytes.NewReader(x1))
	if err != nil {
		t.Fatal(err)
	}
	if back, _ := io.ReadAll(xr); !bytes.Equal(back, data) || !bytes.Equal(x1, x2) {
		t.Error("xz round trip or reproducibility")
	}
}

// R-DEB-011: all files are listed under every architecture and in binary-all; the Release lists every index file with its digests.
func TestR_DEB_011_BuildLayoutAndRelease(t *testing.T) {
	entries := []rows.IndexEntry{
		entry("stable", "main", "amd64", "hello", "1.0"), entry("stable", "main", "all", "docs", "1.0"), entry("stable", "contrib", "arm64", "extra", "1.0"),
	}
	date := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	set := Settings{Origin: "trove", Label: "deb-local", Description: "web/deb-local stable", Architectures: []string{"amd64"}, XZ: true}
	st, err := Build("stable", entries, nil, set, date)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Architectures, " ") != "amd64 arm64" || strings.Join(st.Components, " ") != "contrib main" {
		t.Errorf("lists: %v %v", st.Architectures, st.Components)
	}
	// 2 components x (2 architectures + all) x (gz, xz)
	if len(st.Files) != 12 {
		t.Fatalf("%d files", len(st.Files))
	}
	byPath := map[string]File{}
	for _, f := range st.Files {
		byPath[f.Path] = f
	}
	listed := map[string]File{}
	for _, f := range st.Listed {
		listed[f.Path] = f
	}
	unzip := func(p string) []control.Paragraph {
		zr, err := gzip.NewReader(bytes.NewReader(byPath[p].Body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		return paragraphs(t, b)
	}
	for p, want := range map[string]int{
		"main/binary-amd64/Packages.gz":    2, // hello and the all file
		"main/binary-arm64/Packages.gz":    1, // the all file only
		"main/binary-all/Packages.gz":      1,
		"contrib/binary-amd64/Packages.gz": 0,
		"contrib/binary-arm64/Packages.gz": 1,
	} {
		if n := len(unzip(p)); n != want {
			t.Errorf("%s lists %d, want %d", p, n, want)
		}
	}
	rel := paragraphs(t, st.Release)
	if len(rel) != 1 {
		t.Fatalf("release paragraphs: %d", len(rel))
	}
	r := rel[0]
	for k, want := range map[string]string{
		"Origin": "trove", "Label": "deb-local", "Suite": "stable", "Codename": "stable", "Date": "Sun, 08 Nov 2026 10:00:00 UTC",
		"Architectures": "amd64 arm64", "Components": "contrib main", "Acquire-By-Hash": "yes", "Description": "web/deb-local stable",
	} {
		if r.Value(k) != want {
			t.Errorf("%s = %q, want %q", k, r.Value(k), want)
		}
	}
	if _, ok := r.Get("Valid-Until"); ok {
		t.Error("a local Release has a Valid-Until")
	}
	for _, field := range []string{"MD5Sum", "SHA256"} {
		lines := strings.Split(r.Value(field), "\n")[1:]
		// the 12 index files and the checksums of the 6 uncompressed Packages that APT checks the decompressed content against
		if len(lines) != 18 {
			t.Fatalf("%s lists %d entries", field, len(lines))
		}
		for _, l := range lines {
			parts := strings.Fields(l)
			f := listed[parts[2]]
			if _, stored := byPath[parts[2]]; !stored && strings.Contains(parts[2], ".") {
				t.Errorf("%s lists %s, which is no file of the suite", field, parts[2])
			}
			sum := f.SHA256
			if field == "MD5Sum" {
				sum = f.MD5
			}
			if parts[0] != sum || parts[1] != strconv.FormatInt(f.Size, 10) {
				t.Errorf("%s line %q does not match %s", field, l, parts[2])
			}
		}
	}
	again, _ := Build("stable", entries, nil, set, date)
	if !bytes.Equal(st.Release, again.Release) {
		t.Error("two builds from equal rows give different Release bytes")
	}
}

// R-DEB-014: a suite whose last file left keeps the empty indexes of the components it had.
func TestR_DEB_014_ExtraComponentsKeepEmptyIndexes(t *testing.T) {
	st, err := Build("stable", nil, []string{"main"}, Settings{Architectures: []string{"amd64"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 2 || st.Components[0] != "main" {
		t.Errorf("files %d components %v", len(st.Files), st.Components)
	}
}

// R-DEB-009: more than 32 architectures are refused.
func TestR_DEB_009_ArchitectureBound(t *testing.T) {
	var archs []string
	for i := 0; i < 33; i++ {
		archs = append(archs, "a"+strconv.Itoa(i))
	}
	if _, err := Build("stable", nil, nil, Settings{Architectures: archs}, time.Now()); err == nil {
		t.Error("33 architectures built")
	}
}

// R-DEB-010: the Date trails the database clock by 60 s, follows the previous Date by a second, waits for a key and never runs ahead of
// the database time minus 59 s.
func TestR_DEB_010_PickDate(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 30, 400_000_000, time.UTC)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	prev := func(s string) *time.Time { v := at(s); return &v }
	for _, tc := range []struct {
		name     string
		prev     *time.Time
		earliest time.Time
		date     string
		wait     time.Duration
	}{
		{"first build", nil, time.Time{}, "2026-11-08T09:59:30Z", 0},
		{"after an earlier build", prev("2026-11-08T09:59:10Z"), time.Time{}, "2026-11-08T09:59:30Z", 0},
		{"a build a second ago", prev("2026-11-08T09:59:30Z"), time.Time{}, "2026-11-08T09:59:31Z", 0},
		{"many builds: wait for the clock", prev("2026-11-08T09:59:31Z"), time.Time{}, "2026-11-08T09:59:32Z", 600 * time.Millisecond},
		{"a young key", nil, at("2026-11-08T09:59:31Z"), "2026-11-08T09:59:31Z", 0},
		{"a key that cannot sign yet waits", nil, at("2026-11-08T10:00:00Z"), "2026-11-08T10:00:00Z", 28*time.Second + 600*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, w := PickDate(now, tc.prev, tc.earliest)
			if !d.Equal(at(tc.date)) || w != tc.wait {
				t.Errorf("= %s, wait %s; want %s, wait %s", d.Format(time.RFC3339), w, tc.date, tc.wait)
			}
		})
	}
}

// R-DEB-012: the current generation, the superseded ones for the keep time and the newest three of the rest stay; what only dropped
// generations name is given up.
func TestR_DEB_012_Retention(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	old, recent := now.Add(-3*time.Hour), now.Add(-5*time.Minute)
	gen := func(n int64, sup *time.Time, h string) Generation {
		return Generation{Number: n, SupersededAt: sup, Files: map[string]string{"main/binary-amd64/Packages.gz": h, "main/binary-amd64/Packages.xz": "x" + h}}
	}
	gens := []Generation{gen(1, &old, "h1"), gen(2, &old, "h2"), gen(3, &old, "h3"), gen(4, &old, "h4"), gen(5, &recent, "h5"), gen(6, nil, "h6")}
	kept, dropped := Retain(gens, now, time.Hour)
	// the current 6 and the newest three superseded ones (5 is recent, 4 and 3 are old); 1 and 2 are older than keep and beyond three
	if len(kept) != 4 || kept[0].Number != 3 || len(dropped) != 2 || dropped[0].Number != 1 {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
	next := map[string]string{"main/binary-amd64/Packages.gz": "h7"}
	given := GivenUp(kept, dropped, gens[5].Files, next)
	want := []string{
		"main/binary-amd64/Packages.xz", "main/binary-amd64/by-hash/SHA256/h1", "main/binary-amd64/by-hash/SHA256/h2",
		"main/binary-amd64/by-hash/SHA256/xh1", "main/binary-amd64/by-hash/SHA256/xh2",
	}
	if strings.Join(given, ",") != strings.Join(want, ",") {
		t.Errorf("given up %v, want %v", given, want)
	}
	// An index file whose hash a kept generation names too stays.
	same := Generation{Number: 7, Files: map[string]string{"main/binary-amd64/Packages.gz": "h1"}}
	k2, d2 := Retain([]Generation{gen(1, &old, "h1"), gen(2, &old, "h2"), gen(3, &old, "h3"), gen(4, &old, "h4"), same}, now, time.Hour)
	for _, p := range GivenUp(k2, d2, same.Files, same.Files) {
		if strings.HasSuffix(p, "/h1") {
			t.Errorf("a path of the kept generation is given up: %s", p)
		}
	}
}

func FuzzPackages(f *testing.F) {
	f.Add("Package: a\nVersion: 1\n", "pool/a")
	f.Fuzz(func(t *testing.T, ctl, filename string) {
		e := rows.IndexEntry{Package: "a", Version: "1", Architecture: "all", Control: ctl, Filename: filename, Size: 1, MD5: "m", SHA256: "s"}
		out, err := Packages([]rows.IndexEntry{e, e})
		if err != nil {
			return
		}
		// Whatever the writer accepted reads back as exactly two paragraphs: a value cannot start a paragraph.
		r := control.NewReader(bytes.NewReader(out), false)
		n := 0
		for {
			if _, err := r.Next(); err != nil {
				break
			}
			n++
		}
		if n != 2 {
			t.Fatalf("%d paragraphs from two entries:\n%s", n, out)
		}
	})
}
