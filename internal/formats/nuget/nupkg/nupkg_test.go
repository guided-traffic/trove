package nupkg

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
)

func inspect(t testing.TB, data []byte, lim Limits) (*Result, error) {
	t.Helper()
	return Inspect(context.Background(), bytes.NewReader(data), int64(len(data)), lim)
}

func TestR_NUGET_005_NupkgInspect(t *testing.T) {
	data := mustBytes(t, good())
	res, err := inspect(t, data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata.ID != "Acme.Widgets" || res.Metadata.Version != "1.2.3" || res.Metadata.Authors != "Acme" {
		t.Errorf("metadata = %+v", res.Metadata)
	}
	if string(res.Nuspec) != nuspecXML {
		t.Error("the .nuspec is not returned byte for byte")
	}
	if res.NuspecEntry.Name != "Acme.Widgets.nuspec" {
		t.Errorf("nuspec entry = %q", res.NuspecEntry.Name)
	}
	if got := len(res.Archive.Entries()); got != 5 {
		t.Errorf("entries = %d", got)
	}
	dll, ok := res.Archive.Lookup(`LIB\NET8.0\acme.widgets.dll`)
	if !ok || dll.Name != "lib/net8.0/Acme.Widgets.dll" {
		t.Errorf("lookup by another case and separator: %+v %v", dll, ok)
	}
	if _, ok := res.Archive.Lookup("lib/net8.0/missing.dll"); ok {
		t.Error("a missing part is found")
	}
	body, err := res.Archive.Read(context.Background(), dll, 4096)
	if err != nil || len(body) != 1000 {
		t.Errorf("read: %d bytes, %v", len(body), err)
	}
}

func TestR_NUGET_005_NupkgNameNormalization(t *testing.T) {
	bd := good().with(ent{name: `content\sub\file.txt`, body: []byte("x")}, ent{name: "tools/a%20b.txt", body: []byte("x")})
	res, err := inspect(t, mustBytes(t, bd), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := res.Archive.Lookup("content/sub/file.txt")
	if !ok || e.Name != "content/sub/file.txt" {
		t.Errorf("backslashes read as slashes: %+v %v", e, ok)
	}
	if e, ok := res.Archive.Lookup("tools/a b.txt"); !ok || e.Path != "tools/a b.txt" || e.Name != "tools/a%20b.txt" {
		t.Errorf("escapes decoded once for lookup: %+v %v", e, ok)
	}
}

func TestR_NUGET_005_NupkgNuspecAtRootOnly(t *testing.T) {
	// a nuspec below the root does not count, and one at the root does under any case
	nested := good().without("Acme.Widgets.nuspec").with(nuspecEnt("pkg/Acme.Widgets.nuspec"))
	if _, err := inspect(t, mustBytes(t, nested), Limits{}); !errors.Is(err, ErrNuspecCount) {
		t.Errorf("nested only: %v", err)
	}
	upper := good().without("Acme.Widgets.nuspec").with(nuspecEnt("ACME.WIDGETS.NUSPEC"))
	if res, err := inspect(t, mustBytes(t, upper), Limits{}); err != nil || res.NuspecEntry.Name != "ACME.WIDGETS.NUSPEC" {
		t.Errorf("upper case: %v", err)
	}
	both := good().with(nuspecEnt("lib/other.nuspec"))
	if _, err := inspect(t, mustBytes(t, both), Limits{}); err != nil {
		t.Errorf("a nested nuspec next to the root one is fine: %v", err)
	}
}

// The malformed-fixture table of R-NUGET-005: every row answers 400 with the
// code of the package pipeline and the reason that names the rule.
func TestR_NUGET_005_NupkgMalformed(t *testing.T) {
	dirty := mustBytes(t, good())
	short := dirty[:len(dirty)/2]
	zeros := bytes.Repeat([]byte{0}, 900<<10)
	tests := []struct {
		name   string
		data   []byte
		reason error
	}{
		{"no nuspec", mustBytes(t, good().without("Acme.Widgets.nuspec")), ErrNuspecCount},
		{"two nuspec entries at the root", mustBytes(t, good().with(nuspecEnt("acme.widgets.NUSPEC"))), ErrDuplicateName},
		{"two different nuspec names at the root", mustBytes(t, good().with(nuspecEnt("Other.nuspec"))), ErrNuspecCount},
		{"one name twice with different content", mustBytes(t, good().with(ent{name: "Acme.Widgets.nuspec", body: []byte("<package/>")})), ErrDuplicateName},
		{"one name twice that differs in case", mustBytes(t, good().with(ent{name: "LIB/NET8.0/acme.widgets.dll", body: []byte("x")})), ErrDuplicateName},
		{"one name twice that differs in the separator", mustBytes(t, good().with(ent{name: `lib\net8.0\Acme.Widgets.dll`, body: []byte("x")})), ErrDuplicateName},
		{"a name twice through an escape", mustBytes(t, good().with(ent{name: "lib/net8.0/Acme.Widgets%2Edll", body: []byte("x")})), ErrDuplicateName},
		{"a directory and a file of one name", mustBytes(t, good().with(ent{name: "lib", body: []byte("x")})), ErrDuplicateName},
		{"backslash traversal", mustBytes(t, good().with(ent{name: `..\evil.dll`, body: []byte("x")})), ErrName},
		{"slash traversal", mustBytes(t, good().with(ent{name: `lib/../../evil.dll`, body: []byte("x")})), ErrName},
		{"traversal through an escape", mustBytes(t, good().with(ent{name: `%2e%2e/evil.dll`, body: []byte("x")})), ErrName},
		{"traversal through an escaped dot", mustBytes(t, good().with(ent{name: `lib/.%2E/evil.dll`, body: []byte("x")})), ErrName},
		{"separator hidden in an escape", mustBytes(t, good().with(ent{name: `lib%2F..%2Fevil.dll`, body: []byte("x")})), ErrName},
		{"backslash hidden in an escape", mustBytes(t, good().with(ent{name: `lib%5Cevil.dll`, body: []byte("x")})), ErrName},
		{"absolute path", mustBytes(t, good().with(ent{name: `/etc/evil`, body: []byte("x")})), ErrName},
		{"absolute path with a backslash", mustBytes(t, good().with(ent{name: `\evil`, body: []byte("x")})), ErrName},
		{"drive letter", mustBytes(t, good().with(ent{name: `C:\evil.dll`, body: []byte("x")})), ErrName},
		{"drive letter without a separator", mustBytes(t, good().with(ent{name: `c:evil.dll`, body: []byte("x")})), ErrName},
		{"dot segment", mustBytes(t, good().with(ent{name: `lib/./a.dll`, body: []byte("x")})), ErrName},
		{"empty segment", mustBytes(t, good().with(ent{name: `lib//a.dll`, body: []byte("x")})), ErrName},
		{"double-dot with a trailing dot", mustBytes(t, good().with(ent{name: `.../a.dll`, body: []byte("x")})), ErrName},
		{"NUL in a name", mustBytes(t, good().with(ent{name: "lib/a\x00.dll", body: []byte("x")})), ErrName},
		{"control character in a name", mustBytes(t, good().with(ent{name: "lib/a\n.dll", body: []byte("x")})), ErrName},
		{"invalid UTF-8 in a name", mustBytes(t, good().with(ent{name: "lib/\xff\xfe.dll", body: []byte("x")})), ErrName},
		{"escape that decodes to invalid UTF-8", mustBytes(t, good().with(ent{name: "lib/%ff.dll", body: []byte("x")})), ErrName},
		{"name longer than the bound", mustBytes(t, good().with(ent{name: strings.Repeat("a", MaxNameBytes+1), body: []byte("x")})), ErrName},
		{"central name differs from the local header name", mustBytes(t, good().with(ent{name: "lib/a.dll", localName: "lib/b.dll", body: []byte("x")})), ErrLocalMismatch},
		{"local name differs in length", mustBytes(t, good().with(ent{name: "lib/a.dll", localName: "lib/a.dl", body: []byte("x")})), ErrLocalMismatch},
		{"local name differs in case", mustBytes(t, good().with(ent{name: "lib/a.dll", localName: "lib/A.dll", body: []byte("x")})), ErrLocalMismatch},
		{"the nuspec is named differently in its local header", mustBytes(t, build{ents: []ent{{name: "Acme.Widgets.nuspec", localName: "evil.nuspec", body: []byte(nuspecXML)}}}), ErrLocalMismatch},
		{"encrypted entry", mustBytes(t, good().with(ent{name: "lib/secret.dll", body: []byte("x"), flags: 0x1})), ErrEncrypted},
		{"strong encryption", mustBytes(t, good().with(ent{name: "lib/secret.dll", body: []byte("x"), flags: 0x40})), ErrEncrypted},
		{"split archive", mustBytes(t, func() build { b := good(); b.disk = 1; return b }()), ErrMultiDisk},
		{"an entry whose data runs over the next entry", mustBytes(t, overlapping()), ErrOverlap},
		{"not a zip", []byte("this is not an archive at all, not even close"), ErrNotZip},
		{"empty input", nil, ErrNotZip},
		{"truncated", short, ErrNotZip},
		{"gzip", []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff, 0x03, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrNotZip},
		{"declares 10 GiB", mustBytes(t, good().with(ent{name: "lib/huge.bin", body: []byte("x"), usize: 10 << 30, zip64: true})), ErrLimit},
		{"declares sizes beyond int64", mustBytes(t, good().with(ent{name: "lib/huge.bin", body: []byte("x"), usize: 1 << 62, zip64: true}).with(ent{name: "lib/huge2.bin", body: []byte("x"), usize: 1 << 62, zip64: true})), ErrLimit},
		{"a nuspec that inflates by more than the ratio", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: zeros}}}), ErrLimit},
		{"a nuspec larger than the bound", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: bytes.Repeat([]byte("<!-- x -->"), 150_000), stored: true}}}), nuspec.ErrTooLarge},
		{"a nuspec that is larger than it declares", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(nuspecXML), usize: 20}}}), ErrEntry},
		{"a nuspec that is smaller than it declares", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(nuspecXML), usize: int64(len(nuspecXML)) + 50}}}), ErrEntry},
		{"a nuspec with a wrong checksum", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(nuspecXML), crc: 12345}}}), ErrEntry},
		{"a nuspec in a method nobody reads", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(nuspecXML), method: 99}}}), ErrEntry},
		{"a nuspec with a document type declaration", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(`<!DOCTYPE package [<!ENTITY a "b">]><package><metadata><id>a</id><version>1</version></metadata></package>`)}}}), nuspec.ErrDoctype},
		{"a nuspec that is no XML", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte("garbage")}}}), nuspec.ErrNotXML},
		{"a nuspec without an id", mustBytes(t, build{ents: []ent{{name: "a.nuspec", body: []byte(`<package><metadata><version>1</version></metadata></package>`)}}}), nuspec.ErrMissingFields},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := inspect(t, tc.data, Limits{})
			if err == nil {
				t.Fatalf("accepted: %+v", res.Metadata)
			}
			var e *errs.Error
			if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != nuspec.CodeInvalidPackage || !IsRefusal(err) {
				t.Fatalf("error = %#v, want a 400 %s", err, nuspec.CodeInvalidPackage)
			}
			if !errors.Is(err, tc.reason) {
				t.Errorf("reason = %v, want %v", err, tc.reason)
			}
		})
	}
}

func TestR_NUGET_005_NupkgEntryCountLimit(t *testing.T) {
	bd := good()
	for i := range 20 {
		bd = bd.with(ent{name: "content/f" + string(rune('a'+i)) + ".txt", body: []byte("x")})
	}
	data := mustBytes(t, bd)
	if _, err := inspect(t, data, Limits{MaxEntries: 24}); !errors.Is(err, ErrLimit) {
		t.Errorf("25 entries against 24: %v", err)
	}
	if _, err := inspect(t, data, Limits{MaxEntries: 25}); err != nil {
		t.Errorf("25 entries against 25: %v", err)
	}
	if _, err := inspect(t, data, Limits{}); err != nil {
		t.Errorf("the default bound admits it: %v", err)
	}
}

func TestR_NUGET_005_NupkgUncompressedLimit(t *testing.T) {
	data := mustBytes(t, good().with(ent{name: "lib/big.bin", body: []byte("x"), usize: 1 << 20}))
	if _, err := inspect(t, data, Limits{MaxUncompressedBytes: 1 << 19}); !errors.Is(err, ErrLimit) {
		t.Errorf("declared total above the bound: %v", err)
	}
	// a declaration is not inflated: the archive opens when the bound admits it
	if _, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), Limits{MaxUncompressedBytes: 2 << 20}); err != nil {
		t.Errorf("declared total within the bound: %v", err)
	}
}

func TestR_NUGET_005_NupkgZip64(t *testing.T) {
	bd := good()
	bd.zip64 = true
	for i := range bd.ents {
		bd.ents[i].zip64 = true
	}
	res, err := inspect(t, mustBytes(t, bd), Limits{})
	if err != nil {
		t.Fatalf("a zip64 archive: %v", err)
	}
	if res.Metadata.ID != "Acme.Widgets" {
		t.Errorf("id = %q", res.Metadata.ID)
	}
	// a zip64 locator that lies
	data := mustBytes(t, bd)
	i := bytes.LastIndex(data, []byte{0x50, 0x4b, 0x06, 0x07})
	data[i+8] ^= 0xff
	if _, err := inspect(t, data, Limits{}); !errors.Is(err, ErrNotZip) {
		t.Errorf("a broken zip64 locator: %v", err)
	}
}

// An end record in the comment must not be taken for the archive's own.
func TestR_NUGET_005_NupkgCommentCannotHideAnArchive(t *testing.T) {
	inner := mustBytes(t, build{ents: []ent{{name: "evil.nuspec", body: []byte(strings.Replace(nuspecXML, "Acme.Widgets", "Evil", 1))}}})
	outer := good()
	outer.comment = string(inner)
	data := mustBytes(t, outer)
	res, err := inspect(t, data, Limits{})
	if err == nil && res.Metadata.ID != "Acme.Widgets" {
		t.Errorf("the archive in the comment decided the identity: %q", res.Metadata.ID)
	}
}

type countingReaderAt struct {
	r    *bytes.Reader
	read atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read.Add(int64(n))
	return n, err
}

// Memory is bounded by the central directory and the nuspec, not by the archive:
// a package with a large entry is inspected without reading it.
func TestR_ARCH_028_NupkgInspectDoesNotReadTheArchive(t *testing.T) {
	big := bytes.Repeat([]byte{0xA5, 0x5A, 0x11, 0xEE}, 16<<20/4)
	data := mustBytes(t, good().with(ent{name: "lib/big.bin", body: big, stored: true}))
	if len(data) < 16<<20 {
		t.Fatalf("fixture too small: %d", len(data))
	}
	c := &countingReaderAt{r: bytes.NewReader(data)}
	res, err := Inspect(context.Background(), c, int64(len(data)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata.ID != "Acme.Widgets" {
		t.Errorf("id = %q", res.Metadata.ID)
	}
	if got := c.read.Load(); got > 256<<10 {
		t.Errorf("Inspect read %d bytes of a %d byte archive", got, len(data))
	}
}

// A package whose entries are declared far beyond the bound is refused from the
// central directory, without reading the data and within the deadline.
func TestR_ARCH_028_NupkgTenGiBIsRefusedFast(t *testing.T) {
	data := mustBytes(t, good().with(ent{name: "lib/expands.bin", body: bytes.Repeat([]byte{0}, 1<<20), usize: 10 << 30, zip64: true}))
	c := &countingReaderAt{r: bytes.NewReader(data)}
	start := time.Now()
	_, err := Inspect(context.Background(), c, int64(len(data)), Limits{})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
	if got := c.read.Load(); got > 256<<10 {
		t.Errorf("read %d bytes", got)
	}
}

func TestR_ARCH_028_NupkgReadIsBounded(t *testing.T) {
	bd := good().with(
		ent{name: "docs/a.md", body: bytes.Repeat([]byte("a"), 3000), stored: true},
		ent{name: "docs/b.md", body: bytes.Repeat([]byte("b"), 3000), stored: true},
	)
	data := mustBytes(t, bd)
	res, err := inspect(t, data, Limits{MaxReadBytes: 5000})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := res.Archive.Lookup("docs/a.md")
	b, _ := res.Archive.Lookup("docs/b.md")
	if _, err := res.Archive.Read(context.Background(), a, 4096); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := res.Archive.Read(context.Background(), b, 4096); !errors.Is(err, ErrLimit) {
		t.Errorf("the total read is bounded: %v", err)
	}
	if _, err := res.Archive.Read(context.Background(), a, 100); !errors.Is(err, ErrLimit) {
		t.Errorf("the per-entry bound is applied: %v", err)
	}
	if _, err := res.Archive.Read(context.Background(), Entry{Name: "forged"}, 100); !errors.Is(err, ErrEntry) {
		t.Errorf("an entry of another archive: %v", err)
	}
	dir, _ := res.Archive.Lookup("lib")
	if _, err := res.Archive.Read(context.Background(), dir, 100); !errors.Is(err, ErrEntry) {
		t.Errorf("a directory has no content: %v", err)
	}
}

func TestR_ARCH_028_NupkgLimitsOnlyTighten(t *testing.T) {
	l := Limits{MaxRatio: 1000, MaxNuspecBytes: 1 << 30, MaxReadBytes: 1 << 40, Deadline: time.Hour}.WithDefaults()
	if l.MaxRatio != DefaultMaxRatio || l.MaxNuspecBytes != DefaultMaxNuspecBytes || l.MaxReadBytes != DefaultMaxReadBytes || l.Deadline != DefaultDeadline {
		t.Errorf("limits were loosened: %+v", l)
	}
	l = Limits{MaxRatio: 10, MaxNuspecBytes: 100}.WithDefaults()
	if l.MaxRatio != 10 || l.MaxNuspecBytes != 100 {
		t.Errorf("limits were not tightened: %+v", l)
	}
}

func TestR_ARCH_028_NupkgHonoursTheContext(t *testing.T) {
	data := mustBytes(t, good())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Inspect(ctx, bytes.NewReader(data), int64(len(data)), Limits{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if IsRefusal(err) {
		t.Error("a cancelled context is no refusal of the package")
	}
}

func FuzzR_ARCH_028_Inspect(f *testing.F) {
	f.Add(good().bytes())
	zip64 := good()
	zip64.zip64 = true
	for i := range zip64.ents {
		zip64.ents[i].zip64 = true
	}
	f.Add(zip64.bytes())
	f.Add(good().with(ent{name: `..\evil.dll`, body: []byte("x")}).bytes())
	f.Add(good().with(ent{name: "lib/a.dll", localName: "lib/b.dll", body: []byte("x")}).bytes())
	f.Add(good().with(ent{name: "lib/huge.bin", body: []byte("x"), usize: 10 << 30, zip64: true}).bytes())
	f.Add(build{ents: []ent{{name: "a.nuspec", body: bytes.Repeat([]byte{0}, 200<<10)}}}.bytes())
	f.Add(build{ents: []ent{{name: "a.nuspec", body: []byte(`<!DOCTYPE a><package/>`)}}}.bytes())
	f.Add([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxEntries: 1000, MaxUncompressedBytes: 1 << 30, Deadline: 5 * time.Second}
		c := &countingReaderAt{r: bytes.NewReader(data)}
		res, err := Inspect(context.Background(), c, int64(len(data)), lim)
		if err != nil {
			if !IsRefusal(err) {
				t.Fatalf("an error that is no refusal: %#v", err)
			}
			return
		}
		roots := 0
		seen := map[string]bool{}
		for _, e := range res.Archive.Entries() {
			k := foldKey(e.Path)
			if seen[k] {
				t.Fatalf("duplicate path %q accepted", e.Path)
			}
			seen[k] = true
			for seg := range strings.SplitSeq(strings.TrimSuffix(e.Path, "/"), "/") {
				if strings.Trim(seg, ". ") == "" {
					t.Fatalf("unsafe segment in %q", e.Path)
				}
			}
			if strings.HasPrefix(e.Path, "/") || strings.Contains(e.Name, `\`) {
				t.Fatalf("unsafe name %q", e.Name)
			}
			if !e.Dir && !strings.Contains(e.Path, "/") && strings.HasSuffix(strings.ToLower(e.Path), ".nuspec") {
				roots++
			}
		}
		if roots != 1 || res.Metadata.ID == "" || res.Metadata.Version == "" {
			t.Fatalf("accepted without exactly one nuspec or identity: %d %+v", roots, res.Metadata)
		}
		// the whole archive is never read: the scan and the nuspec only
		if c.read.Load() > int64(len(data))+64<<10 {
			t.Fatalf("read %d bytes of %d", c.read.Load(), len(data))
		}
	})
}
