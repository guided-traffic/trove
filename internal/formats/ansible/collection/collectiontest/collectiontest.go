// Package collectiontest builds collection archives for tests: a valid archive from a [Spec] in the layout `ansible-galaxy collection
// build` writes (MANIFEST.json and FILES.json first, a directory entry per directory), and the entries of one as a list a test edits
// before it packs them, so a crafted archive differs from a valid one in exactly the thing under test. It is a test helper and
// imports the standard library only.
package collectiontest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// Spec describes a valid collection archive.
type Spec struct {
	Namespace, Name, Version string
	// Info are members merged into collection_info (authors, description, readme, ...).
	Info map[string]any
	// Dependencies are collection_info.dependencies.
	Dependencies map[string]string
	// Files are the regular files besides the generated documents: name to content. A "meta/runtime.yml" here is the runtime file.
	Files map[string]string
}

// Entry is one entry of the tar stream.
type Entry struct {
	Name string
	Body []byte
	// Type is the tar type flag; 0 means a regular file, or a directory when Dir is set.
	Type byte
	Dir  bool
	// Link is the target of a link entry.
	Link string
}

// Sum is the hex SHA-256 of b.
func Sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// FileName is the name `ansible-galaxy collection build` gives the archive.
func (s Spec) FileName() string { return s.Namespace + "-" + s.Name + "-" + s.Version + ".tar.gz" }

// Build packs the valid archive of the spec.
func Build(s Spec) []byte { return Pack(Entries(s)) }

// Entries are the entries of the valid archive: MANIFEST.json, FILES.json, then the directories and files in name order.
func Entries(s Spec) []Entry {
	type fileEntry struct {
		Name         string  `json:"name"`
		FType        string  `json:"ftype"`
		ChksumType   *string `json:"chksum_type"`
		ChksumSHA256 *string `json:"chksum_sha256"`
		Format       int     `json:"format"`
	}
	sha := "sha256"
	listed := []fileEntry{{Name: ".", FType: "dir", Format: 1}}
	var names []string
	for n := range s.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	dirs := map[string]bool{}
	var body []Entry
	for _, n := range names {
		for d := path.Dir(n); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	var all []string
	for d := range dirs {
		all = append(all, d)
	}
	all = append(all, names...)
	sort.Strings(all)
	for _, n := range all {
		if dirs[n] {
			listed = append(listed, fileEntry{Name: n, FType: "dir", Format: 1})
			body = append(body, Entry{Name: n, Dir: true})
			continue
		}
		c := []byte(s.Files[n])
		h := Sum(c)
		listed = append(listed, fileEntry{Name: n, FType: "file", ChksumType: &sha, ChksumSHA256: &h, Format: 1})
		body = append(body, Entry{Name: n, Body: c})
	}
	files, _ := json.Marshal(map[string]any{"files": listed, "format": 1})
	info := map[string]any{
		"namespace": s.Namespace, "name": s.Name, "version": s.Version, "authors": []string{"trove tests"},
		"readme": "README.md", "tags": []string{}, "description": "A test collection.", "license": []string{"MIT"},
		"license_file": nil, "repository": "", "documentation": "", "homepage": "", "issues": "",
		"dependencies": map[string]string{},
	}
	for k, v := range s.Info {
		info[k] = v
	}
	if s.Dependencies != nil {
		info["dependencies"] = s.Dependencies
	}
	manifest, _ := json.Marshal(map[string]any{
		"collection_info": info,
		"file_manifest_file": map[string]any{
			"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": Sum(files), "format": 1,
		},
		"format": 1,
	})
	return append([]Entry{{Name: "MANIFEST.json", Body: manifest}, {Name: "FILES.json", Body: files}}, body...)
}

// Pack writes the entries as a gzip-compressed tar archive.
func Pack(es []Entry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.Name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.Body))}
		switch {
		case e.Type != 0:
			h.Typeflag = e.Type
			h.Linkname = e.Link
			if e.Type == tar.TypeSymlink || e.Type == tar.TypeLink {
				h.Size = 0
			}
		case e.Dir:
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
			if !strings.HasSuffix(h.Name, "/") {
				h.Name += "/"
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			panic(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(e.Body); err != nil {
				panic(err)
			}
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

// Find returns the index of the entry named name, or -1.
func Find(es []Entry, name string) int {
	for i, e := range es {
		if e.Name == name {
			return i
		}
	}
	return -1
}

// Refile returns the manifest entries with FILES.json replaced by files and the digest in MANIFEST.json recomputed, so a test that
// changes FILES.json keeps the archive consistent in everything else.
func Refile(es []Entry, files []byte) []Entry {
	out := append([]Entry(nil), es...)
	i, j := Find(out, "FILES.json"), Find(out, "MANIFEST.json")
	out[i].Body = files
	var m map[string]any
	if err := json.Unmarshal(out[j].Body, &m); err != nil {
		panic(err)
	}
	m["file_manifest_file"].(map[string]any)["chksum_sha256"] = Sum(files)
	out[j].Body, _ = json.Marshal(m)
	return out
}
