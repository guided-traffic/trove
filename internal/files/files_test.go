package files

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestPathHelpers(t *testing.T) {
	t.Run("R-STOR-003 the ancestors of a path are its implicit folders, from the top", func(t *testing.T) {
		for in, want := range map[string][]string{
			"a":       nil,
			"a/b":     {"a"},
			"a/b/c/d": {"a", "a/b", "a/b/c"},
		} {
			if got := ancestors(in); !slices.Equal(got, want) {
				t.Errorf("ancestors(%q) = %v, want %v", in, got, want)
			}
		}
	})
	t.Run("R-STOR-003 the parent of a top-level entry is empty", func(t *testing.T) {
		for in, want := range map[string]string{"a": "", "a/b": "a", "a/b/c": "a/b"} {
			if got := parentOf(in); got != want {
				t.Errorf("parentOf(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("R-STOR-018 the subtree of a folder is the range from its slash to the byte after it", func(t *testing.T) {
		lo, hi := subtreeBounds("a/b")
		if lo != "a/b/" || hi != "a/b0" {
			t.Errorf("bounds = %q, %q", lo, hi)
		}
		// "a/b-c" sorts after "a/b/" in the C collation? '-' (0x2D) < '/' (0x2F): outside the range.
		for _, in := range []string{"a/b/x", "a/b/x/y", "a/b/\xff"} {
			if in < lo || in >= hi {
				t.Errorf("%q is below a/b but outside [%q, %q)", in, lo, hi)
			}
		}
		for _, out := range []string{"a/b", "a/b-c", "a/b.x", "a/b0", "a/bb", "a/c"} {
			if out >= lo && out < hi {
				t.Errorf("%q is not below a/b but inside [%q, %q)", out, lo, hi)
			}
		}
		if lo, hi := subtreeBounds(""); lo != "" || hi != "" {
			t.Errorf("the root has no bounds: %q, %q", lo, hi)
		}
	})
	t.Run("R-STOR-007 an actor id is used only when it is a uuid", func(t *testing.T) {
		id := func(s string) string { return Caller{Actor: audit.Actor{ID: s}}.actorID() }
		if id("0198a000-0000-7000-8000-000000000001") == "" || id("admin") != "" || id("") != "" || id("0198a000-0000-7000-8000-00000000000g") != "" {
			t.Error("actorID")
		}
	})
}

func TestRepositoryUploadLimit(t *testing.T) {
	for cfg, want := range map[string]int64{
		`{}`:                                 0,
		`{"storage":{"maxUploadSize":1024}}`: 1024,
		`{"storage":{"quota":"1TiB"}}`:       0,
		``:                                   0,
	} {
		if got, err := maxUpload(repository.Repository{Config: []byte(cfg)}); err != nil || got != want {
			t.Errorf("maxUpload(%s) = %d, %v, want %d", cfg, got, err, want)
		}
	}
	// R-STOR-004: a limit that cannot be decoded refuses the upload, it does not lift it.
	for _, cfg := range []string{
		`{"storage":{"maxUploadSize":-5}}`, `{"storage":{"maxUploadSize":1e9}}`, `{"storage":{"maxUploadSize":"10"}}`,
		`{"storage":{"maxUploadSize":null}}`, `{"storage":{"maxUploadSize":1.5}}`, `not json`,
	} {
		if got, err := maxUpload(repository.Repository{Config: []byte(cfg)}); err == nil {
			t.Errorf("R-STOR-004 maxUpload(%s) = %d, want an error", cfg, got)
		}
	}
}

func TestView(t *testing.T) {
	links := Links{Base: "https://trove.example.com/"}
	var d blob.Digest
	d[0] = 0x5b
	file := Item{
		Repository: "generic-local", Path: "tools/wid get/widget 2.3.bin", Kind: KindFile, Size: 48213, Digest: d,
		SHA1: make([]byte, 20), MD5: make([]byte, 16), SHA512: make([]byte, 64), ContentStatus: "ok",
		CreatedAt: time.Date(2026, 10, 28, 9, 14, 3, 117_000_000, time.UTC), ModifiedAt: time.Date(2026, 10, 28, 9, 14, 3, 117_000_000, time.UTC),
		CreatedBy: "user:alice", DeclaredSHA256: d[:],
	}

	t.Run("R-STOR-016 a file is described with digests, urls, package null and the content address as ETag", func(t *testing.T) {
		raw, err := json.Marshal(file.View(links))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["kind"] != "file" || m["size"] != float64(48213) || m["mediaType"] != "application/octet-stream" {
			t.Errorf("%s", raw)
		}
		if p, present := m["package"]; !present || p != nil {
			t.Errorf("package = %v (present %v)", p, present)
		}
		if p, present := m["upstreamUrl"]; !present || p != nil {
			t.Errorf("upstreamUrl = %v", p)
		}
		if m["downloadUrl"] != "https://trove.example.com/files/generic-local/tools/wid%20get/widget%202.3.bin" {
			t.Errorf("downloadUrl = %v", m["downloadUrl"])
		}
		if m["itemUrl"] != "https://trove.example.com/api/v1/repositories/generic-local/items?path=tools%2Fwid+get%2Fwidget+2.3.bin" {
			t.Errorf("itemUrl = %v", m["itemUrl"])
		}
		if c := m["created"].(map[string]any); c["at"] != "2026-10-28T09:14:03.117Z" || c["by"] != "user:alice" {
			t.Errorf("created = %v", c)
		}
		if m["modified"].(map[string]any)["by"] != nil {
			t.Errorf("modified.by = %v, want null for a principal without a record", m["modified"])
		}
		if got := m["digests"].(map[string]any)["sha256"]; got != d.Hex() {
			t.Errorf("sha256 = %v", got)
		}
		if _, ok := m["declaredDigests"].(map[string]any)["sha256"]; !ok {
			t.Errorf("declaredDigests = %v", m["declaredDigests"])
		}
		if file.ETag() != `"`+d.Hex()+`"` {
			t.Errorf("ETag = %s", file.ETag())
		}
	})

	t.Run("R-STOR-016 a folder carries explicit and the listing, no size and no digests", func(t *testing.T) {
		folder := Item{Repository: "generic-local", Path: "tools", Kind: KindFolder, Explicit: true}
		raw, _ := json.Marshal(folder.View(links))
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["kind"] != "folder" || m["explicit"] != true || m["listingUrl"] != "https://trove.example.com/api/v1/repositories/generic-local/children?path=tools" {
			t.Errorf("%s", raw)
		}
		for _, k := range []string{"size", "digests", "downloadUrl"} {
			if _, ok := m[k]; ok {
				t.Errorf("a folder has %s", k)
			}
		}
	})

	t.Run("R-ARCH-007 a content link is built from one place, every segment encoded", func(t *testing.T) {
		got := links.File("r", "a b/c%d/é", false)
		if got != "https://trove.example.com/files/r/a%20b/c%25d/%C3%A9" || strings.Contains(got, "//files") {
			t.Errorf("File = %s", got)
		}
		if got := links.File("r", "dir", true); !strings.HasSuffix(got, "/dir/") {
			t.Errorf("a folder link ends in a slash: %s", got)
		}
	})
}
