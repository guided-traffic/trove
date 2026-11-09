package rows

import (
	"path"
	"sort"
	"strings"
)

// parseSuffixes are the compressions the parse job reads, in the order it prefers them: the smallest download first (R-DEB-023). The
// empty suffix is the plain file.
func parseSuffixes() []string { return []string{".xz", ".gz", ".zst", ".bz2", ""} }

// IndexBase cuts the compression suffix off the path of a Packages or Sources index and reports the suffix. ok is false for every
// other file. The directory part is kept: "main/binary-amd64/Packages.xz" is ("main/binary-amd64/Packages", ".xz").
func IndexBase(p string) (base, suffix string, ok bool) {
	for _, s := range parseSuffixes() {
		b, found := strings.CutSuffix(p, s)
		if !found {
			continue
		}
		name := path.Base(b)
		if name == "Packages" || name == "Sources" {
			return b, s, true
		}
		return "", "", false
	}
	return "", "", false
}

// IsSources reports whether the canonical index path names a Sources index.
func IsSources(indexPath string) bool { return path.Base(indexPath) == "Sources" }

// PreferredIndexes lists the Packages indexes (and, when sources is set, the Sources indexes) a release lists, one per canonical path,
// each as the variant the parse job prefers (xz, gz, zst, bz2, plain), sorted by path. A file below a *.diff directory is no index of
// this kind.
func PreferredIndexes(files []ReleaseFile, sources bool) []IndexRef {
	best := map[string]IndexRef{}
	rank := map[string]int{}
	for i, s := range parseSuffixes() {
		rank[s] = i
	}
	for _, f := range files {
		if strings.Contains(f.Path, ".diff/") || strings.Contains(f.Path, "/by-hash/") {
			continue
		}
		base, suffix, ok := IndexBase(f.Path)
		if !ok || (IsSources(base) && !sources) {
			continue
		}
		cur, have := best[base]
		if have {
			_, curSuffix, _ := IndexBase(cur.File)
			if rank[curSuffix] <= rank[suffix] {
				continue
			}
		}
		best[base] = IndexRef{Path: base, File: f.Path, SHA256: f.SHA256, Size: f.Size}
	}
	out := make([]IndexRef, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// FileOf finds the entry of a release's file list for a path relative to the release directory.
func FileOf(files []ReleaseFile, p string) (ReleaseFile, bool) {
	for _, f := range files {
		if f.Path == p {
			return f, true
		}
	}
	return ReleaseFile{}, false
}

// InScope reports whether an index of a release directory belongs to the scope of a name question: its component and architecture
// are among those the scope serves. An index at the top of the directory (a flat repository's Packages) is in every scope; a path
// of the form <component>/binary-<arch>/Packages is in it when both are named, and a Sources index when the scope serves its
// component (the architecture "source" is not a scope's).
func InScope(indexPath string, components, architectures []string) bool {
	parts := strings.Split(indexPath, "/")
	if len(parts) < 3 {
		return true
	}
	component := strings.Join(parts[:len(parts)-2], "/")
	kind := parts[len(parts)-2]
	if IsSources(indexPath) {
		return kind == "source" && contains(components, component)
	}
	arch, ok := strings.CutPrefix(kind, "binary-")
	if !ok {
		return false
	}
	return contains(components, component) && (arch == "all" || contains(architectures, arch))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
