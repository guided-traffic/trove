package deb

import (
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// The layout of R-DEB-003 and R-DEB-008. A local repository stores a package file at its canonical pool path and generates the
// files below dists/; a remote stores what its upstream serves at the path the upstream serves it (the repository path equals the
// path below remote.url); a virtual repository serves its members' files below members/<memberKey>/.
const (
	dirDists   = "dists"
	dirPool    = "pool"
	dirMembers = "members"
	dirUpload  = "upload"
	// SigningKeyFile is the public key clients trust (R-DEB-018).
	SigningKeyFile = "signing-key.asc"
	// DirByHash is the directory of an index file's content-addressed copies (R-DEB-012).
	DirByHash = "by-hash"
	// HashSHA256 is the hash directory below by-hash/.
	HashSHA256 = "SHA256"
)

// The release documents of a suite (R-DEB-011).
const (
	FileInRelease  = "InRelease"
	FileRelease    = "Release"
	FileReleaseGPG = "Release.gpg"
)

// IsReleaseFile reports whether name is one of the three release documents.
func IsReleaseFile(name string) bool {
	return name == FileInRelease || name == FileRelease || name == FileReleaseGPG
}

// PoolPrefix is the first directory below pool/ for a source package: its first character, or its first four when it starts with
// "lib", as the Debian archive lays out its pool (R-DEB-008).
func PoolPrefix(source string) string {
	n := 1
	if strings.HasPrefix(source, "lib") {
		n = min(4, len(source))
	}
	return source[:min(n, len(source))]
}

// PoolPath is the canonical path of a binary package file (Q-DEB-5):
// pool/<prefix>/<source>/<package>_<version without epoch>_<architecture>.deb.
func PoolPath(source, pkg, version, architecture string) string {
	if source == "" {
		source = pkg
	}
	return dirPool + "/" + PoolPrefix(source) + "/" + source + "/" + pkg + "_" + StripEpoch(version) + "_" + architecture + ".deb"
}

// SuitePath is the path of a file below dists/<suite>/.
func SuitePath(suite, rel string) string { return dirDists + "/" + suite + "/" + rel }

// ByHashPath is the path of the by-hash copy of an index file: the directory of the file, then by-hash/SHA256/<hex>. dir is relative
// to dists/<suite>/ ("main/binary-amd64"; "" for a file of the suite itself).
func ByHashPath(suite, dir, sha256hex string) string {
	if dir != "" {
		dir += "/"
	}
	return SuitePath(suite, dir+DirByHash+"/"+HashSHA256+"/"+sha256hex)
}

// PackagesPath is the path of an index of a suite, a component and an architecture; ext is ".gz" or ".xz" (R-DEB-011).
func PackagesPath(suite, component, architecture, ext string) string {
	return SuitePath(suite, component+"/binary-"+architecture+"/Packages"+ext)
}

var extRE = regexp.MustCompile(`^[a-z0-9]+$`)

// fileForm cuts a package file name into <package>_<version>_<architecture>.<ext>. ok is false for a name that does not have the
// form or whose parts break the grammars (R-DEB-003).
func fileForm(file string) (pkg, version, arch string, ok bool) {
	i := strings.LastIndexByte(file, '.')
	if i < 0 || !extRE.MatchString(file[i+1:]) {
		return "", "", "", false
	}
	parts := strings.Split(file[:i], "_")
	if len(parts) != 3 {
		return "", "", "", false
	}
	pkg, version, arch = parts[0], parts[1], parts[2]
	if !ValidPackage(pkg) || !ValidVersion(version) || strings.Contains(version, ":") || !ValidArchitecture(arch) {
		return "", "", "", false
	}
	return pkg, version, arch, true
}

// indexCompressions are the suffixes of the compressions an index name carries (R-DEB-003).
func indexCompressions() []string { return []string{".gz", ".xz", ".bz2", ".lzma", ".zst"} }

// IsIndexName reports whether file names an index of a flat repository: Packages, Sources, Contents-<arch> or Translation-<lang>,
// each optionally compressed, and Index (R-DEB-003). An Index belongs to a *.diff directory; the caller decides that.
func IsIndexName(file string) bool {
	base := file
	for _, s := range indexCompressions() {
		if b, ok := strings.CutSuffix(file, s); ok {
			base = b
			break
		}
	}
	switch {
	case base == "Packages" || base == "Sources" || base == "Index":
		return true
	case strings.HasPrefix(base, "Contents-") && len(base) > len("Contents-"):
		return true
	case strings.HasPrefix(base, "Translation-") && len(base) > len("Translation-"):
		return true
	}
	return false
}

// reservedFirst are the first segments the routes of the classes keep for themselves; a flat directory of a remote repository does
// not start with one.
func reservedFirst(seg string) bool {
	switch seg {
	case dirDists, dirPool, dirMembers, dirUpload, SigningKeyFile:
		return true
	}
	return false
}

// Classify says how a canonical path behaves and which package it belongs to (R-DEB-003). It decides by path and file name only,
// never by stored state (R-ARCH-008): a .deb in a flat directory is a package file, an index name in a flat directory an index
// file.
//
// A path below pool/ or members/<memberKey>/ whose file name has the form <package>_<version>_<arch>.<ext> belongs to the package
// and the version of its name (the version without its epoch, which file names omit). A path of that kind whose name does not have
// the form has no package and gets the part of its file name before the first '_' as its routing name, so that patterns and claims
// still protect it. Every other path (release documents, index files, by-hash files, the signing key) is a coordinate of the
// repository without a package name, which only grants without package patterns cover (Q-DEB-20).
func (f *Format) Classify(p formats.Path) formats.PathInfo {
	segs := strings.Split(p.Path, "/")
	switch {
	case p.Path == SigningKeyFile:
		return formats.PathInfo{Kind: formats.PathComputed}
	case segs[0] == dirDists:
		return classifyDists(segs)
	case segs[0] == dirMembers:
		if len(segs) < 3 {
			return formats.PathInfo{Kind: formats.PathMetadata}
		}
		return classifyFile(segs[2:])
	}
	return classifyFile(segs)
}

// classifyDists: below dists/ a by-hash file is immutable content, every other file a document of the suite.
func classifyDists(segs []string) formats.PathInfo {
	n := len(segs)
	if n >= 4 && segs[n-3] == DirByHash && segs[n-2] == HashSHA256 {
		return formats.PathInfo{Kind: formats.Content}
	}
	return formats.PathInfo{Kind: formats.PathMetadata}
}

// classifyFile classifies a path outside dists/ by its file name: a release document or an index file is metadata of no package;
// any other file is content, of the package its name says.
func classifyFile(segs []string) formats.PathInfo {
	file := segs[len(segs)-1]
	if IsReleaseFile(file) {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	if len(segs) >= 2 && strings.HasSuffix(segs[len(segs)-2], ".diff") {
		// An Index and the patches below a *.diff directory (R-DEB-021).
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	if IsIndexName(file) {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	if pkg, version, _, ok := fileForm(file); ok {
		return formats.PathInfo{Kind: formats.Content, Package: pkg, Version: version}
	}
	info := formats.PathInfo{Kind: formats.Content}
	if name, _, found := strings.Cut(file, "_"); found && name != "" {
		info.RoutingName = name
	}
	return info
}

// PackageDocumentPath reports ok false: an APT archive has no document per package. A remote answers the shadowing check from its
// parsed index rows through its NameServer (R-DEB-023, Q-REPO-270).
func (f *Format) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }
