package all

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

func TestCatalog(t *testing.T) {
	t.Run("R-GENERIC-001 R-REPO-003 the generic format is in the catalog and lists only the local class", func(t *testing.T) {
		f, ok := formats.Lookup("generic")
		if !ok {
			t.Fatal("generic is not registered by formats/all")
		}
		d := f.Descriptor()
		if d.ID != "generic" || !d.Supports(formats.Local) || d.Supports(formats.Remote) || d.Supports(formats.Virtual) {
			t.Fatalf("descriptor = %+v", d)
		}
	})
}
