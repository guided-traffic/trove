package registration

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// TestR_NUGET_008_TheMetadataTheWriterStoresIsTheMetadataTheDocumentsRead: the ingestion writes
// nuget_version.metadata with rows.Metadata and the documents decode it with ParseMetadata; a key
// that one of them spells differently would show up as a missing field or a 500.
func TestR_NUGET_008_TheMetadataTheWriterStoresIsTheMetadataTheDocumentsRead(t *testing.T) {
	accept := true
	written := rows.Metadata{
		Title: "Widgets", Summary: "Small.", Description: "Widget primitives.", Language: "en-US", Authors: "Acme, Contoso",
		Tags: []string{"a", "b"}, ProjectURL: "https://example.com/p", IconURL: "https://example.com/i.png", LicenseURL: "https://example.com/l",
		License:                  &rows.License{Type: "expression", Version: "1.0", Value: "MIT"},
		RequireLicenseAcceptance: accept, MinClientVersion: "4.3.0",
		DependencyGroups: []rows.DependencyGroup{
			{TargetFramework: "net8.0", Dependencies: []rows.Dependency{{ID: "Contoso.Util", Version: "[1.0.0, 2.0.0)"}}},
			{Dependencies: []rows.Dependency{{ID: "Other.Pkg"}}},
		},
	}
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := Metadata{
		Title: "Widgets", Summary: "Small.", Description: "Widget primitives.", Language: "en-US", Authors: "Acme, Contoso",
		Tags: Tags{"a", "b"}, ProjectURL: "https://example.com/p", IconURL: "https://example.com/i.png", LicenseURL: "https://example.com/l",
		License:                  &License{Type: "expression", Version: "1.0", Value: "MIT"},
		RequireLicenseAcceptance: &accept, MinClientVersion: "4.3.0",
		DependencyGroups: []DependencyGroup{
			{TargetFramework: "net8.0", Dependencies: []Dependency{{ID: "Contoso.Util", Version: "[1.0.0, 2.0.0)"}}},
			{Dependencies: []Dependency{{ID: "Other.Pkg"}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %+v\nwant    %+v", got, want)
	}
}
