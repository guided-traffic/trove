package generic

import "github.com/guided-traffic/trove/internal/formats"

// ClientCurl is the one client of the format (Q-GENERIC-8).
const ClientCurl = "curl"

// CurlFunction is the name of the shell function the configure snippet defines and
// the other snippets and the guide call.
const CurlFunction = "trove_curl"

// SetupTemplates are the client setup snippets of R-GENERIC-016 for curl in a POSIX
// shell. The configure snippet defines the function trove_curl, which hands curl the
// credential as configuration on standard input (--config - with a here-document),
// so that the token never becomes an argument of curl, visible in the process list
// and in a shell trace (Q-GENERIC-9). The header is the one line that holds
// ${TROVE_TOKEN}, so credentials=none drops it and leaves a function that sends no
// credential. Deleting has no tab of its own (Q-API-160); the guide names it.
func (Format) SetupTemplates() []formats.SetupTemplate {
	const function = CurlFunction + `() {
  curl --silent --show-error --config - "$@" <<EOF
header = "Authorization: Bearer ${TROVE_TOKEN}"
EOF
}
`
	return []formats.SetupTemplate{
		{ID: "curl.function.configure", Client: ClientCurl, Section: "configure", Lang: "shell", Template: function},
		{ID: "curl.install", Client: ClientCurl, Section: "install", Lang: "shell", Template: CurlFunction + ` --fail --remote-name "{{.RegistryURL}}<path>"` + "\n"},
		{ID: "curl.publish", Client: ClientCurl, Section: "publish", Lang: "shell", Template: CurlFunction + ` --fail --upload-file <file> "{{.RegistryURL}}<path>"` + "\n"},
	}
}
