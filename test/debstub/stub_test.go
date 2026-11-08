package debstub

import (
	"bytes"
	"crypto/md5"  //nolint:gosec // the test checks the MD5Sum list of a Release
	"crypto/sha1" //nolint:gosec // the test checks the SHA1 list of a Release
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub/debgen"
)

func TestR_DEB_035_KeysAreFixedAndTroveReadsThem(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(Config{})
	for i, k := range a.Keys() {
		if k.Fingerprint != b.Keys()[i].Fingerprint || k.Public != b.Keys()[i].Public {
			t.Errorf("key %s differs between two stubs", k.Name)
		}
		if len(k.Fingerprint) != 40 {
			t.Errorf("fingerprint %q", k.Fingerprint)
		}
	}
	// The fingerprints are pinned: documents and tests name them (docs/developer/deb.md).
	for name, want := range map[string]string{
		"a": "744FA9DC6DDF29FCBBDCFC3F0F1B0C0EE2D15353", "b": "1740FB0FDF7BEDEAE568BFDFA14B934907B0A66E",
		"c": "112CAA63A6EDBB4E9727D9D67BF765B76B7E6242", "d": "AA0B4230C41119252C6CFF1C942F62DCF3C4BBE2",
	} {
		if k, _ := a.Key(name); k.Fingerprint != want {
			t.Errorf("key %s = %s, want %s", name, k.Fingerprint, want)
		}
	}
	certs, err := openpgp.ParseCertificates(mustRing(t, a, "a", "b", "c"), time.Now(), openpgp.Limits{})
	if err != nil || len(certs) != 3 {
		t.Fatalf("trove reads keys a to c: %v", err)
	}
	for _, c := range certs {
		if c.Algorithm != openpgp.AlgorithmEdDSALegacy || !c.Created.Equal(KeyCreated) || !c.BoundSince.Equal(KeyCreated) {
			t.Errorf("certificate %s: algorithm %d, created %s, bound %s", c.Fingerprint, c.Algorithm, c.Created, c.BoundSince)
		}
	}
	// The key d expired in 2024 and trove refuses it as a configured key, as the spec says.
	if _, err := openpgp.ParseCertificates(mustRing(t, a, "d"), time.Now(), openpgp.Limits{}); !errors.Is(err, openpgp.ErrInvalid) || !strings.Contains(err.Error(), "expired") {
		t.Errorf("key d: %v", err)
	}
	if _, ok := a.Key("zz"); ok {
		t.Error("an unknown key was found")
	}
}

func mustRing(t *testing.T, s *Server, names ...string) []byte {
	t.Helper()
	r, err := s.Keyring(names...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A release is signed so that trove's own verifier, an independent implementation of the same specification, accepts it.
func TestR_DEB_035_ReleasesVerifyWithTrovesVerifier(t *testing.T) {
	f := newFixture(t, Config{})
	certs := f.certs("a")
	now := time.Now()
	for _, dir := range []string{"/debian/dists/stable/", "/debian/dists/testing/", "/debian/dists/ratel/", "/debian/dists/bulk/", "/flat/binary/", "/flatroot/", "/flatroot/./"} {
		release := f.must200(dir + "Release")
		in := f.must200(dir + "InRelease")
		gpg := f.must200(dir + "Release.gpg")
		text, signer, err := openpgp.VerifyClear(in, certs, now, openpgp.Limits{})
		if err != nil || signer != certs[0].Fingerprint {
			t.Fatalf("%s: InRelease: %q, %v", dir, signer, err)
		}
		// gpg frames the text without its last line ending; the signed text is the Release up to there.
		if string(text) != strings.TrimSuffix(string(release), "\n") {
			t.Errorf("%s: the InRelease text is not the Release", dir)
		}
		if signer, err := openpgp.VerifyDetached(release, gpg, certs, now, openpgp.Limits{}); err != nil || signer != certs[0].Fingerprint {
			t.Errorf("%s: Release.gpg: %q, %v", dir, signer, err)
		}
	}
	if f.stub.Counts().OfKind(ClassRelease, "GET") != 21 {
		t.Errorf("counted %d release requests, want 21", f.stub.Counts().OfKind(ClassRelease, "GET"))
	}
}

func TestR_DEB_035_SameBytesOnEveryRequest(t *testing.T) {
	f := newFixture(t, Config{})
	a, b := f.must200("/debian/dists/stable/InRelease"), f.must200("/debian/dists/stable/InRelease")
	if !bytes.Equal(a, b) {
		t.Error("two requests got different InRelease bytes")
	}
	if !bytes.Equal(f.must200("/debian/dists/stable/Release.gpg"), f.must200("/debian/dists/stable/Release.gpg")) {
		t.Error("two requests got different Release.gpg bytes")
	}
}

func TestR_DEB_035_ReleaseListsEveryFileWithTheRightDigests(t *testing.T) {
	f := newFixture(t, Config{})
	for _, dir := range []string{"/debian/dists/stable/", "/debian/dists/testing/", "/flat/binary/", "/flatroot/"} {
		fields, sums := parseRelease(t, f.must200(dir+"Release"))
		if (fields["Suite"] == "" || fields["Codename"] == "" || fields["Date"] == "") || fields["Acquire-By-Hash"] != "yes" {
			t.Errorf("%s: fields %v", dir, fields)
		}
		if _, err := time.Parse(releaseTimeFormat, fields["Date"]); err != nil {
			t.Errorf("%s: Date %q: %v", dir, fields["Date"], err)
		}
		if len(sums["MD5Sum"]) == 0 || len(sums["MD5Sum"]) != len(sums["SHA256"]) || len(sums["SHA1"]) != len(sums["SHA512"]) {
			t.Fatalf("%s: lists %v", dir, mapLens(sums))
		}
		for i, e := range sums["SHA256"] {
			body := f.must200(dir + e.path)
			if len(body) != e.size || sha(body) != e.hash {
				t.Errorf("%s%s: %d bytes %s, the Release says %d %s", dir, e.path, len(body), sha(body), e.size, e.hash)
			}
			if m := md5.Sum(body); hex.EncodeToString(m[:]) != sums["MD5Sum"][i].hash { //nolint:gosec // see the import
				t.Errorf("%s%s: MD5Sum", dir, e.path)
			}
			if s := sha1.Sum(body); hex.EncodeToString(s[:]) != sums["SHA1"][i].hash { //nolint:gosec // see the import
				t.Errorf("%s%s: SHA1", dir, e.path)
			}
			if s := sha512.Sum512(body); hex.EncodeToString(s[:]) != sums["SHA512"][i].hash {
				t.Errorf("%s%s: SHA512", dir, e.path)
			}
			// Every listed file is also reachable by each of its hashes.
			dirOf := dir
			if j := strings.LastIndexByte(e.path, '/'); j >= 0 {
				dirOf += e.path[:j+1]
			}
			for algo, list := range map[string][]releaseFile{"MD5Sum": sums["MD5Sum"], "SHA1": sums["SHA1"], "SHA256": sums["SHA256"], "SHA512": sums["SHA512"]} {
				if got := f.must200(dirOf + "by-hash/" + algo + "/" + list[i].hash); !bytes.Equal(got, body) {
					t.Errorf("%s: by-hash/%s of %s differs", dir, algo, e.path)
				}
			}
		}
	}
}

func mapLens(m map[string][]releaseFile) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[k] = len(v)
	}
	return out
}

func TestR_DEB_035_IndexesNamePackagesThatInstall(t *testing.T) {
	f := newFixture(t, Config{})
	plain := f.must200("/debian/dists/stable/main/binary-amd64/Packages")
	if gz := f.must200("/debian/dists/stable/main/binary-amd64/Packages.gz"); !bytes.Equal(gunzipBytes(t, gz), plain) {
		t.Error("Packages.gz is not Packages compressed")
	}
	for _, want := range []string{"Package: trove-hello\n", "Version: 1.0-1\n", "Architecture: amd64\n", "Filename: pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb\n", "Package: libtrove1\n", "Source: libtrove\n", "SHA256: "} {
		if !strings.Contains(string(plain), want) {
			t.Errorf("Packages lacks %q", want)
		}
	}
	// Architecture all lives in binary-all, contrib in its own component, testing has newer versions.
	all := string(f.must200("/debian/dists/stable/main/binary-all/Packages"))
	if !strings.Contains(all, "Package: trove-hello-doc\n") || strings.Contains(all, "Package: trove-hello\n") {
		t.Errorf("binary-all:\n%s", all)
	}
	if c := string(f.must200("/debian/dists/stable/contrib/binary-amd64/Packages")); !strings.Contains(c, "Package: trove-extra\n") || strings.Contains(c, "trove-tools") {
		t.Errorf("contrib:\n%s", c)
	}
	if tt := string(f.must200("/debian/dists/testing/main/binary-amd64/Packages")); !strings.Contains(tt, "Version: 1.1-1\n") || strings.Contains(tt, "Version: 1.0-1\nInstalled") {
		t.Errorf("testing:\n%s", tt)
	}
	if !strings.Contains(string(f.must200("/debian/dists/testing/main/binary-all/Packages")), "Package: trove-newpkg\n") {
		t.Error("testing lacks trove-newpkg")
	}
	// Every Filename is served and matches the paragraph's Size and SHA256.
	for _, dir := range []string{"stable", "testing"} {
		for _, comp := range []string{"main", "contrib"} {
			for _, arch := range []string{"amd64", "arm64", "all"} {
				idx := string(f.must200(fmt.Sprintf("/debian/dists/%s/%s/binary-%s/Packages", dir, comp, arch)))
				for _, para := range strings.Split(strings.TrimSpace(idx), "\n\n") {
					if para == "" {
						continue
					}
					fields := paragraph(para)
					deb := f.must200("/debian/" + fields["Filename"])
					if fmt.Sprint(len(deb)) != fields["Size"] || sha(deb) != fields["SHA256"] {
						t.Errorf("%s: %s does not match its paragraph", dir, fields["Filename"])
					}
				}
			}
		}
	}
	src := string(f.must200("/debian/dists/stable/main/source/Sources"))
	fields := paragraph(strings.TrimSpace(src))
	if fields["Package"] != "trove-hello" || fields["Directory"] != "pool/main/t/trove-hello" || !strings.Contains(fields["Files"], ".dsc") {
		t.Errorf("Sources: %v", fields)
	}
	for _, line := range strings.Split(fields["Checksums-Sha256"], "\n") {
		if parts := strings.Fields(line); len(parts) == 3 {
			if body := f.must200("/debian/" + fields["Directory"] + "/" + parts[2]); sha(body) != parts[0] {
				t.Errorf("source file %s", parts[2])
			}
		}
	}
}

// paragraph parses one deb822 paragraph; a multi-line field keeps its continuation lines after a newline.
func paragraph(p string) map[string]string {
	out := map[string]string{}
	name := ""
	for _, line := range strings.Split(p, "\n") {
		if strings.HasPrefix(line, " ") {
			out[name] += "\n" + strings.TrimSpace(line)
			continue
		}
		n, v, _ := strings.Cut(line, ":")
		name = n
		out[name] = strings.TrimSpace(v)
	}
	return out
}

func TestR_DEB_035_FlatRepositories(t *testing.T) {
	f := newFixture(t, Config{})
	dir := string(f.must200("/flat/binary/Packages"))
	if !strings.Contains(dir, "Filename: binary/trove-flat_1.0-1_all.deb\n") {
		t.Errorf("flat directory:\n%s", dir)
	}
	if deb := f.must200("/flat/binary/trove-flat_1.0-1_all.deb"); len(deb) == 0 {
		t.Error("flat package")
	}
	root := string(f.must200("/flatroot/Packages"))
	if !strings.Contains(root, "Filename: trove-flat_1.0-1_all.deb\n") {
		t.Errorf("flat root:\n%s", root)
	}
	// APT asks for /./InRelease; the stub serves it and counts the path as written.
	f.stub.Reset()
	f.must200("/flatroot/./InRelease")
	f.must200("/flatroot/InRelease")
	c := f.stub.Counts()
	if c.Requests("GET", "/flatroot/./InRelease") != 1 || c.Requests("GET", "/flatroot/InRelease") != 1 {
		t.Errorf("counts %+v", c.Entries)
	}
	if fields, _ := parseRelease(t, f.must200("/flatroot/Release")); fields["Components"] != "" {
		t.Errorf("a flat Release names components: %v", fields)
	}
	if f.must200("/flatroot/trove-flat_1.0-1_all.deb") == nil {
		t.Error("flat root package")
	}
}

func TestR_DEB_035_CodenamesAreTheSameRelease(t *testing.T) {
	f := newFixture(t, Config{})
	if !bytes.Equal(f.must200("/debian/dists/ratel/Release"), f.must200("/debian/dists/stable/Release")) {
		t.Error("dists/ratel is not dists/stable")
	}
	fields, _ := parseRelease(t, f.must200("/debian/dists/stable/Release"))
	if fields["Suite"] != "stable" || fields["Codename"] != "ratel" || fields["Architectures"] != "all amd64 arm64" || fields["Components"] != "main contrib" {
		t.Errorf("fields %v", fields)
	}
	if r := f.get("/debian/dists/ghost/Release"); r.status != 404 {
		t.Errorf("an unknown suite = %d", r.status)
	}
}

func TestR_DEB_035_LargeIndex(t *testing.T) {
	f := newFixture(t, Config{BulkParagraphs: DefaultBulkParagraphs})
	start := time.Now()
	gz := f.must200("/debian/dists/bulk/main/binary-amd64/Packages.gz")
	plain := gunzipBytes(t, gz)
	if n := strings.Count(string(plain), "\n\n"); n != DefaultBulkParagraphs {
		t.Errorf("%d paragraphs, want %d", n, DefaultBulkParagraphs)
	}
	if len(plain) < 20<<20 {
		t.Errorf("the index is %d bytes", len(plain))
	}
	t.Logf("70,000 paragraphs, %d bytes plain, %d gzip, built and fetched in %s", len(plain), len(gz), time.Since(start))
	// It is built on first use, not at start.
	g := newFixture(t, Config{BulkParagraphs: 10})
	if g.stub.archiveByID("bulk").built {
		t.Error("the large index was built at start")
	}
}

func TestR_DEB_035_ConditionalRequestsAndValidators(t *testing.T) {
	f := newFixture(t, Config{})
	path := "/debian/dists/stable/main/binary-amd64/Packages.gz"
	first := f.get(path)
	etag, lm := first.header.Get("ETag"), first.header.Get("Last-Modified")
	if etag == "" || lm == "" {
		t.Fatalf("validators %q %q", etag, lm)
	}
	if r := f.do("GET", path, map[string]string{"If-None-Match": etag}); r.status != 304 || len(r.body) != 0 {
		t.Errorf("If-None-Match = %d", r.status)
	}
	if r := f.do("GET", path, map[string]string{"If-Modified-Since": lm}); r.status != 304 {
		t.Errorf("If-Modified-Since = %d", r.status)
	}
	if r := f.do("GET", path, map[string]string{"If-None-Match": `"other"`}); r.status != 200 {
		t.Errorf("another etag = %d", r.status)
	}
	if r := f.do("HEAD", path, nil); r.status != 200 || r.header.Get("Content-Length") == "" || len(r.body) != 0 {
		t.Errorf("HEAD = %d", r.status)
	}
	if c := f.stub.Counts(); c.Conditional(ClassIndex) != 3 || c.NotModified() != 2 {
		t.Errorf("conditional %d, 304 %d", c.Conditional(ClassIndex), c.NotModified())
	}
	if err := f.stub.SetMode(Mode{NoValidators: true}); err != nil {
		t.Fatal(err)
	}
	r := f.do("GET", path, map[string]string{"If-None-Match": etag, "If-Modified-Since": lm})
	if r.status != 200 || r.header.Get("ETag") != "" || r.header.Get("Last-Modified") != "" {
		t.Errorf("without validators: %d %v", r.status, r.header)
	}
	if r := f.do("POST", path, nil); r.status != 405 {
		t.Errorf("POST = %d", r.status)
	}
}

func TestR_DEB_035_RangeRequests(t *testing.T) {
	f := newFixture(t, Config{})
	path := "/debian/pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb"
	full := f.must200(path)
	r := f.do("GET", path, map[string]string{"Range": "bytes=10-19"})
	if r.status != 206 || !bytes.Equal(r.body, full[10:20]) {
		t.Errorf("range = %d %q", r.status, r.body)
	}
}

func TestR_DEB_035_ByHashSwitch(t *testing.T) {
	f := newFixture(t, Config{})
	_, sums := parseRelease(t, f.must200("/debian/dists/stable/Release"))
	byHash := "/debian/dists/stable/main/binary-amd64/by-hash/SHA256/" + sumOf(sums, "main/binary-amd64/Packages.gz")
	f.must200(byHash)
	if err := f.stub.SetMode(Mode{NoByHash: true}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(byHash); r.status != 404 {
		t.Errorf("by-hash with the switch off = %d", r.status)
	}
	fields, _ := parseRelease(t, f.must200("/debian/dists/stable/Release"))
	if _, ok := fields["Acquire-By-Hash"]; ok {
		t.Error("Acquire-By-Hash is still announced")
	}
	if r := f.get("/debian/dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("0", 64)); r.status != 404 {
		t.Errorf("an unknown hash = %d", r.status)
	}
}

func sumOf(sums map[string][]releaseFile, path string) string {
	for _, e := range sums["SHA256"] {
		if e.path == path {
			return e.hash
		}
	}
	return ""
}

func TestR_DEB_035_NewGenerationKeepsHistoryAndMakesPdiffs(t *testing.T) {
	f := newFixture(t, Config{})
	dir := "/debian/dists/stable/"
	idxPath := "main/binary-amd64/Packages"
	oldRelease := f.must200(dir + "Release")
	oldFields, oldSums := parseRelease(t, oldRelease)
	oldPlain := f.must200(dir + idxPath)
	oldGz := f.must200(dir + idxPath + ".gz")
	oldDate, _ := time.Parse(releaseTimeFormat, oldFields["Date"])
	if r := f.get(dir + idxPath + ".diff/Index"); r.status != 404 {
		t.Fatalf("a pdiff before the second generation = %d", r.status)
	}

	date, err := f.stub.NewGeneration("stable")
	if err != nil {
		t.Fatal(err)
	}
	if !date.After(oldDate) {
		t.Errorf("the new Date %s is not after %s", date, oldDate)
	}
	newFields, newSums := parseRelease(t, f.must200(dir+"Release"))
	if newFields["Date"] == oldFields["Date"] {
		t.Error("the Date did not change")
	}
	newPlain := f.must200(dir + idxPath)
	if !bytes.HasPrefix(newPlain, oldPlain) || !strings.Contains(string(newPlain[len(oldPlain):]), "Package: trove-zz-g001\n") {
		t.Fatal("the new Packages is not the old plus the new package")
	}
	// Only the index that changed has a new digest; the others keep theirs.
	changed := 0
	for _, e := range newSums["SHA256"] {
		if sumOf(oldSums, e.path) != e.hash {
			changed++
			if !strings.HasPrefix(e.path, "main/binary-amd64/") {
				t.Errorf("%s changed", e.path)
			}
		}
	}
	if changed != 3 { // Packages, Packages.gz and the new Packages.diff/Index
		t.Errorf("%d listed files changed", changed)
	}
	// The history stays reachable by hash.
	if got := f.must200(dir + "main/binary-amd64/by-hash/SHA256/" + sha(oldGz)); !bytes.Equal(got, oldGz) {
		t.Error("the old Packages.gz is gone from by-hash")
	}
	if got := f.must200(dir + "main/binary-amd64/by-hash/SHA256/" + sha(oldPlain)); !bytes.Equal(got, oldPlain) {
		t.Error("the old Packages is gone from by-hash")
	}
	if got := f.must200("/debian/pool/main/t/trove-zz-g001/trove-zz-g001_1.0-1_amd64.deb"); len(got) == 0 {
		t.Error("the new package is not served")
	}

	// The pdiff: the Index names the current digest and one patch that turns the old content into the new.
	index := string(f.must200(dir + idxPath + ".diff/Index"))
	for _, want := range []string{"SHA256-Current: " + sha(newPlain) + " " + fmt.Sprint(len(newPlain)) + "\n", "SHA256-History:\n " + sha(oldPlain) + " " + fmt.Sprint(len(oldPlain)) + " ", "SHA256-Patches:\n", "SHA256-Download:\n"} {
		if !strings.Contains(index, want) {
			t.Errorf("Index lacks %q:\n%s", want, index)
		}
	}
	name := strings.Fields(strings.SplitN(strings.SplitN(index, "SHA256-Download:\n", 2)[1], "\n", 2)[0])[2]
	patchGz := f.must200(dir + idxPath + ".diff/" + name)
	patch := gunzipBytes(t, patchGz)
	if !strings.Contains(index, sha(patchGz)) || !strings.Contains(index, sha(patch)) {
		t.Error("the Index does not name the patch's digests")
	}
	if got := applyAppend(t, oldPlain, patch); !bytes.Equal(got, newPlain) {
		t.Error("the patch does not turn the old Packages into the new one")
	}
	if got := f.must200(dir + idxPath + ".diff/by-hash/SHA256/" + sha(patchGz)); !bytes.Equal(got, patchGz) {
		t.Error("the patch is not reachable by hash")
	}

	// A second generation chains: two patches in the history.
	if _, err := f.stub.NewGeneration("ratel"); err != nil { // the codename names the same archive
		t.Fatal(err)
	}
	index = string(f.must200(dir + idxPath + ".diff/Index"))
	if n := strings.Count(strings.SplitN(strings.SplitN(index, "SHA256-History:\n", 2)[1], "SHA256-Patches:", 2)[0], "\n"); n != 2 {
		t.Errorf("%d history entries:\n%s", n, index)
	}
	if n, _, _ := f.stub.Generation("stable"); n != 2 {
		t.Errorf("generation %d", n)
	}
	if _, err := f.stub.NewGeneration("nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("an unknown archive: %v", err)
	}
	// The release stays verifiable after a generation.
	if _, _, err := openpgp.VerifyClear(f.must200(dir+"InRelease"), f.certs("a"), time.Now(), openpgp.Limits{}); err != nil {
		t.Errorf("the new InRelease does not verify: %v", err)
	}
}

// applyAppend applies the ed script the stub writes, "<n>a", the new lines and ".", the only form it makes.
func applyAppend(t *testing.T, old, script []byte) []byte {
	t.Helper()
	head, rest, _ := bytes.Cut(script, []byte("\n"))
	var n int
	if _, err := fmt.Sscanf(string(head), "%da", &n); err != nil {
		t.Fatalf("script head %q", head)
	}
	text := bytes.TrimSuffix(rest, []byte(".\n"))
	lines := bytes.SplitAfter(old, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if n != len(lines) {
		t.Fatalf("the patch appends after line %d of %d", n, len(lines))
	}
	return append(bytes.Clone(old), text...)
}

func TestR_DEB_035_FlatNewGeneration(t *testing.T) {
	f := newFixture(t, Config{})
	for _, id := range []string{"flat", "flatroot"} {
		if _, err := f.stub.NewGeneration(id); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(f.must200("/flat/binary/Packages")), "Filename: binary/trove-zz-g001_1.0-1_all.deb\n") {
		t.Error("flat generation")
	}
	f.must200("/flat/binary/trove-zz-g001_1.0-1_all.deb")
	f.must200("/flatroot/trove-zz-g001_1.0-1_all.deb")
	f.must200("/flatroot/Packages.diff/Index")
}

func TestR_DEB_035_SigningSwitches(t *testing.T) {
	f := newFixture(t, Config{})
	now := time.Now()
	a, b := f.certs("a")[0], f.certs("b")[0]
	// The stub's key d is expired, so trove cannot even configure it; the verifier is handed its certificate directly.
	dRing := mustRing(t, f.stub, "d")
	dCerts := certsAt(t, dRing, time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC))
	dir := "/debian/dists/stable/"

	verify := func(mode Mode, certs ...openpgp.Certificate) (string, string) {
		t.Helper()
		if err := f.stub.SetMode(mode); err != nil {
			t.Fatal(err)
		}
		release := f.must200(dir + "Release")
		_, signer, errIn := openpgp.VerifyClear(f.must200(dir+"InRelease"), certs, now, openpgp.Limits{})
		signerGPG, errGPG := openpgp.VerifyDetached(release, f.must200(dir+"Release.gpg"), certs, now, openpgp.Limits{})
		if signer != signerGPG || openpgp.Reason(errIn) != openpgp.Reason(errGPG) {
			t.Errorf("InRelease (%q, %v) and Release.gpg (%q, %v) disagree", signer, errIn, signerGPG, errGPG)
		}
		return signer, openpgp.Reason(errIn)
	}

	tests := []struct {
		name   string
		mode   Mode
		certs  []openpgp.Certificate
		signer string
		reason string
	}{
		{"default: signed by a", Mode{}, []openpgp.Certificate{a}, a.Fingerprint, ""},
		{"keys [b]", Mode{}, []openpgp.Certificate{b}, "", openpgp.ReasonUnknownKey},
		{"a and b co-sign, keys [a]", Mode{SignWith: []string{"a", "b"}}, []openpgp.Certificate{a}, a.Fingerprint, ""},
		{"a and b co-sign, keys [b]", Mode{SignWith: []string{"a", "b"}}, []openpgp.Certificate{b}, b.Fingerprint, ""},
		{"a and unknown c, keys [a]", Mode{SignWith: []string{"c", "a"}}, []openpgp.Certificate{a}, a.Fingerprint, ""},
		{"unknown c alone, keys [a]", Mode{SignWith: []string{"c"}}, []openpgp.Certificate{a}, "", openpgp.ReasonUnknownKey},
		{"SHA-1", Mode{SignHash: "sha1"}, []openpgp.Certificate{a}, "", openpgp.ReasonWeakHash},
		{"SHA-256", Mode{SignHash: "sha256"}, []openpgp.Certificate{a}, a.Fingerprint, ""},
		{"broken signature", Mode{BrokenSignature: true}, []openpgp.Certificate{a}, "", openpgp.ReasonBadSignature},
		{"signature 5 minutes ahead", Mode{SignatureSkewSeconds: 300 + 60}, []openpgp.Certificate{a}, a.Fingerprint, ""},
		{"signature 20 minutes ahead", Mode{SignatureSkewSeconds: 1200 + 60}, []openpgp.Certificate{a}, "", openpgp.ReasonFuture},
		{"expired key d", Mode{SignWith: []string{"d"}}, dCerts, "", openpgp.ReasonKeyInvalid},
		{"no Hash header", Mode{NoHashHeader: true}, []openpgp.Certificate{a}, a.Fingerprint, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signer, reason := verify(tc.mode, tc.certs...)
			if signer != tc.signer || reason != tc.reason {
				t.Fatalf("signer %q, reason %q; want %q, %q", signer, reason, tc.signer, tc.reason)
			}
		})
	}

	t.Run("the Hash header is gone", func(t *testing.T) {
		_ = f.stub.SetMode(Mode{NoHashHeader: true})
		if in := f.must200(dir + "InRelease"); bytes.Contains(in, []byte("MESSAGE-----\nHash:")) {
			t.Error("InRelease still has a Hash header")
		}
		_ = f.stub.SetMode(Mode{})
		if in := f.must200(dir + "InRelease"); !bytes.Contains(in, []byte("MESSAGE-----\nHash: SHA512\n")) {
			t.Error("InRelease has no Hash header")
		}
	})
	t.Run("InRelease absent", func(t *testing.T) {
		_ = f.stub.SetMode(Mode{NoInRelease: true})
		if r := f.get(dir + "InRelease"); r.status != 404 {
			t.Errorf("InRelease = %d", r.status)
		}
		f.must200(dir + "Release")
		f.must200(dir + "Release.gpg")
	})
}

// certsAt reads certificates at a given time, for keys that have expired since.
func certsAt(t *testing.T, ring []byte, at time.Time) []openpgp.Certificate {
	t.Helper()
	certs, err := openpgp.ParseCertificates(ring, at, openpgp.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return certs
}

func TestR_DEB_035_DateAndValidUntilSwitches(t *testing.T) {
	f := newFixture(t, Config{})
	dir := "/debian/dists/stable/"
	date := func() time.Time {
		fields, _ := parseRelease(t, f.must200(dir+"Release"))
		d, err := time.Parse(releaseTimeFormat, fields["Date"])
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	base := date()
	if age := time.Since(base); age < 30*time.Second || age > 3*time.Minute {
		t.Errorf("the Date is %s old, want about a minute", age)
	}
	_ = f.stub.SetMode(Mode{Date: DateOlder})
	if got := date(); !got.Equal(base.Add(-time.Hour)) {
		t.Errorf("older Date %s, base %s", got, base)
	}
	_ = f.stub.SetMode(Mode{Date: DateAhead})
	if got := time.Until(date()); got < 19*time.Minute || got > 21*time.Minute {
		t.Errorf("ahead Date is %s ahead", got)
	}
	// The releases stay verifiable: the Date is the only fault.
	if _, _, err := openpgp.VerifyClear(f.must200(dir+"InRelease"), f.certs("a"), time.Now(), openpgp.Limits{}); err != nil {
		t.Errorf("ahead: %v", err)
	}
	_ = f.stub.SetMode(Mode{ValidUntil: ValidUntilPassed})
	fields, _ := parseRelease(t, f.must200(dir+"Release"))
	if vu, _ := time.Parse(releaseTimeFormat, fields["Valid-Until"]); !vu.Before(time.Now()) {
		t.Errorf("Valid-Until %q has not passed", fields["Valid-Until"])
	}
	_ = f.stub.SetMode(Mode{ValidUntil: ValidUntilSixDays})
	fields, _ = parseRelease(t, f.must200(dir+"Release"))
	if vu, _ := time.Parse(releaseTimeFormat, fields["Valid-Until"]); time.Until(vu) < 5*24*time.Hour || time.Until(vu) > 6*24*time.Hour {
		t.Errorf("Valid-Until %q", fields["Valid-Until"])
	}
	_ = f.stub.SetMode(Mode{})
	if fields, _ = parseRelease(t, f.must200(dir+"Release")); fields["Valid-Until"] != "" {
		t.Error("Valid-Until without the switch")
	}
}

func TestR_DEB_035_ContentSwitches(t *testing.T) {
	f := newFixture(t, Config{})
	dir := "/debian/dists/stable/"
	deb := "/debian/pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb"

	_ = f.stub.SetMode(Mode{NotFound: []string{"stable"}})
	for _, p := range []string{dir + "InRelease", dir + "Release", dir + "main/binary-amd64/Packages", "/debian/dists/ratel/Release"} {
		if r := f.get(p); r.status != 404 {
			t.Errorf("%s = %d", p, r.status)
		}
	}
	f.must200("/debian/dists/testing/Release")
	f.must200(deb)

	_ = f.stub.SetMode(Mode{})
	right := f.must200(deb)
	_ = f.stub.SetMode(Mode{WrongPackage: []string{"trove-hello_1.0-1_amd64.deb"}})
	wrong := f.must200(deb)
	if len(wrong) != len(right) || bytes.Equal(wrong, right) {
		t.Error("the package does not differ from its index entry")
	}
	if !bytes.Equal(f.must200("/debian/pool/main/t/trove-hello/trove-hello_1.0-1_arm64.deb"), f.stub.corpus.static["/debian/pool/main/t/trove-hello/trove-hello_1.0-1_arm64.deb"]) {
		t.Error("another package changed")
	}

	_ = f.stub.SetMode(Mode{})
	_, sums := parseRelease(t, f.must200(dir+"Release"))
	idx := "main/binary-amd64/Packages"
	_ = f.stub.SetMode(Mode{IndexMismatch: true})
	for _, p := range []string{idx, idx + ".gz"} {
		body := f.must200(dir + p)
		if sha(body) == sumOf(sums, p) {
			t.Errorf("%s still matches the Release", p)
		}
	}
	if got := f.must200(dir + "main/binary-amd64/by-hash/SHA256/" + sumOf(sums, idx)); sha(got) != sumOf(sums, idx) {
		t.Error("by-hash changed with the mismatch switch")
	}
}

func TestR_DEB_035_DownDelayFailAndThrottle(t *testing.T) {
	f := newFixture(t, Config{})
	path := "/debian/dists/stable/Release"
	_ = f.stub.SetMode(Mode{Down: DownUnavailable})
	if r := f.get(path); r.status != 503 {
		t.Errorf("down = %d", r.status)
	}
	_ = f.stub.SetMode(Mode{Down: DownReset})
	if res, err := f.client.Get(f.base + path); err == nil {
		_ = res.Body.Close()
		t.Error("a reset connection answered")
	}
	_ = f.stub.SetMode(Mode{FailStatus: map[string]int{ClassPool: 502}})
	if r := f.get("/debian/pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb"); r.status != 502 {
		t.Errorf("fail_status = %d", r.status)
	}
	f.must200(path)
	_ = f.stub.SetMode(Mode{DelayMS: map[string]int{ClassRelease: 300}})
	start := time.Now()
	f.must200(path)
	if time.Since(start) < 300*time.Millisecond {
		t.Error("the delay did not apply")
	}
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{"nope": 1}}); err == nil {
		t.Error("an unknown class was accepted")
	}
	if err := f.stub.SetMode(Mode{SignWith: []string{"x"}}); err == nil {
		t.Error("an unknown key was accepted")
	}

	_ = f.stub.SetMode(Mode{})
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleTooManyRequests, RetryAfterSeconds: 7, Nth: 2, Count: 1}); err != nil {
		t.Fatal(err)
	}
	f.must200(path)
	r := f.get(path)
	if r.status != 429 || r.header.Get("Retry-After") != "7" {
		t.Errorf("throttled = %d %v", r.status, r.header)
	}
	f.must200(path)
	if st := f.stub.ThrottleState(); st.Seen != 3 || st.Throttled != 1 {
		t.Errorf("state %+v", st)
	}
	_ = f.stub.SetThrottle(Throttle{Mode: ThrottleQuota403, RetryAfterSeconds: 30, Class: ClassPool})
	if r := f.get("/debian/pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb"); r.status != 403 || r.header.Get("X-RateLimit-Reset") == "" {
		t.Errorf("quota = %d %v", r.status, r.header)
	}
	f.must200(path) // another class, not throttled
	if err := f.stub.SetThrottle(Throttle{Mode: "bogus"}); err == nil {
		t.Error("a bogus throttle was accepted")
	}
}

func TestR_DEB_035_CountsAndInflight(t *testing.T) {
	f := newFixture(t, Config{})
	f.must200("/debian/dists/stable/InRelease")
	f.must200("/debian/dists/stable/main/binary-amd64/Packages.gz")
	f.must200("/debian/pool/main/t/trove-hello/trove-hello_1.0-1_amd64.deb")
	f.get("/debian/dists/stable/nope")
	_, sums := parseRelease(t, f.must200("/debian/dists/stable/Release"))
	f.must200("/debian/dists/stable/main/binary-amd64/by-hash/SHA256/" + sumOf(sums, "main/binary-amd64/Packages"))
	c := f.stub.Counts()
	if c.OfKind(ClassRelease, "GET") != 2 || c.OfKind(ClassIndex, "GET") != 1 || c.OfKind(ClassPool, "") != 1 || c.OfKind(ClassByHash, "") != 1 || c.Total() != 6 {
		t.Errorf("counts %+v", c.Entries)
	}
	if c.Delivered(ClassPool) != 1 || c.Prefixed("/debian/dists/", "GET") != 5 {
		t.Errorf("delivered %d, prefixed %d", c.Delivered(ClassPool), c.Prefixed("/debian/dists/", "GET"))
	}
	for _, e := range c.Entries {
		if e.Path == "/debian/dists/stable/nope" && e.Statuses["404"] != 1 {
			t.Errorf("statuses %v", e.Statuses)
		}
	}
	if in := f.stub.Inflight(); in.Open != 0 || in.Max < 1 || in.Requests != 6 {
		t.Errorf("inflight %+v", in)
	}
	f.stub.Reset()
	if f.stub.Counts().Total() != 0 || f.stub.Inflight().Requests != 0 {
		t.Error("Reset did not zero the counters")
	}
}

func TestR_DEB_035_ControlAPI(t *testing.T) {
	f := newFixture(t, Config{ControlToken: testToken})
	if r := f.get("/-/stub/counts"); r.status != 401 {
		t.Errorf("no token = %d", r.status)
	}
	f.must200("/debian/dists/stable/Release")
	if r := f.control("GET", "/-/stub/counts", ""); r.status != 200 || !strings.Contains(string(r.body), `"/debian/dists/stable/Release"`) {
		t.Errorf("counts = %d %s", r.status, r.body)
	}
	if f.stub.Counts().Total() != 1 {
		t.Error("control requests were counted")
	}
	if r := f.control("POST", "/-/stub/mode", `{"sign_with":["a","b"],"no_by_hash":true}`); r.status != 200 || !strings.Contains(string(r.body), `"no_by_hash":true`) {
		t.Errorf("mode = %d %s", r.status, r.body)
	}
	if r := f.control("POST", "/-/stub/mode", `{"bogus":1}`); r.status != 400 {
		t.Errorf("unknown field = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/mode", `{"sign_hash":"md5"}`); r.status != 400 {
		t.Errorf("bad hash = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/throttle", `{"mode":"429","retry_after_seconds":3}`); r.status != 200 {
		t.Errorf("throttle = %d %s", r.status, r.body)
	}
	if r := f.control("POST", "/-/stub/generation", `{"archive":"testing"}`); r.status != 200 || !strings.Contains(string(r.body), `"generation":1`) {
		t.Errorf("generation = %d %s", r.status, r.body)
	}
	if r := f.control("POST", "/-/stub/generation", `{"archive":"nope"}`); r.status != 404 {
		t.Errorf("unknown archive = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/archives", ""); r.status != 200 || !strings.Contains(string(r.body), `"id":"flatroot"`) {
		t.Errorf("archives = %d %s", r.status, r.body)
	}
	if r := f.control("GET", "/-/stub/release?archive=stable", ""); r.status != 200 || !strings.Contains(string(r.body), "Suite: stable") {
		t.Errorf("release = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/keys", ""); r.status != 200 || !strings.Contains(string(r.body), `"fingerprint"`) {
		t.Errorf("keys = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/keyring?names=a,b", ""); r.status != 200 || !bytes.HasPrefix(r.body, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Errorf("keyring = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/keyring?names=zz", ""); r.status != 400 {
		t.Errorf("keyring of an unknown key = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/files", ""); r.status != 200 || !strings.Contains(string(r.body), "trove-hello_1.0-1_amd64.deb") {
		t.Errorf("files = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/inflight", ""); r.status != 200 {
		t.Errorf("inflight = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/reset?all=true", ""); r.status != 204 {
		t.Errorf("reset = %d", r.status)
	}
	if m := f.stub.Mode(); len(m.SignWith) != 0 || m.NoByHash {
		t.Errorf("mode after reset all: %+v", m)
	}
	if n, _, _ := f.stub.Generation("testing"); n != 0 {
		t.Errorf("generation after reset all: %d", n)
	}
	if st := f.stub.ThrottleState(); st.Throttle != (Throttle{}) {
		t.Errorf("throttle after reset all: %+v", st)
	}
	// Without a token configured the API does not exist.
	g := newFixture(t, Config{})
	if r := g.get("/-/stub/counts"); r.status != 404 {
		t.Errorf("API without token = %d", r.status)
	}
}

func TestR_DEB_035_ArchiveBytesAreDeterministic(t *testing.T) {
	a, b := newFixture(t, Config{}), newFixture(t, Config{})
	for _, p := range []string{"/debian/dists/stable/main/binary-amd64/Packages.gz", "/debian/dists/testing/main/source/Sources.gz", "/flat/binary/Packages", "/debian/pool/main/t/trove-hello/trove-hello_1.0-1_all.deb"} {
		if p == "/debian/pool/main/t/trove-hello/trove-hello_1.0-1_all.deb" {
			p = "/debian/pool/main/t/trove-hello/trove-hello-doc_1.0-1_all.deb"
		}
		if !bytes.Equal(a.must200(p), b.must200(p)) {
			t.Errorf("%s differs between two stubs", p)
		}
	}
}

func TestR_DEB_035_GeneratedPackagesMatchDebgen(t *testing.T) {
	f := newFixture(t, Config{})
	want, err := debgen.Build(debgen.Spec{Package: "trove-config", Version: "1.0-1", Architecture: "all", Provides: "trove-config-default", Description: "default configuration"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.must200("/debian/pool/main/t/trove-config/trove-config_1.0-1_all.deb"); !bytes.Equal(got, want.Bytes) {
		t.Error("the served package is not what debgen builds for its spec")
	}
}

func TestR_DEB_035_NoTLSNoService(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("127.0.0.1:0", nil); err == nil {
		t.Error("the stub started without a certificate")
	}
	var _ http.Handler = s
}
