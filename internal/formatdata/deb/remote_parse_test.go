package deb

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

func paragraph(t *testing.T, text string) control.Paragraph {
	t.Helper()
	p, err := control.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const sha = "0000000000000000000000000000000000000000000000000000000000000001"

func TestBinaryEntryReadsOneRowAndSkipsAParagraphThatBreaksTheRules(t *testing.T) {
	good := "Package: hello\nVersion: 1:2.10-3\nArchitecture: amd64\nFilename: ./pool/main/h/hello/hello_2.10-3_amd64.deb\nSize: 100\nSHA256: " + sha + "\n"
	var b batch
	if n := binaryEntry(paragraph(t, good), &b); n != 1 || b.arg.Paths[0] != "pool/main/h/hello/hello_2.10-3_amd64.deb" || b.arg.Sizes[0] != 100 || b.arg.Versions[0] != "1:2.10-3" {
		t.Fatalf("good = %d %+v", n, b.arg)
	}
	for name, doc := range map[string]string{
		"no SHA256":        strings.Replace(good, "SHA256: "+sha+"\n", "", 1),
		"a short SHA256":   strings.Replace(good, sha, "abcd", 1),
		"a path above":     strings.Replace(good, "./pool", "../pool", 1),
		"an absolute path": strings.Replace(good, "./pool", "/pool", 1),
		"a bad name":       strings.Replace(good, "Package: hello", "Package: Hello", 1),
		"a bad version":    strings.Replace(good, "1:2.10-3", "!", 1),
		"no Filename":      strings.Replace(good, "Filename: ./pool/main/h/hello/hello_2.10-3_amd64.deb\n", "", 1),
		"a negative size":  strings.Replace(good, "Size: 100", "Size: -1", 1),
	} {
		var skipped batch
		if n := binaryEntry(paragraph(t, doc), &skipped); n != 0 || skipped.n != 0 {
			t.Errorf("%s was read", name)
		}
	}
}

func TestSourceEntriesReadEveryFileOfAPackageBelowItsDirectory(t *testing.T) {
	doc := "Package: hello\nVersion: 2.10-3\nDirectory: pool/main/h/hello\nChecksums-Sha256:\n " + sha + " 10 hello_2.10-3.dsc\n " +
		sha + " 20 hello_2.10.orig.tar.gz\n " + sha + " 30 ../evil\n"
	var b batch
	if n := sourceEntries(paragraph(t, doc), &b); n != 2 || b.arg.Paths[0] != "pool/main/h/hello/hello_2.10-3.dsc" || b.arg.Architectures[1] != "source" {
		t.Errorf("sources = %d %+v", n, b.arg)
	}
}

func TestDecompressOpensTheFormsOfAnIndexUnderTheBound(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("Package: a\n"))
	_ = zw.Close()
	r, err := decompress("main/binary-amd64/Packages.gz", gz.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(r); string(got) != "Package: a\n" {
		t.Errorf("gz = %q", got)
	}
	if r, err = decompress("Packages", []byte("plain")); err != nil {
		t.Fatal(err)
	} else if got, _ := io.ReadAll(r); string(got) != "plain" {
		t.Errorf("plain = %q", got)
	}
	if _, err := decompress("Packages.gz", []byte("not gzip")); err == nil {
		t.Error("a file that is no gzip stream was opened")
	}
	if _, err := decompress("Packages.xz", []byte("not xz at all, not xz at all")); err == nil {
		t.Error("a file that is no xz stream was opened")
	}
	// A stream that expands beyond the bound fails with TooLarge and not quietly.
	lr := untrusted.Limit(strings.NewReader(strings.Repeat("x", 100)), 10)
	if _, err := io.ReadAll(lr); !untrusted.IsTooLarge(err) {
		t.Errorf("the bounded reader = %v", err)
	}
}

func TestParseDedupKeyCoalescesPerRepositoryDirectoryAndIndex(t *testing.T) {
	a := parseDedupKey(7, "dists/stable", "main/binary-amd64/Packages")
	if a == parseDedupKey(7, "dists/stable", "main/binary-arm64/Packages") || a == parseDedupKey(8, "dists/stable", "main/binary-amd64/Packages") ||
		a == parseDedupKey(7, "dists/testing", "main/binary-amd64/Packages") {
		t.Error("two index paths share a key")
	}
	if spec := parseSpec(ParseArgs{RepoID: 7, Dir: "dists/stable", Path: "p"}); spec.Kind != RemoteParseJobKind || spec.TargetKey != "7" {
		t.Errorf("spec = %+v", spec)
	}
}
