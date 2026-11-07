//go:build integration

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The user avatar of R-API-055 on the running composition root: the role api, the real blob store, the real chain and the real
// authentication stage.

var avatarURLPattern = regexp.MustCompile(`^/api/v1/avatars/[0-9a-f]{64}$`)

// avatarImage is a w x h image, blue with a red corner.
func avatarImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/3 && y < h/2 {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{B: 255, A: 255})
			}
		}
	}
	return img
}

func avatarPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, avatarImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func avatarJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, avatarImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngDeclaring is a PNG header that declares w x h pixels and holds no image data.
func pngDeclaring(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	c := binary.BigEndian.AppendUint32(nil, uint32(len(ihdr)))
	c = append(c, "IHDR"...)
	c = append(c, ihdr...)
	c = binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
	return append([]byte("\x89PNG\r\n\x1a\n"), c...)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) putAvatar(r *testenv.Replica, token, mediaType string, body []byte, extra ...string) testenv.Answer {
	e.t.Helper()
	hdr := append([]string{"Content-Type", mediaType}, extra...)
	return e.stack.Do(r, "PUT", "/api/v1/users/me/avatar", token, body, hdr...)
}

func avatarURLOf(t *testing.T, a testenv.Answer) string {
	t.Helper()
	url, _ := a.JSON(t)["avatarUrl"].(string)
	return url
}

func TestR_API_055_AvatarLifecycle(t *testing.T) {
	e := newEnv(t, true)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "avatar-api", Role: "api"})
	count := func(sql string, args ...any) int64 { return e.stack.Number(sql, args...) }

	t.Run("R-API-055 a user without an avatar has a null avatarUrl", func(t *testing.T) {
		me := e.stack.Do(r, "GET", "/api/v1/users/me", e.alice.token, nil)
		if me.Status != http.StatusOK {
			t.Fatalf("%d %s", me.Status, me.Body)
		}
		if v, present := me.JSON(t)["avatarUrl"]; !present || v != nil {
			t.Fatalf("avatarUrl = %v (present %v), want null", v, present)
		}
	})

	var first string
	t.Run("R-API-055 an upload is re-encoded and named by its digest", func(t *testing.T) {
		put := e.putAvatar(r, e.alice.token, "image/png", avatarPNG(t, 96, 64))
		if put.Status != http.StatusOK {
			t.Fatalf("%d %s", put.Status, put.Body)
		}
		first = avatarURLOf(t, put)
		if !avatarURLPattern.MatchString(first) {
			t.Fatalf("avatarUrl = %q", first)
		}
		for _, who := range []struct{ name, token string }{{"the owner", e.alice.token}, {"another user", e.bob.token}, {"an administrator", e.admin.token}} {
			got := e.stack.Do(r, "GET", first, who.token, nil)
			if got.Status != http.StatusOK {
				t.Fatalf("%s reads the avatar: %d %s", who.name, got.Status, got.Body)
			}
			want := map[string]string{
				"Content-Type":            "image/png",
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "default-src 'none'; sandbox",
				"Content-Disposition":     `inline; filename="avatar.png"`,
				"Cache-Control":           "private, max-age=31536000, immutable",
			}
			for k, v := range want {
				if got.Header.Get(k) != v {
					t.Errorf("%s: %s = %q, want %q", who.name, k, got.Header.Get(k), v)
				}
			}
			sum := sha256.Sum256(got.Body)
			if hex.EncodeToString(sum[:]) != strings.TrimPrefix(first, "/api/v1/avatars/") || got.Header.Get("ETag") != `"`+hex.EncodeToString(sum[:])+`"` {
				t.Errorf("%s: the body is not the digest's, ETag %q", who.name, got.Header.Get("ETag"))
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(got.Body))
			if err != nil || cfg.Width != 256 || cfg.Height != 256 {
				t.Errorf("%s: the stored image is %dx%d (%v), want 256x256", who.name, cfg.Width, cfg.Height, err)
			}
		}
		again := e.stack.Do(r, "GET", first, e.bob.token, nil, "If-None-Match", `"`+strings.TrimPrefix(first, "/api/v1/avatars/")+`"`)
		if again.Status != http.StatusNotModified {
			t.Errorf("revalidation: %d, want 304", again.Status)
		}
		if n := count("SELECT count(*) FROM trove.audit_event WHERE action = 'user.avatar.set' AND resource_id = $1", e.alice.ID); n != 1 {
			t.Errorf("%d user.avatar.set events, want 1", n)
		}
	})

	t.Run("R-API-055 the user records of the owner and of others carry the URL", func(t *testing.T) {
		me := e.stack.Do(r, "GET", "/api/v1/users/me", e.alice.token, nil)
		if got := avatarURLOf(t, me); got != first {
			t.Errorf("users/me: %q, want %q", got, first)
		}
		one := e.stack.Do(r, "GET", "/api/v1/users/alice", e.admin.token, nil)
		if got := avatarURLOf(t, one); got != first {
			t.Errorf("users/alice: %q, want %q", got, first)
		}
		list := e.stack.Do(r, "GET", "/api/v1/users?q=bob", e.admin.token, nil)
		if list.Status != http.StatusOK || !strings.Contains(string(list.Body), `"avatarUrl":null`) {
			t.Errorf("the list shows bob without an avatar: %d %s", list.Status, list.Body)
		}
		tagBefore := one.Header.Get("ETag")
		if tagBefore == "" {
			t.Error("the user has no ETag")
		}
	})

	t.Run("R-API-055 the same image again changes nothing", func(t *testing.T) {
		put := e.putAvatar(r, e.alice.token, "image/png", avatarPNG(t, 96, 64))
		if put.Status != http.StatusOK || avatarURLOf(t, put) != first {
			t.Fatalf("%d %s", put.Status, put.Body)
		}
		if n := count("SELECT count(*) FROM trove.audit_event WHERE action = 'user.avatar.set' AND resource_id = $1", e.alice.ID); n != 1 {
			t.Errorf("%d user.avatar.set events after the repeat, want 1", n)
		}
	})

	var second string
	t.Run("R-API-055 a replacement gets a new URL and the old image is no longer served", func(t *testing.T) {
		tagBefore := e.stack.Do(r, "GET", "/api/v1/users/alice", e.admin.token, nil).Header.Get("ETag")
		put := e.putAvatar(r, e.alice.token, "image/jpeg", avatarJPEG(t, 120, 90))
		if put.Status != http.StatusOK {
			t.Fatalf("%d %s", put.Status, put.Body)
		}
		second = avatarURLOf(t, put)
		if second == first || !avatarURLPattern.MatchString(second) {
			t.Fatalf("second URL %q, first %q", second, first)
		}
		if got := e.stack.Do(r, "GET", second, e.bob.token, nil); got.Status != http.StatusOK {
			t.Errorf("the new image: %d", got.Status)
		}
		old := e.stack.Do(r, "GET", first, e.bob.token, nil)
		if old.Status != http.StatusNotFound {
			t.Errorf("the replaced image: %d, want 404", old.Status)
		}
		if n := count("SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = decode($1, 'hex')", strings.TrimPrefix(first, "/api/v1/avatars/")); n != 1 {
			t.Errorf("the replaced blob is queued for collection %d times, want 1", n)
		}
		if tagAfter := e.stack.Do(r, "GET", "/api/v1/users/alice", e.admin.token, nil).Header.Get("ETag"); tagAfter == tagBefore {
			t.Errorf("the ETag of the user stayed %s although its avatarUrl changed", tagAfter)
		}
	})

	t.Run("R-API-055 only an administrator removes another user's avatar", func(t *testing.T) {
		for name, token := range map[string]string{"another user": e.bob.token, "a reader": e.reader.token} {
			if got := e.stack.Do(r, "DELETE", "/api/v1/users/alice/avatar", token, nil); got.Status != http.StatusForbidden {
				t.Errorf("%s: %d %s, want 403", name, got.Status, got.Body)
			}
		}
		if got := e.stack.Do(r, "GET", second, e.bob.token, nil); got.Status != http.StatusOK {
			t.Fatalf("the avatar is gone after refused deletes: %d", got.Status)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/nobody/avatar", e.admin.token, nil); got.Status != http.StatusNotFound {
			t.Errorf("an unknown user: %d, want 404", got.Status)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/alice/avatar", e.admin.token, nil); got.Status != http.StatusNoContent {
			t.Fatalf("the administrator: %d %s", got.Status, got.Body)
		}
		if got := e.stack.Do(r, "GET", second, e.bob.token, nil); got.Status != http.StatusNotFound {
			t.Errorf("after the removal: %d, want 404", got.Status)
		}
		if v := e.stack.Do(r, "GET", "/api/v1/users/me", e.alice.token, nil).JSON(t)["avatarUrl"]; v != nil {
			t.Errorf("avatarUrl after the removal: %v", v)
		}
		if n := count("SELECT count(*) FROM trove.audit_event WHERE action = 'user.avatar.removed' AND resource_id = $1 AND details->>'source' = 'admin'", e.alice.ID); n != 1 {
			t.Errorf("%d user.avatar.removed events by the administrator, want 1", n)
		}
	})

	t.Run("R-API-055 a user removes their own avatar, also when there is none", func(t *testing.T) {
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/avatar", e.carol.token, nil); got.Status != http.StatusNoContent {
			t.Fatalf("no avatar: %d %s", got.Status, got.Body)
		}
		if put := e.putAvatar(r, e.carol.token, "image/webp", mustRead(t, "../../../internal/avatar/testdata/lossy.webp")); put.Status != http.StatusOK {
			t.Fatalf("WebP: %d %s", put.Status, put.Body)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/avatar", e.carol.token, nil); got.Status != http.StatusNoContent {
			t.Fatalf("own removal: %d", got.Status)
		}
		if n := count("SELECT count(*) FROM trove.audit_event WHERE action = 'user.avatar.removed' AND resource_id = $1 AND details->>'source' = 'self'", e.carol.ID); n != 1 {
			t.Errorf("%d self-removal events, want 1", n)
		}
	})

	t.Run("R-API-055 the digest of a blob that is no avatar is not served", func(t *testing.T) {
		for _, d := range []string{strings.Repeat("0", 64), "abc", strings.Repeat("A", 64)} {
			if got := e.stack.Do(r, "GET", "/api/v1/avatars/"+d, e.bob.token, nil); got.Status != http.StatusNotFound && got.Status != http.StatusBadRequest {
				t.Errorf("%q: %d, want 404", d, got.Status)
			}
		}
		if got := e.stack.Do(r, "GET", "/api/v1/avatars/"+strings.Repeat("0", 64), "", nil); got.Status != http.StatusUnauthorized {
			t.Errorf("without a credential: %d, want 401", got.Status)
		}
	})

	t.Run("R-API-055 the deletion of a user removes the avatar and queues the blob", func(t *testing.T) {
		dave := e.stack.User("dave")
		tok := e.stack.Token(dave, scope.Default())
		put := e.putAvatar(r, tok, "image/png", avatarPNG(t, 64, 64))
		if put.Status != http.StatusOK {
			t.Fatalf("%d %s", put.Status, put.Body)
		}
		digest := strings.TrimPrefix(avatarURLOf(t, put), "/api/v1/avatars/")
		if n := count("SELECT count(*) FROM trove.user_avatars WHERE user_id = $1::uuid", dave.ID); n != 1 {
			t.Fatalf("%d avatar rows, want 1", n)
		}
		if del := e.stack.Do(r, "DELETE", "/api/v1/users/dave", e.admin.token, nil); del.Status != http.StatusNoContent {
			t.Fatalf("delete user: %d %s", del.Status, del.Body)
		}
		if n := count("SELECT count(*) FROM trove.user_avatars WHERE user_id = $1::uuid", dave.ID); n != 0 {
			t.Errorf("%d avatar rows after the user was deleted, want 0", n)
		}
		if n := count("SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = decode($1, 'hex')", digest); n != 1 {
			t.Errorf("the blob is queued for collection %d times, want 1", n)
		}
		if got := e.stack.Do(r, "GET", "/api/v1/avatars/"+digest, e.bob.token, nil); got.Status != http.StatusNotFound {
			t.Errorf("the deleted user's image: %d, want 404", got.Status)
		}
	})
}

func TestR_API_055_AvatarRefusals(t *testing.T) {
	e := newEnv(t, true)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "avatar-refuse", Role: "api"})
	valid := avatarPNG(t, 64, 64)
	truncated := valid[:len(valid)/2]
	over := append(append([]byte{}, valid...), make([]byte, 1<<20+1-len(valid))...)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	cases := []struct {
		name, mediaType string
		body            []byte
		status          int
		code            string
	}{
		{"a body of 1 MiB + 1 byte", "image/png", over, http.StatusRequestEntityTooLarge, "payload_too_large"},
		{"SVG declared as PNG", "image/png", svg, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"SVG declared as SVG", "image/svg+xml", svg, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"HTML declared as HTML", "text/html", []byte("<html></html>"), http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"JSON", "application/json", []byte(`{"a":1}`), http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"a JPEG declared as PNG", "image/png", avatarJPEG(t, 64, 64), http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"a GIF declared as PNG", "image/png", append([]byte("GIF89a\x40\x00\x40\x00\x00\x00\x00"), make([]byte, 40)...), http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"a bomb: 100,000 x 100,000 pixels in a header", "image/png", pngDeclaring(100000, 100000), http.StatusUnprocessableEntity, "validation_failed"},
		{"a PNG of 20 x 20 pixels", "image/png", avatarPNG(t, 20, 20), http.StatusUnprocessableEntity, "validation_failed"},
		{"a truncated PNG", "image/png", truncated, http.StatusUnprocessableEntity, "validation_failed"},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			got := e.putAvatar(r, e.alice.token, c.mediaType, c.body)
			if got.Status != c.status || got.Code(t) != c.code {
				t.Fatalf("%d %s, want %d %s", got.Status, got.Body, c.status, c.code)
			}
			if got.Status == http.StatusUnprocessableEntity && !strings.Contains(string(got.Body), `"errors"`) {
				t.Errorf("a 422 without errors[]: %s", got.Body)
			}
		})
	}
	t.Run("R-API-055 an empty body", func(t *testing.T) {
		if got := e.stack.Do(r, "PUT", "/api/v1/users/me/avatar", e.alice.token, []byte{}, "Content-Type", "image/png"); got.Status != http.StatusBadRequest {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
	})
	t.Run("R-API-055 a request without a credential", func(t *testing.T) {
		if got := e.putAvatar(r, "", "image/png", valid); got.Status != http.StatusUnauthorized {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
	})
	t.Run("R-API-055 a service account has no avatar", func(t *testing.T) {
		sa := e.stack.ServiceAccount("ci-bot")
		tok := e.stack.Token(sa, scope.Default())
		if got := e.putAvatar(r, tok, "image/png", valid); got.Status != http.StatusForbidden {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/avatar", tok, nil); got.Status != http.StatusForbidden {
			t.Fatalf("delete: %d %s", got.Status, got.Body)
		}
	})
	t.Run("R-API-055 nothing was stored by the refused uploads", func(t *testing.T) {
		if n := e.stack.Number("SELECT count(*) FROM trove.user_avatars"); n != 0 {
			t.Fatalf("%d avatar rows", n)
		}
	})
}

func TestR_API_055_AvatarWritesNeedTheCSRFHeaderOfASession(t *testing.T) {
	const password = "correct horse battery staple 1"
	e := newEnv(t, true)
	file := testenv.WriteFile(t, t.TempDir(), "break-glass", password)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "avatar-csrf", Role: "api", Env: []string{
		"TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE=" + file,
	}})
	const origin = "http://trove.test"
	login := e.stack.Do(r, "POST", "/auth/local/login", "", map[string]string{"username": "admin", "password": password}, "Origin", origin)
	if login.Status != http.StatusNoContent {
		t.Fatalf("local login: %d %s", login.Status, login.Body)
	}
	var cookie, csrf string
	for _, c := range (&http.Response{Header: login.Header}).Cookies() {
		switch c.Name {
		case session.CookieSession:
			cookie = c.Name + "=" + c.Value
		case session.CookieCSRF:
			csrf = c.Value
		}
	}
	valid := avatarPNG(t, 64, 64)
	t.Run("R-API-055 a session write without the CSRF header is refused", func(t *testing.T) {
		got := e.putAvatar(r, "", "image/png", valid, "Cookie", cookie, "Origin", origin)
		if got.Status != http.StatusForbidden || got.Code(t) != "csrf_failed" {
			t.Fatalf("PUT: %d %s", got.Status, got.Body)
		}
		got = e.stack.Do(r, "DELETE", "/api/v1/users/me/avatar", "", nil, "Cookie", cookie, "Origin", origin)
		if got.Status != http.StatusForbidden || got.Code(t) != "csrf_failed" {
			t.Fatalf("DELETE: %d %s", got.Status, got.Body)
		}
	})
	t.Run("R-API-055 with the header the session uploads, and the image is a same-origin resource of the session", func(t *testing.T) {
		got := e.putAvatar(r, "", "image/png", valid, "Cookie", cookie, "Origin", origin, "X-CSRF-Token", csrf)
		if got.Status != http.StatusOK {
			t.Fatalf("PUT: %d %s", got.Status, got.Body)
		}
		url := avatarURLOf(t, got)
		img := e.stack.Do(r, "GET", url, "", nil, "Cookie", cookie)
		if img.Status != http.StatusOK || img.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("GET with the session cookie only: %d %v", img.Status, img.Header)
		}
		if del := e.stack.Do(r, "DELETE", "/api/v1/users/me/avatar", "", nil, "Cookie", cookie, "Origin", origin, "X-CSRF-Token", csrf); del.Status != http.StatusNoContent {
			t.Fatalf("DELETE: %d %s", del.Status, del.Body)
		}
	})
}
