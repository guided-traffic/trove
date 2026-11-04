//go:build integration

package files

import (
	"strings"
	"testing"
)

// TestContentRoutes is R-API-025 and Q-UI-106 against the real stores: the UI's content routes serve what /files/
// serves, for the session cookie only, and decide exactly like the routes they mirror.
func TestContentRoutes(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	const content = "/api/v1/repositories/generic-local/content/"
	session := []string{sessionMarker, "1"}

	t.Run("R-API-025 Q-UI-106 an upload through the content route is the file /files/ serves, on another replica too", func(t *testing.T) {
		res := a.do("ci", "PUT", content+"ui/a%20b.txt", strings.NewReader("hello"), session...)
		if res.status != 201 {
			t.Fatalf("upload: %d %s", res.status, res.body)
		}
		item := res.json(t)
		if item["contentUrl"] != "/api/v1/repositories/generic-local/content/ui/a%20b.txt" {
			t.Errorf("contentUrl = %v", item["contentUrl"])
		}
		for _, r := range []*replica{a, b} {
			viaFiles := r.get("reader", "/files/generic-local/ui/a%20b.txt")
			viaContent := r.do("reader", "GET", content+"ui/a%20b.txt", nil, session...)
			if viaFiles.status != 200 || viaContent.status != 200 || viaContent.text() != "hello" || viaFiles.text() != "hello" {
				t.Fatalf("files %d %q, content %d %q", viaFiles.status, viaFiles.text(), viaContent.status, viaContent.text())
			}
			for _, k := range []string{"Etag", "Repr-Digest", "Accept-Ranges", "Content-Disposition", "X-Content-Type-Options"} {
				if viaFiles.header.Get(k) != viaContent.header.Get(k) {
					t.Errorf("%s: files %q, content %q", k, viaFiles.header.Get(k), viaContent.header.Get(k))
				}
			}
		}
		head := b.do("reader", "HEAD", content+"ui/a%20b.txt", nil, session...)
		if head.status != 200 || len(head.body) != 0 {
			t.Errorf("HEAD: %d %q", head.status, head.body)
		}
		part := b.do("reader", "GET", content+"ui/a%20b.txt", nil, append([]string{"Range", "bytes=1-3"}, session...)...)
		if part.status != 206 || part.text() != "ell" {
			t.Errorf("range: %d %q", part.status, part.body)
		}
	})

	t.Run("R-API-025 Q-UI-106 a token is answered like an unknown path, and so is a request without a credential", func(t *testing.T) {
		for _, method := range []string{"GET", "HEAD"} {
			if res := a.do("ci", method, content+"ui/a%20b.txt", nil); res.status != 404 {
				t.Errorf("%s with a token: %d %s", method, res.status, res.body)
			}
		}
		if res := a.do("ci", "PUT", content+"ui/by-token.txt", strings.NewReader("x")); res.status != 404 {
			t.Errorf("PUT with a token: %d %s", res.status, res.body)
		}
		if res := a.get("ci", "/files/generic-local/ui/by-token.txt"); res.status != 404 {
			t.Errorf("a token upload through the content route stored a file: %d", res.status)
		}
		if res := a.do("", "GET", content+"ui/a%20b.txt", nil); res.status != 401 && res.status != 404 {
			t.Errorf("anonymous: %d", res.status)
		}
	})

	t.Run("R-API-025 R-AUTHZ-016 a caller without read gets the answer /files/ gives", func(t *testing.T) {
		viaFiles := a.get("nobody", "/files/generic-local/ui/a%20b.txt")
		viaContent := a.do("nobody", "GET", content+"ui/a%20b.txt", nil, session...)
		if viaContent.status != viaFiles.status || viaContent.status < 400 || strings.Contains(viaContent.text(), "hello") {
			t.Fatalf("files %d %s, content %d %s", viaFiles.status, viaFiles.body, viaContent.status, viaContent.body)
		}
		missing := a.do("reader", "GET", content+"ui/missing.txt", nil, session...)
		if missing.status != 404 {
			t.Errorf("a path that is not stored: %d", missing.status)
		}
	})

	t.Run("R-API-025 R-AUTHZ-016 a reader may not deploy through the content route either", func(t *testing.T) {
		viaFiles := a.put("reader", "/files/generic-local/ui/denied.txt", "x")
		viaContent := a.do("reader", "PUT", content+"ui/denied.txt", strings.NewReader("x"), session...)
		if viaContent.status != viaFiles.status || viaContent.status < 400 {
			t.Fatalf("files %d, content %d %s", viaFiles.status, viaContent.status, viaContent.body)
		}
		if res := a.get("reader", "/files/generic-local/ui/denied.txt"); res.status != 404 {
			t.Errorf("a refused upload stored a file: %d", res.status)
		}
	})

	t.Run("R-API-025 Q-UI-106 an upload above ui.deploy.max_upload_bytes answers 413 and stores nothing", func(t *testing.T) {
		big := strings.Repeat("x", contentLimit+1)
		res := a.do("ci", "PUT", content+"ui/big.bin", strings.NewReader(big), session...)
		if res.status != 413 {
			t.Fatalf("status %d: %.200s", res.status, res.body)
		}
		if got := a.get("reader", "/files/generic-local/ui/big.bin"); got.status != 404 {
			t.Errorf("the refused upload is stored: %d", got.status)
		}
		// /files/ has no limit of its own below the repository's.
		if res := a.put("ci", "/files/generic-local/ui/big-files.bin", big); res.status != 201 {
			t.Errorf("/files/ upload of the same size: %d", res.status)
		}
		ok := a.do("ci", "PUT", content+"ui/fits.bin", strings.NewReader(strings.Repeat("y", contentLimit)), session...)
		if ok.status != 201 {
			t.Errorf("an upload at the limit: %d %s", ok.status, ok.body)
		}
	})

	t.Run("R-API-025 the item description of a file carries contentUrl next to downloadUrl", func(t *testing.T) {
		res := a.do("reader", "GET", "/api/v1/repositories/generic-local/items?path=ui%2Fa%20b.txt", nil)
		if res.status != 200 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		item := res.json(t)
		if item["contentUrl"] != "/api/v1/repositories/generic-local/content/ui/a%20b.txt" || item["downloadUrl"] == nil {
			t.Errorf("item = %v", item)
		}
	})
}
