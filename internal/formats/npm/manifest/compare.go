package manifest

import (
	"fmt"
	"reflect"
	"strings"
)

// Normalize returns a copy of m with the union of the normalization steps of
// R-NPM-007 applied, in the order the dependencies of the steps need. Applying
// it twice changes nothing.
func Normalize(m Manifest, f Facts) Manifest {
	out := m.Clone()
	if out == nil {
		out = Manifest{}
	}
	NormalizeName(out)
	NormalizeVersion(out)
	NormalizeDependencies(out)
	NormalizeBundleDependencies(out)
	NormalizeBin(out, f)
	NormalizeScripts(out)
	AddGypInstall(out, f)
	NormalizeEngines(out)
	return out
}

// comparedFields are the install-relevant fields of R-NPM-007 that the request
// manifest and the tarball's package.json must agree on.
func comparedFields() []string {
	return []string{
		"dependencies", "optionalDependencies", "peerDependencies", "peerDependenciesMeta",
		"bundleDependencies", "bin", "os", "cpu", "engines",
	}
}

// comparedScripts are the lifecycle scripts that run at install time.
func comparedScripts() []string { return []string{"preinstall", "install", "postinstall"} }

// Mismatches normalizes both manifests against the tarball's facts and returns
// the compared fields whose values differ, in a fixed order; scripts are named
// "scripts.<name>". A field that is absent, null, an empty object, an empty
// array or the empty string is the same as an absent one: none of them changes
// what a client installs or runs.
func Mismatches(request, packaged Manifest, f Facts) []string {
	a, b := Normalize(request, f), Normalize(packaged, f)
	var out []string
	for _, k := range comparedFields() {
		if !equalField(a[k], b[k]) {
			out = append(out, k)
		}
	}
	as, _ := a["scripts"].(map[string]any)
	bs, _ := b["scripts"].(map[string]any)
	for _, k := range comparedScripts() {
		if !equalField(as[k], bs[k]) {
			out = append(out, "scripts."+k)
		}
	}
	return out
}

// CheckConsistency is the check of R-NPM-007 step 3 with
// npm.publish.manifest_consistency: reject. It returns a 400
// npm_manifest_mismatch error naming the first differing field, or nil. A
// caller in warn mode uses Mismatches and audits the list.
func CheckConsistency(request, packaged Manifest, f Facts) error {
	diff := Mismatches(request, packaged, f)
	if len(diff) == 0 {
		return nil
	}
	return Mismatch(fmt.Sprintf("%s differs between the publish request and the package.json in the tarball", strings.Join(diff, ", ")))
}

// CheckIdentity requires that the tarball's package.json carries the name and
// version of the request, both after the normalization of R-NPM-007 (trimmed
// name, cleaned version). It always rejects, whatever the consistency mode.
func CheckIdentity(packaged Manifest, name, version string) error {
	p := Normalize(Manifest{"name": packaged["name"], "version": packaged["version"]}, Facts{})
	wantName, wantVersion := strings.TrimSpace(name), version
	if c, ok := CleanVersion(version); ok {
		wantVersion = c
	}
	gotName, _ := p["name"].(string)
	gotVersion, _ := p["version"].(string)
	if gotName != wantName {
		return Mismatch("the name in the package.json of the tarball is not the name of the publish request")
	}
	if gotVersion != wantVersion {
		return Mismatch("the version in the package.json of the tarball is not the version of the publish request")
	}
	return nil
}

func equalField(a, b any) bool {
	if isEmptyValue(a) && isEmptyValue(b) {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	default:
		return false
	}
}
