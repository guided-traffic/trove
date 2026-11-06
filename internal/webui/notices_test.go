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
			assertNotices(t, do(h, "GET", path), path)
		}
	})
}
