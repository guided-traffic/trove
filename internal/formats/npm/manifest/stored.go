package manifest

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ServerFields are the values trove sets in the stored manifest of a version;
// a client never supplies them (R-NPM-007).
type ServerFields struct {
	Name         string // the version's name, for _id
	Version      string // the canonical version, for _id
	Principal    string // the publishing principal's name, for _npmUser
	Integrity    string // sha512 SRI of the tarball
	Shasum       string // hex SHA-1 of the tarball
	FileCount    int64
	UnpackedSize int64
	// HasInstallScript is true when preinstall, install or postinstall is present
	// in the tarball's package.json, or it holds a top-level binding.gyp.
	HasInstallScript bool
	// HasShrinkwrap is true when the tarball holds a top-level npm-shrinkwrap.json.
	HasShrinkwrap bool
}

// HasInstallScript applies the rule of R-NPM-007 to the tarball's package.json:
// a preinstall, install or postinstall script, or a top-level binding.gyp.
func HasInstallScript(packaged Manifest, f Facts) bool {
	if f.HasBindingGyp() {
		return true
	}
	scripts, _ := packaged["scripts"].(map[string]any)
	for _, k := range comparedScripts() {
		if s, ok := scripts[k].(string); ok && s != "" {
			return true
		}
	}
	return false
}

// Stored returns the manifest trove stores for a version: the request's
// manifest with every server-controlled field removed and then set from s.
// "dist.tarball" is not stored; it is added when a document is rendered.
// "hasInstallScript" is written only when true, as the registry does, and
// "_hasShrinkwrap" always.
func Stored(request Manifest, s ServerFields) Manifest {
	out := request.Clone()
	if out == nil {
		out = Manifest{}
	}
	for k := range out {
		if strings.HasPrefix(k, "_") && k != "_id" && k != "_nodeVersion" && k != "_npmVersion" {
			delete(out, k)
		}
	}
	dist, _ := out["dist"].(map[string]any)
	if dist == nil {
		dist = map[string]any{}
	}
	for _, k := range []string{"tarball", "signatures", "attestations"} {
		delete(dist, k)
	}
	if s.Integrity != "" {
		dist["integrity"] = s.Integrity
	} else {
		delete(dist, "integrity")
	}
	dist["shasum"] = s.Shasum
	dist["fileCount"] = json.Number(strconv.FormatInt(s.FileCount, 10))
	dist["unpackedSize"] = json.Number(strconv.FormatInt(s.UnpackedSize, 10))
	out["dist"] = dist

	out["_id"] = s.Name + "@" + s.Version
	out["_npmUser"] = map[string]any{"name": s.Principal}
	out["_hasShrinkwrap"] = s.HasShrinkwrap
	if s.HasInstallScript {
		out["hasInstallScript"] = true
	} else {
		delete(out, "hasInstallScript")
	}
	return out
}
