package debfile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// The bounds the Debian format declares (R-DEB-034).
var lim = untrusted.Limits{Entries: 1000, UncompressedBytes: 64 << 20, Ratio: 100, ManifestEntryBytes: 1 << 20}

const goodControl = "Package: hello\nVersion: 2.10-3\nArchitecture: amd64\nMaintainer: A <a@example.org>\nDescription: greeter\n"

type tarEntry struct {
	name     string
	data     string
	typeflag byte
	link     string
}

func tarOf(t testing.TB, es ...tarEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range es {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: flag, Mode: 0o644, Linkname: e.link, ModTime: time.Unix(0, 0)}
		if flag == tar.TypeReg {
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func compress(t testing.TB, how string, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	var w io.WriteCloser
	var err error
	switch how {
	case "":
		return data
	case ".gz":
		w, err = gzip.NewWriterLevel(&b, gzip.BestSpeed)
	case ".xz":
		w, err = xz.NewWriter(&b)
	case ".zst":
		w, err = zstd.NewWriter(&b)
	default:
		return data // an uncompressed body under a name that claims another method
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type arMember struct{ name, data string }

func arOf(ms ...arMember) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		b.WriteString(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func debOf(t testing.TB, ext string, ctl []tarEntry) []byte {
	t.Helper()
	return arOf(
		arMember{"debian-binary", "2.0\n"},
		arMember{"control.tar" + ext, string(compress(t, ext, tarOf(t, ctl...)))},
		arMember{"data.tar.gz", string(compress(t, ".gz", tarOf(t, tarEntry{name: "./usr/bin/hello", data: "x"})))},
	)
}

func read(t testing.TB, data []byte) (*Package, error) {
	t.Helper()
	return Read(bytes.NewReader(data), int64(len(data)), lim)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestReadGeneratedPackage(t *testing.T) {
	d, err := debgen.Build(debgen.Spec{Package: "libxml2-utils", Version: "1:2.9.14+dfsg-1.3", Architecture: "amd64", Source: "libxml2 (2.9.14+dfsg-1.3)", Depends: "libc6"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := read(t, d.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "libxml2-utils" || p.Version != "1:2.9.14+dfsg-1.3" || p.Architecture != "amd64" || p.Source != "libxml2" {
		t.Fatalf("package = %+v", p)
	}
	if p.Control.Value("Depends") != "libc6" {
		t.Errorf("fields = %+v", p.Control.Fields)
	}
}

// R-DEB-007: gzip (the old default), xz (Debian's) and Zstandard (Ubuntu's) control archives and an uncompressed one are read.
func TestReadControlCompressions(t *testing.T) {
	for _, ext := range []string{"", ".gz", ".xz", ".zst"} {
		t.Run("control.tar"+ext, func(t *testing.T) {
			p, err := read(t, debOf(t, ext, []tarEntry{{name: "./", typeflag: tar.TypeDir}, {name: "./control", data: goodControl}, {name: "./md5sums", data: "x"}}))
			if err != nil || p.Name != "hello" {
				t.Fatalf("%v %v", p, err)
			}
		})
	}
	t.Run("a control file without the dot", func(t *testing.T) {
		if _, err := read(t, debOf(t, ".gz", []tarEntry{{name: "control", data: goodControl}})); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("bzip2 is not accepted", func(t *testing.T) {
		data := arOf(arMember{"debian-binary", "2.0\n"}, arMember{"control.tar.bz2", "BZh"}, arMember{"data.tar.gz", "x"})
		_, err := read(t, data)
		wantCode(t, err, errs.CodeDebUnsupportedCompression)
	})
}

// The declared Filename is data and the computed fields are not carried into the index (R-DEB-007).
func TestNormalizedParagraph(t *testing.T) {
	ctl := goodControl + "Filename: ../../etc/passwd\nSize: 1\nMD5sum: x\nSHA1: x\nSHA256: x\nSHA512: x\nDescription-md5: x\nSection: misc\n"
	p, err := read(t, debOf(t, ".gz", []tarEntry{{name: "./control", data: ctl}}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range p.Control.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "Package,Version,Architecture,Maintainer,Description,Section" {
		t.Fatalf("fields = %v", names)
	}
}

// The violations of R-DEB-007 are 422 deb_invalid_control and name the field.
func TestInvalidControl(t *testing.T) {
	cases := map[string]string{
		"two paragraphs":              goodControl + "\nPackage: other\n",
		"a blank continuation line":   goodControl + "Section: misc\n \n",
		"Package twice":               goodControl + "Package: again\n",
		"a field name with a space":   goodControl + "Pack age: x\n",
		"a required field is missing": "Package: hello\nVersion: 1\nArchitecture: all\nMaintainer: A\n",
		"an empty required field":     "Package: hello\nVersion: 1\nArchitecture: all\nMaintainer: A\nDescription:\n",
		"a Source with no name":       goodControl + "Source:\n",
		"a control character":         goodControl + "Section: a\x01b\n",
	}
	for name, ctl := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := read(t, debOf(t, ".gz", []tarEntry{{name: "./control", data: ctl}}))
			wantCode(t, err, errs.CodeDebInvalidControl)
		})
	}
}

func TestInvalidArchives(t *testing.T) {
	ctl := compress(t, ".gz", tarOf(t, tarEntry{name: "./control", data: goodControl}))
	data := compress(t, ".gz", tarOf(t, tarEntry{name: "./usr/x", data: "x"}))
	bin := arMember{"debian-binary", "2.0\n"}
	cases := map[string][]byte{
		"no ar archive":              []byte("this is a text file"),
		"debian-binary is not first": arOf(arMember{"control.tar.gz", string(ctl)}, bin, arMember{"data.tar.gz", string(data)}),
		"debian-binary of version 3": arOf(arMember{"debian-binary", "3.0\n"}, arMember{"control.tar.gz", string(ctl)}, arMember{"data.tar.gz", string(data)}),
		"a duplicate control.tar.gz": arOf(bin, arMember{"control.tar.gz", string(ctl)}, arMember{"control.tar.gz", string(ctl)}, arMember{"data.tar.gz", string(data)}),
		"no data archive":            arOf(bin, arMember{"control.tar.gz", string(ctl)}),
		"an unknown member":          arOf(bin, arMember{"control.tar.gz", string(ctl)}, arMember{"data.tar.gz", string(data)}, arMember{"extra", "x"}),
		"data before control":        arOf(bin, arMember{"data.tar.gz", string(data)}, arMember{"control.tar.gz", string(ctl)}),
		"the control is not gzip":    arOf(bin, arMember{"control.tar.gz", "not gzip at all"}, arMember{"data.tar.gz", string(data)}),
		"the control is not xz":      arOf(bin, arMember{"control.tar.xz", "not xz at all, really"}, arMember{"data.tar.gz", string(data)}),
		"no control file":            debOf(t, ".gz", []tarEntry{{name: "./md5sums", data: "x"}}),
		"control twice":              debOf(t, ".gz", []tarEntry{{name: "./control", data: goodControl}, {name: "control", data: goodControl}}),
		"control is a link":          debOf(t, ".gz", []tarEntry{{name: "./control", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}),
		"control is a directory":     debOf(t, ".gz", []tarEntry{{name: "./control/", typeflag: tar.TypeDir}}),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := read(t, d)
			wantCode(t, err, errs.CodeDebInvalidArchive)
		})
	}
	t.Run("members whose names start with an underscore may follow", func(t *testing.T) {
		if _, err := read(t, arOf(bin, arMember{"control.tar.gz", string(ctl)}, arMember{"data.tar.gz", string(data)}, arMember{"_gpgorigin", "sig"})); err != nil {
			t.Fatal(err)
		}
	})
}

// The crafted inputs of R-DEB-034, each refused with 413 within the time bound.
func TestBounds(t *testing.T) {
	tooLarge := func(t *testing.T, d []byte) {
		t.Helper()
		start := time.Now()
		_, err := read(t, d)
		if errs.KindOf(err) != errs.TooLarge {
			t.Fatalf("err = %v, want 413", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
	}
	t.Run("a control archive that expands past the bound", func(t *testing.T) {
		// 80 MiB of zeros in a control entry: past the 64 MiB bound (and past the ratio, which is what trips first).
		var plain bytes.Buffer
		tw := tar.NewWriter(&plain)
		_ = tw.WriteHeader(&tar.Header{Name: "./bomb", Typeflag: tar.TypeReg, Mode: 0o644, Size: 80 << 20})
		zeros := make([]byte, 1<<20)
		for range 80 {
			_, _ = tw.Write(zeros)
		}
		_ = tw.Close()
		tooLarge(t, arOf(arMember{"debian-binary", "2.0\n"}, arMember{"control.tar.gz", string(compress(t, ".gz", plain.Bytes()))}, arMember{"data.tar.gz", "x"}))
	})
	t.Run("a control archive with more than 1,000 entries", func(t *testing.T) {
		es := make([]tarEntry, 1001)
		for i := range es {
			es[i] = tarEntry{name: fmt.Sprintf("./f%d", i)}
		}
		tooLarge(t, debOf(t, ".gz", es))
	})
	t.Run("a control file of 100,000 fields", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 100_000; i++ {
			fmt.Fprintf(&b, "X-F%d: v\n", i)
		}
		tooLarge(t, debOf(t, ".gz", []tarEntry{{name: "./control", data: goodControl + b.String()}}))
	})
	t.Run("a control file above 1 MiB", func(t *testing.T) {
		tooLarge(t, debOf(t, ".gz", []tarEntry{{name: "./control", data: goodControl + "X-Big: " + strings.Repeat("a", 2<<20) + "\n"}}))
	})
	t.Run("more than eight members", func(t *testing.T) {
		ms := []arMember{{"debian-binary", "2.0\n"}}
		for i := 0; i < 9; i++ {
			ms = append(ms, arMember{fmt.Sprintf("_m%d", i), "x"})
		}
		tooLarge(t, arOf(ms...))
	})
	t.Run("a control archive above 16 MiB", func(t *testing.T) {
		tooLarge(t, arOf(arMember{"debian-binary", "2.0\n"}, arMember{"control.tar", strings.Repeat("a", MaxControlArchiveBytes+1)}, arMember{"data.tar", "x"}))
	})
}

func FuzzRead(f *testing.F) {
	d, _ := debgen.Build(debgen.Hello("hello", "1.0", "all"))
	f.Add(d.Bytes)
	f.Add([]byte("!<arch>\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Read(bytes.NewReader(data), int64(len(data)), lim)
		if err != nil {
			return
		}
		if p.Name == "" || p.Version == "" || p.Architecture == "" {
			t.Fatalf("accepted a package without identity: %+v", p)
		}
		if _, err := p.Control.Bytes(); err != nil {
			t.Fatalf("accepted a paragraph that does not write: %v", err)
		}
	})
}
