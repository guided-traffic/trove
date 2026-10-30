package corpus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // the legacy dist.shasum of npm documents is a SHA-1 by definition
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// packedAt is the modification time of every tarball entry (the value npm itself uses), so the
// bytes do not depend on when they were produced.
var packedAt = time.Unix(499162500, 0).UTC()

type tarFile struct {
	name string
	body []byte
}

// tarballFiles are the entries of a version's tarball, sorted by name. The manifest carries no scripts.
func tarballFiles(name, version string, deps map[string]string) []tarFile {
	manifest := map[string]any{
		"name":        name,
		"version":     version,
		"description": "Synthetic package " + name + " of the trove test corpus",
		"main":        "index.js",
		"license":     "MIT",
	}
	if len(deps) > 0 {
		manifest["dependencies"] = deps
	}
	pkgJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		panic(err) // a map of strings always marshals
	}
	return []tarFile{
		{"package/README.md", []byte("# " + name + "\n\nVersion " + version + " of a synthetic package.\n")},
		{"package/index.js", indexSource(name, version)},
		{"package/package.json", append(pkgJSON, '\n')},
	}
}

// Tarball returns the package tarball of a version: gzip (stored blocks, so the bytes do not depend
// on the toolchain's deflate implementation) around a tar with sorted entries, fixed times and
// owners. The same arguments always give the same bytes.
func Tarball(name, version string, deps map[string]string) []byte {
	return pack(tarballFiles(name, version, deps))
}

func pack(files []tarFile) []byte {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.NoCompression)
	if err != nil {
		panic(err) // the level is a valid constant
	}
	tw := tar.NewWriter(gz)
	for _, f := range files {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     f.name,
			Mode:     0o644,
			Size:     int64(len(f.body)),
			ModTime:  packedAt,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			panic(err) // writing to a bytes.Buffer cannot fail
		}
		if _, err := tw.Write(f.body); err != nil {
			panic(err)
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// indexSource is a small module whose size varies with the version, so tarballs are not all alike.
func indexSource(name, version string) []byte {
	r := newRNG(fnv64(name + "@" + version))
	var b strings.Builder
	fmt.Fprintf(&b, "'use strict';\nmodule.exports = { name: %q, version: %q };\n", name, version)
	for i, n := 0, 4+r.intn(60); i < n; i++ {
		fmt.Fprintf(&b, "// %016x%016x\n", r.next(), r.next())
	}
	return []byte(b.String())
}

// NewVersion builds a version of a package, including the integrity of its tarball.
func NewVersion(name, version string, deps map[string]string, at time.Time) Version {
	files := tarballFiles(name, version, deps)
	tarball := pack(files)
	sum512 := sha512.Sum512(tarball)
	sum1 := sha1.Sum(tarball) //nolint:gosec // see the import
	size := 0
	for _, f := range files {
		size += len(f.body)
	}
	return Version{
		Version:      version,
		Dependencies: deps,
		Time:         at,
		Dist: Dist{
			Integrity:    "sha512-" + base64.StdEncoding.EncodeToString(sum512[:]),
			Shasum:       hex.EncodeToString(sum1[:]),
			FileCount:    len(files),
			UnpackedSize: size,
		},
	}
}
