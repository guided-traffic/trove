package ansiblegen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func readTarGz(t *testing.T, b []byte) (names []string, data map[string][]byte) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	data = map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, data
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		data[h.Name] = body
	}
}

func TestCollectionIsDeterministic(t *testing.T) {
	spec := Spec{Namespace: "acme", Name: "tools", Version: "1.0.0"}
	a, err := Collection(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Collection(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Data, b.Data) || a.SHA256 != b.SHA256 {
		t.Fatal("two builds of one spec differ")
	}
	spec.Extra = map[string]string{"variant.txt": "x\n"}
	c, err := Collection(spec)
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA256 == a.SHA256 || c.Size == a.Size {
		t.Fatal("a spec with an extra file gives the same archive")
	}
	if a.Filename != "acme-tools-1.0.0.tar.gz" {
		t.Fatalf("filename %q", a.Filename)
	}
}

func TestCollectionManifestsMatchMembers(t *testing.T) {
	a, err := Collection(Spec{Namespace: "acme", Name: "tools", Version: "2.0.0", Dependencies: map[string]string{"acme.base": ">=1.0.0"}})
	if err != nil {
		t.Fatal(err)
	}
	_, data := readTarGz(t, a.Data)

	var manifest struct {
		Info struct {
			Namespace, Name, Version string
			Dependencies             map[string]string
		} `json:"collection_info"`
		FilesFile struct {
			Name   string `json:"name"`
			Sha256 string `json:"chksum_sha256"`
		} `json:"file_manifest_file"`
	}
	if err := json.Unmarshal(data["MANIFEST.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Info.Namespace != "acme" || manifest.Info.Name != "tools" || manifest.Info.Version != "2.0.0" || manifest.Info.Dependencies["acme.base"] != ">=1.0.0" {
		t.Fatalf("collection_info %+v", manifest.Info)
	}
	if sum, _ := Sum(data["FILES.json"]); sum != manifest.FilesFile.Sha256 || manifest.FilesFile.Name != "FILES.json" {
		t.Fatalf("MANIFEST.json names %s for FILES.json, the member is %s", manifest.FilesFile.Sha256, sum)
	}

	var files struct {
		Files []struct {
			Name   string  `json:"name"`
			FType  string  `json:"ftype"`
			Sha256 *string `json:"chksum_sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data["FILES.json"], &files); err != nil {
		t.Fatal(err)
	}
	if len(files.Files) < 2 || files.Files[0].Name != "." || files.Files[0].FType != "dir" {
		t.Fatalf("FILES.json starts with %+v", files.Files)
	}
	seenFile := false
	for _, f := range files.Files[1:] {
		if f.FType == "dir" {
			if _, ok := data[f.Name+"/"]; !ok {
				t.Errorf("directory %s listed but not a member", f.Name)
			}
			continue
		}
		seenFile = true
		sum, _ := Sum(data[f.Name])
		if f.Sha256 == nil || *f.Sha256 != sum {
			t.Errorf("%s: FILES.json says %v, the member is %s", f.Name, f.Sha256, sum)
		}
	}
	if !seenFile {
		t.Fatal("no file listed")
	}
	if string(data["meta/runtime.yml"]) != "requires_ansible: '>=2.14.0'\n" {
		t.Fatalf("runtime.yml %q", data["meta/runtime.yml"])
	}
}

func TestCollectionRequiresIdentity(t *testing.T) {
	if _, err := Collection(Spec{Namespace: "acme", Name: "tools"}); err == nil {
		t.Fatal("a spec without a version was built")
	}
}

func TestRoleArchiveLayout(t *testing.T) {
	b, err := Role(RoleSpec{User: "geerlingguy", Repo: "ansible-role-pip", Tag: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Role(RoleSpec{User: "geerlingguy", Repo: "ansible-role-pip", Tag: "2.0.0"})
	if !bytes.Equal(b, again) {
		t.Fatal("two builds of one role differ")
	}
	names, _ := readTarGz(t, b)
	want := map[string]bool{"ansible-role-pip-2.0.0/": true, "ansible-role-pip-2.0.0/meta/main.yml": true, "ansible-role-pip-2.0.0/tasks/main.yml": true}
	for _, n := range names {
		delete(want, n)
	}
	if len(want) != 0 {
		t.Fatalf("missing members %v of %v", want, names)
	}
	if _, err := Role(RoleSpec{User: "u", Repo: "r"}); err == nil {
		t.Fatal("a role without a tag was built")
	}
}
