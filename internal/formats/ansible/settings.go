package ansible

import (
	_ "embed" // the settings schema
	"encoding/json"
)

// settingsSchema is the JSON Schema 2020-12 of the ansible member of a repository document (R-ARCH-008, R-ANS-019). The repository
// service validates the member against it when a document is written, and GET /api/v1/formats serves it to the create form. Every
// setting is for remote repositories; local and virtual repositories have none.
//
//go:embed settings.schema.json
var settingsSchema []byte

// DefaultClientID is ansible.clientId while the document leaves it out (R-ANS-019).
const DefaultClientID = "cloud-services"

// Settings are the members of the ansible section of a repository document, with the defaults for what the document leaves out.
type Settings struct {
	// AuthURL is ansible.authUrl: the token endpoint of the hub's realm, "" for an upstream called with its credential as it is.
	AuthURL string
	// ClientID is ansible.clientId.
	ClientID string
	// FetchDocumentation is ansible.fetchDocumentation: whether the documentation of a remote's versions is fetched and kept (R-ANS-035,
	// R-ANS-042). It is true while the document leaves it out.
	FetchDocumentation bool
}

// SettingsOf reads the ansible section of a stored repository document. The service refuses a value the schema does not allow, so a
// document that does not parse can only come from a row written around the service; it yields the defaults.
func SettingsOf(config []byte) Settings {
	out := Settings{ClientID: DefaultClientID, FetchDocumentation: true}
	var doc struct {
		Ansible struct {
			AuthURL            string `json:"authUrl"`
			ClientID           string `json:"clientId"`
			FetchDocumentation *bool  `json:"fetchDocumentation"`
		} `json:"ansible"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return out
	}
	out.AuthURL = doc.Ansible.AuthURL
	if doc.Ansible.ClientID != "" {
		out.ClientID = doc.Ansible.ClientID
	}
	if doc.Ansible.FetchDocumentation != nil {
		out.FetchDocumentation = *doc.Ansible.FetchDocumentation
	}
	return out
}
