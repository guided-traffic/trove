package edge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/items"
)

func TestDescribeItem(t *testing.T) {
	links := Links{Base: "https://trove.example.com/"}
	var d blob.Digest
	d[0] = 0x5b
	file := items.Item{
		Repository: "generic-local", Path: "tools/wid get/widget 2.3.bin", Kind: items.KindFile, Size: 48213, Digest: d,
		SHA1: make([]byte, 20), MD5: make([]byte, 16), SHA512: make([]byte, 64), ContentStatus: "ok",
		CreatedAt: time.Date(2026, 10, 28, 9, 14, 3, 117_000_000, time.UTC), ModifiedAt: time.Date(2026, 10, 28, 9, 14, 3, 117_000_000, time.UTC),
		CreatedBy: "user:alice", DeclaredSHA256: d[:],
	}

	t.Run("R-STOR-016 a file is described with digests, urls, package null and the content address as ETag", func(t *testing.T) {
		raw, err := json.Marshal(DescribeItem(file, links))
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

	t.Run("R-STOR-016 the upstream url of an item a remote repository cached is carried", func(t *testing.T) {
		cached := file
		cached.UpstreamURL = "https://registry.example.org/widget/-/widget-2.3.tgz"
		raw, _ := json.Marshal(DescribeItem(cached, links))
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["upstreamUrl"] != cached.UpstreamURL {
			t.Errorf("upstreamUrl = %v", m["upstreamUrl"])
		}
	})

	t.Run("R-STOR-016 a folder carries explicit and the listing, no size and no digests", func(t *testing.T) {
		folder := items.Item{Repository: "generic-local", Path: "tools", Kind: items.KindFolder, Explicit: true}
		raw, _ := json.Marshal(DescribeItem(folder, links))
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
