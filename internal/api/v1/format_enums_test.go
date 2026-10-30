package v1

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the formats the enums are compared with
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// TestR_ARCH_010_PerClassFormatEnums: the OpenAPI document holds one enum of format
// names and one per class, and each equals what the registry says. A format, or a
// class of a format, added to the code without the document fails here; no list of
// formats per class is kept by hand (R-ARCH-010, R-ARCH-008 "Classes is the single
// source of truth").
func TestR_ARCH_010_PerClassFormatEnums(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(EmbeddedYAML(), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"FormatName": {}, "LocalFormatName": {}, "RemoteFormatName": {}, "VirtualFormatName": {}}
	for _, e := range catalog.Default().List() {
		want["FormatName"] = append(want["FormatName"], string(e.ID))
		for _, c := range []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual} {
			if e.Supports(c) {
				name := map[catalog.Class]string{catalog.Local: "LocalFormatName", catalog.Remote: "RemoteFormatName", catalog.Virtual: "VirtualFormatName"}[c]
				want[name] = append(want[name], string(e.ID))
			}
		}
	}
	for name, ids := range want {
		t.Run("R-ARCH-010 "+name+" equals the registry", func(t *testing.T) {
			s, ok := doc.Components.Schemas[name]
			if !ok {
				t.Fatalf("api/openapi.yaml has no schema %s", name)
			}
			got := slices.Clone(s.Enum)
			slices.Sort(got)
			slices.Sort(ids)
			if !slices.Equal(got, ids) {
				t.Fatalf("%s enum = %v, registry = %v", name, got, ids)
			}
		})
	}
}
