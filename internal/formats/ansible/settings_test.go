package ansible

import (
	"encoding/json"
	"testing"
)

func TestR_ANS_001_SettingsOfADocumentHaveTheirDefaults(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want Settings
	}{
		"no member":       {`{}`, Settings{ClientID: "cloud-services", FetchDocumentation: true}},
		"not a document":  {`not json`, Settings{ClientID: "cloud-services", FetchDocumentation: true}},
		"an empty member": {`{"ansible":{}}`, Settings{ClientID: "cloud-services", FetchDocumentation: true}},
		"both": {
			`{"ansible":{"authUrl":"https://sso.example/token","clientId":"trove"}}`,
			Settings{AuthURL: "https://sso.example/token", ClientID: "trove", FetchDocumentation: true},
		},
		"the url alone": {
			`{"ansible":{"authUrl":"https://sso.example/token"}}`,
			Settings{AuthURL: "https://sso.example/token", ClientID: "cloud-services", FetchDocumentation: true},
		},
		"documentation off": {`{"ansible":{"fetchDocumentation":false}}`, Settings{ClientID: "cloud-services"}},
		"documentation on":  {`{"ansible":{"fetchDocumentation":true}}`, Settings{ClientID: "cloud-services", FetchDocumentation: true}},
	} {
		if got := SettingsOf([]byte(tc.doc)); got != tc.want {
			t.Errorf("%s: SettingsOf = %+v, want %+v", name, got, tc.want)
		}
	}
}

// TestR_ANS_001_TheSettingsSchemaIsClosedAndHasNoSettingForLocalAndVirtual: only authUrl, clientId and fetchDocumentation exist, all for
// remote repositories, and unknown fields are refused.
func TestR_ANS_001_TheSettingsSchemaIsClosedAndHasNoSettingForLocalAndVirtual(t *testing.T) {
	var s struct {
		AdditionalProperties bool `json:"additionalProperties"`
		Properties           map[string]struct {
			Classes []string `json:"x-trove-classes"`
			Default any      `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(settingsSchema, &s); err != nil {
		t.Fatal(err)
	}
	if s.AdditionalProperties || len(s.Properties) != 3 {
		t.Fatalf("schema = %+v", s)
	}
	for _, k := range []string{"authUrl", "clientId", "fetchDocumentation"} {
		if p := s.Properties[k]; len(p.Classes) != 1 || p.Classes[0] != "remote" {
			t.Errorf("%s applies to %v, want remote only", k, p.Classes)
		}
	}
	if s.Properties["clientId"].Default != DefaultClientID {
		t.Errorf("clientId default = %v", s.Properties["clientId"].Default)
	}
	if s.Properties["fetchDocumentation"].Default != true {
		t.Errorf("fetchDocumentation default = %v", s.Properties["fetchDocumentation"].Default)
	}
}
