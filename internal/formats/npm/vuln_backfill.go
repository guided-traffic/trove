package npm

import "github.com/guided-traffic/trove/internal/formats"

// ArtifactSuffix names the files of a remote repository that are a version of a package: the tarballs. The scanning's backfill
// looks the cached files without a version up by it (Q-VULN-67).
func (*Format) ArtifactSuffix() string { return tarballSuffix }

// ArtifactVersion returns the package and the version a cached tarball is, as the fill records them (commitFill): the path's own
// classification, so that a file cached by an earlier release gets the version row the fill would give it now. A path that names
// no canonical package and version is no version.
func (f *Format) ArtifactVersion(path string) (pkg, version string, ok bool) {
	info := f.Classify(formats.Path{Path: path})
	if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
		return "", "", false
	}
	return info.Package, info.Version, true
}
