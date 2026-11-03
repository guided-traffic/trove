package nuget

import (
	_ "embed" // the settings schema
	"encoding/json"
)

// settingsSchema is the JSON Schema 2020-12 of the nuget member of a repository document
// (R-ARCH-008, R-NUGET-007, R-NUGET-016). The repository service validates the member
// against it when a document is written, and GET /api/v1/formats serves it to the create
// form. The settings of the symbol server and of the pass-through of vulnerability
// information (R-NUGET-022, R-NUGET-023) are added with their requirements; nuget.vulnerabilityInfo
// takes local and off until then (R-VULN-018).
//
//go:embed settings.schema.json
var settingsSchema []byte

// The values of deleteMode (R-NUGET-007).
const (
	DeleteModeUnlist = "unlist"
	DeleteModeDelete = "delete"
)

// settings are the members of the nuget section of a repository document. The repository
// service stores the section as the document carries it, validated against
// [settingsSchema]; the format reads it back from the stored document of the request's
// repository, with the defaults for what the document leaves out.
type settings struct {
	// DeleteMode is nuget.deleteMode of a local repository.
	DeleteMode string
	// ForwardSearch is nuget.forwardSearch of a remote repository.
	ForwardSearch bool
	// VulnerabilityInfo is nuget.vulnerabilityInfo of every class: local or off, "" when the document leaves it out, which
	// [vulnInfoModeOf] resolves because the default depends on the installation (R-VULN-018, Q-VULN-44).
	VulnerabilityInfo string
}

func defaultSettings() settings {
	return settings{DeleteMode: DeleteModeUnlist, ForwardSearch: true}
}

// settingsOf reads the nuget section of a stored repository document. The service refuses
// a value the schema does not allow, so a value that is none of the modes can only come
// from a row written around the service; it is the default.
func settingsOf(config []byte) settings {
	out := defaultSettings()
	var doc struct {
		NuGet struct {
			DeleteMode    string `json:"deleteMode"`
			ForwardSearch *bool  `json:"forwardSearch"`
			// VulnerabilityInfo is nuget.vulnerabilityInfo (R-VULN-018).
			VulnerabilityInfo string `json:"vulnerabilityInfo"`
		} `json:"nuget"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return out
	}
	switch doc.NuGet.DeleteMode {
	case DeleteModeUnlist, DeleteModeDelete:
		out.DeleteMode = doc.NuGet.DeleteMode
	}
	switch doc.NuGet.VulnerabilityInfo {
	case VulnInfoLocal, VulnInfoOff:
		out.VulnerabilityInfo = doc.NuGet.VulnerabilityInfo
	}
	if doc.NuGet.ForwardSearch != nil {
		out.ForwardSearch = *doc.NuGet.ForwardSearch
	}
	return out
}
