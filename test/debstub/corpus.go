package debstub

import (
	"fmt"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// The layout of the archive the stub serves. Everything is relative to the stub's one host.
const (
	// ArchiveBase holds an ordinary archive: dists/<suite>/ and pool/. A source line is "deb <host>/debian <suite> <components>".
	ArchiveBase = "/debian"
	// FlatBase holds a flat repository whose directory is "binary/": the source line is "deb <host>/flat binary/" and the
	// Filename fields are relative to <host>/flat/.
	FlatBase = "/flat"
	// FlatDir is the directory of the flat repository below FlatBase.
	FlatDir = "binary"
	// FlatRootBase is a flat repository whose Release lies in the base itself: the source line is "deb <host>/flatroot ./", APT
	// requests <host>/flatroot/./InRelease, and the Filename fields are relative to <host>/flatroot/.
	FlatRootBase = "/flatroot"
)

// The suites, components and architectures of the stub.
const (
	SuiteStable  = "stable"
	SuiteTesting = "testing"
	// SuiteBulk holds the large index of the bounds tests: one component, one architecture, Config.BulkParagraphs paragraphs.
	SuiteBulk = "bulk"
	// ArchiveFlat and ArchiveFlatRoot are the ids of the two flat repositories.
	ArchiveFlat     = "flat"
	ArchiveFlatRoot = "flatroot"

	// CodenameStable and CodenameTesting are the second names of the suites: dists/ratel/ is the same release as dists/stable/.
	CodenameStable  = "ratel"
	CodenameTesting = "badger"
)

// pkg is a binary package of the archive with its generated file.
type pkg struct {
	spec debgen.Spec
	deb  []byte
	// control is the package's control file, which a Packages paragraph starts with.
	control string
	sum     debgen.Digests
	// path is the Filename of the Packages paragraph: relative to the archive's base.
	path string
}

func (p *pkg) paragraph() string {
	var b strings.Builder
	b.WriteString(p.control)
	fmt.Fprintf(&b, "Filename: %s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\nSHA512: %s\n", p.path, p.sum.Size, p.sum.MD5, p.sum.SHA1, p.sum.SHA256, p.sum.SHA512)
	return b.String()
}

// srcFile is a file of a source package.
type srcFile struct {
	name string
	data []byte
	sum  debgen.Digests
}

// srcPkg is a source package: a .dsc, an original and a Debian tarball.
type srcPkg struct {
	name, version, comp string
	binaries            []string
	files               []srcFile
}

func (sp *srcPkg) dir() string {
	return fmt.Sprintf("pool/%s/%s/%s", sp.comp, prefixOf(sp.name), sp.name)
}

func (sp *srcPkg) paragraph() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\nBinary: %s\nVersion: %s\nMaintainer: %s\nArchitecture: any all\nStandards-Version: 4.6.2\nFormat: 3.0 (quilt)\nDirectory: %s\nPriority: optional\nSection: misc\n",
		sp.name, strings.Join(sp.binaries, ", "), sp.version, "trove test archive <debstub@trove.invalid>", sp.dir())
	b.WriteString("Files:\n")
	for _, f := range sp.files {
		fmt.Fprintf(&b, " %s %d %s\n", f.sum.MD5, f.sum.Size, f.name)
	}
	b.WriteString("Checksums-Sha1:\n")
	for _, f := range sp.files {
		fmt.Fprintf(&b, " %s %d %s\n", f.sum.SHA1, f.sum.Size, f.name)
	}
	b.WriteString("Checksums-Sha256:\n")
	for _, f := range sp.files {
		fmt.Fprintf(&b, " %s %d %s\n", f.sum.SHA256, f.sum.Size, f.name)
	}
	return b.String()
}

// prefixOf is the pool directory of a source package: its first letter, or the first four for a lib name.
func prefixOf(source string) string {
	if strings.HasPrefix(source, "lib") && len(source) > 3 {
		return source[:4]
	}
	return source[:1]
}

func poolPath(comp, source, file string) string {
	return fmt.Sprintf("pool/%s/%s/%s/%s", comp, prefixOf(source), source, file)
}

// corpus is the generated package set, built once per Server and never changed.
type corpus struct {
	// byArchive maps an archive id and a component to the binary packages of that component.
	binaries map[string]map[string][]*pkg
	sources  map[string][]*srcPkg // by archive id, component main
	static   map[string][]byte    // URL path to the bytes of every package and source file
}

type pkgDef struct {
	name, version, comp, source, depends, provides, desc string
	arches                                               []string
	long                                                 []string
}

// stableDefs and testingDefs are the two suites' packages. The testing suite carries newer versions and one more package.
func defs(suite string) []pkgDef {
	hello, tools, lib, extra, newpkg := "1.0-1", "2.0-1", "1.2-3", "0.9-1", ""
	if suite == SuiteTesting {
		hello, tools, lib, extra, newpkg = "1.1-1", "2.1-1", "1.3-1", "1.0-1", "0.1-1"
	}
	out := []pkgDef{
		{
			name: "trove-hello", version: hello, comp: "main", depends: "libtrove1 (>= 1.2)", arches: []string{"amd64", "arm64"},
			desc: "prints a fixed line", long: []string{"The first package of the trove test archive.", "", "It installs one script that prints a fixed line."},
		},
		{name: "trove-hello-doc", version: hello, comp: "main", source: "trove-hello", arches: []string{"all"}, desc: "documentation of trove-hello"},
		{name: "trove-tools", version: tools, comp: "main", depends: "trove-hello (>= 1.0) | trove-config", arches: []string{"amd64", "arm64"}, desc: "tools of the trove test archive"},
		{name: "trove-config", version: "1.0-1", comp: "main", provides: "trove-config-default", arches: []string{"all"}, desc: "default configuration"},
		{name: "libtrove1", version: lib, comp: "main", source: "libtrove", arches: []string{"amd64", "arm64"}, desc: "shared library of the trove test archive"},
		{name: "trove-extra", version: extra, comp: "contrib", depends: "trove-hello", arches: []string{"amd64"}, desc: "an extra from the contrib component"},
		{name: "trove-extra-data", version: extra, comp: "contrib", source: "trove-extra", arches: []string{"all"}, desc: "data of trove-extra"},
	}
	if newpkg != "" {
		out = append(out, pkgDef{name: "trove-newpkg", version: newpkg, comp: "main", arches: []string{"all"}, desc: "a package only testing has"})
	}
	return out
}

func buildPkg(d pkgDef, arch string) (*pkg, error) {
	spec := debgen.Spec{
		Package: d.name, Version: d.version, Architecture: arch, Depends: d.depends, Provides: d.provides,
		Description: d.desc, LongDescription: d.long,
	}
	if d.source != "" {
		spec.Source = d.source
	}
	deb, err := debgen.Build(spec)
	if err != nil {
		return nil, err
	}
	src := d.name
	if d.source != "" {
		src = d.source
	}
	return &pkg{spec: spec, deb: deb.Bytes, control: deb.Control, sum: deb.Digests(), path: poolPath(d.comp, src, spec.Filename())}, nil
}

func buildSource(name, version, comp string, binaries []string) (*srcPkg, error) {
	upstream := strings.SplitN(version, "-", 2)[0]
	orig, err := debgen.TarGz(map[string][]byte{name + "-" + upstream + "/README": []byte("source of " + name + " " + upstream + "\n")}, []string{name + "-" + upstream + "/README"})
	if err != nil {
		return nil, err
	}
	deb, err := debgen.TarGz(map[string][]byte{"debian/changelog": []byte(name + " (" + version + ") unstable; urgency=low\n\n  * test package\n\n -- trove test archive <debstub@trove.invalid>  Thu, 01 Jan 2026 00:00:00 +0000\n")}, []string{"debian/changelog"})
	if err != nil {
		return nil, err
	}
	sp := &srcPkg{name: name, version: version, comp: comp, binaries: binaries}
	origName, debName, dscName := name+"_"+upstream+".orig.tar.gz", name+"_"+version+".debian.tar.gz", name+"_"+version+".dsc"
	files := []srcFile{{name: origName, data: orig}, {name: debName, data: deb}}
	for i := range files {
		files[i].sum = debgen.Sum(files[i].data)
	}
	var dsc strings.Builder
	fmt.Fprintf(&dsc, "Format: 3.0 (quilt)\nSource: %s\nBinary: %s\nArchitecture: any all\nVersion: %s\nMaintainer: trove test archive <debstub@trove.invalid>\nStandards-Version: 4.6.2\nChecksums-Sha256:\n", name, strings.Join(binaries, ", "), version)
	for _, f := range files {
		fmt.Fprintf(&dsc, " %s %d %s\n", f.sum.SHA256, f.sum.Size, f.name)
	}
	dsc.WriteString("Files:\n")
	for _, f := range files {
		fmt.Fprintf(&dsc, " %s %d %s\n", f.sum.MD5, f.sum.Size, f.name)
	}
	dscFile := srcFile{name: dscName, data: []byte(dsc.String())}
	dscFile.sum = debgen.Sum(dscFile.data)
	sp.files = append([]srcFile{dscFile}, files...)
	return sp, nil
}

func newCorpus() (*corpus, error) {
	c := &corpus{binaries: map[string]map[string][]*pkg{}, sources: map[string][]*srcPkg{}, static: map[string][]byte{}}
	cache := map[string]*pkg{}
	for _, suite := range []string{SuiteStable, SuiteTesting} {
		c.binaries[suite] = map[string][]*pkg{}
		for _, d := range defs(suite) {
			for _, arch := range d.arches {
				key := d.name + "_" + d.version + "_" + arch
				p := cache[key]
				if p == nil {
					var err error
					if p, err = buildPkg(d, arch); err != nil {
						return nil, err
					}
					cache[key] = p
					c.static[ArchiveBase+"/"+p.path] = p.deb
				}
				c.binaries[suite][d.comp] = append(c.binaries[suite][d.comp], p)
			}
		}
		for comp := range c.binaries[suite] {
			ps := c.binaries[suite][comp]
			sort.SliceStable(ps, func(i, j int) bool {
				if ps[i].spec.Package != ps[j].spec.Package {
					return ps[i].spec.Package < ps[j].spec.Package
				}
				return ps[i].spec.Architecture < ps[j].spec.Architecture
			})
		}
		version := "1.0-1"
		if suite == SuiteTesting {
			version = "1.1-1"
		}
		sp, err := buildSource("trove-hello", version, "main", []string{"trove-hello", "trove-hello-doc"})
		if err != nil {
			return nil, err
		}
		c.sources[suite] = []*srcPkg{sp}
		for _, f := range sp.files {
			c.static[ArchiveBase+"/"+sp.dir()+"/"+f.name] = f.data
		}
	}
	return c, nil
}
