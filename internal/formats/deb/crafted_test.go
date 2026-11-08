package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
)

// The crafted packages of R-DEB-034: each stays inside a few megabytes in memory and crosses exactly one bound of the package reader,
// so the refusal is the bound's. The reader is held to 413 or 422 for every one.

type arPart struct {
	name string
	data []byte
}

// arOf writes an ar archive with the global header and one 60-byte header per member.
func arOf(parts ...arPart) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, p := range parts {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", p.name+"/", 0, 0, 0, "100644", len(p.data))
		b.Write(p.data)
		if len(p.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// controlTarGz is a control archive of the named entries, each with the given size of zero bytes except the control file, which has
// the given text. Nothing is held at full size: the gzip stream is written in steps.
func controlTarGz(control string, extra int, zeros int64) []byte {
	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	tw := tar.NewWriter(zw)
	write := func(name string, size int64, src io.Reader) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg})
		_, _ = io.Copy(tw, src)
	}
	write("./control", int64(len(control)), strings.NewReader(control))
	for i := 0; i < extra; i++ {
		write(fmt.Sprintf("./f%d", i), 0, strings.NewReader(""))
	}
	if zeros > 0 {
		write("./padding", zeros, io.LimitReader(zeroReader{}, zeros))
	}
	_ = tw.Close()
	_ = zw.Close()
	return out.Bytes()
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func debWith(control []byte, members ...arPart) []byte {
	parts := []arPart{{name: "debian-binary", data: []byte("2.0\n")}, {name: "control.tar.gz", data: control}}
	parts = append(parts, members...)
	return arOf(parts...)
}

func memInput(name string, data []byte) crafted.Input {
	return crafted.Input{
		Name: name, Size: int64(len(data)),
		Open:     func() io.ReadCloser { return io.NopCloser(bytes.NewReader(data)) },
		ReaderAt: bytes.NewReader(data),
	}
}

const minimalControl = "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\nMaintainer: A <a@example.org>\nDescription: x\n"

func inspectDeb(path string) []formattest.InspectCase {
	const ct = MediaType
	var sb strings.Builder
	sb.WriteString(minimalControl)
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&sb, "X-Field-%d: v\n", i)
	}
	manyFields := sb.String()
	bigField := minimalControl + "X-Big: " + strings.Repeat("a", 2<<20) + "\n"
	members := make([]arPart, 0, 8)
	for i := 0; i < 8; i++ {
		members = append(members, arPart{name: fmt.Sprintf("extra%d", i), data: []byte("x")})
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"not an ar archive", []byte("this is not a Debian package")},
		{"ar archive with ten members", debWith(controlTarGz(minimalControl, 0, 0), members...)},
		{"control archive with 1,001 entries", debWith(controlTarGz(minimalControl, 1001, 0))},
		{"control archive expanding to 70 MiB", debWith(controlTarGz(minimalControl, 0, 70<<20))},
		{"control file with 600 fields", debWith(controlTarGz(manyFields, 0, 0))},
		{"control file with a field of 2 MiB", debWith(controlTarGz(bigField, 0, 0))},
	}
	out := make([]formattest.InspectCase, 0, len(cases))
	for _, c := range cases {
		out = append(out, formattest.InspectCase{Name: c.name, Path: path, MediaType: ct, Input: memInput(c.name, c.data)})
	}
	return out
}
