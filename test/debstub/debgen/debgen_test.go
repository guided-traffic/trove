package debgen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// readAr parses the archive by hand: the global header and the 60-byte member headers.
func readAr(t *testing.T, b []byte) (names []string, data map[string][]byte) {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("!<arch>\n")) {
		t.Fatalf("no ar header: %q", b[:8])
	}
	data = map[string][]byte{}
	rest := b[8:]
	for len(rest) > 0 {
		if len(rest) < 60 || string(rest[58:60]) != "`\n" {
			t.Fatalf("bad member header %q", rest[:min(60, len(rest))])
		}
		name := strings.TrimSpace(string(rest[:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(rest[48:58])))
		if err != nil {
			t.Fatal(err)
		}
		body := rest[60 : 60+size]
		names = append(names, name)
		data[name] = body
		rest = rest[60+size:]
		if size%2 == 1 {
			if len(rest) == 0 || rest[0] != '\n' {
				t.Fatal("a member of odd size is not padded")
			}
			rest = rest[1:]
		}
	}
	return names, data
}

func readTarGz(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = string(body)
	}
}

func TestBuildMakesAnArArchiveDpkgReads(t *testing.T) {
	d, err := Build(Spec{Package: "trove-hello", Version: "1.0-1", Architecture: "amd64", Depends: "libc6 (>= 2.34)", LongDescription: []string{"Prints a fixed line.", "", "Nothing else."}})
	if err != nil {
		t.Fatal(err)
	}
	names, members := readAr(t, d.Bytes)
	if strings.Join(names, ",") != "debian-binary,control.tar.gz,data.tar.gz" {
		t.Fatalf("members = %v", names)
	}
	if string(members["debian-binary"]) != "2.0\n" {
		t.Errorf("debian-binary = %q", members["debian-binary"])
	}
	ctrl := readTarGz(t, members["control.tar.gz"])
	if ctrl["./control"] != d.Control {
		t.Errorf("control = %q", ctrl["./control"])
	}
	for _, want := range []string{"Package: trove-hello\n", "Version: 1.0-1\n", "Architecture: amd64\n", "Depends: libc6 (>= 2.34)\n", "Description: trove test package trove-hello\n Prints a fixed line.\n .\n Nothing else.\n"} {
		if !strings.Contains(d.Control, want) {
			t.Errorf("control lacks %q:\n%s", want, d.Control)
		}
	}
	if !strings.HasSuffix(d.Control, "\n") || strings.Contains(d.Control, "\n\n") {
		t.Errorf("control is not one paragraph: %q", d.Control)
	}
	if !strings.Contains(ctrl["./md5sums"], "usr/bin/trove-hello") {
		t.Errorf("md5sums = %q", ctrl["./md5sums"])
	}
	data := readTarGz(t, members["data.tar.gz"])
	if got := data["./usr/bin/trove-hello"]; got != "#!/bin/sh\necho 'hello from trove-hello 1.0-1 (amd64)'\n" {
		t.Errorf("script = %q", got)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _ := Build(Hello("trove-hello", "1.0-1", "all"))
	b, _ := Build(Hello("trove-hello", "1.0-1", "all"))
	c, _ := Build(Hello("trove-hello", "1.1-1", "all"))
	if !bytes.Equal(a.Bytes, b.Bytes) || bytes.Equal(a.Bytes, c.Bytes) {
		t.Fatal("equal specs must give equal bytes, different specs different bytes")
	}
	if a.Digests().SHA256 != Sum(a.Bytes).SHA256 || a.Digests().Size != int64(len(a.Bytes)) {
		t.Error("digests")
	}
}

func TestFilenameAndOutput(t *testing.T) {
	s := Spec{Package: "p", Version: "2:1.0-1", Architecture: "arm64"}
	if s.Filename() != "p_1.0-1_arm64.deb" {
		t.Errorf("Filename = %q", s.Filename())
	}
	if _, err := Build(Spec{Package: "p"}); err == nil {
		t.Error("an incomplete spec built")
	}
	d, _ := Build(Spec{Package: "p", Version: "1", Architecture: "all", Line: "it's fine"})
	if _, m := readAr(t, d.Bytes); !strings.Contains(readTarGz(t, m["data.tar.gz"])["./usr/bin/p"], `echo 'it'\''s fine'`) {
		t.Error("a quote in the line is not escaped")
	}
}

// TestWriteForDpkg writes a package to $DEBGEN_OUT so that a Debian container can install it (see docs/developer/deb.md); without
// the variable it is skipped.
func TestWriteForDpkg(t *testing.T) {
	out := os.Getenv("DEBGEN_OUT")
	if out == "" {
		t.Skip("DEBGEN_OUT is not set")
	}
	for _, arch := range []string{"all", "amd64"} {
		d, err := Build(Spec{Package: "trove-hello", Version: "1.0-1", Architecture: arch, Depends: "libc6 (>= 2.34)"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out+"/"+d.Spec.Filename(), d.Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
