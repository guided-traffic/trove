package imagescan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

func TestParseReportMergesThePackagesOfOneAdvisoryAndKeepsTheFacts(t *testing.T) {
	rep := `{"SchemaVersion":2,"Metadata":{"OS":{"Family":"alpine","Name":"3.12.0","EOSL":true}},"Results":[
	 {"Class":"os-pkgs","Type":"alpine","Vulnerabilities":[
	  {"VulnerabilityID":"CVE-2021-36159","PkgName":"apk-tools","InstalledVersion":"2.10.5-r1","FixedVersion":"2.10.7-r0","Severity":"CRITICAL","SeveritySource":"nvd","Title":"libfetch read","CweIDs":["CWE-125","bogus"],"CVSS":{"nvd":{"V3Vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:H","V3Score":9.1,"V2Score":6.4}},"PrimaryURL":"https://avd.aquasec.com/nvd/cve-2021-36159","References":["https://example.org/a","ftp://nope"]},
	  {"VulnerabilityID":"CVE-2021-36159","PkgName":"libfetch","InstalledVersion":"2.10.5-r1","Severity":"CRITICAL"}]},
	 {"Class":"lang-pkgs","Type":"npm","Vulnerabilities":[{"VulnerabilityID":"GHSA-xxxx-yyyy-zzzz","PkgName":"lodash","PkgPath":"app/node_modules/lodash/package.json","InstalledVersion":"4.17.20","FixedVersion":"4.17.21","Severity":"HIGH"},
	 {"VulnerabilityID":"../evil","PkgName":"x","Severity":"HIGH"}]}]}`
	det, err := ParseReport(strings.NewReader(rep), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if det.OSFamily != "alpine" || !det.EOSL || len(det.Advisories) != 2 {
		t.Fatalf("%+v", det)
	}
	a := det.Advisories[0]
	if a.ID != "CVE-2021-36159" || a.Severity != advisory.SeverityCritical || a.CVSSScore != 9.1 || a.CVSSVersion != "3.1" {
		t.Fatalf("%+v", a)
	}
	if len(a.Components) != 2 || a.Components[0].Name != "apk-tools" || a.Components[0].FixedVersion != "2.10.7-r0" || a.Components[1].Name != "libfetch" {
		t.Fatalf("%+v", a.Components)
	}
	if len(a.CWEs) != 1 || a.References[0] != "https://avd.aquasec.com/nvd/cve-2021-36159" || len(a.References) != 2 {
		t.Fatalf("%+v %+v", a.CWEs, a.References)
	}
	if a.FixedVersions()[0] != "2.10.7-r0" {
		t.Fatal(a.FixedVersions())
	}
	l := det.Advisories[1]
	if l.Components[0].Class != "lang-pkgs" || l.Components[0].Path == "" {
		t.Fatalf("%+v", l.Components)
	}
}

func TestParseReportRefusesAnotherSchemaAndWhatIsNotAReport(t *testing.T) {
	if _, err := ParseReport(strings.NewReader(`{"SchemaVersion":3}`), 1<<20); err == nil {
		t.Fatal("schema 3 accepted")
	}
	if _, err := ParseReport(strings.NewReader(`not json`), 1<<20); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := ParseReport(strings.NewReader(strings.Repeat(" ", 100)+`{"SchemaVersion":2}`), 10); err == nil {
		t.Fatal("over the bound accepted")
	}
}

func TestParseReportStripsSpoofingCharactersFromAdvisoryText(t *testing.T) {
	rep := `{"SchemaVersion":2,"Results":[{"Class":"os-pkgs","Type":"apk","Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"x","Severity":"LOW","Title":"bad\u202etitle","Description":"ok"}]}]}`
	det, err := ParseReport(strings.NewReader(rep), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if det.Advisories[0].Title != "badtitle" {
		t.Fatalf("%q", det.Advisories[0].Title)
	}
}

// memBlobs is an image in memory, the fixture for the layout.
type memBlobs struct {
	manifest []byte
	blobs    map[string][]byte
}

func (m memBlobs) ReadManifest(context.Context, ImageRef, int64) ([]byte, error) {
	return m.manifest, nil
}

func (m memBlobs) OpenBlob(_ context.Context, _ ImageRef, digest string) (io.ReadCloser, int64, bool, error) {
	b, ok := m.blobs[digest]
	if !ok {
		return nil, 0, false, nil
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), true, nil
}

func digestStr(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func tarOf(t *testing.T, entries map[string]string, zip bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var gz *gzip.Writer
	if zip {
		gz = gzip.NewWriter(&buf)
		w = gz
	}
	tw := tar.NewWriter(w)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func imageWith(layers ...[]byte) memBlobs {
	cfg := []byte(`{"architecture":"amd64"}`)
	m := memBlobs{blobs: map[string][]byte{digestStr(cfg): cfg}}
	var ls []string
	for _, l := range layers {
		m.blobs[digestStr(l)] = l
		ls = append(ls, fmt.Sprintf(`{"digest":%q,"size":%d}`, digestStr(l), len(l)))
	}
	m.manifest = []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q,"size":%d},"layers":[%s]}`, digestStr(cfg), len(cfg), strings.Join(ls, ",")))
	return m
}

func lims() ContentLimits {
	return ContentLimits{MaxLayers: 8, MaxImageBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, MaxEntries: 1000}
}

func wantScanError(t *testing.T, err error, code string) {
	t.Helper()
	var se *ScanError
	if !errors.As(err, &se) || se.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestR_VULN_036_ALayoutHoldsTheVerifiedBlobsOfAnImage(t *testing.T) {
	m := imageWith(tarOf(t, map[string]string{"etc/os-release": "alpine"}, true))
	dir := t.TempDir()
	if err := buildLayout(context.Background(), m, ImageRef{}, dir, lims(), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.json", "oci-layout"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(filepath.Join(dir, "blobs", "sha256"))
	if err != nil || len(ents) != 3 { // config, layer, manifest
		t.Fatalf("%v %d", err, len(ents))
	}
}

func TestR_VULN_036_TheBoundsOfAnUntrustedImageHold(t *testing.T) {
	ctx := context.Background()
	t.Run("path traversal", func(t *testing.T) {
		err := buildLayout(ctx, imageWith(tarOf(t, map[string]string{"../../etc/shadow": "x"}, false)), ImageRef{}, t.TempDir(), lims(), nil)
		wantScanError(t, err, CodePathTraversal)
	})
	t.Run("decompression bomb", func(t *testing.T) {
		big := strings.Repeat("0", 3<<20)
		l := lims()
		err := buildLayout(ctx, imageWith(tarOf(t, map[string]string{"a": big}, true)), ImageRef{}, t.TempDir(), l, nil)
		wantScanError(t, err, CodeDecompressedSize)
	})
	t.Run("too many layers", func(t *testing.T) {
		l := lims()
		l.MaxLayers = 1
		err := buildLayout(ctx, imageWith(tarOf(t, map[string]string{"a": "1"}, false), tarOf(t, map[string]string{"b": "2"}, false)), ImageRef{}, t.TempDir(), l, nil)
		wantScanError(t, err, CodeTooManyLayers)
	})
	t.Run("compressed size", func(t *testing.T) {
		l := lims()
		l.MaxImageBytes = 10
		err := buildLayout(ctx, imageWith(tarOf(t, map[string]string{"a": "1"}, false)), ImageRef{}, t.TempDir(), l, nil)
		wantScanError(t, err, CodeImageTooLarge)
	})
	t.Run("entries", func(t *testing.T) {
		l := lims()
		l.MaxEntries = 1
		err := buildLayout(ctx, imageWith(tarOf(t, map[string]string{"a": "1", "b": "2"}, false)), ImageRef{}, t.TempDir(), l, nil)
		wantScanError(t, err, CodeTooManyEntries)
	})
	t.Run("a blob that is not what the manifest names", func(t *testing.T) {
		m := imageWith(tarOf(t, map[string]string{"a": "1"}, false))
		for d := range m.blobs {
			if strings.HasPrefix(string(m.manifest), "{") && d != "" && len(m.blobs[d]) > 100 {
				m.blobs[d][0] ^= 0xff
			}
		}
		err := buildLayout(ctx, m, ImageRef{}, t.TempDir(), lims(), nil)
		wantScanError(t, err, CodeDigestMismatch)
	})
	t.Run("a layer that has not arrived is retried, not terminal", func(t *testing.T) {
		m := imageWith(tarOf(t, map[string]string{"a": "1"}, false))
		for d := range m.blobs {
			if len(m.blobs[d]) > 100 {
				delete(m.blobs, d)
			}
		}
		err := buildLayout(ctx, m, ImageRef{}, t.TempDir(), lims(), nil)
		var se *ScanError
		if !errors.As(err, &se) || se.Code != CodeLayerMissing || se.Terminal {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a symbolic link may point outside, a hard link may not", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "usr/lib/link", Typeflag: tar.TypeSymlink, Linkname: "../../lib/x"})
		_ = tw.Close()
		if err := buildLayout(ctx, imageWith(buf.Bytes()), ImageRef{}, t.TempDir(), lims(), nil); err != nil {
			t.Fatalf("symlink refused: %v", err)
		}
		var h bytes.Buffer
		tw = tar.NewWriter(&h)
		_ = tw.WriteHeader(&tar.Header{Name: "bad", Typeflag: tar.TypeLink, Linkname: "../../etc/shadow"})
		_ = tw.Close()
		wantScanError(t, buildLayout(ctx, imageWith(h.Bytes()), ImageRef{}, t.TempDir(), lims(), nil), CodePathTraversal)
	})
}

func TestR_VULN_035_TheDatabaseArchiveDecidesNoPath(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name, body string, flag byte) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: flag})
		_, _ = tw.Write([]byte(body))
	}
	write("../escape", "x", tar.TypeReg)
	write("sub/trivy.db", "x", tar.TypeReg)
	write(dbMetaName, `{"Version":2,"UpdatedAt":"2026-11-08T12:51:20Z","NextUpdate":"2026-11-09T12:51:20Z"}`, tar.TypeReg)
	write(dbFileName, "database", tar.TypeReg)
	_ = tw.Close()
	_ = gz.Close()
	meta, err := ReadDBMetadata(bytes.NewReader(buf.Bytes()))
	if err != nil || meta.Version != 2 || meta.UpdatedAt.IsZero() {
		t.Fatalf("%v %+v", err, meta)
	}
	var names []string
	if err := walkArchive(bytes.NewReader(buf.Bytes()), func(name string, _ int64, _ io.Reader) (bool, error) {
		names = append(names, name)
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != dbMetaName || names[1] != dbFileName {
		t.Fatalf("%v", names)
	}
}

func TestR_VULN_035_AnArchiveOfAnotherSchemaIsRefused(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: dbMetaName, Mode: 0o644, Size: 25, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(`{"Version":99,"UpdatedAt":`[:25]))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := ReadDBMetadata(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("a truncated metadata.json was accepted")
	}
}

// TestRealTrivyScansTheFixture is the end-to-end proof of the command line of the pinned binary: analysis to an SBOM, then detection against
// a real database from that SBOM. It needs three things the unit tier does not ship, so it runs when they are named:
//
//	TROVE_TEST_TRIVY_BIN     the binary of .bin/trivy-<version>
//	TROVE_TEST_TRIVY_CACHE   a cache directory holding db/trivy.db
//	TROVE_TEST_IMAGE_LAYOUT  an OCI layout directory of a vulnerable image
func TestRealTrivyScansTheFixture(t *testing.T) {
	bin, cache, layout := os.Getenv("TROVE_TEST_TRIVY_BIN"), os.Getenv("TROVE_TEST_TRIVY_CACHE"), os.Getenv("TROVE_TEST_IMAGE_LAYOUT")
	if bin == "" || cache == "" || layout == "" {
		t.Skip("TROVE_TEST_TRIVY_BIN, TROVE_TEST_TRIVY_CACHE and TROVE_TEST_IMAGE_LAYOUT name the real analyser")
	}
	ctx := context.Background()
	tr, err := NewTrivy(ctx, bin)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Version() == "" {
		t.Fatal("no version")
	}
	dir := t.TempDir()
	sbom, out := filepath.Join(dir, "sbom.json"), filepath.Join(dir, "report.json")
	lim := Limits{Timeout: 120_000_000_000, OutputBytes: 1 << 28, StderrBytes: 4096}
	if err := tr.Analyse(ctx, layout, sbom, dir, lim); err != nil {
		t.Fatal(err)
	}
	if err := tr.Detect(ctx, sbom, out, dir, DatabaseAccess{Args: []string{"--cache-dir", cache}, Digest: "real", Release: func() {}}, lim); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	det, err := ParseReport(f, 1<<28)
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Advisories) == 0 || det.OSFamily == "" {
		t.Fatalf("a vulnerable image reported nothing: %+v", det)
	}
	if want := os.Getenv("TROVE_TEST_IMAGE_ADVISORIES"); want != "" {
		n, _ := strconv.Atoi(want)
		if len(det.Advisories) != n {
			t.Fatalf("%d advisories, want %d", len(det.Advisories), n)
		}
	}
	t.Logf("trivy %s: %s %s, %d advisories, %d packages", tr.Version(), det.OSFamily, det.OSName, len(det.Advisories), packagesOf(sbom))
}
