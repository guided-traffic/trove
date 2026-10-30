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
	t.Run("R-EP-014 generic is exposed through paths alone", func(t *testing.T) {
		e := formats.CatalogEntry(Format{})
		if x := e.Exposure; !x.Exposable || x.Packages || x.Versions || x.Tags || x.Versioning != nil || d.Names != nil {
			t.Fatalf("exposure = %+v, names = %v", x, d.Names)
		}
	})
	t.Run("R-ARCH-008 the descriptor declares the contract version", func(t *testing.T) {
		if d.ContractVersion != formats.ContractVersion {
			t.Fatalf("contract version = %d", d.ContractVersion)
		}
		if err := formats.Validate(Format{}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestQ_GENERIC_7_TheContractWithoutRoutes: the format implements the contract with
// no route, so the dispatcher registers no prefix for it and a Match finds
// nothing; its protocol is /files/ (Q-GENERIC-7).
func TestQ_GENERIC_7_TheContractWithoutRoutes(t *testing.T) {
	f := Format{}
	if len(f.Routes()) != 0 {
		t.Fatal("generic has routes")
	}
	if _, _, err := f.Match(http.MethodGet, "a/b.bin"); err == nil {
		t.Fatal("Match found a route")
	}
	if _, ok := f.PackageDocumentPath("x"); ok {
		t.Fatal("generic has no package names")
	}
	if f.Remote() != nil || f.Virtual() != nil || f.Indexes() != nil {
		t.Fatal("only the local class is implemented")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		p    formats.Path
		want formats.PathKind
		kind formats.RequestKind
	}{
		{"a file", formats.Path{Path: "a/b.bin"}, formats.Content, formats.KindArtifact},
		{"a file that looks like metadata", formats.Path{Path: "maven-metadata.xml"}, formats.Content, formats.KindArtifact},
		{"a file that looks like a package document", formats.Path{Path: "package.json"}, formats.Content, formats.KindArtifact},
		{"a folder", formats.Path{Path: "a", Folder: true}, formats.PathComputed, formats.KindMetadata},
		{"the root", formats.Path{Folder: true}, formats.PathComputed, formats.KindMetadata},
	}
	for _, c := range cases {
		t.Run("R-GENERIC-003 R-REPO-003 "+c.name, func(t *testing.T) {
			got := Format{}.Classify(c.p)
			if got.Kind != c.want || got.Kind.RequestKind() != c.kind {
				t.Fatalf("Classify = %+v, want kind %s", got, c.want)
			}
			if got.Package != "" || got.Version != "" || got.RoutingName != "" {
				t.Fatalf("the generic format has no package identity: %+v", got)
			}
		})
	}
}
