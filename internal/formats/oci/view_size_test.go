package oci

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// TestR_OCI_031_TheMetadataSizeIsTheStoredImageSize: the size of the tag's metadata is the content size the push stored for the
// manifest (the config and layers of an image, the sum over the children of an index), and the platforms keep each child's own.
func TestR_OCI_031_TheMetadataSizeIsTheStoredImageSize(t *testing.T) {
	image := rows.TagView{Size: 400, ContentSize: 30 << 20}
	if got := tagMetadata(image)["size"]; got != int64(30<<20) {
		t.Fatalf("image size = %v", got)
	}
	index := rows.TagView{Size: 300, ContentSize: 30, Children: []rows.Child{{Platform: "linux/amd64", ContentSize: 10}, {Platform: "linux/arm64", ContentSize: 20}}}
	md := tagMetadata(index)
	if md["size"] != int64(30) {
		t.Fatalf("index size = %v", md["size"])
	}
	if ps := md["platforms"].([]map[string]any); len(ps) != 2 || ps[0]["size"] != int64(10) || ps[1]["size"] != int64(20) {
		t.Fatalf("platforms = %v", md["platforms"])
	}
}
