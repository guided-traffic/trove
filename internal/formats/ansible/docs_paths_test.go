package ansible

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// TestR_ANS_035_TheDocsBlobDocumentIsMetadataOfItsVersion: the stored document of a version's documentation is classified as metadata of
// the package and the version and never as an artifact, and the route addresses exactly that path.
func TestR_ANS_035_TheDocsBlobDocumentIsMetadataOfItsVersion(t *testing.T) {
	f := New()
	for _, tc := range []struct {
		path string
		want formats.PathInfo
	}{
		{"collections/acme/tools/docs/1.0.0.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools", Version: "1.0.0"}},
		{"collections/acme/tools/docs/1.0.0-rc.1+b.5.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools", Version: "1.0.0-rc.1+b.5"}},
		{"collections/acme/tools/docs/x.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/docs/1.0.0.tar.gz", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/other/1.0.0.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
	} {
		if got := f.Classify(formats.Path{Path: tc.path}); got != tc.want {
			t.Errorf("Classify(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
	if got := DocsDocumentPath("acme", "tools", "1.0.0"); got != "collections/acme/tools/docs/1.0.0.json" {
		t.Errorf("DocsDocumentPath = %q", got)
	}
	got, ok := CanonicalPath(routeDocs, formats.Params{paramNamespace: "acme", paramName: "tools", paramVersion: "1.0.0"})
	if !ok || got != DocsDocumentPath("acme", "tools", "1.0.0") {
		t.Errorf("CanonicalPath = %q %v", got, ok)
	}
}
