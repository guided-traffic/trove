package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><html lang="en"><head><base href="/ui/en/">` +
	`<link rel="stylesheet" href="styles-ABCD1234.css" nonce="TROVE_CSP_NONCE"></head>` +
	`<body><trove-root ngcspnonce="TROVE_CSP_NONCE"></trove-root>` +
	`<script src="main-ABCD1234.js" type="module" nonce="TROVE_CSP_NONCE"></script></body></html>`

func builtFS() fstest.MapFS {
	m := fstest.MapFS{
		"index.html": {Data: []byte("<style nonce=\"TROVE_CSP_NONCE\"></style>placeholder")},
	}
	for _, l := range []string{"en", "de"} {
		m[l+"/index.html"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(indexHTML, "/ui/en/", "/ui/"+l+"/"))}
		m[l+"/main-ABCD1234.js"] = &fstest.MapFile{Data: []byte("console.log(1)")}
		m[l+"/styles-ABCD1234.css"] = &fstest.MapFile{Data: []byte("body{}")}
		m[l+"/media/font-ABCD1234.woff2"] = &fstest.MapFile{Data: []byte("font")}
		m[l+"/assets.d/x.txt"] = &fstest.MapFile{Data: []byte("x")}
		m[l+"/favicon.ico"] = &fstest.MapFile{Data: []byte("ico")}
	}
	return m
}

func newHandler(t *testing.T, assets fstest.MapFS, mod func(*Options)) *Handler {
	t.Helper()
	opts := Options{Locales: []string{"en", "de"}, DefaultLocale: "en", Assets: assets}
	if mod != nil {
		mod(&opts)
	}
	h, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func do(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var nonceInCSP = regexp.MustCompile(`'nonce-([A-Za-z0-9+/=]{16,})'`)

func TestIndexNonce(t *testing.T) {
	h := newHandler(t, builtFS(), nil)

	t.Run("R-ARCH-004 two requests carry different nonces", func(t *testing.T) {
		a, b := do(h, "GET", "/ui/en/admin/jobs"), do(h, "GET", "/ui/en/admin/jobs")
		na := nonceInCSP.FindStringSubmatch(a.Header().Get("Content-Security-Policy"))
		nb := nonceInCSP.FindStringSubmatch(b.Header().Get("Content-Security-Policy"))
		if na == nil || nb == nil {
			t.Fatalf("no nonce in CSP: %q / %q", a.Header().Get("Content-Security-Policy"), b.Header().Get("Content-Security-Policy"))
		}
		if na[1] == nb[1] {
			t.Fatalf("nonce repeated: %s", na[1])
		}
	})

	t.Run("R-UI-001 body nonce equals header nonce and no placeholder remains", func(t *testing.T) {
		rec := do(h, "GET", "/ui/en/")
		n := nonceInCSP.FindStringSubmatch(rec.Header().Get("Content-Security-Policy"))[1]
		body := rec.Body.String()
		if strings.Contains(body, NoncePlaceholder) {
			t.Fatalf("placeholder left in body: %s", body)
		}
		if got := strings.Count(body, `"`+n+`"`); got != 3 {
			t.Fatalf("nonce appears %d times in body, want 3 (style link, root element, script): %s", got, body)
		}
		if !strings.Contains(body, `ngcspnonce="`+n+`"`) {
			t.Fatalf("root element lacks the nonce: %s", body)
		}
	})

	t.Run("R-UI-001 document headers", func(t *testing.T) {
		rec := do(h, "GET", "/ui/de/")
		want := map[string]string{
			"X-Content-Type-Options":     "nosniff",
			"Referrer-Policy":            "same-origin",
			"Cross-Origin-Opener-Policy": "same-origin",
			"Cache-Control":              "no-cache",
			"Content-Type":               "text/html; charset=utf-8",
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, part := range []string{
			"default-src 'self'", "script-src 'self'", "img-src 'self' data:;", "font-src 'self'",
			"connect-src 'self'", "object-src 'none'", "base-uri 'self'", "form-action 'self'",
			"frame-ancestors 'none'", "require-trusted-types-for 'script'",
			"trusted-types angular angular#bundler",
		} {
			if !strings.Contains(csp, part) {
				t.Errorf("CSP lacks %q: %s", part, csp)
			}
		}
	})

	t.Run("R-UI-001 image origins are added to img-src without wildcard", func(t *testing.T) {
		h := newHandler(t, builtFS(), func(o *Options) {
			o.ImageOrigins = []string{"https://badges.example.net", "https://trove-content.example.net:8443"}
		})
		csp := do(h, "GET", "/ui/en/").Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "img-src 'self' data: https://badges.example.net https://trove-content.example.net:8443;") {
			t.Fatalf("img-src wrong: %s", csp)
		}
	})
}

func TestSPAFallback(t *testing.T) {
	h := newHandler(t, builtFS(), nil)

	t.Run("R-ARCH-004 deep link answers the locale index", func(t *testing.T) {
		for _, p := range []string{"/ui/en/admin/jobs", "/ui/en/packages/npm/@scope/pkg", "/ui/en/admin/", "/ui/en/index.html"} {
			rec := do(h, "GET", p)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<base href="/ui/en/">`) {
				t.Errorf("%s: code %d body %q", p, rec.Code, rec.Body.String())
			}
		}
		rec := do(h, "GET", "/ui/de/admin/jobs")
		if !strings.Contains(rec.Body.String(), `<base href="/ui/de/">`) {
			t.Errorf("de deep link served the wrong locale: %s", rec.Body.String())
		}
	})

	t.Run("R-UI-001 HEAD sends headers and no body", func(t *testing.T) {
		rec := do(h, "HEAD", "/ui/en/admin/jobs")
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("code %d, body %d bytes, csp %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Security-Policy"))
		}
	})

	t.Run("R-UI-001 other methods are refused", func(t *testing.T) {
		rec := do(h, "POST", "/ui/en/")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("code %d allow %q", rec.Code, rec.Header().Get("Allow"))
		}
	})
}

func TestCacheRules(t *testing.T) {
	h := newHandler(t, builtFS(), nil)

	t.Run("R-UI-001 hashed assets are immutable", func(t *testing.T) {
		for _, p := range []string{"/ui/en/main-ABCD1234.js", "/ui/en/styles-ABCD1234.css", "/ui/en/media/font-ABCD1234.woff2"} {
			rec := do(h, "GET", p)
			if rec.Code != 200 {
				t.Fatalf("%s: code %d", p, rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
				t.Errorf("%s: Cache-Control %q", p, got)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s: no nosniff", p)
			}
		}
	})

	t.Run("R-UI-001 asset content types", func(t *testing.T) {
		cases := map[string]string{
			"/ui/en/main-ABCD1234.js":          "text/javascript; charset=utf-8",
			"/ui/en/styles-ABCD1234.css":       "text/css; charset=utf-8",
			"/ui/en/media/font-ABCD1234.woff2": "font/woff2",
			"/ui/en/favicon.ico":               "image/x-icon",
		}
		for p, want := range cases {
			if got := do(h, "GET", p).Header().Get("Content-Type"); got != want {
				t.Errorf("%s: Content-Type %q, want %q", p, got, want)
			}
		}
	})

	t.Run("R-UI-001 index and unhashed files are not cached without revalidation", func(t *testing.T) {
		for _, p := range []string{"/ui/en/", "/ui/en/favicon.ico"} {
			if got := do(h, "GET", p).Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s: Cache-Control %q", p, got)
			}
		}
	})
}

func TestUnknownAssets(t *testing.T) {
	h := newHandler(t, builtFS(), nil)

	t.Run("R-UI-001 unknown asset is a 404 and not the index", func(t *testing.T) {
		for _, p := range []string{"/ui/en/missing-ABCD1234.js", "/ui/en/media/none.png", "/ui/en/a/b.c", "/ui/en/media.d/", "/ui/en/%2e%2e/main.js"} {
			rec := do(h, "GET", p)
			if p == "/ui/en/media.d/" {
				// A trailing slash is a route, not a file.
				if rec.Code != 200 {
					t.Errorf("%s: code %d", p, rec.Code)
				}
				continue
			}
			if rec.Code != 404 {
				t.Errorf("%s: code %d, want 404", p, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("%s: Content-Type %q", p, ct)
			}
		}
	})

	t.Run("R-UI-001 a directory is not served as a file", func(t *testing.T) {
		if rec := do(h, "GET", "/ui/en/assets.d"); rec.Code != 404 {
			t.Errorf("code %d", rec.Code)
		}
	})

	t.Run("R-UI-001 paths outside the mount are 404", func(t *testing.T) {
		if rec := do(h, "GET", "/other"); rec.Code != 404 {
			t.Errorf("code %d", rec.Code)
		}
	})
}

func TestLocaleRedirects(t *testing.T) {
	h := newHandler(t, builtFS(), nil)

	cases := []struct {
		name, target, hdr, want string
	}{
		{"R-UI-001 /ui/ lands on the negotiated locale", "/ui/", "de-DE,de;q=0.9,en;q=0.5", "/ui/de/"},
		{"R-UI-001 without a matching language the default applies", "/ui/", "fr-FR,fr;q=0.9", "/ui/en/"},
		{"R-UI-001 without Accept-Language the default applies", "/ui/", "", "/ui/en/"},
		{"R-UI-001 a route without locale keeps route and query", "/ui/login?local=1", "de", "/ui/de/login?local=1"},
		{"R-UI-001 a locale without trailing slash gets one", "/ui/de?x=1", "en", "/ui/de/?x=1"},
		{"R-UI-001 q-values order the languages", "/ui/admin/jobs", "en;q=0.3, de;q=0.8", "/ui/de/admin/jobs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(h, "GET", c.target, "Accept-Language", c.hdr)
			if rec.Code != http.StatusFound {
				t.Fatalf("code %d", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != c.want {
				t.Fatalf("Location %q, want %q", got, c.want)
			}
			if !strings.Contains(rec.Header().Get("Vary"), "Accept-Language") || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Vary %q Cache-Control %q", rec.Header().Get("Vary"), rec.Header().Get("Cache-Control"))
			}
		})
	}

	t.Run("R-UI-001 a user preference beats Accept-Language", func(t *testing.T) {
		h := newHandler(t, builtFS(), func(o *Options) {
			o.Preference = func(*http.Request) string { return "de" }
		})
		if got := do(h, "GET", "/ui/", "Accept-Language", "en").Header().Get("Location"); got != "/ui/de/" {
			t.Fatalf("Location %q", got)
		}
	})

	t.Run("R-UI-001 an unlisted preference is ignored", func(t *testing.T) {
		h := newHandler(t, builtFS(), func(o *Options) {
			o.Preference = func(*http.Request) string { return "fr" }
		})
		if got := do(h, "GET", "/ui/", "Accept-Language", "de").Header().Get("Location"); got != "/ui/de/" {
			t.Fatalf("Location %q", got)
		}
	})

	t.Run("R-UI-001 a double slash cannot redirect off site", func(t *testing.T) {
		loc := do(h, "GET", "/ui//evil.example/x").Header().Get("Location")
		if !strings.HasPrefix(loc, "/ui/") {
			t.Fatalf("Location %q", loc)
		}
	})
}

func TestPlaceholder(t *testing.T) {
	only := fstest.MapFS{"index.html": builtFS()["index.html"]}

	t.Run("R-ARCH-003 a tree with only the placeholder still serves every locale", func(t *testing.T) {
		h := newHandler(t, only, nil)
		if !h.Placeholder() {
			t.Fatal("Placeholder() = false")
		}
		for _, p := range []string{"/ui/en/", "/ui/de/admin/jobs"} {
			rec := do(h, "GET", p)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "placeholder") {
				t.Errorf("%s: code %d body %q", p, rec.Code, rec.Body.String())
			}
			n := nonceInCSP.FindStringSubmatch(rec.Header().Get("Content-Security-Policy"))
			if n == nil || !strings.Contains(rec.Body.String(), n[1]) {
				t.Errorf("%s: nonce missing or not injected", p)
			}
		}
		if rec := do(h, "GET", "/ui/en/main-ABCD1234.js"); rec.Code != 404 {
			t.Errorf("asset in placeholder mode: code %d", rec.Code)
		}
	})

	t.Run("R-ARCH-003 the embedded tree starts, with or without a web build", func(t *testing.T) {
		h, err := New(Options{Locales: []string{"en", "de"}, DefaultLocale: "en"})
		if err != nil {
			t.Fatalf("New with the embedded tree: %v", err)
		}
		rec := do(h, "GET", "/ui/en/admin/jobs")
		if rec.Code != 200 || nonceInCSP.FindStringSubmatch(rec.Header().Get("Content-Security-Policy")) == nil {
			t.Fatalf("code %d csp %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
		}
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), NoncePlaceholder) {
			t.Fatal("placeholder left in body")
		}
	})

	t.Run("R-ARCH-003 the committed placeholder carries the nonce placeholder", func(t *testing.T) {
		data, err := dist.ReadFile("dist/index.html")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), NoncePlaceholder) {
			t.Fatal("dist/index.html lacks the nonce placeholder")
		}
	})
}

func TestNewRejects(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		opts Options
		want string
	}{
		{"R-ARCH-004 no locales", builtFS(), Options{DefaultLocale: "en"}, "no locales"},
		{"R-ARCH-004 default outside the list", builtFS(), Options{Locales: []string{"en"}, DefaultLocale: "de"}, "default locale"},
		{"R-ARCH-004 bad locale code", builtFS(), Options{Locales: []string{"../x"}, DefaultLocale: "../x"}, "not a valid locale"},
		{"R-ARCH-004 duplicate locale", builtFS(), Options{Locales: []string{"en", "en"}, DefaultLocale: "en"}, "twice"},
		{"R-UI-001 wildcard image origin", builtFS(), Options{Locales: []string{"en"}, DefaultLocale: "en", ImageOrigins: []string{"https://*.example.net"}}, "origin"},
		{"R-UI-001 http image origin", builtFS(), Options{Locales: []string{"en"}, DefaultLocale: "en", ImageOrigins: []string{"http://x.example.net"}}, "https"},
		{"R-UI-001 image origin with path", builtFS(), Options{Locales: []string{"en"}, DefaultLocale: "en", ImageOrigins: []string{"https://x.example.net/a"}}, "https"},
		{"R-UI-001 image origin that injects a directive", builtFS(), Options{Locales: []string{"en"}, DefaultLocale: "en", ImageOrigins: []string{"https://x.example.net; script-src *"}}, "origin"},
		{"R-ARCH-003 empty tree", fstest.MapFS{}, Options{Locales: []string{"en"}, DefaultLocale: "en"}, "placeholder"},
		{"R-ARCH-004 half-built UI", fstest.MapFS{"index.html": builtFS()["index.html"], "en/index.html": builtFS()["en/index.html"]}, Options{Locales: []string{"en", "de"}, DefaultLocale: "en"}, "lacks configured locale"},
		{"R-UI-001 index without nonce placeholder", fstest.MapFS{"en/index.html": {Data: []byte("<html></html>")}}, Options{Locales: []string{"en"}, DefaultLocale: "en"}, NoncePlaceholder},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.Assets = c.fsys
			_, err := New(c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestNegotiate(t *testing.T) {
	locales := []string{"en", "de"}
	cases := map[string]string{
		"de-DE":                   "de",
		"DE":                      "de",
		"fr, de;q=0.5":            "de",
		"en;q=0, de":              "de",
		"*":                       "",
		"":                        "",
		"fr-CA":                   "",
		"en;q=abc, de":            "de",
		"de;q=0.1, en;q=0.9":      "en",
		"en-GB,en;q=0.9,de;q=0.8": "en",
	}
	for in, want := range cases {
		t.Run("R-UI-045 "+in, func(t *testing.T) {
			if got := negotiate(in, locales); got != want {
				t.Errorf("negotiate(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
