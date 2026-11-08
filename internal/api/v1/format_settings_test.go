package v1

import (
	"encoding/json"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"

	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the formats whose schemas are compared
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// TestR_ARCH_008_NpmSettingsSchemaEqualsTheOpenAPIMember: the npm member of the repository
// document is declared twice, as the JSON Schema the format registers (which the service
// validates against and the create form renders) and as the OpenAPI schema NpmSettings
// (which the generated clients are built from). Each property, its type, its values and its
// default must be the same in both; a setting added to one without the other fails here.
func TestR_ARCH_008_NpmSettingsSchemaEqualsTheOpenAPIMember(t *testing.T) {
	settingsSchemaEqualsOpenAPI(t, "npm", "NpmSettings")
}

// TestR_ARCH_008_NugetSettingsSchemaEqualsTheOpenAPIMember is the same comparison for the
// nuget member (R-NUGET-007, R-NUGET-016).
func TestR_ARCH_008_NugetSettingsSchemaEqualsTheOpenAPIMember(t *testing.T) {
	settingsSchemaEqualsOpenAPI(t, "nuget", "NugetSettings")
}

// TestR_ARCH_008_OciSettingsSchemaEqualsTheOpenAPIMember is the same comparison for the oci
// member (R-OCI-026).
func TestR_ARCH_008_OciSettingsSchemaEqualsTheOpenAPIMember(t *testing.T) {
	settingsSchemaEqualsOpenAPI(t, "oci", "OciSettings")
}

// TestR_ARCH_008_DebSettingsSchemaEqualsTheOpenAPIMember is the same comparison for the deb
// member (R-DEB-008, R-DEB-024). The read-only members of the OpenAPI schema (the fingerprints
// of the upstream keys) are answers and no settings, so the registered schema does not list them.
func TestR_ARCH_008_DebSettingsSchemaEqualsTheOpenAPIMember(t *testing.T) {
	settingsSchemaEqualsOpenAPI(t, "deb", "DebSettings")
}

func settingsSchemaEqualsOpenAPI(t *testing.T, format, schema string) {
	t.Helper()
	type property struct {
		Type       any                 `json:"type" yaml:"type"`
		Enum       []string            `json:"enum" yaml:"enum"`
		Default    any                 `json:"default" yaml:"default"`
		Pattern    string              `json:"pattern" yaml:"pattern"`
		ReadOnly   bool                `json:"readOnly" yaml:"readOnly"`
		Properties map[string]property `json:"properties" yaml:"properties"`
	}
	type object struct {
		AdditionalProperties any                 `json:"additionalProperties" yaml:"additionalProperties"`
		Properties           map[string]property `json:"properties" yaml:"properties"`
	}
	var api struct {
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(EmbeddedYAML(), &api); err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.Default().Lookup(format)
	if !ok || len(entry.SettingsSchema) == 0 {
		t.Fatalf("the %s format registers no settings schema", format)
	}
	var registered object
	if err := json.Unmarshal(entry.SettingsSchema, &registered); err != nil {
		t.Fatal(err)
	}
	node, ok := api.Components.Schemas[schema]
	if !ok {
		t.Fatalf("api/openapi.yaml has no schema %s", schema)
	}
	var documented object
	if err := node.Decode(&documented); err != nil {
		t.Fatal(err)
	}
	for name, p := range documented.Properties {
		if p.ReadOnly {
			delete(documented.Properties, name)
		}
	}
	if registered.AdditionalProperties != false || documented.AdditionalProperties != false {
		t.Errorf("additionalProperties: registered %v, documented %v, both must be false", registered.AdditionalProperties, documented.AdditionalProperties)
	}
	if !reflect.DeepEqual(registered.Properties, documented.Properties) {
		t.Fatalf("the registered schema and api/openapi.yaml differ:\n registered %+v\n documented %+v", registered.Properties, documented.Properties)
	}
}
