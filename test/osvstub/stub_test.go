package osvstub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the bucket's digest
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

const token = "control-token"

type fixtureServer struct {
	t    *testing.T
	stub *Stub
	srv  *httptest.Server
}

func newServer(t *testing.T, cfg Config) *fixtureServer {
	t.Helper()
	cfg.ControlToken = token
	stub := New(cfg)
	srv := httptest.NewTLSServer(stub)
	t.Cleanup(srv.Close)
	return &fixtureServer{t: t, stub: stub, srv: srv}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (f *fixtureServer) do(method, path string, hdr map[string]string, body io.Reader) reply {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.srv.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatalf("%s %s: reading the body: %v", method, path, err)
	}
	return reply{resp.StatusCode, resp.Header, b}
}

func (f *fixtureServer) get(path string, hdr ...string) reply {
	f.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return f.do(http.MethodGet, path, h, nil)
}

func (f *fixtureServer) control(method, path string, body any) reply {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	return f.do(method, path, map[string]string{"Authorization": "Bearer " + token}, rd)
}

func (f *fixtureServer) mustControl(method, path string, body any) reply {
	f.t.Helper()
	r := f.control(method, path, body)
	if r.status >= 300 {
		f.t.Fatalf("%s %s: %d %s", method, path, r.status, r.body)
	}
	return r
}

func unzip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = data
	}
	return out
}

func md5hex(b []byte) string {
	s := md5.Sum(b) //nolint:gosec // as the bucket
	return hex.EncodeToString(s[:])
}

func md5b64(b []byte) string {
	s := md5.Sum(b) //nolint:gosec // as the bucket
	return base64.StdEncoding.EncodeToString(s[:])
}

func TestLayoutAndHeaders(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})

	if r := f.get("/ecosystems.txt"); r.status != 200 || string(r.body) != "npm\nNuGet\n" {
		t.Fatalf("ecosystems.txt: %d %q", r.status, r.body)
	}

	r := f.get("/npm/all.zip")
	if r.status != 200 || r.header.Get("Content-Type") != "application/zip" {
		t.Fatalf("all.zip: %d %v", r.status, r.header)
	}
	files := unzip(t, r.body)
	ids := f.stub.IDs("npm")
	if len(files) != len(ids) || len(ids) < 12 {
		t.Fatalf("archive has %d entries, dataset %d", len(files), len(ids))
	}
	for _, id := range ids {
		if _, ok := files[id+".json"]; !ok {
			t.Errorf("archive lacks %s", id)
		}
	}
	if etag := r.header.Get("ETag"); etag != `"`+md5hex(r.body)+`"` {
		t.Errorf("ETag %s is not the quoted MD5 %s", etag, md5hex(r.body))
	}
	hash := r.header.Values("X-Goog-Hash")
	if len(hash) != 2 || !strings.HasPrefix(hash[0], "crc32c=") || hash[1] != "md5="+md5b64(r.body) {
		t.Errorf("x-goog-hash = %v", hash)
	}
	lm, err := http.ParseTime(r.header.Get("Last-Modified"))
	if err != nil || !lm.Equal(f.stub.eco["npm"].changed.Truncate(time.Second)) {
		t.Errorf("Last-Modified %q (%v)", r.header.Get("Last-Modified"), err)
	}
	if r.header.Get("Accept-Ranges") != "bytes" || r.header.Get("Cache-Control") != "public, max-age=3600" {
		t.Errorf("headers: %v", r.header)
	}
}

func TestChangeFileAndRecords(t *testing.T) { // R-VULN-004
	f := newServer(t, Config{})
	ids := f.stub.IDs("npm")
	// The change file: one row per record, newest first, RFC 3339.
	r := f.get("/npm/modified_id.csv")
	rows := strings.Split(strings.TrimSpace(string(r.body)), "\n")
	if r.status != 200 || len(rows) != len(ids) {
		t.Fatalf("modified_id.csv: %d, %d rows for %d records", r.status, len(rows), len(ids))
	}
	prev := "9999"
	for _, row := range rows {
		ts, id, ok := strings.Cut(row, ",")
		if !ok {
			t.Fatalf("row %q", row)
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("row %q: %v", row, err)
		}
		if ts > prev {
			t.Errorf("rows are not newest first at %s", row)
		}
		prev = ts
		rec := f.get("/npm/" + id + ".json")
		var obj struct{ ID, Modified string }
		if rec.status != 200 || json.Unmarshal(rec.body, &obj) != nil || obj.ID != id || obj.Modified != ts {
			t.Errorf("record %s: %d %s (row %s)", id, rec.status, rec.body, row)
		}
		if rec.header.Get("ETag") != `"`+md5hex(rec.body)+`"` || rec.header.Get("Content-Type") != "application/json" {
			t.Errorf("record %s headers: %v", id, rec.header)
		}
	}

	for _, p := range []string{"/PyPI/all.zip", "/npm/nothing.txt", "/npm/", "/", "/npm/GHSA-zzzz-zzzz-zzzz.json", "/npm/a/b.json"} {
		if r := f.get(p); r.status != 404 {
			t.Errorf("%s: %d, want 404", p, r.status)
		}
	}
	if r := f.do(http.MethodPost, "/npm/all.zip", nil, nil); r.status != 405 {
		t.Errorf("POST: %d", r.status)
	}
	if r := f.do(http.MethodHead, "/npm/all.zip", nil, nil); r.status != 200 || len(r.body) != 0 || r.header.Get("Content-Length") == "" {
		t.Errorf("HEAD: %d len %d", r.status, len(r.body))
	}
}

func TestConditionalRequests(t *testing.T) { // R-VULN-004: If-None-Match ends a run as unchanged
	f := newServer(t, Config{})
	for _, p := range []string{"/npm/all.zip", "/npm/modified_id.csv", "/NuGet/all.zip", "/ecosystems.txt", KEVPath, "/npm/" + IDVuln + ".json"} {
		first := f.get(p)
		etag, lm := first.header.Get("ETag"), first.header.Get("Last-Modified")
		if first.status != 200 || etag == "" || lm == "" {
			t.Fatalf("%s: %d %v", p, first.status, first.header)
		}
		if r := f.get(p, "If-None-Match", etag); r.status != 304 || len(r.body) != 0 {
			t.Errorf("%s If-None-Match: %d (%d bytes)", p, r.status, len(r.body))
		}
		if r := f.get(p, "If-None-Match", `"other", `+etag); r.status != 304 {
			t.Errorf("%s If-None-Match list: %d", p, r.status)
		}
		if r := f.get(p, "If-Modified-Since", lm); r.status != 304 {
			t.Errorf("%s If-Modified-Since: %d", p, r.status)
		}
		if r := f.get(p, "If-None-Match", `"0123"`); r.status != 200 {
			t.Errorf("%s with another ETag: %d", p, r.status)
		}
		if r := f.get(p, "If-Modified-Since", time.Unix(0, 0).UTC().Format(http.TimeFormat)); r.status != 200 {
			t.Errorf("%s with an old date: %d", p, r.status)
		}
	}

	// A change makes the old ETag stale, for the archive and the change file.
	old := f.get("/npm/all.zip").header.Get("ETag")
	oldChanges := f.get("/npm/modified_id.csv").header.Get("ETag")
	if _, err := f.stub.UpsertAdvisory("npm", ghsa("GHSA-aaaa-aaaa-9001", "new", nil)); err != nil {
		t.Fatal(err)
	}
	if r := f.get("/npm/all.zip", "If-None-Match", old); r.status != 200 || r.header.Get("ETag") == old {
		t.Errorf("after a change: %d, ETag %s", r.status, r.header.Get("ETag"))
	}
	if r := f.get("/npm/modified_id.csv", "If-None-Match", oldChanges); r.status != 200 {
		t.Errorf("change file after a change: %d", r.status)
	}
	// The other ecosystem is untouched.
	nuget := f.get("/NuGet/all.zip")
	if r := f.get("/NuGet/all.zip", "If-None-Match", nuget.header.Get("ETag")); r.status != 304 {
		t.Errorf("NuGet after an npm change: %d", r.status)
	}
}

func TestRange(t *testing.T) { // R-VULN-004: a ranged read of the change file; a switch turns it off
	f := newServer(t, Config{})
	full := f.get("/npm/modified_id.csv")

	r := f.get("/npm/modified_id.csv", "Range", "bytes=0-59")
	if r.status != 206 || string(r.body) != string(full.body[:60]) ||
		r.header.Get("Content-Range") != "bytes 0-59/"+strconv.Itoa(len(full.body)) {
		t.Fatalf("bytes=0-59: %d %q %v", r.status, r.body, r.header)
	}
	r = f.get("/npm/modified_id.csv", "Range", "bytes=60-")
	if r.status != 206 || string(r.body) != string(full.body[60:]) {
		t.Errorf("bytes=60-: %d", r.status)
	}
	r = f.get("/npm/modified_id.csv", "Range", "bytes=-20")
	if r.status != 206 || string(r.body) != string(full.body[len(full.body)-20:]) {
		t.Errorf("bytes=-20: %d", r.status)
	}
	if r = f.get("/npm/modified_id.csv", "Range", "bytes=99999-"); r.status != 416 {
		t.Errorf("beyond the end: %d", r.status)
	}
	// A range past the end is cut, as the bucket does.
	if r = f.get("/npm/modified_id.csv", "Range", "bytes=0-99999"); r.status != 206 || len(r.body) != len(full.body) {
		t.Errorf("0-99999: %d %d", r.status, len(r.body))
	}

	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{NoRange: true})
	r = f.get("/npm/modified_id.csv", "Range", "bytes=0-59")
	if r.status != 200 || len(r.body) != len(full.body) || r.header.Get("Accept-Ranges") != "" || r.header.Get("Content-Range") != "" {
		t.Errorf("Range off: %d %d %v", r.status, len(r.body), r.header)
	}
	// NuGet keeps Range.
	if r = f.get("/NuGet/modified_id.csv", "Range", "bytes=0-9"); r.status != 206 {
		t.Errorf("NuGet with npm's Range off: %d", r.status)
	}
	f.mustControl(http.MethodDelete, "/-/stub/faults/npm", nil)
	if r = f.get("/npm/modified_id.csv", "Range", "bytes=0-9"); r.status != 206 {
		t.Errorf("after clearing: %d", r.status)
	}
}

func TestFaultError(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{Error: 500})
	for _, p := range []string{"/npm/all.zip", "/npm/modified_id.csv", "/npm/" + IDVuln + ".json"} {
		if r := f.get(p); r.status != 500 {
			t.Errorf("%s: %d", p, r.status)
		}
	}
	if r := f.get("/NuGet/all.zip"); r.status != 200 {
		t.Errorf("NuGet: %d", r.status)
	}
	if r := f.get(KEVPath); r.status != 200 {
		t.Errorf("kev: %d", r.status)
	}
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{Error: 503, Scope: "archive"})
	if r := f.get("/npm/all.zip"); r.status != 503 {
		t.Errorf("scoped archive: %d", r.status)
	}
	if r := f.get("/npm/modified_id.csv"); r.status != 200 {
		t.Errorf("scoped change file: %d", r.status)
	}
	f.mustControl(http.MethodPut, "/-/stub/faults/kev", Fault{Error: 500})
	if r := f.get(KEVPath); r.status != 500 {
		t.Errorf("kev fault: %d", r.status)
	}
}

func TestFaultSlow(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{DelayMS: 150, Scope: "changes"})
	start := time.Now()
	f.get("/npm/modified_id.csv")
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("answered after %s", d)
	}
	start = time.Now()
	f.get("/npm/all.zip")
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("an unscoped object took %s", d)
	}
}

func TestFaultTruncated(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	good := f.get("/npm/all.zip")
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{Truncate: true})
	r := f.get("/npm/all.zip")
	if r.status != 200 || len(r.body) != len(good.body)/2 {
		t.Fatalf("%d, %d of %d bytes", r.status, len(r.body), len(good.body))
	}
	if _, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body))); err == nil {
		t.Error("a truncated archive opens")
	}
	if r.header.Get("ETag") != `"`+md5hex(r.body)+`"` || r.header.Values("X-Goog-Hash")[1] != "md5="+md5b64(r.body) {
		t.Errorf("headers do not describe the cut body: %v", r.header)
	}
}

func TestFaultAborted(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{Abort: true, Scope: "archive"})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.srv.URL+"/npm/all.zip", nil)
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		return // the connection was dropped before the headers: also a failed transfer
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("the body of an aborted transfer read to the end without an error")
	}
}

func TestFaultDigestMismatch(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{DigestMismatch: true})
	r := f.get("/npm/all.zip")
	if r.status != 200 || r.header.Get("ETag") != `"`+md5hex(r.body)+`"` {
		t.Fatalf("%d %v", r.status, r.header)
	}
	if got := r.header.Values("X-Goog-Hash")[1]; got == "md5="+md5b64(r.body) {
		t.Error("the digest is right")
	}
	if _, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body))); err != nil {
		t.Errorf("the archive itself must be intact: %v", err)
	}
	// Only archives are affected.
	c := f.get("/npm/modified_id.csv")
	if c.header.Values("X-Goog-Hash")[1] != "md5="+md5b64(c.body) {
		t.Error("the change file's digest is wrong too")
	}
}

func TestFaultInvalidRecords(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{Filler: 100})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{InvalidPercent: 2})
	n := len(f.stub.IDs("npm"))
	bad := map[string]bool{}
	for name, body := range unzip(t, f.get("/npm/all.zip").body) {
		var obj struct{ ID, Modified string }
		if err := json.Unmarshal(body, &obj); err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339Nano, obj.Modified); err != nil {
			bad[strings.TrimSuffix(name, ".json")] = true
		}
	}
	if want := (n*2 + 99) / 100; len(bad) != want || len(bad) < 2 {
		t.Fatalf("%d invalid records of %d, want %d", len(bad), n, want)
	}
	// A single fetch agrees with the archive.
	for id := range bad {
		if !strings.Contains(string(f.get("/npm/"+id+".json").body), "not-a-timestamp") {
			t.Errorf("%s is invalid in the archive and valid when fetched", id)
		}
	}
	// Named records.
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{Invalid: []string{IDVuln}})
	if !strings.Contains(string(f.get("/npm/"+IDVuln+".json").body), "not-a-timestamp") {
		t.Error("the named record is valid")
	}
	if strings.Contains(string(f.get("/npm/"+IDMal+".json").body), "not-a-timestamp") {
		t.Error("another record is invalid")
	}
}

func TestFaultShrink(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{Filler: 100})
	n := len(f.stub.IDs("npm"))
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{ShrinkPercent: 30})
	files := unzip(t, f.get("/npm/all.zip").body)
	if want := n - (n*30+99)/100; len(files) != want {
		t.Errorf("archive has %d of %d records, want %d", len(files), n, want)
	}
	if rows := strings.Count(string(f.get("/npm/modified_id.csv").body), "\n"); rows != n {
		t.Errorf("the change file lists %d, want all %d", rows, n)
	}
	if fewer := n - len(files); float64(fewer)/float64(n) < 0.30 {
		t.Errorf("%d of %d is less than 30 %%", fewer, n)
	}
}

func TestFaultNotFound(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/npm", Fault{NotFound: []string{IDVuln}})
	if r := f.get("/npm/" + IDVuln + ".json"); r.status != 404 {
		t.Errorf("%d", r.status)
	}
	if !strings.Contains(string(f.get("/npm/modified_id.csv").body), IDVuln) {
		t.Error("the change file must still list it")
	}
	if r := f.get("/npm/" + IDMal + ".json"); r.status != 200 {
		t.Errorf("another record: %d", r.status)
	}
}

func TestFaultControl(t *testing.T) { // R-VULN-004, R-VULN-029
	f := newServer(t, Config{})
	f.mustControl(http.MethodPut, "/-/stub/faults/NuGet", Fault{Error: 500})
	var got map[string]Fault
	if err := json.Unmarshal(f.mustControl(http.MethodGet, "/-/stub/faults", nil).body, &got); err != nil || got["NuGet"].Error != 500 {
		t.Fatalf("faults: %v %v", got, err)
	}
	if r := f.control(http.MethodPut, "/-/stub/faults/PyPI", Fault{Error: 500}); r.status != 404 {
		t.Errorf("unknown ecosystem: %d", r.status)
	}
	if r := f.control(http.MethodPut, "/-/stub/faults/npm", map[string]any{"bogus": 1}); r.status != 400 {
		t.Errorf("unknown field: %d", r.status)
	}
	f.mustControl(http.MethodPost, "/-/stub/reset?all=true", nil)
	if r := f.get("/NuGet/all.zip"); r.status != 200 {
		t.Errorf("after reset all: %d", r.status)
	}
}

func TestMutableDataset(t *testing.T) { // R-VULN-004: add, replace, withdraw; R-VULN-008
	f := newServer(t, Config{})
	before := f.stub.Info().Ecosystems["npm"]

	adv := ghsa("GHSA-aaaa-aaaa-9002", "added later", nil, Affected{Package: npmPkg("trove-test-new"), Ranges: semver(Introduced("0"), Fixed("2.0.0"))})
	r := f.mustControl(http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": "npm", "record": json.RawMessage(adv.JSON())})
	var added struct{ Modified string }
	_ = json.Unmarshal(r.body, &added)

	rows := strings.Split(strings.TrimSpace(string(f.get("/npm/modified_id.csv").body)), "\n")
	if rows[0] != added.Modified+",GHSA-aaaa-aaaa-9002" || len(rows) != before.ChangeRows+1 {
		t.Fatalf("first row %q, %d rows (before %d)", rows[0], len(rows), before.ChangeRows)
	}
	if added.Modified <= rows[1][:len(added.Modified)] {
		t.Errorf("the new row %s is not newer than %s", added.Modified, rows[1])
	}
	if _, ok := unzip(t, f.get("/npm/all.zip").body)["GHSA-aaaa-aaaa-9002.json"]; !ok {
		t.Error("the archive lacks the new record")
	}

	// Replace: the same id, a newer modified, one row.
	adv.Summary = "replaced"
	f.mustControl(http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": "npm", "record": json.RawMessage(adv.JSON())})
	body := string(f.get("/npm/GHSA-aaaa-aaaa-9002.json").body)
	if !strings.Contains(body, "replaced") {
		t.Errorf("record %s", body)
	}
	if n := strings.Count(string(f.get("/npm/modified_id.csv").body), "GHSA-aaaa-aaaa-9002"); n != 1 {
		t.Errorf("%d rows for the record", n)
	}

	// Withdraw: the archive keeps the record with a withdrawn.
	f.mustControl(http.MethodPost, "/-/stub/withdraw", map[string]string{"ecosystem": "npm", "id": IDMal})
	var w struct{ Withdrawn, Modified string }
	_ = json.Unmarshal(unzip(t, f.get("/npm/all.zip").body)[IDMal+".json"], &w)
	if w.Withdrawn == "" || !strings.HasPrefix(string(f.get("/npm/modified_id.csv").body), w.Modified+","+IDMal) {
		t.Errorf("withdrawn %q modified %q", w.Withdrawn, w.Modified)
	}

	// An explicit modified, and a bad request.
	f.mustControl(http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": "npm", "record": json.RawMessage(adv.JSON()), "modified": "2026-10-02T03:04:05Z"})
	if !strings.Contains(string(f.get("/npm/GHSA-aaaa-aaaa-9002.json").body), "2026-10-02T03:04:05.000000Z") {
		t.Error("the chosen modified is not served")
	}
	if r := f.control(http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": "npm", "record": map[string]string{}}); r.status != 400 {
		t.Errorf("a record without an id: %d", r.status)
	}
	if r := f.control(http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": "PyPI", "record": json.RawMessage(adv.JSON())}); r.status != 404 {
		t.Errorf("unknown ecosystem: %d", r.status)
	}

	// Remove.
	f.mustControl(http.MethodPost, "/-/stub/remove", map[string]string{"ecosystem": "npm", "id": "GHSA-aaaa-aaaa-9002"})
	if r := f.get("/npm/GHSA-aaaa-aaaa-9002.json"); r.status != 404 {
		t.Errorf("a removed record: %d", r.status)
	}

	// Reset to the fixture.
	f.mustControl(http.MethodPost, "/-/stub/reset?all=true", nil)
	if got := f.stub.Info().Ecosystems["npm"]; got.ArchiveMD5 != before.ArchiveMD5 {
		t.Errorf("after reset the archive differs: %s != %s", got.ArchiveMD5, before.ArchiveMD5)
	}
}

func TestToggles(t *testing.T) { // R-VULN-006, R-VULN-001 (trove-test-late)
	f := newServer(t, Config{})
	var info Info
	_ = json.Unmarshal(f.mustControl(http.MethodGet, "/-/stub/dataset", nil).body, &info)
	for _, name := range []string{ToggleLate, ToggleInvalidID, ToggleBigDetails, ToggleJSReference, ToggleReal, ToggleReference} {
		if on, ok := info.Toggles[name]; !ok || on {
			t.Errorf("toggle %s: present %v, on %v", name, ok, on)
		}
	}
	if _, ok := unzip(t, f.get("/npm/all.zip").body)[IDLate+".json"]; ok {
		t.Fatal("the late record is there before its toggle")
	}

	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleLate, map[string]bool{"on": true})
	if !strings.HasPrefix(string(f.get("/npm/modified_id.csv").body), "20") || !strings.Contains(strings.SplitN(string(f.get("/npm/modified_id.csv").body), "\n", 2)[0], IDLate) {
		t.Error("the late record is not the newest row")
	}
	if !strings.Contains(string(f.get("/npm/"+IDLate+".json").body), NPMLate) || f.get("/NuGet/"+IDNuGetLate+".json").status != 200 {
		t.Error("the late records are missing")
	}
	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleLate, map[string]bool{"on": false})
	if f.get("/npm/"+IDLate+".json").status != 404 {
		t.Error("the late record stays after the toggle is off")
	}

	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleInvalidID, map[string]bool{"on": true})
	files := unzip(t, f.get("/npm/all.zip").body)
	var found bool
	for name, body := range files {
		if strings.Contains(string(body), `"id":"../x"`) {
			found = true
			if strings.Contains(name, "..") || strings.Contains(name, "/") {
				t.Errorf("the entry name %q is no safe file name", name)
			}
		}
	}
	if !found {
		t.Error("the ../x record is not in the archive")
	}
	if !strings.Contains(string(f.get("/npm/modified_id.csv").body), ",../x\n") {
		t.Error("the change file does not list ../x")
	}

	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleBigDetails, map[string]bool{"on": true})
	var big struct{ Details string }
	_ = json.Unmarshal(f.get("/npm/"+IDBigDetails+".json").body, &big)
	if len(big.Details) != DetailsSize {
		t.Errorf("details are %d bytes", len(big.Details))
	}
	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleJSReference, map[string]bool{"on": true})
	if !strings.Contains(string(f.get("/npm/"+IDJSReference+".json").body), `"javascript:alert(1)"`) {
		t.Error("the javascript: reference is missing")
	}
	if r := f.control(http.MethodPut, "/-/stub/toggles/nope", map[string]bool{"on": true}); r.status != 404 {
		t.Errorf("unknown toggle: %d", r.status)
	}

	f.mustControl(http.MethodPut, "/-/stub/toggles/"+ToggleReal, map[string]bool{"on": true})
	if f.get("/npm/GHSA-vr7g-88fq-vhq3.json").status != 200 || f.get("/NuGet/GHSA-473q-m89c-ghf8.json").status != 200 {
		t.Error("the real records are missing")
	}
}

func TestFiller(t *testing.T) {
	f := newServer(t, Config{})
	base := len(f.stub.IDs("NuGet"))
	f.mustControl(http.MethodPut, "/-/stub/filler/NuGet", map[string]int{"count": 50})
	if n := len(f.stub.IDs("NuGet")); n != base+50 {
		t.Errorf("%d records", n)
	}
	f.mustControl(http.MethodPut, "/-/stub/filler/NuGet", map[string]int{"count": 20})
	if n := len(f.stub.IDs("NuGet")); n != base+20 {
		t.Errorf("%d records after lowering", n)
	}
	f.mustControl(http.MethodPut, "/-/stub/filler/NuGet", map[string]int{"count": 0})
	if n := len(f.stub.IDs("NuGet")); n != base {
		t.Errorf("%d records after clearing", n)
	}
	if r := f.control(http.MethodPut, "/-/stub/filler/NuGet", map[string]int{"count": -1}); r.status != 400 {
		t.Errorf("negative: %d", r.status)
	}
}

func TestKEV(t *testing.T) { // R-VULN-008
	f := newServer(t, Config{})
	type catalog struct {
		Title           string
		CatalogVersion  string
		Count           int
		Vulnerabilities []KEVEntry
	}
	read := func() (catalog, reply) {
		r := f.get(KEVPath)
		var c catalog
		if err := json.Unmarshal(r.body, &c); err != nil {
			t.Fatalf("catalog: %v", err)
		}
		return c, r
	}
	c, first := read()
	if first.status != 200 || c.Count != len(c.Vulnerabilities) || c.Count != 2 || first.header.Get("Content-Type") != "application/json" {
		t.Fatalf("catalog: %d, %+v", first.status, c)
	}
	cve := regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)
	for _, v := range c.Vulnerabilities {
		if !cve.MatchString(v.CVEID) || v.DateAdded == "" || v.DueDate == "" || v.Ransomware == "" {
			t.Errorf("entry %+v", v)
		}
	}
	for _, v := range c.Vulnerabilities {
		if v.CVEID == "CVE-2026-0001" {
			t.Error("the alias CVE is in the catalog from the start")
		}
	}

	f.mustControl(http.MethodPost, "/-/stub/kev", KEVEntry{CVEID: "CVE-2026-0001"})
	c, second := read()
	var added *KEVEntry
	for i := range c.Vulnerabilities {
		if c.Vulnerabilities[i].CVEID == "CVE-2026-0001" {
			added = &c.Vulnerabilities[i]
		}
	}
	if added == nil || c.Count != 3 || added.DateAdded == "" || added.DueDate == "" {
		t.Fatalf("after adding: %+v", c)
	}
	if second.header.Get("ETag") == first.header.Get("ETag") {
		t.Error("the ETag did not change")
	}
	if r := f.get(KEVPath, "If-None-Match", first.header.Get("ETag")); r.status != 200 {
		t.Errorf("the old ETag: %d", r.status)
	}
	if r := f.get(KEVPath, "If-None-Match", second.header.Get("ETag")); r.status != 304 {
		t.Errorf("the new ETag: %d", r.status)
	}
	if r := f.control(http.MethodPost, "/-/stub/kev", KEVEntry{CVEID: "nope"}); r.status != 400 {
		t.Errorf("a bad CVE: %d", r.status)
	}
	f.mustControl(http.MethodDelete, "/-/stub/kev/CVE-2026-0001", nil)
	if c, _ = read(); c.Count != 2 {
		t.Errorf("after removing: %d", c.Count)
	}
	if r := f.control(http.MethodDelete, "/-/stub/kev/CVE-2026-0001", nil); r.status != 404 {
		t.Errorf("removing twice: %d", r.status)
	}
}

func TestRequestLog(t *testing.T) { // R-VULN-004, R-VULN-005: "no request", "exactly 3 record requests"
	f := newServer(t, Config{})
	f.get("/npm/all.zip")
	f.get("/NuGet/all.zip")
	f.get("/npm/modified_id.csv", "Range", "bytes=0-9", "If-None-Match", `"x"`)
	for _, id := range []string{IDVuln, IDMal, IDAlias} {
		f.get("/npm/" + id + ".json")
	}
	f.get("/npm/all.zip", "If-None-Match", f.get("/npm/all.zip").header.Get("ETag"))
	f.get("/nothing")

	c := f.stub.Counts()
	if c.Total != 9 {
		t.Errorf("total %d, want 9 (the ETag lookup above counts too)", c.Total)
	}
	if c.Records("npm") != 3 || c.Kind("npm", KindArchive) != 3 || c.Kind("NuGet", KindArchive) != 1 || c.Kind("npm", KindChanges) != 1 {
		t.Errorf("kinds %v", c.Kinds)
	}
	if p := c.Path("/npm/all.zip"); p.Requests != 3 || p.NotModified != 1 || p.Conditional != 1 {
		t.Errorf("/npm/all.zip: %+v", p)
	}
	if p := c.Path("/npm/modified_id.csv"); p.Ranged != 1 || p.Partial != 1 || p.Conditional != 1 {
		t.Errorf("/npm/modified_id.csv: %+v", p)
	}
	if p := c.Path("/nothing"); p.Errors != 1 {
		t.Errorf("/nothing: %+v", p)
	}
	if c.Under("/npm/") != 7 || c.Under("/PyPI/") != 0 {
		t.Errorf("under: %d", c.Under("/npm/"))
	}
	if len(c.Log) != 9 || c.Log[0].Seq != 1 || c.Log[8].Path != "/nothing" || c.Log[2].Range != "bytes=0-9" || !c.Log[2].Conditional {
		t.Errorf("log: %+v", c.Log)
	}

	// Over the control API; the control requests are not counted; the reset empties it.
	var viaAPI Counts
	if err := json.Unmarshal(f.mustControl(http.MethodGet, "/-/stub/requests", nil).body, &viaAPI); err != nil || viaAPI.Total != 9 || len(viaAPI.Log) != 9 {
		t.Fatalf("GET requests: %v, %+v", err, viaAPI.Total)
	}
	viaAPI = Counts{}
	if err := json.Unmarshal(f.mustControl(http.MethodGet, "/-/stub/requests?log=false", nil).body, &viaAPI); err != nil || len(viaAPI.Log) != 0 || viaAPI.Total != 9 {
		t.Errorf("log=false: %v, %d", err, len(viaAPI.Log))
	}
	f.mustControl(http.MethodPost, "/-/stub/reset", nil)
	if c = f.stub.Counts(); c.Total != 0 || len(c.Paths) != 0 || len(c.Kinds) != 0 {
		t.Errorf("after reset: %+v", c)
	}
	// A plain reset keeps the dataset.
	if f.get("/npm/"+IDVuln+".json").status != 200 {
		t.Error("the dataset is gone")
	}
}

func TestControlAuth(t *testing.T) {
	f := newServer(t, Config{})
	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}, {"Authorization": token}} {
		if r := f.do(http.MethodGet, "/-/stub/requests", hdr, nil); r.status != 401 || r.header.Get("WWW-Authenticate") == "" {
			t.Errorf("%v: %d", hdr, r.status)
		}
	}
	if r := f.control(http.MethodGet, "/-/stub/nothing", nil); r.status != 404 {
		t.Errorf("unknown control path: %d", r.status)
	}
	if r := f.control(http.MethodDelete, "/-/stub/requests", nil); r.status != 405 {
		t.Errorf("wrong method: %d", r.status)
	}
	// Without a token the API does not exist.
	srv := httptest.NewTLSServer(New(Config{}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/-/stub/requests")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("no token configured: %d", resp.StatusCode)
	}
}

func TestStartIsTLSOnly(t *testing.T) {
	stub := New(Config{})
	if _, err := stub.Start("127.0.0.1:0", nil); err == nil {
		t.Fatal("started without a certificate")
	}
	ss, err := NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: ss.ClientConfig()}, Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, run.URL+"/ecosystems.txt", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("%d", resp.StatusCode)
	}
	plain := &http.Client{Timeout: 5 * time.Second}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+run.Addr+"/ecosystems.txt", nil)
	if resp, err := plain.Do(req); err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == 200 {
			t.Error("served plain HTTP")
		}
	}
}

func TestFixtureContent(t *testing.T) { // R-VULN-006 id rules, R-VULN-029 fixture
	idRule := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fix := DefaultFixture()
	seen := map[string]bool{}
	for eco, recs := range fix.Base {
		for _, r := range recs {
			if !idRule.MatchString(r.ID) {
				t.Errorf("%s %s: base record id breaks the R-VULN-006 rule", eco, r.ID)
			}
			if seen[eco+r.ID] {
				t.Errorf("%s %s twice", eco, r.ID)
			}
			seen[eco+r.ID] = true
			var a Advisory
			if err := json.Unmarshal(r.Body, &a); err != nil || a.ID != r.ID {
				t.Errorf("%s: %v", r.ID, err)
			}
			for _, aff := range a.Affected {
				if aff.Package.Ecosystem != eco {
					t.Errorf("%s: affected ecosystem %s in the %s archive", r.ID, aff.Package.Ecosystem, eco)
				}
			}
			if strings.HasPrefix(r.ID, "GHSA-") && !regexp.MustCompile(`^GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}$`).MatchString(r.ID) {
				t.Errorf("%s does not look like a GHSA id", r.ID)
			}
		}
	}
	for _, need := range []struct{ eco, id string }{
		{"npm", IDVuln},
		{"npm", IDMal},
		{"npm", IDAlias},
		{"npm", IDAliasCVE},
		{"npm", IDAcmeMal},
		{"npm", IDLeftPad},
		{"npm", IDCVSS},
		{"npm", IDQualitative},
		{"npm", IDNoSeverity},
		{"npm", IDBadRange},
		{"npm", IDList},
		{"npm", IDWithdrawn},
		{"NuGet", IDNuGetHigh},
		{"NuGet", IDNuGetMal},
		{"NuGet", IDNuGetMedium},
	} {
		if !seen[need.eco+need.id] {
			t.Errorf("fixture lacks %s %s", need.eco, need.id)
		}
	}
}

func TestFixtureRecords(t *testing.T) { // R-VULN-029
	fix := DefaultFixture()
	byID := func(eco, id string) Advisory {
		for _, r := range fix.Base[eco] {
			if r.ID == id {
				var a Advisory
				_ = json.Unmarshal(r.Body, &a)
				return a
			}
		}
		t.Fatalf("no %s", id)
		return Advisory{}
	}
	if a := byID("npm", IDVuln); a.Affected[0].Package.Name != "trove-test-vuln" || a.Affected[0].Ranges[0].Events[1]["fixed"] != "1.0.1" || a.Severity[0].Type != "CVSS_V3" || !strings.HasPrefix(a.Severity[0].Score, "CVSS:3.1/") {
		t.Errorf("trove-test-vuln: %+v", a)
	}
	if a := byID("npm", IDMal); a.Affected[0].Package.Name != "trove-test-mal" || a.Affected[0].Versions[0] != "1.0.0" || len(a.Affected[0].Ranges) != 0 || len(a.Severity) != 0 {
		t.Errorf("trove-test-mal: %+v", a)
	}
	if a := byID("npm", IDAlias); a.Aliases[0] != "CVE-2026-0001" || byID("npm", IDAliasCVE).Aliases[0] != IDAlias {
		t.Error("alias pair")
	}
	if a := byID("npm", IDCVSS); len(a.Severity) != 2 || a.Severity[1].Type != "CVSS_V4" {
		t.Errorf("both vectors: %+v", a.Severity)
	}
	if a := byID("npm", IDQualitative); len(a.Severity) != 0 || a.DatabaseSpecific["severity"] != "MODERATE" {
		t.Errorf("qualitative: %+v", a)
	}
	if a := byID("npm", IDNoSeverity); len(a.Severity) != 0 || len(a.DatabaseSpecific) != 0 {
		t.Errorf("nothing: %+v", a)
	}
	if a := byID("npm", IDBadRange); a.Affected[0].Ranges[0].Events[1]["fixed"] != "not-a-version" {
		t.Errorf("bad range: %+v", a)
	}
	if byID("npm", IDWithdrawn).Withdrawn == "" || byID("npm", IDAcmeMal).Affected[0].Package.Name != "@acme/ui" {
		t.Error("withdrawn or @acme/ui")
	}
	if a := byID("npm", IDLeftPad); a.Affected[0].Ranges[0].Events[1]["last_affected"] != "1.3.0" {
		t.Errorf("left-pad: %+v", a)
	}
}

// The NuGet fixture names versions of the NuGet test upstream's default corpus; this test is the check that the two
// stay in step.
func TestNuGetFixtureMatchesTheNuGetUpstream(t *testing.T) { // R-VULN-029
	corpus := nugetstub.Default()
	for _, want := range []struct{ id, version string }{
		{NuGetHigh, "13.0.1"}, {NuGetHigh, "13.0.3"}, {NuGetMal, "1.0.0"}, {NuGetMedium, "2.0.0"}, {NuGetLate, "2.1.0"},
	} {
		p, ok := corpus.Get(want.id)
		if !ok {
			t.Errorf("the NuGet test upstream has no %s", want.id)
			continue
		}
		var has bool
		for _, v := range p.Versions {
			has = has || v.Version == want.version
		}
		if !has {
			t.Errorf("the NuGet test upstream has no %s %s", want.id, want.version)
		}
	}
}

func TestRealRecords(t *testing.T) {
	real := Real()
	if len(real) != 22 {
		t.Fatalf("%d frozen records, want 22", len(real))
	}
	raw, err := realFS.ReadFile("testdata/real/SOURCE.json")
	if err != nil {
		t.Fatal(err)
	}
	var src struct {
		Taken   string
		Records []struct{ Ecosystem, ID, SHA256 string }
	}
	if err := json.Unmarshal(raw, &src); err != nil || len(src.Records) != len(real) {
		t.Fatalf("SOURCE.json: %v, %d records", err, len(src.Records))
	}
	if _, err := time.Parse(time.DateOnly, src.Taken); err != nil {
		t.Errorf("taken %q: %v", src.Taken, err)
	}
	want := map[string]string{}
	for _, r := range src.Records {
		want[r.Ecosystem+"/"+r.ID] = r.SHA256
	}
	var mal, withdrawn int
	for _, r := range real {
		sum := sha256.Sum256(r.Body)
		if want[r.Ecosystem+"/"+r.ID] != hex.EncodeToString(sum[:]) {
			t.Errorf("%s %s differs from SOURCE.json", r.Ecosystem, r.ID)
		}
		var o struct {
			ID, Modified, Withdrawn string
			Affected                []struct{ Package Package }
		}
		if err := json.Unmarshal(r.Body, &o); err != nil || o.ID != r.ID || o.Modified == "" {
			t.Errorf("%s: %v", r.ID, err)
		}
		if strings.HasPrefix(r.ID, "MAL-") {
			mal++
		}
		if o.Withdrawn != "" {
			withdrawn++
		}
	}
	if mal < 4 || withdrawn < 3 {
		t.Errorf("%d MAL- and %d withdrawn records", mal, withdrawn)
	}
	for _, id := range []string{"GHSA-vr7g-88fq-vhq3", "GHSA-473q-m89c-ghf8"} {
		if _, ok := want["npm/"+id]; !ok {
			if _, ok := want["NuGet/"+id]; !ok {
				t.Errorf("%s is not frozen", id)
			}
		}
	}
}

// R-VULN-017 (E4b.5): the recorded reference data is what SOURCE.json says, every identifier of the public answers has its
// record, and the toggle serves the records.
func TestReferenceData(t *testing.T) {
	raw, err := referenceFS.ReadFile("testdata/reference/SOURCE.json")
	if err != nil {
		t.Fatal(err)
	}
	var src struct {
		Taken string
		Files []struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(raw, &src); err != nil || len(src.Files) == 0 {
		t.Fatalf("SOURCE.json: %v, %d files", err, len(src.Files))
	}
	if _, err := time.Parse(time.DateOnly, src.Taken); err != nil {
		t.Errorf("taken %q: %v", src.Taken, err)
	}
	for _, f := range src.Files {
		b, err := referenceFS.ReadFile("testdata/reference/" + f.Path)
		if err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s differs from SOURCE.json", f.Path)
		}
	}
	ref := ReferenceAnswers()
	if ref.Taken != src.Taken {
		t.Errorf("taken %q, SOURCE.json %q", ref.Taken, src.Taken)
	}
	have := map[string]bool{}
	for _, r := range ReferenceRecords() {
		have[r.ID] = true
		var o struct{ ID, Modified string }
		if err := json.Unmarshal(r.Body, &o); err != nil || o.ID != r.ID || o.Modified == "" {
			t.Errorf("%s: %v", r.ID, err)
		}
	}
	for _, answer := range [][]byte{ref.PublicAnswer, ref.ControlAnswer} {
		var a map[string][]struct{ URL string }
		if err := json.Unmarshal(answer, &a); err != nil {
			t.Fatal(err)
		}
		for name, advs := range a {
			for _, x := range advs {
				if id := strings.TrimPrefix(x.URL, "https://github.com/advisories/"); !have[id] {
					t.Errorf("%s: no record of %s", name, id)
				}
			}
		}
	}
	stub := New(Config{})
	if err := stub.Toggle(ToggleReference, true); err != nil {
		t.Fatal(err)
	}
	for id := range have {
		if !slices.Contains(stub.IDs("npm"), id) {
			t.Errorf("the toggle does not serve %s", id)
		}
	}
}

func TestDeterministicArchive(t *testing.T) {
	a, _ := New(Config{}).Archive("npm")
	b, _ := New(Config{}).Archive("npm")
	if !bytes.Equal(a, b) {
		t.Error("two stubs hold different archives for the same fixture")
	}
}

func TestClockOrdersChanges(t *testing.T) {
	// A clock that stands still still yields strictly increasing modified times.
	frozen := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	stub := New(Config{Now: func() time.Time { return frozen }})
	var last time.Time
	for i := range 3 {
		mod, err := stub.UpsertAdvisory("npm", ghsa("GHSA-aaaa-aaaa-"+strings.Repeat("9", 3)+strconv.Itoa(i), "x", nil))
		if err != nil {
			t.Fatal(err)
		}
		if !mod.After(last) {
			t.Errorf("modified %s is not after %s", mod, last)
		}
		last = mod
	}
}
