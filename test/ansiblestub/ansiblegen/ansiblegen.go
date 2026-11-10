// Package ansiblegen generates Ansible collection archives and role archives in code: a gzip-compressed tar archive with the
// MANIFEST.json and FILES.json that ansible-galaxy builds, and the directory layout of a role as GitHub serves it. The bytes depend
// on the Spec alone (fixed modification times, owners and gzip header), so equal inputs give equal files and equal digests, and no
// archive is a fixture file.
//
// The counting stub (test/ansiblestub) serves these files, and the integration and e2e tests use the same generator to publish
// collections to a local repository. A generated collection installs with ansible-galaxy: the checksums of FILES.json and of
// MANIFEST.json are those of the members.
package ansiblegen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ModTime is the modification time of every member of a generated archive, so that equal inputs give equal bytes.
var ModTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Spec describes one collection version.
type Spec struct {
	Namespace string
	Name      string
	Version   string
	// Dependencies maps "ns.name" to a version requirement; nil means none.
	Dependencies map[string]string
	// RequiresAnsible is the content of meta/runtime.yml; default ">=2.14.0".
	RequiresAnsible string
	Authors         []string // default ["trove test <ansiblestub@trove.invalid>"]
	Description     string   // default "trove test collection <ns>.<name>"
	License         []string // default ["GPL-3.0-or-later"]
	Tags            []string // default ["test"]
	// Extra adds files (path to content) to the archive; the stub's digest modes use it to make a second, different archive of
	// the same version.
	Extra map[string]string
}

// Archive is a generated collection archive.
type Archive struct {
	Filename string
	Data     []byte
	SHA256   string
	Size     int64
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// FullName is "<namespace>.<name>".
func (s Spec) FullName() string { return s.Namespace + "." + s.Name }

// Filename is the name ansible-galaxy gives the archive: <namespace>-<name>-<version>.tar.gz.
func (s Spec) Filename() string { return s.Namespace + "-" + s.Name + "-" + s.Version + ".tar.gz" }

// Sum is the SHA-256 (hex) and the size of data.
func Sum(data []byte) (string, int64) {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), int64(len(data))
}

type file struct {
	name string
	data []byte
	dir  bool
}

// Files returns the members of the archive without MANIFEST.json and FILES.json, directories first, in path order.
func (s Spec) files() []file {
	runtime := "requires_ansible: '" + orDefault(s.RequiresAnsible, ">=2.14.0") + "'\n"
	files := map[string]string{
		"README.md":                        "# " + s.FullName() + "\n\nA test collection of the trove counting stub, version " + s.Version + ".\n",
		"meta/runtime.yml":                 runtime,
		"plugins/README.md":                "No plugins.\n",
		"roles/hello/tasks/main.yml":       "---\n- name: Say hello\n  ansible.builtin.debug:\n    msg: hello from " + s.FullName() + " " + s.Version + "\n",
		"roles/hello/meta/main.yml":        "---\ngalaxy_info:\n  author: trove\n  description: hello\n  license: GPL-3.0-or-later\n  min_ansible_version: '2.14'\n  platforms: []\ndependencies: []\n",
		"docs/" + s.Name + "/overview.rst": "Overview of " + s.FullName() + "\n",
	}
	for k, v := range s.Extra {
		files[k] = v
	}
	dirs := map[string]bool{}
	for name := range files {
		for i := 0; i < len(name); i++ {
			if name[i] == '/' {
				dirs[name[:i]] = true
			}
		}
	}
	var out []file
	for d := range dirs {
		out = append(out, file{name: d, dir: true})
	}
	for n, c := range files {
		out = append(out, file{name: n, data: []byte(c)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type filesEntry struct {
	Name         string  `json:"name"`
	FType        string  `json:"ftype"`
	ChksumType   *string `json:"chksum_type"`
	ChksumSHA256 *string `json:"chksum_sha256"`
	Format       int     `json:"format"`
}

// Collection builds the archive of a collection version.
func Collection(s Spec) (Archive, error) {
	if s.Namespace == "" || s.Name == "" || s.Version == "" {
		return Archive{}, errors.New("ansiblegen: namespace, name and version are required")
	}
	deps := s.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	sha := "sha256"
	list := []filesEntry{{Name: ".", FType: "dir", Format: 1}}
	members := s.files()
	for _, f := range members {
		if f.dir {
			list = append(list, filesEntry{Name: f.name, FType: "dir", Format: 1})
			continue
		}
		sum, _ := Sum(f.data)
		list = append(list, filesEntry{Name: f.name, FType: "file", ChksumType: &sha, ChksumSHA256: &sum, Format: 1})
	}
	filesJSON, err := marshalIndent(map[string]any{"files": list, "format": 1})
	if err != nil {
		return Archive{}, err
	}
	filesSum, _ := Sum(filesJSON)
	manifest, err := marshalIndent(map[string]any{
		"collection_info": map[string]any{
			"namespace":     s.Namespace,
			"name":          s.Name,
			"version":       s.Version,
			"authors":       orSlice(s.Authors, []string{"trove test <ansiblestub@trove.invalid>"}),
			"readme":        "README.md",
			"tags":          orSlice(s.Tags, []string{"test"}),
			"description":   orDefault(s.Description, "trove test collection "+s.FullName()),
			"license":       orSlice(s.License, []string{"GPL-3.0-or-later"}),
			"license_file":  nil,
			"dependencies":  deps,
			"repository":    "https://example.invalid/" + s.Namespace + "/" + s.Name,
			"documentation": "https://example.invalid/" + s.Namespace + "/" + s.Name + "/docs",
			"homepage":      "https://example.invalid/" + s.Namespace + "/" + s.Name,
			"issues":        "https://example.invalid/" + s.Namespace + "/" + s.Name + "/issues",
		},
		"file_manifest_file": map[string]any{"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": filesSum, "format": 1},
		"format":             1,
	})
	if err != nil {
		return Archive{}, err
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(f file) error {
		h := &tar.Header{Name: f.name, ModTime: ModTime, Uname: "root", Gname: "root", Format: tar.FormatUSTAR}
		if f.dir {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
			h.Name += "/"
		} else {
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(f.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(f.data)
		return err
	}
	ordered := append([]file{{name: "FILES.json", data: filesJSON}, {name: "MANIFEST.json", data: manifest}}, members...)
	for _, f := range ordered {
		if err := write(f); err != nil {
			return Archive{}, fmt.Errorf("ansiblegen: %s: %w", f.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return Archive{}, err
	}
	if err := zw.Close(); err != nil {
		return Archive{}, err
	}
	sum, size := Sum(buf.Bytes())
	return Archive{Filename: s.Filename(), Data: buf.Bytes(), SHA256: sum, Size: size}, nil
}

func orSlice(v, def []string) []string {
	if len(v) > 0 {
		return v
	}
	return def
}

// marshalIndent renders JSON with four spaces, as ansible-galaxy writes its manifests.
func marshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "    ")
}

// RoleSpec describes a role archive as GitHub serves a tag: one directory "<repo>-<tag>" with the role below it.
type RoleSpec struct {
	User string
	Repo string
	Tag  string
}

// Role builds the archive of a role tag.
func Role(s RoleSpec) ([]byte, error) {
	if s.User == "" || s.Repo == "" || s.Tag == "" {
		return nil, errors.New("ansiblegen: user, repo and tag are required")
	}
	top := s.Repo + "-" + s.Tag
	members := []file{
		{name: top, dir: true},
		{name: top + "/README.md", data: []byte("# " + s.Repo + "\n\nA test role of the trove counting stub, tag " + s.Tag + ".\n")},
		{name: top + "/defaults", dir: true},
		{name: top + "/defaults/main.yml", data: []byte("---\nrole_greeting: hello from " + s.User + "/" + s.Repo + " " + s.Tag + "\n")},
		{name: top + "/meta", dir: true},
		{name: top + "/meta/main.yml", data: []byte("---\ngalaxy_info:\n  author: " + s.User + "\n  description: a test role\n  license: MIT\n  min_ansible_version: '2.14'\n  platforms: []\ndependencies: []\n")},
		{name: top + "/tasks", dir: true},
		{name: top + "/tasks/main.yml", data: []byte("---\n- name: Say hello\n  ansible.builtin.debug:\n    msg: \"{{ role_greeting }}\"\n")},
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range members {
		h := &tar.Header{Name: f.name, ModTime: ModTime, Uname: "root", Gname: "root", Format: tar.FormatUSTAR}
		if f.dir {
			h.Typeflag, h.Mode, h.Name = tar.TypeDir, 0o755, f.name+"/"
		} else {
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(f.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
