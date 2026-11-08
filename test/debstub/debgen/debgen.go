// Package debgen generates installable Debian binary packages in code: an ar archive of debian-binary, control.tar.gz and
// data.tar.gz, where the data archive holds one script that prints a fixed line. The bytes depend on the Spec alone (fixed
// modification times, owners and gzip headers), so equal inputs give equal files and equal digests.
//
// The counting stub archive (test/debstub) serves these files, and the integration and e2e tests use the same generator to
// upload packages to a local repository. A generated file installs with dpkg and runs.
package debgen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"  //nolint:gosec // the md5sums file and the MD5sum field of a Packages paragraph are defined by Debian
	"crypto/sha1" //nolint:gosec // the SHA1 field of a Packages paragraph is defined by Debian
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ModTime is the modification time of every member of a generated package, so that equal inputs give equal bytes.
var ModTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Spec describes one binary package.
type Spec struct {
	Package      string
	Version      string
	Architecture string // amd64, arm64, all, ...
	Maintainer   string // default "trove test archive <debstub@trove.invalid>"
	Section      string // default "misc"
	Priority     string // default "optional"
	Description  string // the synopsis, default "trove test package <Package>"
	// LongDescription is the extended description, one line per element; an empty element is a paragraph break.
	LongDescription []string
	Depends         string
	PreDepends      string
	Provides        string
	Conflicts       string
	MultiArch       string
	Homepage        string
	// Source is the source package name when it differs from Package.
	Source string
	// Line is the line the installed script prints; default "hello from <Package> <Version> (<Architecture>)".
	Line string
}

func (s Spec) maintainer() string {
	return orDefault(s.Maintainer, "trove test archive <debstub@trove.invalid>")
}
func (s Spec) section() string     { return orDefault(s.Section, "misc") }
func (s Spec) priority() string    { return orDefault(s.Priority, "optional") }
func (s Spec) description() string { return orDefault(s.Description, "trove test package "+s.Package) }

// Output is the line the installed script prints.
func (s Spec) Output() string {
	return orDefault(s.Line, fmt.Sprintf("hello from %s %s (%s)", s.Package, s.Version, s.Architecture))
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// Filename is the file name dpkg-deb gives the package: <name>_<version>_<architecture>.deb. A colon of an epoch is left out,
// as dpkg-deb does.
func (s Spec) Filename() string {
	v := s.Version
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	return s.Package + "_" + v + "_" + s.Architecture + ".deb"
}

// ScriptPath is where the package installs its script.
func (s Spec) ScriptPath() string { return "/usr/bin/" + s.Package }

// Hello is the common package of the tests: the name, version and architecture, nothing else.
func Hello(name, version, architecture string) Spec {
	return Spec{Package: name, Version: version, Architecture: architecture}
}

// Deb is a generated package.
type Deb struct {
	Spec Spec
	// Bytes is the .deb file.
	Bytes []byte
	// Control is the text of its control file, one deb822 paragraph.
	Control string
	// InstalledSize is the size of the installed files in KiB, as the control file says it.
	InstalledSize int
}

// Digests are the checksums a Packages paragraph and a Release carry.
type Digests struct {
	Size   int64
	MD5    string
	SHA1   string
	SHA256 string
	SHA512 string
}

// Sum computes the digests of data.
func Sum(data []byte) Digests {
	m := md5.Sum(data)   //nolint:gosec // see the import
	s1 := sha1.Sum(data) //nolint:gosec // see the import
	s256 := sha256.Sum256(data)
	s512 := sha512.Sum512(data)
	return Digests{Size: int64(len(data)), MD5: hex.EncodeToString(m[:]), SHA1: hex.EncodeToString(s1[:]), SHA256: hex.EncodeToString(s256[:]), SHA512: hex.EncodeToString(s512[:])}
}

// Digests are the checksums of the .deb file.
func (d *Deb) Digests() Digests { return Sum(d.Bytes) }

// ControlText renders the control file of the spec.
func ControlText(s Spec, installedKiB int) string {
	var b strings.Builder
	field := func(name, value string) {
		if value != "" {
			b.WriteString(name + ": " + value + "\n")
		}
	}
	field("Package", s.Package)
	field("Source", s.Source)
	field("Version", s.Version)
	field("Architecture", s.Architecture)
	field("Maintainer", s.maintainer())
	field("Installed-Size", strconv.Itoa(installedKiB))
	field("Pre-Depends", s.PreDepends)
	field("Depends", s.Depends)
	field("Conflicts", s.Conflicts)
	field("Provides", s.Provides)
	field("Section", s.section())
	field("Priority", s.priority())
	field("Multi-Arch", s.MultiArch)
	field("Homepage", s.Homepage)
	b.WriteString("Description: " + s.description() + "\n")
	for _, line := range s.LongDescription {
		if line == "" {
			b.WriteString(" .\n")
		} else {
			b.WriteString(" " + line + "\n")
		}
	}
	return b.String()
}

// Build generates the package.
func Build(s Spec) (*Deb, error) {
	if s.Package == "" || s.Version == "" || s.Architecture == "" {
		return nil, errors.New("debgen: Package, Version and Architecture are required")
	}
	script := []byte("#!/bin/sh\necho '" + strings.ReplaceAll(s.Output(), "'", "'\\''") + "'\n")
	installedKiB := max(1, (len(script)+1023)/1024)
	control := ControlText(s, installedKiB)
	md5sum := md5.Sum(script) //nolint:gosec // see the import
	md5sums := hex.EncodeToString(md5sum[:]) + "  " + strings.TrimPrefix(s.ScriptPath(), "/") + "\n"

	ctrlTar, err := tarGz([]entry{
		{name: "./", dir: true},
		{name: "./control", data: []byte(control), mode: 0o644},
		{name: "./md5sums", data: []byte(md5sums), mode: 0o644},
	})
	if err != nil {
		return nil, err
	}
	dataTar, err := tarGz([]entry{
		{name: "./", dir: true},
		{name: "./usr/", dir: true},
		{name: "./usr/bin/", dir: true},
		{name: "." + s.ScriptPath(), data: script, mode: 0o755},
	})
	if err != nil {
		return nil, err
	}
	deb := ar([]member{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar.gz", data: ctrlTar},
		{name: "data.tar.gz", data: dataTar},
	})
	return &Deb{Spec: s, Bytes: deb, Control: control, InstalledSize: installedKiB}, nil
}

type entry struct {
	name string
	data []byte
	mode int64
	dir  bool
}

func tarGz(entries []entry) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	// The gzip header carries no name and the zero modification time, so equal inputs give equal bytes.
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, ModTime: ModTime, Uname: "root", Gname: "root", Format: tar.FormatGNU}
		if e.dir {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		} else {
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, e.mode, int64(len(e.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if !e.dir {
			if _, err := tw.Write(e.data); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type member struct {
	name string
	data []byte
}

// ar writes the archive format of dpkg: the global header, then per member a 60-byte header (name, modification time, owner,
// group, mode, size, terminator) and the data padded to an even length.
func ar(members []member) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range members {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, ModTime.Unix(), 0, 0, "100644", len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// Gzip compresses data deterministically (no name, zero modification time), the way the stub serves its compressed indexes.
func Gzip(data []byte) []byte {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = gz.Write(data)
	_ = gz.Close()
	return buf.Bytes()
}

// TarGz builds a gzip-compressed tar of regular files with the fixed modification time, for the source archives of the stub.
func TarGz(files map[string][]byte, order []string) ([]byte, error) {
	entries := make([]entry, 0, len(order))
	for _, name := range order {
		entries = append(entries, entry{name: name, data: files[name], mode: 0o644})
	}
	return tarGz(entries)
}
