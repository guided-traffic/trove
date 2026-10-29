package generic

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

func TestDescriptor(t *testing.T) {
	d := Format{}.Descriptor()
	t.Run("R-GENERIC-001 the descriptor", func(t *testing.T) {
		if d.ID != "generic" || d.Layout != "generic" || d.ProtocolBasePath != "/files/{key}/" || d.PatternSubject != formats.SubjectPath {
			t.Fatalf("descriptor = %+v", d)
		}
		if len(d.Classes) != 1 || d.Classes[0] != formats.Local {
			t.Fatalf("classes = %v: only the local class is implemented", d.Classes)
		}
		if d.DefaultUpstreamURL != "" || d.DefaultMetadataTTLSeconds != nil || d.DefaultNotFoundTTLSeconds == nil || *d.DefaultNotFoundTTLSeconds != 1800 {
			t.Fatalf("defaults = %+v", d)
		}
		if d.PackageNames || d.PriorityMembers || d.PinsAndClaims || d.Caps {
			t.Fatal("the generic format has no package names, priority members, pins, claims or caps")
		}
	})
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		name, method, path string
		folder             bool
		want               formats.RequestKind
	}{
		{"GET of a file", http.MethodGet, "a/b.bin", false, formats.KindArtifact},
		{"HEAD of a file", http.MethodHead, "a/b.bin", false, formats.KindArtifact},
		{"GET of a folder", http.MethodGet, "a", true, formats.KindOther},
		{"GET of the root", http.MethodGet, "", true, formats.KindOther},
		{"GET of the root without the flag", http.MethodGet, "", false, formats.KindOther},
		{"PUT", http.MethodPut, "a/b.bin", false, formats.KindWrite},
		{"PUT of a folder", http.MethodPut, "a", true, formats.KindWrite},
		{"DELETE", http.MethodDelete, "a/b.bin", false, formats.KindWrite},
		{"POST", http.MethodPost, "a", false, formats.KindWrite},
		{"a file that looks like metadata", http.MethodGet, "maven-metadata.xml", false, formats.KindArtifact},
		{"a file that looks like a package document", http.MethodGet, "package.json", false, formats.KindArtifact},
	}
	for _, c := range cases {
		t.Run("R-GENERIC-003 R-REPO-003 "+c.name, func(t *testing.T) {
			got := Format{}.Describe(c.method, c.path, c.folder)
			if got.Kind != c.want || got.Path != c.path {
				t.Fatalf("Describe = %+v, want kind %s", got, c.want)
			}
			if got.Package != "" || got.Version != "" || got.RoutingName != "" || got.Variant != "" {
				t.Fatalf("the generic format has no package identity: %+v", got)
			}
			if got.Kind == formats.KindMetadata {
				t.Fatal("no generic path is metadata")
			}
		})
	}
}
