package webui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// fakeShared is a shared asset store of files by "<locale>/<path>" that counts its reads.
type fakeShared struct {
	files map[string][]byte
	err   error         // returned for every read when set
	gate  chan struct{} // a read waits for it when set
	calls atomic.Int64
	mu    sync.Mutex
	asked []string
}

func (f *fakeShared) Asset(ctx context.Context, locale, path string) ([]byte, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, locale+"/"+path)
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if b, ok := f.files[locale+"/"+path]; ok {
		return b, nil
	}
	return nil, ErrNotShared
}

func TestIsHashed(t *testing.T) {
	t.Run("R-UI-001 the names of the production build are hashed", func(t *testing.T) {
		// the names of a real build of web/ (2026-11-06): esbuild's base32 hashes and the chunk optimizer's base64url ones
		for _, p := range []string{
			"main-6E6OBYEH.js", "polyfills-EI5HTWPC.js", "styles-DYCJMKF7.css", "media/material-icons-LEZCGFVT.woff2",
			"chunk-Bo__WLzq.js", "chunk-b3RHWPW-.js", "chunk-2Di7xEzG.js", "chunk-BLBWX6NU.js",
		} {
			if !IsHashed(p) {
				t.Errorf("IsHashed(%q) = false", p)
			}
		}
	})
	t.Run("R-UI-001 nothing else is hashed", func(t *testing.T) {
		for _, p := range []string{
			"", "index.html", "favicon.ico", "main.js", "main-ABCDEFG.js", "main-ABCDEFGHI.js", "main-ABCD1234",
			"../main-ABCD1234.js", "./main-ABCD1234.js", "media/../main-ABCD1234.js", "a/b/c/main-ABCD1234.js",
			".main-ABCD1234.js", "-ABCD1234.js", "media//font-ABCD1234.woff2", "me.dia/font-ABCD1234.woff2",
			"main-ABCD1234.verylongext", "main-ABCD 234.js", "main-ABCD1234.js/", strings.Repeat("a", 250) + "-ABCD1234.js",
		} {
			if IsHashed(p) {
				t.Errorf("IsHashed(%q) = true", p)
			}
		}
	})
}

func TestHashedFiles(t *testing.T) {
	t.Run("R-HA-020 the hashed files of every locale, by locale and path, without index.html and the unhashed copies", func(t *testing.T) {
		got, err := HashedFiles(builtFS(), []string{"en", "de"})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range got {
			names = append(names, f.Name())
		}
		want := []string{
			"de/main-ABCD1234.js", "de/media/font-ABCD1234.woff2", "de/styles-ABCD1234.css",
			"en/main-ABCD1234.js", "en/media/font-ABCD1234.woff2", "en/styles-ABCD1234.css",
		}
		if strings.Join(names, " ") != strings.Join(want, " ") {
			t.Fatalf("hashed files\n got %v\nwant %v", names, want)
		}
		if got[0].Size != int64(len("console.log(1)")) || got[0].Locale != "de" || got[0].Path != "main-ABCD1234.js" {
			t.Errorf("first file %+v", got[0])
		}
	})
	t.Run("R-HA-020 the placeholder tree has none", func(t *testing.T) {
		got, err := HashedFiles(fstest.MapFS{"index.html": builtFS()["index.html"]}, []string{"en", "de"})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("R-HA-020 a locale that is no locale code is refused", func(t *testing.T) {
		if _, err := HashedFiles(builtFS(), []string{"../x"}); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("R-HA-020 the embedded tree lists, with or without a web build", func(t *testing.T) {
		tree, err := Tree(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := HashedFiles(tree, []string{"en", "de"}); err != nil {
			t.Fatal(err)
		}
	})
}

// otherRelease is the store of a release whose bundle has other names: its main script, a lazy chunk and a font.
func otherRelease() *fakeShared {
	return &fakeShared{files: map[string][]byte{
		"en/main-OTHER123.js":              []byte("console.log('other en')"),
		"de/main-OTHER123.js":              []byte("console.log('other de')"),
		"en/chunk-b3RHWPW-.js":             []byte("export const c=1"),
		"en/media/font-OTHER123.woff2":     []byte("other font"),
		"en/main-ABCD1234.js":              []byte("the store's copy, never served while the bundle has the file"),
		"en/favicon-OTHER123.ico.disabled": []byte("x"),
	}}
}

func TestSharedAssets(t *testing.T) {
	t.Run("R-HA-020 a hashed file of another release is served from the store with the headers of the bundle's files", func(t *testing.T) {
		store := otherRelease()
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		cases := map[string]struct{ body, ctype string }{
			"/ui/en/main-OTHER123.js":          {"console.log('other en')", "text/javascript; charset=utf-8"},
			"/ui/de/main-OTHER123.js":          {"console.log('other de')", "text/javascript; charset=utf-8"},
			"/ui/en/chunk-b3RHWPW-.js":         {"export const c=1", "text/javascript; charset=utf-8"},
			"/ui/en/media/font-OTHER123.woff2": {"other font", "font/woff2"},
		}
		for p, want := range cases {
			rec := do(h, "GET", p)
			if rec.Code != 200 || rec.Body.String() != want.body {
				t.Errorf("%s: code %d body %q", p, rec.Code, rec.Body.String())
				continue
			}
			hd := rec.Header()
			if hd.Get("Content-Type") != want.ctype || hd.Get("Cache-Control") != "public, max-age=31536000, immutable" || hd.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s: headers %v", p, hd)
			}
		}
	})

	t.Run("R-HA-020 the replica's own file wins over the store's", func(t *testing.T) {
		store := otherRelease()
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		if rec := do(h, "GET", "/ui/en/main-ABCD1234.js"); rec.Body.String() != "console.log(1)" {
			t.Fatalf("body %q", rec.Body.String())
		}
		if n := store.calls.Load(); n != 0 {
			t.Fatalf("the store was asked %d times for a file of the bundle", n)
		}
	})

	t.Run("R-HA-020 a hashed name no release published is a 404", func(t *testing.T) {
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = otherRelease() })
		rec := do(h, "GET", "/ui/en/chunk-NOTTHERE.js")
		if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("code %d content type %q", rec.Code, rec.Header().Get("Content-Type"))
		}
	})

	t.Run("R-HA-020 the store is never asked for index.html, a route, an unhashed or an invalid path", func(t *testing.T) {
		store := otherRelease()
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		for _, p := range []string{
			"/ui/en/", "/ui/en/index.html", "/ui/en/admin/jobs", "/ui/en/favicon.ico", "/ui/en/missing.ico", "/ui/en/main.js",
			"/ui/en/favicon-OTHER123.ico.disabled", "/ui/en/%2e%2e/main-OTHER123.js", "/ui/en/a/b/c/main-OTHER123.js",
			"/ui/en/assets.d",
		} {
			do(h, "GET", p)
		}
		if n := store.calls.Load(); n != 0 {
			t.Fatalf("the store was asked %d times: %v", n, store.asked)
		}
	})

	t.Run("R-HA-020 the locale of the request is the locale the store is asked for", func(t *testing.T) {
		store := otherRelease()
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		if rec := do(h, "GET", "/ui/de/chunk-b3RHWPW-.js"); rec.Code != 404 {
			t.Fatalf("the en chunk was served under de: code %d", rec.Code)
		}
		if strings.Join(store.asked, ",") != "de/chunk-b3RHWPW-.js" {
			t.Fatalf("asked %v", store.asked)
		}
	})

	t.Run("R-HA-020 a store that cannot be asked answers 503 with Retry-After, not cached, and is reported", func(t *testing.T) {
		store := otherRelease()
		store.err = errors.New("object store unreachable")
		var reported []string
		h := newHandler(t, builtFS(), func(o *Options) {
			o.Shared = store
			o.OnSharedError = func(locale, path string, err error) { reported = append(reported, locale+"/"+path+": "+err.Error()) }
		})
		for range 2 {
			rec := do(h, "GET", "/ui/en/main-OTHER123.js")
			if rec.Code != 503 || rec.Header().Get("Retry-After") != "1" || rec.Header().Get("Cache-Control") != "no-store" ||
				rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("code %d headers %v", rec.Code, rec.Header())
			}
		}
		if n := store.calls.Load(); n != 2 {
			t.Errorf("a failed read was remembered: %d reads for two requests", n)
		}
		if len(reported) != 2 || !strings.Contains(reported[0], "en/main-OTHER123.js: object store unreachable") {
			t.Errorf("reported %v", reported)
		}
		// the replica's own files do not depend on the store
		if rec := do(h, "GET", "/ui/en/main-ABCD1234.js"); rec.Code != 200 {
			t.Errorf("own file: code %d", rec.Code)
		}
		if rec := do(h, "GET", "/ui/en/"); rec.Code != 200 {
			t.Errorf("index: code %d", rec.Code)
		}
	})

	t.Run("R-HA-020 a file read once is served from memory, a name the store did not know is remembered for its TTL", func(t *testing.T) {
		store := otherRelease()
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		now := time.Unix(1_800_000_000, 0)
		h.shared.now = func() time.Time { return now }
		for range 3 {
			do(h, "GET", "/ui/en/main-OTHER123.js")
			do(h, "GET", "/ui/en/chunk-NOTTHERE.js")
		}
		if n := store.calls.Load(); n != 2 {
			t.Fatalf("%d reads of the store for one file and one unknown name", n)
		}
		now = now.Add(sharedMissTTL + time.Second)
		store.files["en/chunk-NOTTHERE.js"] = []byte("published meanwhile")
		if rec := do(h, "GET", "/ui/en/chunk-NOTTHERE.js"); rec.Code != 200 || rec.Body.String() != "published meanwhile" {
			t.Fatalf("after the TTL: code %d body %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("R-HA-020 concurrent requests for one file read the store once", func(t *testing.T) {
		store := otherRelease()
		store.gate = make(chan struct{})
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = store })
		var wg sync.WaitGroup
		codes := make(chan int, 8)
		for range 8 {
			wg.Go(func() { codes <- do(h, "GET", "/ui/en/main-OTHER123.js").Code })
		}
		waitUntil(t, func() bool { return store.calls.Load() == 1 })
		time.Sleep(50 * time.Millisecond) // the other seven join the read in flight
		close(store.gate)
		wg.Wait()
		close(codes)
		for c := range codes {
			if c != 200 {
				t.Errorf("code %d", c)
			}
		}
		if n := store.calls.Load(); n != 1 {
			t.Fatalf("%d reads for eight concurrent requests", n)
		}
	})

	t.Run("R-HA-020 HEAD of a shared file sends its headers and no body", func(t *testing.T) {
		h := newHandler(t, builtFS(), func(o *Options) { o.Shared = otherRelease() })
		rec := do(h, "HEAD", "/ui/en/main-OTHER123.js")
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "23" {
			t.Fatalf("code %d body %d length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
		}
	})

	t.Run("R-HA-020 without a store a missing hashed file is a 404, as before", func(t *testing.T) {
		h := newHandler(t, builtFS(), nil)
		if rec := do(h, "GET", "/ui/en/main-OTHER123.js"); rec.Code != http.StatusNotFound {
			t.Fatalf("code %d", rec.Code)
		}
	})

	t.Run("R-HA-020 a binary without the web build serves the store's files too", func(t *testing.T) {
		h := newHandler(t, fstest.MapFS{"index.html": builtFS()["index.html"]}, func(o *Options) { o.Shared = otherRelease() })
		if rec := do(h, "GET", "/ui/en/main-OTHER123.js"); rec.Code != 200 {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

func TestSharedCacheBound(t *testing.T) {
	t.Run("R-HA-020 the files kept in memory stay within the bound, the oldest out first", func(t *testing.T) {
		c := newSharedCache(&fakeShared{})
		big := make([]byte, sharedCacheBytes/3+1)
		c.keep("en/a-AAAAAAAA.js", big)
		c.keep("en/b-BBBBBBBB.js", big)
		c.keep("en/c-CCCCCCCC.js", big)
		if _, ok := c.files["en/a-AAAAAAAA.js"]; ok || c.bytes > sharedCacheBytes || len(c.files) != 2 {
			t.Fatalf("kept %d files of %d bytes", len(c.files), c.bytes)
		}
		c.keep("en/huge-HHHHHHHH.js", make([]byte, sharedCacheBytes+1))
		if _, ok := c.files["en/huge-HHHHHHHH.js"]; ok {
			t.Fatal("a file larger than the bound was kept")
		}
	})
}

// TestHashedPatternOfTheBuild keeps the pattern of web/scripts/hashed.mjs, which build:embed checks every build against, equal
// to the server's: a build whose names the server would not take as hashed fails there, not in a rolling upgrade.
func TestHashedPatternOfTheBuild(t *testing.T) {
	t.Run("R-UI-001 build:embed checks the build against the pattern the server serves by (Q-UI-140)", func(t *testing.T) {
		src, err := os.ReadFile("../../web/scripts/hashed.mjs")
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{
			"HASHED_NAME":      "/" + hashedName.String() + "/",
			"HASHED_DIR":       "/" + hashedDir.String() + "/",
			"MAX_HASHED_DEPTH": strconv.Itoa(maxHashedDepth),
		} {
			m := regexp.MustCompile(`(?m)^export const ` + name + ` = (.+);$`).FindSubmatch(src)
			if m == nil || string(m[1]) != want {
				t.Errorf("web/scripts/hashed.mjs: %s is %q, internal/webui has %q", name, m, want)
			}
		}
	})
}

func TestChunkCaching(t *testing.T) {
	t.Run("R-UI-001 a chunk named with a base64url hash of either case is immutable (Q-UI-140)", func(t *testing.T) {
		fsys := builtFS()
		fsys["en/chunk-b3RHWPW-.js"] = &fstest.MapFile{Data: []byte("export{}")}
		fsys["en/chunk-Bo__WLzq.js"] = &fstest.MapFile{Data: []byte("export{}")}
		h := newHandler(t, fsys, nil)
		for _, p := range []string{"/ui/en/chunk-b3RHWPW-.js", "/ui/en/chunk-Bo__WLzq.js"} {
			rec := do(h, "GET", p)
			if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Errorf("%s: code %d Cache-Control %q", p, rec.Code, rec.Header().Get("Cache-Control"))
			}
		}
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
