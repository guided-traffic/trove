package collection_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

func spec() collectiontest.Spec {
	return collectiontest.Spec{
		Namespace: "acme", Name: "tools", Version: "1.0.0",
		Dependencies: map[string]string{"community.general": ">=1.0.0"},
		Files: map[string]string{
			"README.md":            "# tools\n",
			"plugins/modules/x.py": "print('x')\n",
			"meta/runtime.yml":     "---\nrequires_ansible: '>=2.14.0'\n",
		},
	}
}

func read(t *testing.T, data []byte) (*collection.Archive, error) {
	t.Helper()
	return collection.ReadArchive(context.Background(), bytes.NewReader(data), collection.DefaultLimits())
}

func wantReason(t *testing.T, err error, reason string) *collection.Error {
	t.Helper()
	var ce *collection.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want a collection.Error with reason %s", err, reason)
	}
	if ce.Reason != reason {
		t.Fatalf("reason = %s (%s), want %s", ce.Reason, ce.Detail, reason)
	}
	return ce
}

func TestR_ANS_010_AValidArchiveIsAccepted(t *testing.T) {
	a, err := read(t, collectiontest.Build(spec()))
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != "acme" || a.Name != "tools" || a.Version != "1.0.0" || a.FileName() != "acme-tools-1.0.0.tar.gz" {
		t.Errorf("identity = %+v", a)
	}
	if a.RequiresAnsible != ">=2.14.0" || a.Files != 3 || a.ReadmeName != "README.md" || len(a.Warnings) != 0 {
		t.Errorf("RequiresAnsible %q, Files %d, ReadmeName %q, Warnings %v", a.RequiresAnsible, a.Files, a.ReadmeName, a.Warnings)
	}
	if a.Dependencies["community.general"] != ">=1.0.0" || len(a.Dependencies) != 1 {
		t.Errorf("Dependencies = %v", a.Dependencies)
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(a.Info, &info); err != nil {
		t.Fatalf("Info is not JSON: %v", err)
	}
	for _, k := range []string{"namespace", "name", "version", "dependencies"} {
		if _, ok := info[k]; ok {
			t.Errorf("Info holds %q, which is not metadata", k)
		}
	}
	if string(info["description"]) != `"A test collection."` || string(info["license_file"]) != "null" {
		t.Errorf("Info = %s", a.Info)
	}
}

// TestR_ANS_026_ReadFileReturnsOneMemberForAScreen: the README of an archive is found by streaming up to it, cut at the bound, and a
// member the archive lacks is not found.
func TestR_ANS_026_ReadFileReturnsOneMemberForAScreen(t *testing.T) {
	s := spec()
	s.Files["docs/big.md"] = strings.Repeat("0123456789", 20_000)
	data := collectiontest.Build(s)
	got, found, err := collection.ReadFile(context.Background(), bytes.NewReader(data), collection.DefaultLimits(), "README.md", 64<<10)
	if err != nil || !found || string(got) != "# tools\n" {
		t.Fatalf("README = %q %v %v", got, found, err)
	}
	got, found, err = collection.ReadFile(context.Background(), bytes.NewReader(data), collection.DefaultLimits(), "docs/big.md", 1000)
	if err != nil || !found || len(got) != 1000 {
		t.Fatalf("a member over the bound: %d bytes, %v, %v", len(got), found, err)
	}
	if _, found, err = collection.ReadFile(context.Background(), bytes.NewReader(data), collection.DefaultLimits(), "nope.md", 1000); err != nil || found {
		t.Errorf("a member the archive lacks: %v %v", found, err)
	}
	if _, _, err = collection.ReadFile(context.Background(), bytes.NewReader([]byte("not an archive")), collection.DefaultLimits(), "README.md", 1000); err == nil {
		t.Error("an archive that is none was read")
	}
}

func TestR_ANS_010_NoRuntimeFileMeansNoRequiresAnsible(t *testing.T) {
	s := spec()
	delete(s.Files, "meta/runtime.yml")
	a, err := read(t, collectiontest.Build(s))
	if err != nil || a.RequiresAnsible != "" || len(a.Warnings) != 0 {
		t.Fatalf("%+v, %v", a, err)
	}
}

func TestR_ANS_010_ALeadingDotSlashIsAccepted(t *testing.T) {
	es := collectiontest.Entries(spec())
	for i := range es {
		es[i].Name = "./" + es[i].Name
	}
	if _, err := read(t, collectiontest.Pack(es)); err != nil {
		t.Fatal(err)
	}
}

func TestR_ANS_010_MembersInAnyOrderAreAccepted(t *testing.T) {
	es := collectiontest.Entries(spec())
	rev := make([]collectiontest.Entry, len(es))
	for i, e := range es {
		rev[len(es)-1-i] = e
	}
	if _, err := read(t, collectiontest.Pack(rev)); err != nil {
		t.Fatalf("the documents last: %v", err)
	}
}

func TestR_ANS_010_RuntimeFileThatCannotBeTrustedIsIgnoredWithAWarning(t *testing.T) {
	for name, yml := range map[string]string{
		"an anchor":       "requires_ansible: &a '>=2.14'\nother: *a\n",
		"a tag":           "requires_ansible: !!python/object:os.system 'x'\n",
		"a merge key":     "base: &b {a: 1}\nrequires_ansible: x\nm:\n  <<: *b\n",
		"not a mapping":   "- requires_ansible\n",
		"not valid yaml":  "requires_ansible: [\n",
		"a number":        "requires_ansible: 2.14\n",
		"a nested value":  "requires_ansible: {a: b}\n",
		"too many nodes":  "requires_ansible: ok\nlist: [" + strings.Repeat("1,", collection.MaxRuntimeNodes) + "1]\n",
		"a long value":    "requires_ansible: '" + strings.Repeat("x", 300) + "'\n",
		"a control byte":  "requires_ansible: \"a\\u0007b\"\n",
		"an alias at top": "a: &x 1\nb: *x\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := spec()
			s.Files["meta/runtime.yml"] = yml
			a, err := read(t, collectiontest.Build(s))
			if err != nil {
				t.Fatal(err)
			}
			if a.RequiresAnsible != "" || len(a.Warnings) != 1 {
				t.Errorf("RequiresAnsible %q, Warnings %v", a.RequiresAnsible, a.Warnings)
			}
		})
	}
	t.Run("an oversized file is a bound crossed", func(t *testing.T) {
		s := spec()
		s.Files["meta/runtime.yml"] = "requires_ansible: ok\n#" + strings.Repeat("x", collection.MaxRuntimeBytes)
		_, err := read(t, collectiontest.Build(s))
		if ce := wantReason(t, err, collection.ReasonBound); !ce.Bound {
			t.Error("Bound is false")
		}
	})
}

// TestR_ANS_031_CraftedArchivesAreRefusedEachForItsReason is the crafted corpus of R-ANS-031 and the acceptance list of R-ANS-010.
func TestR_ANS_031_CraftedArchivesAreRefusedEachForItsReason(t *testing.T) {
	edit := func(f func(es []collectiontest.Entry) []collectiontest.Entry) []byte {
		return collectiontest.Pack(f(collectiontest.Entries(spec())))
	}
	manifestWith := func(mutate func(m map[string]any)) []byte {
		return edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			i := collectiontest.Find(es, "MANIFEST.json")
			var m map[string]any
			if err := json.Unmarshal(es[i].Body, &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			es[i].Body, _ = json.Marshal(m)
			return es
		})
	}
	info := func(m map[string]any) map[string]any { return m["collection_info"].(map[string]any) }
	filesWith := func(mutate func(files []any) []any) []byte {
		return edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			i := collectiontest.Find(es, "FILES.json")
			var m map[string]any
			if err := json.Unmarshal(es[i].Body, &m); err != nil {
				t.Fatal(err)
			}
			m["files"] = mutate(m["files"].([]any))
			b, _ := json.Marshal(m)
			return collectiontest.Refile(es, b)
		})
	}
	cases := []struct {
		name   string
		data   []byte
		reason string
	}{
		{"not gzip", []byte("this is not an archive"), collection.ReasonUnreadable},
		{"empty", nil, collection.ReasonUnreadable},
		{"a symlink member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "plugins/link", Type: tar.TypeSymlink, Link: "/etc/passwd"})
		}), collection.ReasonUnreadable},
		{"a hard link member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "plugins/link", Type: tar.TypeLink, Link: "README.md"})
		}), collection.ReasonUnreadable},
		{"a device member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "dev", Type: tar.TypeChar})
		}), collection.ReasonUnreadable},
		{"a member ../x", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "../x", Body: []byte("x")})
		}), collection.ReasonUnsafeName},
		{"a member plugins/../../x", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "plugins/../../x", Body: []byte("x")})
		}), collection.ReasonUnsafeName},
		{"an absolute member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "/etc/x", Body: []byte("x")})
		}), collection.ReasonUnsafeName},
		{"a backslash member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "a\\b", Body: []byte("x")})
		}), collection.ReasonUnsafeName},
		{"a duplicate member name", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "README.md", Body: []byte("other")})
		}), collection.ReasonUnreadable},
		{"no MANIFEST.json", edit(func(es []collectiontest.Entry) []collectiontest.Entry { return es[1:] }), collection.ReasonMissingManifest},
		{"no FILES.json", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es[:1:1], es[2:]...)
		}), collection.ReasonMissingFiles},
		{"a MANIFEST.json in a directory", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			es[0].Name = "pkg/MANIFEST.json"
			return es
		}), collection.ReasonMissingManifest},
		{"a MANIFEST.json that is not JSON", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			es[0].Body = []byte("{")
			return es
		}), collection.ReasonManifestInvalid},
		{"a manifest without collection_info", manifestWith(func(m map[string]any) { delete(m, "collection_info") }), collection.ReasonManifestInvalid},
		{"a manifest that does not describe FILES.json", manifestWith(func(m map[string]any) {
			m["file_manifest_file"].(map[string]any)["name"] = "OTHER.json"
		}), collection.ReasonManifestInvalid},
		{"a FILES.json whose digest differs from the manifest's", manifestWith(func(m map[string]any) {
			m["file_manifest_file"].(map[string]any)["chksum_sha256"] = strings.Repeat("0", 64)
		}), collection.ReasonFilesChecksum},
		{"a manifest name with upper case", manifestWith(func(m map[string]any) { info(m)["name"] = "Tools" }), collection.ReasonInvalidName},
		{"a namespace with a dot", manifestWith(func(m map[string]any) { info(m)["namespace"] = "a.b" }), collection.ReasonInvalidName},
		{"a namespace of digits", manifestWith(func(m map[string]any) { info(m)["namespace"] = "123" }), collection.ReasonInvalidName},
		{"a name that starts with an underscore", manifestWith(func(m map[string]any) { info(m)["name"] = "_x" }), collection.ReasonInvalidName},
		{"a missing name", manifestWith(func(m map[string]any) { delete(info(m), "name") }), collection.ReasonInvalidName},
		{"a version 1.0", manifestWith(func(m map[string]any) { info(m)["version"] = "1.0" }), collection.ReasonInvalidVersion},
		{"a version that is a number", manifestWith(func(m map[string]any) { info(m)["version"] = 1 }), collection.ReasonInvalidVersion},
		{"a dependency that is no collection name", manifestWith(func(m map[string]any) {
			info(m)["dependencies"] = map[string]string{"a.b.c": "*"}
		}), collection.ReasonInvalidDependencies},
		{"a dependency requirement that is no string", manifestWith(func(m map[string]any) {
			info(m)["dependencies"] = map[string]any{"a.b": 1}
		}), collection.ReasonInvalidDependencies},
		{"dependencies that are a list", manifestWith(func(m map[string]any) { info(m)["dependencies"] = []string{"a.b"} }), collection.ReasonInvalidDependencies},
		{"too many dependencies", manifestWith(func(m map[string]any) {
			deps := map[string]string{}
			for i := range collection.MaxDependencies + 1 {
				deps["a.n"+strings.Repeat("x", 1)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] = "*"
			}
			info(m)["dependencies"] = deps
		}), collection.ReasonInvalidDependencies},
		{"a FILES.json that lies about a digest", filesWith(func(files []any) []any {
			for _, f := range files {
				if e := f.(map[string]any); e["name"] == "README.md" {
					e["chksum_sha256"] = strings.Repeat("a", 64)
				}
			}
			return files
		}), collection.ReasonMemberChecksum},
		{"a FILES.json that lists a file the archive lacks", filesWith(func(files []any) []any {
			return append(files, map[string]any{"name": "ghost.txt", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": strings.Repeat("a", 64), "format": 1})
		}), collection.ReasonMemberMissing},
		{"a FILES.json that does not list a member", filesWith(func(files []any) []any {
			var out []any
			for _, f := range files {
				if f.(map[string]any)["name"] != "plugins/modules/x.py" {
					out = append(out, f)
				}
			}
			return out
		}), collection.ReasonUnlistedMember},
		{"an unlisted member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return append(es, collectiontest.Entry{Name: "extra.txt", Body: []byte("x")})
		}), collection.ReasonUnlistedMember},
		{"a changed member", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			es[collectiontest.Find(es, "README.md")].Body = []byte("changed")
			return es
		}), collection.ReasonMemberChecksum},
		{"a listed file without a digest", filesWith(func(files []any) []any {
			for _, f := range files {
				if e := f.(map[string]any); e["name"] == "README.md" {
					e["chksum_sha256"] = nil
				}
			}
			return files
		}), collection.ReasonFilesInvalid},
		{"an entry type that is neither file nor dir", filesWith(func(files []any) []any {
			return append(files, map[string]any{"name": "x", "ftype": "link", "format": 1})
		}), collection.ReasonFilesInvalid},
		{"a file listed twice", filesWith(func(files []any) []any {
			for _, f := range files {
				if e := f.(map[string]any); e["name"] == "README.md" {
					return append(files, e)
				}
			}
			return files
		}), collection.ReasonFilesInvalid},
		{"a FILES.json that is not JSON", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			return collectiontest.Refile(es, []byte("[1,2"))
		}), collection.ReasonFilesInvalid},
		{"a manifest nested 100,000 deep", edit(func(es []collectiontest.Entry) []collectiontest.Entry {
			es[0].Body = []byte(strings.Repeat("[", 100_000))
			return es
		}), collection.ReasonBound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := read(t, tc.data)
			wantReason(t, err, tc.reason)
		})
	}
}

func TestR_ANS_031_BoundsAreRefusedAsBounds(t *testing.T) {
	t.Run("more entries than the limit", func(t *testing.T) {
		data, err := readAll(crafted.TarGzEntries(100_000).Open())
		if err != nil {
			t.Fatal(err)
		}
		_, err = read(t, data)
		if ce := wantReason(t, err, collection.ReasonBound); !ce.Bound {
			t.Error("Bound is false")
		}
	})
	t.Run("a manifest of 2 GiB is never inflated", func(t *testing.T) {
		rc := crafted.TarGzEntry("MANIFEST.json", 2<<30).Open()
		defer func() { _ = rc.Close() }()
		data, err := readAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		_, err = read(t, data)
		wantReason(t, err, collection.ReasonBound)
	})
	t.Run("a member that compresses beyond the ratio", func(t *testing.T) {
		s := spec()
		s.Files["big.bin"] = strings.Repeat("\x00", 16<<20)
		_, err := read(t, collectiontest.Build(s))
		wantReason(t, err, collection.ReasonBound)
	})
	t.Run("a context that ended is the caller's error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		data := collectiontest.Build(spec())
		_, err := collection.ReadArchive(ctx, bytes.NewReader(data), collection.DefaultLimits())
		var ce *collection.Error
		if err == nil || errors.As(err, &ce) {
			t.Errorf("error = %v, want the context's", err)
		}
	})
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(readerFunc(r.Read))
	return b.Bytes(), err
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestR_ANS_002_PartsAndFileNames(t *testing.T) {
	for _, s := range []string{"acme", "a1", "a_b", "x" + strings.Repeat("a", 63), "9a", "a9"} {
		if !collection.ValidPart(s) {
			t.Errorf("ValidPart(%q) = false", s)
		}
	}
	for _, s := range []string{"", "_a", "A", "Acme", "a-b", "a.b", "123", "_", "a b", "é", "x" + strings.Repeat("a", 64)} {
		if collection.ValidPart(s) {
			t.Errorf("ValidPart(%q) = true", s)
		}
	}
	ns, n, ok := collection.SplitName("community.general")
	if !ok || ns != "community" || n != "general" {
		t.Errorf("SplitName = %q %q %v", ns, n, ok)
	}
	for _, s := range []string{"a.b.c", "a", "_a.b", "A.b", ".b", "a."} {
		if _, _, ok := collection.SplitName(s); ok {
			t.Errorf("SplitName(%q) is ok", s)
		}
	}
	if got := collection.FileName("acme", "tools", "1.0.0-rc.1"); got != "acme-tools-1.0.0-rc.1.tar.gz" {
		t.Errorf("FileName = %q", got)
	}
	ns, n, v, ok := collection.ParseFileName("acme-tools-1.0.0-rc.1.tar.gz")
	if !ok || ns != "acme" || n != "tools" || v != "1.0.0-rc.1" {
		t.Errorf("ParseFileName = %q %q %q %v", ns, n, v, ok)
	}
	for _, s := range []string{"acme-tools.tar.gz", "acme-tools-1.0.0.zip", "Acme-tools-1.0.0.tar.gz", "acme-tools-.tar.gz", "-tools-1.0.0.tar.gz", "acme.tar.gz"} {
		if _, _, _, ok := collection.ParseFileName(s); ok {
			t.Errorf("ParseFileName(%q) is ok", s)
		}
	}
}

// FuzzR_ANS_031_ReadArchive: whatever the bytes, the reader returns an archive or an error and never panics, never runs past the
// limits it was given, and an archive it accepts has a valid identity.
func FuzzR_ANS_031_ReadArchive(f *testing.F) {
	valid := collectiontest.Build(spec())
	f.Add(valid)
	f.Add(valid[:len(valid)/2])
	f.Add([]byte{0x1f, 0x8b})
	f.Add(collectiontest.Pack([]collectiontest.Entry{{Name: "MANIFEST.json", Body: []byte("{}")}}))
	lim := untrusted.DefaultLimits()
	lim.Entries, lim.UncompressedBytes, lim.StreamBytes = 200, 8<<20, 16<<20
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := collection.ReadArchive(context.Background(), bytes.NewReader(data), lim)
		if err != nil {
			return
		}
		if !collection.ValidPart(a.Namespace) || !collection.ValidPart(a.Name) || a.Version == "" {
			t.Fatalf("accepted archive with identity %q %q %q", a.Namespace, a.Name, a.Version)
		}
	})
}
