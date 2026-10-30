package npmstub

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// The recorded set: trimmed real documents and their real tarballs, for the shapes a synthetic
// corpus cannot fake (trusted-publisher provenance, attestations and signatures in dist, a
// capitalised legacy name, widely used packages). One directory per package, "/" of a scoped name
// written as "__": document.json and the tarballs by file name. `npmstub record` writes them by
// hand from a live registry; nothing in a test run reaches the network.
//
//go:embed testdata/recorded
var recordedFS embed.FS

func embeddedRecorded() (fs.FS, error) { return fs.Sub(recordedFS, "testdata/recorded") }

// DirName is the directory of a recorded package.
func DirName(name string) string { return strings.ReplaceAll(name, "/", "__") }

// abbreviatedKeys are the fields of a version that the install document keeps.
var abbreviatedKeys = []string{
	"name", "version", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies",
	"peerDependenciesMeta", "bin", "directories", "dist", "engines", "cpu", "os", "libc", "deprecated",
	"hasInstallScript", "_hasShrinkwrap",
}

type recorded struct {
	name     string
	dir      string
	doc      map[string]json.RawMessage
	versions map[string]map[string]json.RawMessage
}

func loadRecorded(fsys fs.FS) (map[string]*recorded, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("recorded set: %w", err)
	}
	out := map[string]*recorded{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "document.json"))
		if err != nil {
			return nil, fmt.Errorf("recorded set: %w", err)
		}
		r := &recorded{dir: e.Name()}
		if err := json.Unmarshal(raw, &r.doc); err != nil {
			return nil, fmt.Errorf("recorded set %s: %w", e.Name(), err)
		}
		if err := json.Unmarshal(r.doc["name"], &r.name); err != nil || DirName(r.name) != e.Name() {
			return nil, fmt.Errorf("recorded set %s: the document names %q", e.Name(), r.name)
		}
		if err := json.Unmarshal(r.doc["versions"], &r.versions); err != nil {
			return nil, fmt.Errorf("recorded set %s: versions: %w", e.Name(), err)
		}
		out[r.name] = r
	}
	return out, nil
}

// render writes the document for a registry served at base, with every dist.tarball pointing there.
func (r *recorded) render(base string, abbreviated bool) ([]byte, error) {
	versions := make(map[string]map[string]json.RawMessage, len(r.versions))
	for ver, fields := range r.versions {
		out := make(map[string]json.RawMessage, len(fields))
		for k, v := range fields {
			if !abbreviated || slices.Contains(abbreviatedKeys, k) {
				out[k] = v
			}
		}
		var dist map[string]json.RawMessage
		if err := json.Unmarshal(out["dist"], &dist); err != nil {
			return nil, fmt.Errorf("recorded %s@%s: dist: %w", r.name, ver, err)
		}
		url, err := json.Marshal(corpus.TarballURL(base, r.name, ver))
		if err != nil {
			return nil, err
		}
		dist["tarball"] = url
		if out["dist"], err = json.Marshal(dist); err != nil {
			return nil, err
		}
		versions[ver] = out
	}
	doc := map[string]any{"name": r.name, "dist-tags": r.doc["dist-tags"], "versions": versions}
	if abbreviated {
		doc["modified"] = r.modified()
	} else {
		for k, v := range r.doc {
			if _, set := doc[k]; !set {
				doc[k] = v
			}
		}
	}
	return json.Marshal(doc)
}

func (r *recorded) modified() json.RawMessage {
	var times map[string]json.RawMessage
	if err := json.Unmarshal(r.doc["time"], &times); err != nil || times["modified"] == nil {
		return json.RawMessage(`""`)
	}
	return times["modified"]
}
