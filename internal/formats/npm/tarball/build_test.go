package tarball

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"
)

// ent describes one tar entry of a crafted archive.
type ent struct {
	name     string
	typ      byte // tar.TypeReg when 0
	body     string
	link     string
	declared int64 // a size to declare without writing the body; the archive ends after the header
	pax      map[string]string
	rawPAX   string // the body of a PAX extended header written before the entry, as it is
}

const pkgJSON = `{"name":"@acme/ui","version":"1.2.0","dependencies":{"react":"^19.0.0"}}`

// goodEntries is a valid package.
func goodEntries() []ent {
	return []ent{
		{name: "package/package.json", body: pkgJSON},
		{name: "package/index.js", body: "module.exports = 1\n"},
		{name: "package/README.md", body: "# Acme UI\n"},
	}
}

// buildTar writes the entries as a tar archive. An entry with a declared size
// ends the archive after its header, so a declared size never has to exist.
func buildTar(t testing.TB, ents ...ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		if typ == tar.TypeXGlobalHeader {
			if err := tw.WriteHeader(&tar.Header{Typeflag: typ, PAXRecords: e.pax}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if e.rawPAX != "" {
			// tar.Writer refuses to write an extended header by hand
			if err := tw.Flush(); err != nil {
				t.Fatal(err)
			}
			buf.Write(rawBlock("PaxHeaders/x", tar.TypeXHeader, e.rawPAX))
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Linkname: e.link, PAXRecords: e.pax, Format: tar.FormatPAX}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if e.declared > 0 {
			hdr.Size = e.declared
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes() // no Close: the body and the end blocks never come
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawBlock returns a ustar header block and the padded body of one entry.
func rawBlock(name string, typ byte, body string) []byte {
	var hdr [512]byte
	copy(hdr[0:], name)
	copy(hdr[100:], "0000644\x00")
	copy(hdr[108:], "0000000\x00")
	copy(hdr[116:], "0000000\x00")
	copy(hdr[124:], fmt.Sprintf("%011o\x00", len(body)))
	copy(hdr[136:], "00000000000\x00")
	copy(hdr[148:], "        ")
	hdr[156] = typ
	copy(hdr[257:], "ustar\x0000")
	sum := 0
	for _, b := range hdr {
		sum += int(b)
	}
	copy(hdr[148:], fmt.Sprintf("%06o\x00 ", sum))
	out := append(hdr[:], body...)
	if pad := len(body) % 512; pad != 0 {
		out = append(out, make([]byte, 512-pad)...)
	}
	return out
}

func gz(t testing.TB, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// build returns the gzip-compressed tar of the entries.
func build(t testing.TB, ents ...ent) []byte {
	t.Helper()
	return gz(t, buildTar(t, ents...))
}
