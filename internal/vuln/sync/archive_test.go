package sync

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// zipEntry is one entry of an archive built in memory.
type zipEntry struct {
	name    string
	body    []byte
	deflate bool
}

func record(id string, pad int) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"modified":"2026-10-01T00:00:00Z","summary":%q}`, id, strings.Repeat("x", pad)))
}

// hexPad is text that deflates to about half its size.
func hexPad(n int) string {
	var b strings.Builder
	x := uint64(88172645463325252)
	for b.Len() < n {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		fmt.Fprintf(&b, "%016x", x)
	}
	return b.String()[:n]
}

func buildArchive(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		method := zip.Store
		if e.deflate {
			method = zip.Deflate
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// scanner is a service that logs into a buffer and reads archives with the limits of the sync.
type scanner struct {
	s   *Service
	buf *bytes.Buffer
}

func newScanner(maxRecord config.ByteSize) *scanner {
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.MaxRecordSize = maxRecord
	buf := &bytes.Buffer{}
	return &scanner{s: &Service{o: Options{Config: cfg}, log: slog.New(slog.NewJSONHandler(buf, nil))}, buf: buf}
}

func (sc *scanner) scan(t *testing.T, data []byte) (*scanResult, error) {
	t.Helper()
	z, err := untrusted.NewZipDeclared(context.Background(), bytes.NewReader(data), int64(len(data)), sc.s.zipLimits())
	if err != nil {
		t.Fatal(err)
	}
	var ad match.Adapter = testAdapter{}
	return sc.s.scanArchive(context.Background(), z, "npm", ad)
}

// lines are the log lines written so far, decoded.
func (sc *scanner) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(sc.buf.String()), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func normalEntries(n int) []zipEntry {
	var out []zipEntry
	for i := range n {
		id := fmt.Sprintf("GHSA-%04d", i)
		out = append(out, zipEntry{name: id + ".json", body: record(id, 10)})
	}
	return out
}

// R-VULN-004, Q-VULN-116: an entry above max_record_size is rejected, counted in rejected and records, remembered by its name and
// logged with its size, the limit and the key, and the scan goes on with the others.
func TestR_VULN_004_AnEntryAboveTheRecordSizeIsRejectedAndTheScanGoesOn(t *testing.T) {
	sc := newScanner(4096)
	entries := append(normalEntries(5), zipEntry{name: "GHSA-big.json", body: record("GHSA-big", 6000)})
	res, err := sc.scan(t, buildArchive(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	if res.records != 6 || res.rejected != 1 || len(res.metas) != 5 {
		t.Fatalf("records %d, rejected %d, metas %d", res.records, res.rejected, len(res.metas))
	}
	if len(res.kept) != 1 || res.kept[0] != "GHSA-big" {
		t.Errorf("kept %v", res.kept)
	}
	lines := sc.lines(t)
	if len(lines) != 1 {
		t.Fatalf("%d log lines: %s", len(lines), sc.buf)
	}
	l := lines[0]
	size := len(record("GHSA-big", 6000))
	if l["level"] != "WARN" || l["msg"] != "an advisory record was rejected" || l["source"] != "osv" || l["ecosystem"] != "npm" ||
		l["entry"] != "GHSA-big.json" || l["id"] != "GHSA-big" || l["size"] != float64(size) || l["limit"] != float64(4096) ||
		l["key"] != "vulnerabilities.sources.osv.max_record_size" {
		t.Errorf("log line %v", l)
	}
	if res.invalid != 0 {
		t.Errorf("a bound rejection counts as a validation failure: %d", res.invalid)
	}
}

// R-VULN-004, Q-VULN-117: a rejected entry is known by its name when the name is "<id>.json" with a valid id and no "/", a
// record that fails validation included; any other name gives no identifier, and a stored record of a known name is not gone.
func TestR_VULN_004_ARejectedEntryIsKnownByItsName(t *testing.T) {
	for name, want := range map[string]string{
		"GHSA-x.json": "GHSA-x", "MAL-2026-1234.json": "MAL-2026-1234",
		"a/GHSA-x.json": "", "..json": "", ".json": "", "bad id.json": "", "GHSA-x": "", "GHSA-x.JSON": "",
		strings.Repeat("a", 129) + ".json": "",
	} {
		if got, _ := entryID(name); got != want {
			t.Errorf("entryID(%q) = %q, want %q", name, got, want)
		}
	}
	sc := newScanner(4096)
	data := buildArchive(t, append(normalEntries(300), []zipEntry{
		{name: "GHSA-x.json", body: record("GHSA-x", 6000)},
		{name: "a/GHSA-y.json", body: record("GHSA-y", 6000)},
		{name: "..json", body: record("GHSA-w", 6000)},
		{name: "GHSA-z.json", body: []byte(`{"id":"not valid!","modified":"2026-10-01T00:00:00Z"}`)},
		{name: "GHSA-v.json", body: []byte(`not json`)},
	}...)...)
	res, err := sc.scan(t, data)
	if err != nil {
		t.Fatal(err)
	}
	if res.rejected != 5 || len(res.metas) != 300 {
		t.Fatalf("rejected %d, metas %d", res.rejected, len(res.metas))
	}
	if got := strings.Join(res.kept, ","); got != "GHSA-x,GHSA-z,GHSA-v" {
		t.Errorf("kept %q", got)
	}
	gone := res.gone([]string{"GHSA-0001", "GHSA-x", "GHSA-z", "GHSA-v", "GHSA-y", "GHSA-old"})
	if strings.Join(gone, ",") != "GHSA-y,GHSA-old" {
		t.Errorf("gone %v", gone)
	}
}

// R-VULN-004, Q-VULN-116: an entry whose stream is damaged is not a rejected record but a damaged archive, and it ends the run
// with archive_limit and the reader's reason.
func TestR_VULN_004_ADamagedEntryStillEndsTheRun(t *testing.T) {
	body := record("GHSA-dmg", 0)
	body = append(body[:len(body)-2], []byte(`,"details":"`+hexPad(3000)+`"}`)...)
	data := buildArchive(t, zipEntry{name: "GHSA-dmg.json", body: body, deflate: true})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	off, err := zr.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		data[int(off)+100+i] ^= 0xFF
	}
	sc := newScanner(config.MiB)
	_, err = sc.scan(t, data)
	if err == nil {
		t.Fatal("a damaged entry was scanned")
	}
	e := asError(err)
	if e.Code != errs.CodeArchiveLimit || !strings.HasPrefix(e.Detail, "the archive is not a valid zip archive: ") {
		t.Fatalf("%s: %s", e.Code, e.Detail)
	}
}

// R-VULN-004, Q-VULN-119: the first 100 rejections of a run and ecosystem are logged one by one, and one line counts the rest.
func TestR_VULN_004_RejectionsAreLoggedOneByOneUpToAHundred(t *testing.T) {
	sc := newScanner(4096)
	var entries []zipEntry
	for i := range 150 {
		id := fmt.Sprintf("GHSA-big-%03d", i)
		entries = append(entries, zipEntry{name: id + ".json", body: record(id, 5000)})
	}
	res, err := sc.scan(t, buildArchive(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	if res.rejected != 150 {
		t.Fatalf("rejected %d", res.rejected)
	}
	each, counts := 0, 0
	for _, l := range sc.lines(t) {
		switch l["msg"] {
		case "an advisory record was rejected":
			each++
		case "further advisory records were rejected and are not logged one by one":
			counts++
			if l["count"] != float64(50) || l["source"] != "osv" || l["ecosystem"] != "npm" {
				t.Errorf("count line %v", l)
			}
		}
	}
	if each != 100 || counts != 1 {
		t.Fatalf("%d lines one by one, %d count lines", each, counts)
	}
}

// R-VULN-004, Q-VULN-121: the problem of a bound or a guard names what was crossed: the measured value, the limit, the key or
// "fixed", and the entry; a name with a control or bidirectional character arrives escaped.
func TestR_VULN_004_TheProblemOfABoundOrAGuardNamesWhatWasCrossed(t *testing.T) {
	detail := func(err error) (code, detail string) {
		raw, code := problemJSON(err, time.Now(), time.Time{})
		var p struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return code, p.Detail
	}
	hostile := "GHSA-\u202e\x01evil.json"
	res := &scanResult{records: 200, rejected: 5, invalid: 5, firstEntry: hostile, firstReason: "larger than 1048576 bytes"}
	for _, tt := range []struct {
		name string
		err  error
		code string
		want []string
	}{
		{
			"a bound of the archive", archiveError(untrusted.TooLarge("the archive holds more than 1000000 entries")), errs.CodeArchiveLimit,
			[]string{"the archive crosses a fixed bound of the import: the archive holds more than 1000000 entries"},
		},
		{
			"an archive that is not valid", archiveError(untrusted.Refused(`entry "a.json" is damaged`)), errs.CodeArchiveLimit,
			[]string{`the archive is not a valid zip archive: entry "a.json" is damaged`},
		},
		{
			"an archive above max_archive_size with its size", archiveTooLarge(1<<30, 3<<30, nil), errs.CodeArchiveTooLarge,
			[]string{"vulnerabilities.sources.osv.max_archive_size (1073741824 bytes): 3221225472 bytes"},
		},
		{
			"an archive above max_archive_size without a size", archiveTooLarge(1<<30, -1, nil), errs.CodeArchiveTooLarge,
			[]string{"vulnerabilities.sources.osv.max_archive_size (1073741824 bytes)"},
		},
		{
			"too many rejected records", problemf(errs.CodeTooManyInvalidRecords, res.invalidDetail(), nil), errs.CodeTooManyInvalidRecords,
			[]string{"5 of 200 records were rejected by validation (2.50 %), more than the fixed 1 %; the first: ", `"GHSA-` + `\u202e\x01` + `evil.json": larger than 1048576 bytes`},
		},
		{
			"a shrink", problemf(errs.CodeShrinkRefused, shrinkDetail(300, 1000, 0.2), nil), errs.CodeShrinkRefused,
			[]string{"the archive would delete 300 of 1000 stored records (30.00 %), more than vulnerabilities.sources.osv.max_shrink (0.2)"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, d := detail(tt.err)
			if code != tt.code {
				t.Fatalf("code %q, want %q", code, tt.code)
			}
			for _, w := range tt.want {
				if !strings.Contains(d, w) {
					t.Errorf("detail %q lacks %q", d, w)
				}
			}
			if strings.ContainsAny(d, "\u202e\x01") {
				t.Errorf("detail %q holds a raw control or bidirectional character", d)
			}
		})
	}
	var f *failure
	if !errors.As(archiveTooLarge(1<<30, -1, nil), &f) || strings.Contains(f.detail, ":") {
		t.Errorf("no size known: %+v", f)
	}
}

// R-VULN-004, Q-VULN-116: a checkpoint is resumed only by a run with the same ecosystem, archive, batch cut and adapter version
// that has committed a batch; any other starts over.
func TestR_VULN_004_ACheckpointOfAnotherBatchCutOrAdapterVersionIsNotResumed(t *testing.T) {
	base := checkpoint{Ecosystem: "npm", Ident: `"etag-1"`, Cut: "cut-a", AdapterVersion: 3, Batches: 4}
	for _, tt := range []struct {
		name   string
		change func(*checkpoint)
		want   bool
	}{
		{"the same identity", func(*checkpoint) {}, true},
		{"another ecosystem", func(c *checkpoint) { c.Ecosystem = "NuGet" }, false},
		{"another ETag", func(c *checkpoint) { c.Ident = `"etag-2"` }, false},
		{"another batch cut", func(c *checkpoint) { c.Cut = "cut-b" }, false},
		{"another adapter version", func(c *checkpoint) { c.AdapterVersion = 2 }, false},
		{"no batch committed", func(c *checkpoint) { c.Batches = 0 }, false},
		{"a checkpoint without the identity", func(c *checkpoint) { c.Cut, c.AdapterVersion = "", 0 }, false},
	} {
		cp := base
		tt.change(&cp)
		if got := cp.resumes("npm", `"etag-1"`, "cut-a", 3); got != tt.want {
			t.Errorf("%s: resumes = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// R-VULN-004, Q-VULN-116: the digest of the batch cut changes with the records that pass the first pass, with their order and with
// the batch ends, and not with anything else.
func TestR_VULN_004_TheBatchCutDigestFollowsTheRecordsThatPass(t *testing.T) {
	metas := func(ids ...string) []entryMeta {
		var out []entryMeta
		for i, id := range ids {
			out = append(out, entryMeta{idx: i, id: id, size: 10})
		}
		return out
	}
	base := cutDigest(metas("a", "b", "c"), []int{3})
	if again := cutDigest(metas("a", "b", "c"), []int{3}); again != base {
		t.Error("the same cut has two digests")
	}
	for name, other := range map[string]string{
		"a record left out":  cutDigest(metas("a", "c"), []int{2}),
		"another order":      cutDigest(metas("a", "c", "b"), []int{3}),
		"other batch ends":   cutDigest(metas("a", "b", "c"), []int{1, 3}),
		"ids that run on":    cutDigest(metas("ab", "c"), []int{2}),
		"an empty first run": cutDigest(nil, nil),
	} {
		if other == base {
			t.Errorf("%s: the digest did not change", name)
		}
	}
}

// R-VULN-004, Q-VULN-118: only the records that fail validation count for the guard of 1 %; once they exceed 1 % of the entries
// the central directory lists, the scan stops and the rest of the archive is not read.
func TestR_VULN_004_TheGuardCountsValidationRejectionsAndStopsTheScanEarly(t *testing.T) {
	sc := newScanner(4096)
	var entries []zipEntry
	for i := range 20 { // bound rejections: not counted by the guard
		id := fmt.Sprintf("GHSA-big-%03d", i)
		entries = append(entries, zipEntry{name: id + ".json", body: record(id, 5000)})
	}
	for i := range 100 {
		id := fmt.Sprintf("GHSA-ok-%03d", i)
		entries = append(entries, zipEntry{name: id + ".json", body: record(id, 10)})
	}
	res, err := sc.scan(t, buildArchive(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	if res.rejected != 20 || res.invalid != 0 || res.tooManyInvalid() {
		t.Fatalf("rejected %d, invalid %d: bound rejections must not count for the guard", res.rejected, res.invalid)
	}

	sc = newScanner(config.MiB)
	entries = nil
	for i := range 3 { // 3 of 200 fail validation: more than 1 %
		entries = append(entries, zipEntry{name: fmt.Sprintf("GHSA-bad-%d.json", i), body: []byte("not json")})
	}
	for i := range 197 {
		id := fmt.Sprintf("GHSA-ok-%03d", i)
		entries = append(entries, zipEntry{name: id + ".json", body: record(id, 10)})
	}
	res, err = sc.scan(t, buildArchive(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	if !res.tooManyInvalid() || res.invalid != 3 || res.records != 200 || len(res.metas) != 0 {
		t.Fatalf("invalid %d of %d, %d valid records read: the scan did not stop at the third", res.invalid, res.records, len(res.metas))
	}
	if d := res.invalidDetail(); !strings.HasPrefix(d, "3 of 200 records were rejected by validation (1.50 %), more than the fixed 1 %; the first: \"GHSA-bad-0.json\": ") {
		t.Errorf("detail %q", d)
	}
}

// R-VULN-004, Q-VULN-116: the ratio bound is above what deflate can reach, so a record of repeated bytes, which deflate packs to
// about 1000:1, is read; an entry whose header declares far more than its stream can inflate to is rejected unread.
func TestR_VULN_004_TheRatioBoundIsAboveWhatDeflateReachesAndStopsAForgedHeader(t *testing.T) {
	sc := newScanner(config.MiB)
	data := buildArchive(t, zipEntry{name: "GHSA-flat.json", body: record("GHSA-flat", 700_000), deflate: true}, zipEntry{name: "GHSA-ok.json", body: record("GHSA-ok", 10)})
	res, err := sc.scan(t, data)
	if err != nil {
		t.Fatal(err)
	}
	if res.rejected != 0 || len(res.metas) != 2 {
		t.Fatalf("rejected %d, metas %d: a legitimate record of repeated bytes was rejected", res.rejected, len(res.metas))
	}

	forged := buildArchive(t, zipEntry{name: "GHSA-forged.json", body: record("GHSA-forged", 100), deflate: true}, zipEntry{name: "GHSA-ok.json", body: record("GHSA-ok", 10)})
	cd := bytes.Index(forged, []byte{0x50, 0x4b, 0x01, 0x02})
	if cd < 0 {
		t.Fatal("no central directory")
	}
	binary.LittleEndian.PutUint32(forged[cd+24:], 1<<20) // the declared size of the first entry: 1 MiB from about 100 bytes
	res, err = newScanner(config.MiB).scan(t, forged)
	if err != nil {
		t.Fatal(err)
	}
	if res.rejected != 1 || res.invalid != 0 || len(res.metas) != 1 || len(res.kept) != 1 {
		t.Fatalf("rejected %d, invalid %d, metas %d, kept %v", res.rejected, res.invalid, len(res.metas), res.kept)
	}
}

// R-VULN-004, Q-VULN-116: entries above max_record_size are rejected unread and add nothing to the archive's declared total, so
// 600 entries that declare 9 MiB each (5.4 GiB, above the 4 GiB total) at max_record_size 8 MiB are 600 rejections and the scan
// completes; the entry count bounds the archive then.
func TestR_VULN_004_RejectedEntriesDoNotUseUpTheDeclaredTotal(t *testing.T) {
	var entries []zipEntry
	for i := range 600 {
		entries = append(entries, zipEntry{name: fmt.Sprintf("GHSA-big-%03d.json", i), body: record(fmt.Sprintf("GHSA-big-%03d", i), 10)})
	}
	entries = append(entries, normalEntries(3)...)
	data := buildArchive(t, entries...)
	// Declare 9 MiB for each of the 600 entries in the central directory, as an archive that lies about sizes would.
	sig := []byte{0x50, 0x4b, 0x01, 0x02}
	for at, n := 0, 0; n < 600; n++ {
		i := bytes.Index(data[at:], sig)
		if i < 0 {
			t.Fatalf("central directory record %d not found", n)
		}
		at += i
		binary.LittleEndian.PutUint32(data[at+24:], 9<<20)
		at += len(sig)
	}
	sc := newScanner(8 * config.MiB)
	res, err := sc.scan(t, data)
	if err != nil {
		t.Fatalf("the scan ended the run: %v", err)
	}
	if res.rejected != 600 || res.invalid != 0 || len(res.metas) != 3 || res.tooManyInvalid() {
		t.Fatalf("rejected %d, invalid %d, metas %d", res.rejected, res.invalid, len(res.metas))
	}
}
