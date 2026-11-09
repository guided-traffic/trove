package index

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
)

func para(pkg, ver, arch, filename string) string {
	return fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nFilename: %s\nSize: 1\nSHA256: %s\n", pkg, ver, arch, filename, strings.Repeat("a", 64))
}

func keepAll(string) bool { return true }

// R-DEB-025, Q-DEB-38: the first member in the order keeps a package, version and architecture; a later member's paragraph for it is left
// out; the Filename of a kept paragraph is the member path and every other field stays as it was.
func TestMergeMemberTheFirstMemberKeepsAVersionAndFilenamesAreMemberPaths(t *testing.T) {
	seen := NewSeen(0)
	var out Plain
	first := para("hello", "1.0", "amd64", "pool/h/hello/hello_1.0_amd64.deb") + "\n" + para("hello", "2.0", "amd64", "pool/h/hello/hello_2.0_amd64.deb")
	second := para("hello", "2.0", "amd64", "pool/main/h/hello/other.deb") + "\n" + para("hello", "3.0", "amd64", "pool/main/h/hello/hello_3.0_amd64.deb")
	r1, err := MergeMember(context.Background(), &out, seen, "local", strings.NewReader(first), keepAll)
	if err != nil || r1.Kept != 2 {
		t.Fatalf("first: %+v %v", r1, err)
	}
	r2, err := MergeMember(context.Background(), &out, seen, "remote", strings.NewReader(second), keepAll)
	if err != nil || r2.Kept != 1 || r2.Others != 1 {
		t.Fatalf("second: %+v %v", r2, err)
	}
	got := map[string]string{}
	rd := control.NewReader(bytes.NewReader(out.Bytes()), false)
	for {
		p, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[p.Value("Version")] = p.Value("Filename")
		if p.Value("SHA256") != strings.Repeat("a", 64) || p.Value("Size") != "1" {
			t.Errorf("a field changed: %v", p.Fields)
		}
	}
	want := map[string]string{
		"1.0": "members/local/pool/h/hello/hello_1.0_amd64.deb",
		"2.0": "members/local/pool/h/hello/hello_2.0_amd64.deb",
		"3.0": "members/remote/pool/main/h/hello/hello_3.0_amd64.deb",
	}
	for v, f := range want {
		if got[v] != f {
			t.Errorf("version %s: Filename %q, want %q", v, got[v], f)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d paragraphs, want %d", len(got), len(want))
	}
}

// R-DEB-034: a paragraph with a name, version or architecture outside the grammar, or a Filename that points elsewhere, is left out and
// counted; the names the keep function refuses are not merged.
func TestMergeMemberSkipsWhatDoesNotValidateAndWhatIsNotKept(t *testing.T) {
	doc := strings.Join([]string{
		para("good", "1.0", "amd64", "pool/g/good/good_1.0_amd64.deb"),
		para("Bad_Name", "1.0", "amd64", "pool/b/x.deb"),
		para("evil", "1.0", "amd64", "../../etc/passwd"),
		para("evil2", "1.0", "amd64", "/abs/x.deb"),
		para("evil3", "1.0", "amd64", "pool/a/../b.deb"),
		para("evil4", "1.0", "amd64", `pool\x.deb`),
		para("badver", "bad version", "amd64", "pool/b/x.deb"),
		para("other", "1.0", "amd64", "pool/o/other/other_1.0_amd64.deb"),
	}, "\n")
	var out Plain
	res, err := MergeMember(context.Background(), &out, NewSeen(0), "m", strings.NewReader(doc), func(n string) bool { return n != "other" })
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != 1 || res.Skipped != 6 || res.Others != 1 {
		t.Errorf("%+v", res)
	}
}

// Q-DEB-38: the set of seen hashes is bounded; a merge over more distinct packages than the bound fails with ErrTooMany.
func TestMergeMemberIsBoundedByTheSeenSet(t *testing.T) {
	var doc strings.Builder
	for i := 0; i < 5; i++ {
		if i > 0 {
			doc.WriteString("\n")
		}
		doc.WriteString(para(fmt.Sprintf("pkg%d", i), "1.0", "amd64", fmt.Sprintf("pool/p/pkg%d.deb", i)))
	}
	var out Plain
	_, err := MergeMember(context.Background(), &out, NewSeen(3), "m", strings.NewReader(doc.String()), keepAll)
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("err = %v, want ErrTooMany", err)
	}
}

// R-DEB-026: the same members and the same documents give the same bytes, plain and compressed.
func TestMergeIsDeterministic(t *testing.T) {
	doc := para("a-pkg", "1.0", "amd64", "pool/a/a.deb") + "\n" + para("b-pkg", "1.0", "amd64", "pool/b/b.deb")
	build := func() []byte {
		w, err := NewPackagesWriter(true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := MergeMember(context.Background(), w, NewSeen(0), "m", strings.NewReader(doc), keepAll); err != nil {
			t.Fatal(err)
		}
		listed, files, err := w.Close("main/binary-amd64/Packages")
		if err != nil {
			t.Fatal(err)
		}
		var all bytes.Buffer
		all.WriteString(listed.SHA256)
		for _, f := range files {
			all.WriteString(f.Path + f.SHA256)
		}
		return all.Bytes()
	}
	if !bytes.Equal(build(), build()) {
		t.Error("two merges of the same document differ")
	}
}

// R-DEB-026: the compressed files hold the merged index and the Release lists the uncompressed one (Q-DEB-56).
func TestPackagesWriterWritesTheVariants(t *testing.T) {
	w, err := NewPackagesWriter(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MergeMember(context.Background(), w, NewSeen(0), "m", strings.NewReader(para("a-pkg", "1.0", "amd64", "pool/a/a.deb")), keepAll); err != nil {
		t.Fatal(err)
	}
	if w.Paragraphs() != 1 {
		t.Errorf("paragraphs = %d", w.Paragraphs())
	}
	listed, files, err := w.Close("main/binary-amd64/Packages")
	if err != nil {
		t.Fatal(err)
	}
	if listed.Path != "main/binary-amd64/Packages" || len(files) != 2 {
		t.Fatalf("listed %q, %d files", listed.Path, len(files))
	}
	for _, f := range files {
		if f.Path != "main/binary-amd64/Packages.gz" && f.Path != "main/binary-amd64/Packages.xz" {
			t.Errorf("file %q", f.Path)
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(files[0].Body))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if int64(len(plain)) != listed.Size {
		t.Errorf("the listed size %d is not the plain size %d", listed.Size, len(plain))
	}
}

// R-DEB-025: the earliest Valid-Until of the members' releases is the Valid-Until of the merged Release; none gives none.
func TestAssembleCarriesTheValidUntil(t *testing.T) {
	date := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	until := date.Add(48 * time.Hour)
	s := Settings{}
	with, err := Assemble("stable", []string{"amd64"}, []string{"main"}, nil, nil, s, date, &until)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with.Release), "Valid-Until: Wed, 11 Nov 2026 12:00:00 UTC") {
		t.Errorf("Release:\n%s", with.Release)
	}
	without, err := Assemble("stable", []string{"amd64"}, []string{"main"}, nil, nil, s, date, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without.Release), "Valid-Until") {
		t.Errorf("Release:\n%s", without.Release)
	}
}

func FuzzMergeMember(f *testing.F) {
	f.Add(para("hello", "1.0", "amd64", "pool/h/hello.deb"))
	f.Add("Package: x\n\n\n\nVersion")
	f.Fuzz(func(t *testing.T, doc string) {
		var out Plain
		_, _ = MergeMember(context.Background(), &out, NewSeen(100), "m", strings.NewReader(doc), keepAll)
		rd := control.NewReader(bytes.NewReader(out.Bytes()), false)
		for {
			p, err := rd.Next()
			if err != nil {
				return
			}
			if !strings.HasPrefix(p.Value("Filename"), "members/m/") {
				t.Fatalf("a merged Filename leaves the member path: %q", p.Value("Filename"))
			}
		}
	})
}
