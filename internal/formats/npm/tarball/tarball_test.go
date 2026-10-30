package tarball

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
)

func validate(t testing.TB, data []byte, lim Limits) (*Result, error) {
	t.Helper()
	return Validate(context.Background(), bytes.NewReader(data), lim)
}

func wantError(t *testing.T, err error, status int, code string) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("error = %v, want an *errs.Error", err)
	}
	if e.Status() != status || e.Code != code {
		t.Fatalf("error = %d %s (%s), want %d %s", e.Status(), e.Code, e.Detail, status, code)
	}
}

func wantBad(t *testing.T, data []byte, lim Limits) {
	t.Helper()
	res, err := validate(t, data, lim)
	if err == nil {
		t.Fatalf("accepted: %+v", res)
	}
	t.Logf("rejected: %v", err)
	wantError(t, err, 400, manifest.CodeBadPublish)
}

func wantTooLarge(t *testing.T, data []byte, lim Limits) {
	t.Helper()
	res, err := validate(t, data, lim)
	if err == nil {
		t.Fatalf("accepted: %+v", res)
	}
	t.Logf("rejected: %v", err)
	wantError(t, err, 413, manifest.CodeTooLarge)
}

func TestValidate(t *testing.T) {
	t.Run("R-NPM-007 a valid package yields its manifest, facts and digests", func(t *testing.T) {
		data := build(t,
			ent{name: "package/", typ: tar.TypeDir},
			ent{name: "package/package.json", body: pkgJSON},
			ent{name: "package/lib/", typ: tar.TypeDir},
			ent{name: "package/lib/a.js", body: "a"},
			ent{name: "package/binding.gyp", body: "{}"},
			ent{name: "package/npm-shrinkwrap.json", body: "{}"},
			ent{name: "package/Readme.markdown", body: "# Hello\n"},
			ent{name: "package/README.md", body: "second readme"},
		)
		res, err := validate(t, data, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Manifest["name"] != "@acme/ui" || res.Manifest["version"] != "1.2.0" {
			t.Fatalf("manifest = %v", res.Manifest)
		}
		wantFiles := []string{"package.json", "lib/a.js", "binding.gyp", "npm-shrinkwrap.json", "Readme.markdown", "README.md"}
		if !reflect.DeepEqual(res.Facts.Files, wantFiles) {
			t.Fatalf("files = %v, want %v", res.Facts.Files, wantFiles)
		}
		if res.FileCount != 6 || res.UnpackedSize != int64(len(pkgJSON)+1+2+2+8+13) {
			t.Fatalf("fileCount = %d, unpackedSize = %d", res.FileCount, res.UnpackedSize)
		}
		if !res.HasShrinkwrap || !res.Facts.HasBindingGyp() {
			t.Fatal("shrinkwrap or binding.gyp not seen")
		}
		if res.Readme != "# Hello\n" || res.ReadmeFilename != "Readme.markdown" {
			t.Fatalf("readme = %q (%s): the first top-level README* file wins", res.Readme, res.ReadmeFilename)
		}
		s1, s512 := sha1.Sum(data), sha512.Sum512(data) //nolint:gosec // npm's dist.shasum
		if res.SHA1Hex() != hex.EncodeToString(s1[:]) || res.Integrity() != "sha512-"+base64.StdEncoding.EncodeToString(s512[:]) {
			t.Fatalf("digests differ: %s %s", res.SHA1Hex(), res.Integrity())
		}
		if res.Size != int64(len(data)) {
			t.Fatalf("size = %d, want %d", res.Size, len(data))
		}
	})

	t.Run("R-NPM-007 the digests cover every byte whatever the read sizes", func(t *testing.T) {
		data := build(t, goodEntries()...)
		want := sha512.Sum512(data)
		for name, r := range map[string]io.Reader{
			"one byte at a time": iotest.OneByteReader(bytes.NewReader(data)),
			"half reads":         iotest.HalfReader(bytes.NewReader(data)),
			"data and EOF":       iotest.DataErrReader(bytes.NewReader(data)),
		} {
			res, err := Validate(context.Background(), r, Limits{})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if res.SHA512 != want {
				t.Fatalf("%s: digest differs", name)
			}
		}
	})

	t.Run("R-NPM-007 directory entries may follow their children and come implicitly", func(t *testing.T) {
		for name, ents := range map[string][]ent{
			"children first": {{name: "package/lib/a.js", body: "a"}, {name: "package/lib/", typ: tar.TypeDir}, {name: "package/package.json", body: pkgJSON}},
			"no folders":     goodEntries(),
			"root last":      {{name: "package/package.json", body: pkgJSON}, {name: "package/", typ: tar.TypeDir}},
		} {
			if _, err := validate(t, build(t, ents...), Limits{}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})

	t.Run("R-NPM-007 the first component is whatever the archive uses, but one", func(t *testing.T) {
		ents := []ent{{name: "pkg/package.json", body: pkgJSON}, {name: "pkg/a", body: "a"}}
		res, err := validate(t, build(t, ents...), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res.Facts.Files, []string{"package.json", "a"}) {
			t.Fatalf("files = %v", res.Facts.Files)
		}
	})

	t.Run("R-NPM-007 long names travel in PAX headers and are read back", func(t *testing.T) {
		long := "package/" + strings.Repeat("d/", 100) + "f.js"
		res, err := validate(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: long, body: "x"}), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Facts.Files[1] != strings.TrimPrefix(long, "package/") {
			t.Fatalf("files = %v", res.Facts.Files)
		}
	})

	t.Run("R-NPM-010 the README is cut at 64 KiB inside a character", func(t *testing.T) {
		body := strings.Repeat("a", DefaultMaxReadmeBytes-1) + "é" + "tail"
		res, err := validate(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/README", body: body}), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Readme != strings.Repeat("a", DefaultMaxReadmeBytes-1) {
			t.Fatalf("readme has %d bytes", len(res.Readme))
		}
	})

	t.Run("R-NPM-007 a README below a folder is none", func(t *testing.T) {
		res, err := validate(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/docs/README.md", body: "x"}), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Readme != "" {
			t.Fatalf("readme = %q", res.Readme)
		}
	})
}

func TestPathRules(t *testing.T) {
	withPkg := func(e ...ent) []ent { return append([]ent{{name: "package/package.json", body: pkgJSON}}, e...) }
	cases := []struct {
		name string
		ents []ent
	}{
		{"a second package.json adding a postinstall script", append(goodEntries(), ent{name: "package/package.json", body: `{"name":"@acme/ui","version":"1.2.0","scripts":{"postinstall":"evil"}}`})},
		{"a package.json that is a symbolic link", []ent{{name: "package/package.json", typ: tar.TypeSymlink, link: "real.json"}, {name: "package/real.json", body: pkgJSON}}},
		{"a package.json that is a hard link", []ent{{name: "package/real.json", body: pkgJSON}, {name: "package/package.json", typ: tar.TypeLink, link: "package/real.json"}}},
		{"a package.json that is a directory", []ent{{name: "package/package.json/", typ: tar.TypeDir}}},
		{"a package.json that is a FIFO", []ent{{name: "package/package.json", typ: tar.TypeFifo}}},
		{"entries named ./package/...", []ent{{name: "./package/package.json", body: pkgJSON}}},
		{"package.json at ./ in the middle", withPkg(ent{name: "package/./index.js", body: "x"})},
		{"an empty component", withPkg(ent{name: "package//index.js", body: "x"})},
		{"a dot-dot component", withPkg(ent{name: "package/a/../b.js", body: "x"})},
		{"a first component of dot-dot", []ent{{name: "../package/package.json", body: pkgJSON}}},
		{"an absolute path", []ent{{name: "/package/package.json", body: pkgJSON}}},
		{"a backslash", withPkg(ent{name: `package\evil.js`, body: "x"})},
		{"a control character", withPkg(ent{name: "package/a\x01b", body: "x"})},
		{"a name that is not UTF-8", withPkg(ent{name: "package/\xff\xfe", body: "x"})},
		{"package.json and other/package.json", []ent{{name: "package/package.json", body: pkgJSON}, {name: "other/package.json", body: pkgJSON}}},
		{"package/package.json and a sibling top-level file", withPkg(ent{name: "stray.js", body: "x"})},
		{"no package.json", []ent{{name: "package/index.js", body: "x"}}},
		{"package.json only below a folder", []ent{{name: "package/sub/package.json", body: pkgJSON}}},
		{"an empty archive", nil},
		{"the same file twice", withPkg(ent{name: "package/a.js", body: "1"}, ent{name: "package/a.js", body: "2"})},
		{"a folder twice", withPkg(ent{name: "package/d/", typ: tar.TypeDir}, ent{name: "package/d/", typ: tar.TypeDir})},
		{"the package folder twice", withPkg(ent{name: "package/", typ: tar.TypeDir}, ent{name: "package/", typ: tar.TypeDir})},
		{"a file where a folder is", withPkg(ent{name: "package/d/x.js", body: "1"}, ent{name: "package/d", body: "2"})},
		{"a folder after a file of its name", withPkg(ent{name: "package/d", body: "2"}, ent{name: "package/d/", typ: tar.TypeDir})},
		{"an entry below a file", withPkg(ent{name: "package/f", body: "1"}, ent{name: "package/f/x", body: "2"})},
		{"a file named like the package folder", []ent{{name: "package", body: "x"}, {name: "package/package.json", body: pkgJSON}}},
		{"Package.json differing in case", withPkg(ent{name: "package/Package.json", body: `{"name":"x"}`})},
		{"PACKAGE.JSON differing in case", append([]ent{{name: "package/PACKAGE.JSON", body: `{"name":"x"}`}}, withPkg()...)},
		{"a character device", withPkg(ent{name: "package/dev", typ: tar.TypeChar})},
		{"a global PAX header", withPkg(ent{name: "package/pax_global_header", typ: tar.TypeXGlobalHeader, pax: map[string]string{"comment": "x"}})},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.name+" answers 400 npm_bad_publish", func(t *testing.T) {
			wantBad(t, build(t, c.ents...), Limits{})
		})
	}

	t.Run("R-NPM-007 a package.json with the letter case of its folder is no hit", func(t *testing.T) {
		if _, err := validate(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/sub/Package.json", body: "{}"}), Limits{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLinks(t *testing.T) {
	base := func(e ent) []byte { return build(t, append(goodEntries(), e)...) }
	bad := map[string]ent{
		"an absolute symbolic link":           {name: "package/l", typ: tar.TypeSymlink, link: "/etc/passwd"},
		"a symbolic link out of the package":  {name: "package/l", typ: tar.TypeSymlink, link: "../../etc/passwd"},
		"a nested link out of the package":    {name: "package/a/l", typ: tar.TypeSymlink, link: "../../x"},
		"a link to the folder above":          {name: "package/l", typ: tar.TypeSymlink, link: ".."},
		"a link with a backslash":             {name: "package/l", typ: tar.TypeSymlink, link: `..\x`},
		"a link without a target":             {name: "package/l", typ: tar.TypeSymlink, link: ""},
		"a hard link to another first folder": {name: "package/l", typ: tar.TypeLink, link: "other/package.json"},
		"a hard link with an absolute target": {name: "package/l", typ: tar.TypeLink, link: "/package/package.json"},
		"a hard link with dot-dot":            {name: "package/l", typ: tar.TypeLink, link: "package/../../x"},
		"a hard link to the folder":           {name: "package/l", typ: tar.TypeLink, link: "package"},
		"the package folder as a link":        {name: "package/", typ: tar.TypeSymlink, link: "x"},
	}
	for name, e := range bad {
		t.Run("R-NPM-007 "+name+" is refused", func(t *testing.T) { wantBad(t, base(e), Limits{}) })
	}
	good := map[string]ent{
		"a symbolic link inside the package":    {name: "package/l", typ: tar.TypeSymlink, link: "index.js"},
		"a symbolic link into a sibling folder": {name: "package/a/l", typ: tar.TypeSymlink, link: "../index.js"},
		"a hard link inside the package":        {name: "package/l", typ: tar.TypeLink, link: "package/index.js"},
	}
	for name, e := range good {
		t.Run("R-NPM-007 "+name+" is accepted", func(t *testing.T) {
			res, err := validate(t, base(e), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if res.FileCount != 3 {
				t.Fatalf("a link counts as a file: fileCount = %d", res.FileCount)
			}
		})
	}
	t.Run("S-6 a link whose target passes through another link is refused, in either order", func(t *testing.T) {
		// package/sub/l2 -> .. stays in the package; package/l1 -> sub/l2/../.. cleans to the
		// package folder lexically and resolves two levels above it on a file system.
		l2 := ent{name: "package/sub/l2", typ: tar.TypeSymlink, link: ".."}
		l1 := ent{name: "package/l1", typ: tar.TypeSymlink, link: "sub/l2/../.."}
		wantBad(t, build(t, append(goodEntries(), l2, l1)...), Limits{})
		wantBad(t, build(t, append(goodEntries(), l1, l2)...), Limits{})
		// The same through a hard link: package/h names a path below the link.
		wantBad(t, build(t, append(goodEntries(), l2, ent{name: "package/h", typ: tar.TypeLink, link: "package/sub/l2/index.js"})...), Limits{})
		// A target below a link is no way around it, whatever the depth.
		deep := ent{name: "package/a/b/l3", typ: tar.TypeSymlink, link: "../../sub/l2/x"}
		wantBad(t, build(t, append(goodEntries(), l2, deep)...), Limits{})
	})
	t.Run("S-6 a link to a link, or through a folder, is accepted", func(t *testing.T) {
		l2 := ent{name: "package/sub/l2", typ: tar.TypeSymlink, link: ".."}
		for name, es := range map[string][]ent{
			"a link that names another link last": {l2, {name: "package/l1", typ: tar.TypeSymlink, link: "sub/l2"}},
			"a target through a folder":           {{name: "package/lib/", typ: tar.TypeDir}, {name: "package/lib/x.js", body: "x"}, {name: "package/l", typ: tar.TypeSymlink, link: "lib/../lib/x.js"}},
		} {
			if _, err := validate(t, build(t, append(goodEntries(), es...)...), Limits{}); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("R-NPM-007 nothing may be written through a link", func(t *testing.T) {
		wantBad(t, build(t, append(goodEntries(), ent{name: "package/l", typ: tar.TypeSymlink, link: "lib"}, ent{name: "package/l/evil.js", body: "x"})...), Limits{})
	})
}

func TestResourceLimits(t *testing.T) {
	t.Run("R-ARCH-028 a package.json that declares 2 GiB is refused without being read or allocated", func(t *testing.T) {
		data := build(t, ent{name: "package/package.json", declared: 2 << 30})
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := validate(t, data, Limits{})
		runtime.ReadMemStats(&after)
		wantError(t, err, 413, manifest.CodeTooLarge)
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
			t.Fatalf("allocated %d bytes", grown)
		}
	})

	t.Run("R-ARCH-028 a package.json of 1 MiB passes, one byte more does not", func(t *testing.T) {
		doc := func(n int) string {
			head := `{"name":"@acme/ui","version":"1.2.0","x":"`
			// random text, so the ratio check does not fire
			rnd := rand.New(rand.NewSource(1))
			var b strings.Builder
			b.WriteString(head)
			for b.Len() < n-2 {
				b.WriteString(hex.EncodeToString([]byte{byte(rnd.Intn(256)), byte(rnd.Intn(256)), byte(rnd.Intn(256))}))
			}
			return b.String()[:n-2] + `"}`
		}
		if _, err := validate(t, build(t, ent{name: "package/package.json", body: doc(manifest.DefaultMaxBytes)}), Limits{}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
		wantTooLarge(t, build(t, ent{name: "package/package.json", body: doc(manifest.DefaultMaxBytes + 1)}), Limits{})
	})

	t.Run("R-ARCH-028 a package.json that compresses by more than 100:1 is refused", func(t *testing.T) {
		body := `{"name":"a","version":"1.0.0","x":"` + strings.Repeat("0", 900<<10) + `"}`
		wantTooLarge(t, build(t, ent{name: "package/package.json", body: body}), Limits{})
	})

	t.Run("R-ARCH-028 a small repetitive package.json is fine", func(t *testing.T) {
		body := `{"name":"a","version":"1.0.0","x":"` + strings.Repeat("0", 20<<10) + `"}`
		if _, err := validate(t, build(t, ent{name: "package/package.json", body: body}), Limits{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-ARCH-028 a package.json nested 100,000 deep is refused", func(t *testing.T) {
		body := `{"name":"a","version":"1.0.0","deep":` + strings.Repeat("[", 100000) + strings.Repeat("]", 100000) + `}`
		wantBad(t, build(t, ent{name: "package/package.json", body: body}), Limits{MaxManifestBytes: 1 << 20, MaxRatio: 1 << 20})
	})

	t.Run("R-NPM-007 a package.json that is not a JSON object is refused", func(t *testing.T) {
		for _, body := range []string{"", "[]", "null", "{", `{"a":1} x`, "\xff\xfe"} {
			wantBad(t, build(t, ent{name: "package/package.json", body: body}), Limits{})
		}
	})

	t.Run("R-ARCH-028 more entries than max_tar_entries answer 413", func(t *testing.T) {
		ents := goodEntries()
		for i := 0; i < 20; i++ {
			ents = append(ents, ent{name: "package/f" + string(rune('a'+i)), body: "x"})
		}
		wantTooLarge(t, build(t, ents...), Limits{MaxEntries: 10})
		if _, err := validate(t, build(t, ents...), Limits{MaxEntries: 23}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
		wantTooLarge(t, build(t, ents...), Limits{MaxEntries: 22})
	})

	t.Run("R-ARCH-028 entries that declare more than max_unpacked_size answer 413 before they are inflated", func(t *testing.T) {
		wantTooLarge(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/big.bin", declared: 3 << 30}), Limits{})
		big := bytes.Repeat([]byte{0}, 3<<20)
		wantTooLarge(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/a.bin", body: string(big)}), Limits{MaxUnpackedBytes: 2 << 20})
	})

	t.Run("R-ARCH-028 declared sizes add up across entries", func(t *testing.T) {
		ents := []ent{{name: "package/package.json", body: pkgJSON}}
		for i := 0; i < 4; i++ {
			ents = append(ents, ent{name: "package/f" + string(rune('a'+i)), body: strings.Repeat("x", 1000)})
		}
		wantTooLarge(t, build(t, ents...), Limits{MaxUnpackedBytes: 3000})
		if _, err := validate(t, build(t, ents...), Limits{MaxUnpackedBytes: int64(len(pkgJSON)) + 4000}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})

	t.Run("R-ARCH-028 a gzip bomb that is no tar stops at the inflation cap", func(t *testing.T) {
		bomb := gz(t, make([]byte, 16<<20)) // 16 MiB of zeros: a few KiB compressed
		if len(bomb) > 1<<20 {
			t.Fatalf("the bomb is %d bytes", len(bomb))
		}
		wantTooLarge(t, bomb, Limits{MaxUnpackedBytes: 1 << 20, MaxEntries: 10})
	})

	t.Run("S-4 an archive that expands by more than the overall ratio is refused whatever its size", func(t *testing.T) {
		zeros := func(n int) []byte {
			return build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/blob.bin", body: string(make([]byte, n))})
		}
		// 64 MiB of zeros is about 64 KiB compressed: a ratio of 1000:1.
		wantTooLarge(t, zeros(64<<20), Limits{})
		// A small archive is never judged by its ratio.
		if _, err := validate(t, zeros(4<<20), Limits{}); err != nil {
			t.Fatalf("4 MiB of zeros: %v", err)
		}
		// The cap is the caller's to tighten or lift.
		if _, err := validate(t, zeros(64<<20), Limits{MaxTotalRatio: 1 << 20}); err != nil {
			t.Fatalf("with the cap lifted: %v", err)
		}
		wantTooLarge(t, zeros(16<<20), Limits{MaxTotalRatio: 10})
	})

	t.Run("R-ARCH-028 padding after the archive is read, not trusted, and bounded", func(t *testing.T) {
		raw := buildTar(t, goodEntries()...)
		padded := gz(t, append(raw, make([]byte, 8<<20)...))
		if _, err := validate(t, padded, Limits{}); err != nil {
			t.Fatalf("zero padding: %v", err)
		}
		wantTooLarge(t, padded, Limits{MaxUnpackedBytes: 1 << 20, MaxEntries: 10})
	})

	t.Run("R-ARCH-028 a tarball over the compressed cap answers 413", func(t *testing.T) {
		data := build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/a", body: strings.Repeat("abcdefghij", 100000)})
		wantTooLarge(t, data, Limits{MaxCompressedBytes: int64(len(data) - 1)})
		if _, err := validate(t, data, Limits{MaxCompressedBytes: int64(len(data))}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})

	t.Run("R-ARCH-028 entry names are bounded", func(t *testing.T) {
		wantBad(t, build(t, ent{name: "package/package.json", body: pkgJSON}, ent{name: "package/" + strings.Repeat("n", 2000), body: "x"}), Limits{})
		wantTooLarge(t, build(t, goodEntries()...), Limits{MaxPathBytes: 100})
	})

	t.Run("R-ARCH-028 a tarball of 64 MiB streams through in constant memory", func(t *testing.T) {
		if testing.Short() {
			t.Skip("builds 64 MiB")
		}
		data := streamingTarball(t, 64<<20)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		// 64 MiB of zeros is a ratio of about 1000:1, which the overall cap refuses by default.
		res, err := validate(t, data, Limits{MaxTotalRatio: 1 << 20})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if res.UnpackedSize < 64<<20 {
			t.Fatalf("unpacked size %d", res.UnpackedSize)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
			t.Fatalf("allocated %d bytes for 64 MiB of content", grown)
		}
	})
}

// streamingTarball builds a valid tarball whose content is n bytes of zeros.
func streamingTarball(t testing.TB, n int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(name string, size int64, fill []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tw, bytes.NewReader(fill), int64(len(fill))); err != nil {
			t.Fatal(err)
		}
		for rest := size - int64(len(fill)); rest > 0; {
			chunk := min(rest, 1<<20)
			if _, err := tw.Write(make([]byte, chunk)); err != nil {
				t.Fatal(err)
			}
			rest -= chunk
		}
	}
	write("package/package.json", int64(len(pkgJSON)), []byte(pkgJSON))
	write("package/data.bin", n, nil)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStreamErrors(t *testing.T) {
	good := build(t, goodEntries()...)
	notTar := gz(t, []byte(strings.Repeat("this is not a tar archive\n", 100)))

	t.Run("R-NPM-007 the tarball must be gzip-compressed tar", func(t *testing.T) {
		for name, data := range map[string][]byte{
			"empty":                  nil,
			"plain text":             []byte("hello"),
			"a plain tar":            buildTar(t, goodEntries()...),
			"gzip of something else": notTar,
			"truncated":              good[:len(good)/2],
			"truncated in the CRC":   good[:len(good)-3],
			"just the gzip header":   good[:10],
		} {
			t.Run(name, func(t *testing.T) { wantBad(t, data, Limits{}) })
		}
	})

	t.Run("R-NPM-007 a corrupt CRC is refused", func(t *testing.T) {
		data := append([]byte(nil), good...)
		data[len(data)-6] ^= 0xff
		wantBad(t, data, Limits{})
	})

	t.Run("R-NPM-007 a second gzip member is trailing data", func(t *testing.T) {
		wantBad(t, append(append([]byte(nil), good...), good...), Limits{})
	})

	t.Run("R-NPM-007 bytes after the gzip stream are refused", func(t *testing.T) {
		wantBad(t, append(append([]byte(nil), good...), 0), Limits{})
	})

	t.Run("R-NPM-007 data after the tar end blocks is refused, zero padding is not", func(t *testing.T) {
		raw := buildTar(t, goodEntries()...)
		wantBad(t, gz(t, append(append([]byte(nil), raw...), []byte("trailing")...)), Limits{})
		if _, err := validate(t, gz(t, append(append([]byte(nil), raw...), make([]byte, 4096)...)), Limits{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-ARCH-028 a gzip header with a huge file name is refused", func(t *testing.T) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Name = strings.Repeat("n", 200<<10)
		_, _ = zw.Write(buildTar(t, goodEntries()...))
		_ = zw.Close()
		wantBad(t, buf.Bytes(), Limits{})
	})

	t.Run("R-NPM-007 a tar header with a bad checksum is refused", func(t *testing.T) {
		raw := buildTar(t, goodEntries()...)
		raw[148] ^= 0x01
		wantBad(t, gz(t, raw), Limits{})
	})

	t.Run("R-NPM-007 a sparse file is refused", func(t *testing.T) {
		wantBad(t, build(t, append(goodEntries(), ent{name: "package/s", rawPAX: "23 GNU.sparse.size=100\n", body: "x"})...), Limits{})
	})

	t.Run("a read error of the source is not a client error", func(t *testing.T) {
		boom := errors.New("connection reset")
		_, err := Validate(context.Background(), io.MultiReader(bytes.NewReader(good[:len(good)/2]), iotest.ErrReader(boom)), Limits{})
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the source error", err)
		}
		if _, ok := errs.As(err); ok {
			t.Fatalf("a source error became %v", err)
		}
	})

	t.Run("R-ARCH-028 a cancelled context stops the pass", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Validate(ctx, bytes.NewReader(good), Limits{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("R-ARCH-028 a deadline that passes mid-stream stops the pass", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := io.MultiReader(bytes.NewReader(good[:20]), readFunc(func(p []byte) (int, error) {
			cancel()
			return 0, nil
		}), bytes.NewReader(good[20:]))
		_, err := Validate(ctx, r, Limits{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func TestLimitsDefaults(t *testing.T) {
	t.Run("R-ARCH-028 the defaults are the limits of R-NPM-007 and R-ARCH-028", func(t *testing.T) {
		d := DefaultLimits()
		if d.MaxCompressedBytes != 256<<20 || d.MaxUnpackedBytes != 2<<30 || d.MaxEntries != 100000 ||
			d.MaxManifestBytes != 1<<20 || d.MaxRatio != 100 || d.MaxManifestDepth != 64 {
			t.Fatalf("defaults = %+v", d)
		}
		if got := (Limits{MaxEntries: 5}).WithDefaults(); got.MaxEntries != 5 || got.MaxUnpackedBytes != d.MaxUnpackedBytes {
			t.Fatalf("got %+v", got)
		}
	})
}

// FuzzTarball must neither panic nor exceed the limits it is given, and every
// tarball it accepts must satisfy the invariants the publish path relies on.
func FuzzTarball(f *testing.F) {
	for _, seed := range [][]ent{
		goodEntries(),
		{{name: "package/package.json", body: pkgJSON}, {name: "package/l", typ: tar.TypeSymlink, link: "index.js"}},
		{{name: "package/package.json", body: pkgJSON}, {name: "package/package.json", body: pkgJSON}},
		{{name: "./package/package.json", body: pkgJSON}},
		{{name: "package/package.json", declared: 2 << 30}},
		{{name: "package/", typ: tar.TypeDir}, {name: "package/a/b/c", body: "x"}, {name: "package/package.json", body: pkgJSON}},
	} {
		f.Add(build(f, seed...))
		f.Add(buildTar(f, seed...))
	}
	f.Add([]byte{})
	f.Add(gz(f, make([]byte, 1<<20)))
	lim := Limits{MaxCompressedBytes: 1 << 20, MaxUnpackedBytes: 1 << 20, MaxEntries: 50, MaxManifestBytes: 1 << 16, MaxNameBytes: 256, MaxPathBytes: 1 << 16}
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := Validate(context.Background(), bytes.NewReader(data), lim)
		if err != nil {
			if _, ok := errs.As(err); !ok {
				t.Fatalf("a bad input became an unclassified error: %v", err)
			}
			return
		}
		if int64(len(data)) > lim.MaxCompressedBytes || res.Size != int64(len(data)) {
			t.Fatalf("size %d of %d bytes accepted", res.Size, len(data))
		}
		if res.UnpackedSize > lim.MaxUnpackedBytes || res.FileCount > int64(lim.MaxEntries) || int64(len(res.Facts.Files)) != res.FileCount {
			t.Fatalf("limits exceeded: %+v", res)
		}
		if want := sha512.Sum512(data); res.SHA512 != want {
			t.Fatal("sha512 differs")
		}
		if want := sha1.Sum(data); res.SHA1 != want { //nolint:gosec // npm's dist.shasum
			t.Fatal("sha1 differs")
		}
		if res.Manifest == nil {
			t.Fatal("no manifest")
		}
		seen := map[string]bool{}
		hasPkg := false
		for _, p := range res.Facts.Files {
			if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "\\") || seen[p] {
				t.Fatalf("bad install path %q", p)
			}
			for _, c := range strings.Split(p, "/") {
				if c == "" || c == "." || c == ".." {
					t.Fatalf("unclean install path %q", p)
				}
			}
			seen[p] = true
			hasPkg = hasPkg || p == "package.json"
		}
		if !hasPkg {
			t.Fatal("accepted without package.json")
		}
		if len(res.Readme) > DefaultMaxReadmeBytes+utf8Slack {
			t.Fatalf("readme of %d bytes", len(res.Readme))
		}
	})
}

// utf8Slack allows for the replacement characters of invalid bytes in a README.
const utf8Slack = 3 * DefaultMaxReadmeBytes
