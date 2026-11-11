package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// noticesFile is the file that web/scripts/build-embed.mjs writes into every locale directory (Q-UI-144).
const noticesFile = "3rdpartylicenses.txt"

// formatLogoNotice is the notice of the format logos below web/public, which build-embed.mjs appends to the notices (Q-UI-227).
const formatLogoNotice = "formats/NOTICE.txt"

// noticesSample has the shape of the built file: the section of Inter, whose font licence is OFL-1.1, and the section of the
// icon font, which is Apache-2.0 (build-embed.mjs appends it, because Angular's file does not carry it).
const noticesSample = `--------------------------------------------------------------------------------
Package: @fontsource-variable/inter
License: "OFL-1.1"

Copyright 2016 The Inter Project Authors (https://github.com/rsms/inter)

This Font Software is licensed under the SIL Open Font License, Version 1.1.

SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007

--------------------------------------------------------------------------------
Package: material-icons
Version: 1.13.14
License: "Apache-2.0"

                                 Apache License
                           Version 2.0, January 2004
`

// assertNotices checks the answer of the notices file: its status, its headers and the two licences it has to name.
func assertNotices(t *testing.T, rec *httptest.ResponseRecorder, path string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: code %d", path, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("%s: Content-Type %q", path, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("%s: Cache-Control %q; the notices are not hashed and are revalidated", path, got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options %q", path, got)
	}
	for _, want := range []string{"Package: @fontsource-variable/inter", "OFL-1.1", "SIL OPEN FONT LICENSE Version 1.1", "Package: material-icons", "Apache-2.0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: the notices do not contain %q", path, want)
		}
	}
}

// TestR_UI_001_ThirdPartyNotices: the UI ships the notices of its bundled software at a stable, unauthenticated address
// under every locale (Q-UI-144). The file is not hashed, so the shared asset store never holds it and every replica serves
// the notices of its own build.
func TestR_UI_001_ThirdPartyNotices(t *testing.T) {
	t.Run("the build writes the notices into every locale directory (Q-UI-144)", func(t *testing.T) {
		src, err := os.ReadFile("../../web/scripts/build-embed.mjs")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "const notices = '"+noticesFile+"'") {
			t.Errorf("web/scripts/build-embed.mjs does not name %s as the notices file", noticesFile)
		}
		if !strings.Contains(string(src), "writeFileSync(join(target, locale, notices), text)") {
			t.Error("web/scripts/build-embed.mjs does not write the notices into each locale directory")
		}
		if !strings.Contains(string(src), "const formatLogos = '"+formatLogoNotice+"'") {
			t.Errorf("web/scripts/build-embed.mjs does not append %s, the notice of the format logos (Q-UI-227)", formatLogoNotice)
		}
	})

	t.Run("the notices are not a hashed file, so the store neither holds nor serves them (Q-UI-144)", func(t *testing.T) {
		if IsHashed(noticesFile) {
			t.Fatalf("%s is hashed: the store would publish it under a name that does not change with its content", noticesFile)
		}
	})

	t.Run("the handler serves the notices of each locale as text, revalidated, with the licences named", func(t *testing.T) {
		fsys := builtFS()
		for _, l := range []string{"en", "de"} {
			fsys[l+"/"+noticesFile] = &fstest.MapFile{Data: []byte(noticesSample)}
		}
		h := newHandler(t, fsys, nil)
		for _, l := range []string{"en", "de"} {
			path := "/ui/" + l + "/" + noticesFile
			assertNotices(t, do(h, "GET", path), path)
		}
	})

	t.Run("the embedded build carries the notices of every locale (Q-UI-144)", func(t *testing.T) {
		h, err := New(Options{Locales: []string{"en", "de"}, DefaultLocale: "en"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if h.Placeholder() {
			// Without a web build there is no notices file to find. CI's web job builds the UI and sets TROVE_UI_BUILT=1,
			// where the absence is a failure.
			if os.Getenv("TROVE_UI_BUILT") != "" {
				t.Fatal("TROVE_UI_BUILT is set, but internal/webui holds no web build (run make web-embed)")
			}
			t.Skip("no web build is embedded (make web-embed); the web job of the release workflow requires it with TROVE_UI_BUILT=1")
		}
		for _, l := range []string{"en", "de"} {
			path := "/ui/" + l + "/" + noticesFile
			rec := do(h, "GET", path)
			assertNotices(t, rec, path)
			assertFormatLogos(t, h, l, rec.Body.String())
		}
	})
}

// assertFormatLogos checks the format logos of an embedded build (Q-UI-225, Q-UI-227): their notice is part of the notices
// of the locale, and each logo is served from the copies of web/public, unhashed and revalidated like the notices.
func assertFormatLogos(t *testing.T, h http.Handler, locale, notices string) {
	t.Helper()
	if !strings.Contains(notices, "trove is not affiliated with, sponsored or endorsed by any of them.") {
		t.Errorf("%s: the notices lack the format logos of %s", locale, formatLogoNotice)
	}
	for _, logo := range []string{"formats/deb/openlogo-nd.svg", "formats/nuget/nuget.svg", "formats/oci/oci-icon-color.svg"} {
		p := "/ui/" + locale + "/" + logo
		got := do(h, "GET", p)
		hd := got.Header()
		if got.Code != http.StatusOK || hd.Get("Content-Type") != "image/svg+xml" || hd.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: code %d, Content-Type %q, Cache-Control %q", p, got.Code, hd.Get("Content-Type"), hd.Get("Cache-Control"))
		}
	}
}
