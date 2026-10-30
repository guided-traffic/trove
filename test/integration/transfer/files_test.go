//go:build integration

package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
)

// content returns n deterministic bytes that differ by seed.
func content(n int, seed byte) []byte {
	b := make([]byte, n)
	x := uint32(seed)*2654435761 + 12345
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func reprDigest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(s[:]) + ":"
}

// webTransferEnv is an installation of the harness with two replicas, the repository generic-local and the
// users of the transfer tests: ci may read and deploy, maint may also overwrite and
// delete, reader may read.
type webTransferEnv struct {
	*webEnv
	a, b *webReplica
	val  *apitest.Validator // the stack's: it records the operations the answers of the suite reached
}

func newWebTransferEnv(t *testing.T, opts ...func(*webOpts)) *webTransferEnv {
	t.Helper()
	e := newWebEnv(t)
	e.breakGlass()
	o := webOpts{}
	for _, f := range opts {
		f(&o)
	}
	te := &webTransferEnv{webEnv: e, val: e.stack.Validator()}
	te.a = e.newReplica("a", o)
	te.b = e.newReplica("b", o)
	te.a.createRepository("generic-local", nil)
	for _, u := range []string{"ci", "maint", "reader", "nobody"} {
		e.user(u)
	}
	e.grant(te.a, "ci-deploy", []string{"generic-local"}, authz.PatternLists{}, map[string][]string{"user:ci": {"read", "deploy"}})
	e.grant(te.a, "maint-all", []string{"generic-local"}, authz.PatternLists{}, map[string][]string{"user:maint": {"read", "deploy", "overwrite", "delete"}})
	e.grant(te.a, "reader-read", []string{"generic-local"}, authz.PatternLists{}, map[string][]string{"user:reader": {"read"}})
	te.await("the policies on both replicas", func(r *webReplica) bool {
		return (r.put("ci", "/files/generic-local/.probe-ci", "x").status/100 == 2) && r.get("reader", "/files/generic-local/.probe-ci").status == 200 &&
			r.get("maint", "/files/generic-local/.probe-ci").status == 200
	})
	// The probe file served only to wait for the policies.
	if res := te.a.do("maint", "DELETE", "/files/generic-local/.probe-ci", nil); res.status != 204 {
		t.Fatalf("remove the probe: %d %s", res.status, res.body)
	}
	return te
}

// await waits until cond holds on both replicas.
func (te *webTransferEnv) await(what string, cond func(*webReplica) bool) {
	te.t.Helper()
	for _, r := range []*webReplica{te.a, te.b} {
		waitFor(te.t, what, 10*time.Second, func() bool { return cond(r) })
	}
}

// header returns a header of a response without the volatile ones in a comparison.
func (r response) hdr(k string) string { return r.header.Get(k) }

func TestE1_4_RawTransfer(t *testing.T) {
	te := newWebTransferEnv(t)
	a, b := te.a, te.b
	data := content(48213, 1)
	path := "/files/generic-local/tools/widget/2.3/widget-2.3.bin"

	t.Run("R-STOR-007 R-GENERIC-004 E1.4 a deploy answers 201 with the item description, a repeat of the same bytes 200", func(t *testing.T) {
		res := a.put("ci", path, string(data), "Content-Type", "application/x-widget")
		if res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		m := res.json(t)
		d := m["digests"].(map[string]any)
		if m["kind"] != "file" || m["size"] != float64(len(data)) || d["sha256"] != sum(data) || m["mediaType"] != "application/x-widget" || m["package"] != nil {
			t.Errorf("item = %s", res.body)
		}
		if res.hdr("ETag") != `"`+sum(data)+`"` || !strings.HasSuffix(res.hdr("Location"), path) {
			t.Errorf("ETag %q, Location %q", res.hdr("ETag"), res.hdr("Location"))
		}
		if m["created"].(map[string]any)["by"] != "user:ci" {
			t.Errorf("created = %v", m["created"])
		}
		again := b.put("ci", path, string(data), "Content-Type", "application/x-widget")
		if again.status != 200 || again.json(t)["digests"].(map[string]any)["sha256"] != sum(data) {
			t.Fatalf("re-upload through B: %d %s", again.status, again.body)
		}
		if n := te.number("SELECT count(*) FROM items WHERE path = 'tools/widget/2.3/widget-2.3.bin'"); n != 1 {
			t.Errorf("%d items", n)
		}
		if n := te.number("SELECT count(*) FROM blobs WHERE sha256 = decode($1, 'hex')", sum(data)); n != 1 {
			t.Errorf("%d blobs", n)
		}
	})

	t.Run("R-STOR-012 E1.4 a range request returns exactly 100 bytes, equal to the first 100 bytes of the file", func(t *testing.T) {
		for _, r := range []*webReplica{a, b} {
			res := r.get("reader", path, "Range", "bytes=0-99")
			if res.status != 206 || !bytes.Equal(res.body, data[:100]) || res.hdr("Content-Length") != "100" || res.hdr("Content-Range") != fmt.Sprintf("bytes 0-99/%d", len(data)) {
				t.Fatalf("%s: %d %v (%d bytes)", r.name, res.status, res.header, len(res.body))
			}
		}
		if res := a.get("reader", path, "Range", "bytes=-10"); res.status != 206 || !bytes.Equal(res.body, data[len(data)-10:]) {
			t.Errorf("suffix range: %d", res.status)
		}
		if res := a.get("reader", path, "Range", "bytes=0-9,20-29"); res.status != 200 || !bytes.Equal(res.body, data) {
			t.Errorf("several ranges are answered with the whole content: %d", res.status)
		}
	})

	t.Run("R-STOR-012 a download carries Content-Length and no chunked coding, the content address as ETag, the digests and the validators", func(t *testing.T) {
		resp, err := a.raw("reader", "GET", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !bytes.Equal(got, data) || resp.ContentLength != int64(len(data)) || len(resp.TransferEncoding) != 0 {
			t.Fatalf("%d, %d bytes, Content-Length %d, Transfer-Encoding %v", resp.StatusCode, len(got), resp.ContentLength, resp.TransferEncoding)
		}
		h := resp.Header
		if h.Get("ETag") != `"`+sum(data)+`"` || h.Get("Accept-Ranges") != "bytes" || h.Get("Repr-Digest") != reprDigest(data) ||
			h.Get("Content-Type") != "application/x-widget" || h.Get("Last-Modified") == "" || h.Get("Cache-Control") != "private, no-cache" {
			t.Errorf("headers = %v", h)
		}
		if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment; filename*=UTF-8''widget-2.3.bin") {
			t.Errorf("Content-Disposition = %q", h.Get("Content-Disposition"))
		}
		want := h.Get("Repr-Digest")
		if res := a.get("reader", path, "Want-Repr-Digest", "sha-512=10"); !strings.HasPrefix(res.hdr("Repr-Digest"), want+", sha-512=:") {
			t.Errorf("Repr-Digest with Want-Repr-Digest = %q", res.hdr("Repr-Digest"))
		}
	})

	t.Run("R-STOR-012 HEAD and conditional requests are answered from PostgreSQL without a request to the object store", func(t *testing.T) {
		before := a.obj.gets()
		head := a.do("reader", "HEAD", path, nil)
		if head.status != 200 || head.hdr("Content-Length") != strconv.Itoa(len(data)) || head.hdr("ETag") != `"`+sum(data)+`"` || head.hdr("Accept-Ranges") != "bytes" || len(head.body) != 0 {
			t.Fatalf("HEAD: %d %v", head.status, head.header)
		}
		if res := a.get("reader", path, "If-None-Match", `"`+sum(data)+`"`); res.status != 304 || len(res.body) != 0 || res.hdr("ETag") == "" {
			t.Fatalf("If-None-Match: %d %v", res.status, res.header)
		}
		if res := a.get("reader", path, "If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)); res.status != 304 {
			t.Fatalf("If-Modified-Since: %d", res.status)
		}
		if res := a.get("reader", path, "If-None-Match", `"other"`); res.status != 200 {
			t.Fatalf("a stale validator: %d", res.status)
		}
		if got := a.obj.gets(); got != before+1 { // only the last, a full 200
			t.Fatalf("object store GETs grew by %d, want 1", got-before)
		}
		if res := a.get("reader", path, "If-Match", `"other"`); res.status != 412 {
			t.Errorf("If-Match: %d", res.status)
		}
	})

	t.Run("R-STOR-012 an unsatisfiable range answers 416 with the size", func(t *testing.T) {
		res := a.get("reader", path, "Range", "bytes=5000000000-")
		if res.status != 416 || res.hdr("Content-Range") != fmt.Sprintf("bytes */%d", len(data)) {
			t.Fatalf("%d %v", res.status, res.header)
		}
		if res.json(t)["code"] != "range_not_satisfiable" {
			t.Errorf("body = %s", res.body)
		}
	})

	t.Run("R-STOR-012 R-ARCH-024 a resumed range with If-Range is answered alike on every replica", func(t *testing.T) {
		for _, r := range []*webReplica{a, b} {
			res := r.get("reader", path, "Range", "bytes=100-", "If-Range", `"`+sum(data)+`"`)
			if res.status != 206 || !bytes.Equal(res.body, data[100:]) {
				t.Fatalf("%s: %d (%d bytes)", r.name, res.status, len(res.body))
			}
			other := r.get("reader", path, "Range", "bytes=100-", "If-Range", `"`+sum([]byte("other"))+`"`)
			if other.status != 200 || !bytes.Equal(other.body, data) {
				t.Fatalf("%s: a changed ETag must answer the whole content: %d", r.name, other.status)
			}
		}
	})

	t.Run("R-GENERIC-004 R-STOR-007 a Repr-Digest that differs from the content is refused with 409 and stores nothing", func(t *testing.T) {
		body := content(5000, 2)
		blobs := te.number("SELECT count(*) FROM blobs")
		items := te.number("SELECT count(*) FROM items")
		wrong := reprDigest([]byte("not the content"))
		problemOf(t, a.put("ci", "/files/generic-local/bad/digest.bin", string(body), "Repr-Digest", wrong), 409, "conflict")
		if te.number("SELECT count(*) FROM blobs") != blobs || te.number("SELECT count(*) FROM items") != items {
			t.Fatal("a refused upload left a blob or an item")
		}
		if res := a.get("reader", "/files/generic-local/bad/digest.bin"); res.status != 404 {
			t.Errorf("the refused file is served: %d", res.status)
		}
		ok := a.put("ci", "/files/generic-local/good/digest.bin", string(body), "Repr-Digest", reprDigest(body))
		if ok.status != 201 || ok.json(t)["declaredDigests"].(map[string]any)["sha256"] != sum(body) {
			t.Fatalf("%d %s", ok.status, ok.body)
		}
		problemOf(t, a.put("ci", "/files/generic-local/bad/header.bin", "x", "Repr-Digest", "sha-256=nonsense"), 400, "invalid_header")
	})

	t.Run("R-ARCH-007 R-GENERIC-005 E1.4 an uploaded evil.svg is served with the four content headers", func(t *testing.T) {
		svg := `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(document.domain)"><script>alert(1)</script></svg>`
		if res := a.put("ci", "/files/generic-local/web/evil.svg", svg, "Content-Type", "image/svg+xml"); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		for _, r := range []*webReplica{a, b} {
			res := r.get("reader", "/files/generic-local/web/evil.svg")
			h := res.header
			if res.status != 200 || res.text() != svg {
				t.Fatalf("%s: %d", r.name, res.status)
			}
			for k, want := range map[string]string{
				"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox; default-src 'none'",
				"Cross-Origin-Resource-Policy": "same-site", "Content-Disposition": "attachment; filename*=UTF-8''evil.svg",
				"Content-Type": "image/svg+xml",
			} {
				if h.Get(k) != want {
					t.Errorf("%s: %s = %q, want %q", r.name, k, h.Get(k), want)
				}
			}
		}
		for _, status := range []int{200, 206, 304} { // the headers do not depend on the status
			hdr := []string{}
			switch status {
			case 206:
				hdr = []string{"Range", "bytes=0-4"}
			case 304:
				hdr = []string{"If-None-Match", `"` + sum([]byte(svg)) + `"`}
			}
			res := a.get("reader", "/files/generic-local/web/evil.svg", hdr...)
			if res.status != status || res.hdr("Content-Security-Policy") != "sandbox; default-src 'none'" || res.hdr("X-Content-Type-Options") != "nosniff" {
				t.Errorf("status %d: %d %v", status, res.status, res.header)
			}
		}
	})

	t.Run("R-GENERIC-004 any file is accepted: package.json, x.tgz and an empty file are not special (500 MiB: TestR_STOR_004_LargeUploadIsStreamedThroughTheSpool)", func(t *testing.T) {
		for _, name := range []string{"package.json", "x.tgz", "empty.bin"} {
			body := "{}"
			if name == "empty.bin" {
				body = ""
			}
			if res := a.put("ci", "/files/generic-local/misc/"+name, body); res.status != 201 {
				t.Errorf("%s: %d %s", name, res.status, res.body)
			}
			if res := b.get("reader", "/files/generic-local/misc/"+name); res.status != 200 || res.text() != body {
				t.Errorf("%s read: %d %q", name, res.status, res.body)
			}
		}
		if res := a.get("reader", "/files/generic-local/misc/empty.bin", "Range", "bytes=0-0"); res.status != 416 {
			t.Errorf("a range of an empty file: %d", res.status)
		}
	})

	t.Run("R-API-012 If-None-Match: * makes a deploy create-only", func(t *testing.T) {
		p := "/files/generic-local/ci/create-only.bin"
		if res := a.put("ci", p, "one", "If-None-Match", "*"); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		problemOf(t, b.put("ci", p, "one", "If-None-Match", "*"), 412, "precondition_failed")
		problemOf(t, b.put("ci", p, "two", "If-None-Match", "*"), 412, "precondition_failed")
	})

	t.Run("R-API-002 the answers of the files operations matched the document", func(t *testing.T) {
		if missing := te.val.Uncovered("deployFile", "downloadFile", "headFile"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}

func TestR_AUTHZ_006_OverwriteNeedsItsOwnAction(t *testing.T) {
	te := newWebTransferEnv(t)
	a, b := te.a, te.b
	p := "/files/generic-local/app/1.0/app.tgz"
	first, second := content(3000, 3), content(3000, 4)

	if res := a.put("ci", p, string(first)); res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}
	t.Run("R-GENERIC-004 R-AUTHZ-006 with deploy but not overwrite, a second PUT of different bytes answers 403 and the first content is still served", func(t *testing.T) {
		before := a.obj.puts() + b.obj.puts()
		res := b.put("ci", p, string(second))
		if res.status != 403 || !strings.Contains(res.json(t)["detail"].(string), "overwrite") {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if a.obj.puts()+b.obj.puts() != before {
			t.Error("the refused upload reached the object store")
		}
		if got := a.get("reader", p); !bytes.Equal(got.body, first) {
			t.Fatal("the first content is not served any more")
		}
		if res := a.put("ci", p, string(first)); res.status != 200 {
			t.Fatalf("an identical re-upload needs deploy only: %d", res.status)
		}
	})
	t.Run("R-AUTHZ-006 deploy and overwrite replace the content, answer 201 and keep the creator", func(t *testing.T) {
		res := b.put("maint", p, string(second))
		if res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		m := res.json(t)
		if m["digests"].(map[string]any)["sha256"] != sum(second) || m["created"].(map[string]any)["by"] != "user:ci" || m["modified"].(map[string]any)["by"] != "user:maint" {
			t.Errorf("item = %s", res.body)
		}
		if got := a.get("reader", p); !bytes.Equal(got.body, second) {
			t.Fatal("the new content is not served")
		}
		if n := te.number("SELECT count(*) FROM items WHERE path = 'app/1.0/app.tgz'"); n != 1 {
			t.Errorf("%d items", n)
		}
		deployed := a.rec.ofType("artifact.deployed")
		deployed = append(deployed, b.rec.ofType("artifact.deployed")...)
		var overwritten int
		for _, ev := range deployed {
			if ev.Path == "app/1.0/app.tgz" && strings.Contains(ev.Details, `"overwritten":true`) {
				overwritten++
			}
		}
		if overwritten != 1 {
			t.Errorf("%d overwriting artifact.deployed events", overwritten)
		}
	})
	t.Run("R-AUTHZ-006 a caller who only reads cannot deploy", func(t *testing.T) {
		problemOf(t, a.put("reader", "/files/generic-local/r/x", "x"), 403, "forbidden")
		problemOf(t, a.put("nobody", "/files/generic-local/r/x", "x"), 403, "forbidden")
		problemOf(t, a.put("", "/files/generic-local/r/x", "x"), 401, "credentials_required")
	})
}

func TestR_GENERIC_002_Paths(t *testing.T) {
	te := newWebTransferEnv(t)
	a := te.a
	base := "/files/generic-local/"

	t.Run("R-GENERIC-002 invalid paths answer 400 invalid_path before authorization", func(t *testing.T) {
		for _, p := range []string{"a/%2e%2e/x", "a//b", "a%00b", "a%5Cb", "a/./b", "a/../b", "%2e%2e/x", "a%2F%2Fb"} {
			for _, user := range []string{"ci", "nobody"} { // authorization does not run first
				problemOf(t, a.put(user, base+p, "x"), 400, "invalid_path")
			}
			// An anonymous caller never reaches the handler while anonymous access is off:
			// the authentication stage of the chain refuses it first (R-AUTH-037).
			problemOf(t, a.put("", base+p, "x"), 401, "credentials_required")
			problemOf(t, a.get("reader", base+p), 400, "invalid_path")
			problemOf(t, a.do("maint", "DELETE", base+p, nil), 400, "invalid_path")
		}
		long := strings.Repeat("a", 256)
		problemOf(t, a.put("ci", base+long, "x"), 400, "invalid_path")
	})
	t.Run("R-GENERIC-002 folders arise implicitly from the paths of files and are listed", func(t *testing.T) {
		if res := a.put("ci", base+"a/b/c/d.bin", "d"); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		m := a.get("reader", "/api/v1/repositories/generic-local/children?path=a&folders=true&recursive=true").json(t)
		var got []string
		for _, it := range m["items"].([]any) {
			x := it.(map[string]any)
			got = append(got, x["kind"].(string)+":"+x["path"].(string))
		}
		if fmt.Sprint(got) != "[folder:b folder:b/c file:b/c/d.bin]" {
			t.Errorf("children = %v", got)
		}
		it := a.get("reader", "/api/v1/repositories/generic-local/items?path=a/b").json(t)
		if it["kind"] != "folder" || it["explicit"] != false {
			t.Errorf("implicit folder = %v", it)
		}
	})
	t.Run("R-GENERIC-002 Report.pdf and report.pdf are two items, and Unicode is not normalised", func(t *testing.T) {
		for _, n := range []string{"Report.pdf", "report.pdf", "caf%C3%A9.txt", "cafe%CC%81.txt"} {
			if res := a.put("ci", base+"names/"+n, n); res.status != 201 {
				t.Errorf("%s: %d %s", n, res.status, res.body)
			}
		}
		if n := te.number("SELECT count(*) FROM items WHERE parent_path = 'names'"); n != 4 {
			t.Errorf("%d items", n)
		}
	})
	t.Run("R-GENERIC-002 R-STOR-007 a path names a file or a folder, never both: 409 path_conflict", func(t *testing.T) {
		if res := a.put("ci", base+"pc/file", "x"); res.status != 201 {
			t.Fatal(res.status)
		}
		problemOf(t, a.put("ci", base+"pc/file/below", "x"), 409, "path_conflict")
		problemOf(t, a.put("ci", base+"pc/file/", ""), 409, "path_conflict")
		if res := a.put("ci", base+"pc/dir/inside", "x"); res.status != 201 {
			t.Fatal(res.status)
		}
		problemOf(t, a.put("ci", base+"pc/dir", "x"), 409, "path_conflict")
		if n := te.number("SELECT count(*) FROM items WHERE path LIKE 'pc/%'"); n != 3 {
			t.Errorf("%d items under pc (a refused deploy must leave no folder behind)", n)
		}
	})
	t.Run("R-STOR-007 a PUT with a trailing slash creates an explicit folder: 201, then 200, and it stays when empty", func(t *testing.T) {
		res := a.put("ci", base+"empty-folder/", "")
		if res.status != 201 || res.json(t)["kind"] != "folder" || res.json(t)["explicit"] != true {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if again := a.put("ci", base+"empty-folder/", ""); again.status != 200 {
			t.Fatalf("%d", again.status)
		}
		problemOf(t, a.put("ci", base+"empty-folder/", "payload"), 400, "malformed_request")
		list := a.get("reader", base+"")
		if list.status != 200 || !strings.Contains(list.text(), `"path":"empty-folder"`) {
			t.Fatalf("%d %s", list.status, list.body)
		}
		m := a.get("reader", "/api/v1/repositories/generic-local/children?path=empty-folder").json(t)
		if len(m["items"].([]any)) != 0 {
			t.Errorf("children = %v", m["items"])
		}
		if res := a.do("maint", "DELETE", base+"pc/dir/inside", nil); res.status != 204 {
			t.Fatalf("%d", res.status)
		}
		if n := te.number("SELECT count(*) FROM items WHERE path = 'pc/dir'"); n != 0 {
			t.Error("an implicit folder stays after its last child went")
		}
		if n := te.number("SELECT count(*) FROM items WHERE path = 'empty-folder'"); n != 1 {
			t.Error("an explicit folder went away")
		}
	})
}

func TestR_STOR_017_DirectoryIndex(t *testing.T) {
	te := newWebTransferEnv(t)
	a := te.a
	for _, n := range []string{"<script>.txt", "javascript:alert(1)", "plain.txt"} {
		if res := a.put("ci", "/files/generic-local/tools/"+escape(n), "x"); res.status != 201 {
			t.Fatalf("%s: %d %s", n, res.status, res.body)
		}
	}
	t.Run("R-STOR-017 an HTML index escapes every name and links each as ./ plus the encoded name", func(t *testing.T) {
		res := a.get("reader", "/files/generic-local/tools/", "Accept", "text/html")
		page := res.text()
		if res.status != 200 || !strings.HasPrefix(res.hdr("Content-Type"), "text/html") {
			t.Fatalf("%d %v", res.status, res.header)
		}
		if strings.Contains(page, "<script>") || !strings.Contains(page, "&lt;script&gt;.txt") || !strings.Contains(page, `href="./javascript%3Aalert(1)"`) {
			t.Fatalf("index:\n%s", page)
		}
		h := res.header
		if h.Get("Content-Security-Policy") != "default-src 'none'" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cross-Origin-Resource-Policy") != "same-site" || h.Get("Content-Disposition") != "" {
			t.Errorf("headers of the index: %v", h)
		}
	})
	t.Run("R-STOR-017 the JSON index has the same entries; a folder without its slash is redirected", func(t *testing.T) {
		res := a.get("reader", "/files/generic-local/tools/")
		if res.status != 200 || len(res.json(t)["items"].([]any)) != 3 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		redirect := a.get("reader", "/files/generic-local/tools")
		if redirect.status != 301 || redirect.hdr("Location") != "/files/generic-local/tools/" {
			t.Fatalf("%d %v", redirect.status, redirect.header)
		}
		root := a.get("reader", "/files/generic-local/")
		if root.status != 200 || !strings.Contains(root.text(), `"path":"tools"`) {
			t.Fatalf("root: %d %s", root.status, root.body)
		}
	})
	t.Run("R-STOR-017 the index is paged with a cursor", func(t *testing.T) {
		var names []string
		url := "/files/generic-local/tools/?limit=1"
		for range 10 {
			m := a.get("reader", url).json(t)
			for _, it := range m["items"].([]any) {
				names = append(names, it.(map[string]any)["path"].(string))
			}
			next, _ := m["nextCursor"].(string)
			if next == "" {
				break
			}
			url = "/files/generic-local/tools/?limit=1&cursor=" + next
		}
		if len(names) != 3 {
			t.Fatalf("walk = %v", names)
		}
	})
}

func escape(name string) string {
	var b strings.Builder
	for _, c := range []byte(name) {
		if c == '<' || c == '>' {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
