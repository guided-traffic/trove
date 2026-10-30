package config

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

// UI is the ui section: what the admin UI and the package screens read from the
// server (R-UI-008, R-API-030). The locales are not configuration yet
// (internal/app/misc.go).
type UI struct {
	Readme      UIReadme      `yaml:"readme"`
	ClientSetup UIClientSetup `yaml:"client_setup"`
}

// UIReadme bounds and shapes the README rendering (R-UI-008).
type UIReadme struct {
	MaxSourceBytes       ByteSize `yaml:"max_source_bytes" default:"512KiB" validate:"min=1KiB,max=16MiB" doc:"Largest README source that is rendered; a longer one is cut and the answer says so."`
	ExternalImageOrigins []string `yaml:"external_image_origins" default:"[]" doc:"Exact https origins (https://host[:port], no path, no wildcard) whose images a README may show. Every other external image is shown as a link. The same list becomes the img-src of the UI's Content-Security-Policy."`
}

// UIClientSetup shapes the snippets of the client setup (R-API-030).
type UIClientSetup struct {
	TokenEnvVar string        `yaml:"token_env_var" default:"TROVE_TOKEN" validate:"max=64" doc:"Name of the environment variable that snippets with credentials=env read the token from."`
	TokenTTL    time.Duration `yaml:"token_ttl" default:"720h" validate:"min=1m" doc:"Lifetime the setup dialog asks for when it creates a token; at most auth.tokens.pat.max_ttl."`
}

var envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks the image origins and the variable name. An origin is exactly
// https://host[:port]: the list feeds the Content-Security-Policy, so a path, a
// wildcard or a second directive in an entry would widen it (R-UI-008).
func (u *UI) Validate(r *Report) {
	for _, o := range u.Readme.ExternalImageOrigins {
		if err := validateImageOrigin(o); err != nil {
			r.Add("readme.external_image_origins", "%q: %v", o, err)
		}
	}
	if !envVarName.MatchString(u.ClientSetup.TokenEnvVar) {
		r.Add("client_setup.token_env_var", "%q is not an environment variable name ([A-Za-z_][A-Za-z0-9_]*)", u.ClientSetup.TokenEnvVar)
	}
}

func validateImageOrigin(s string) error {
	if strings.ContainsAny(s, "*; ,'\"\r\n") {
		return errString("contains a character that is not allowed in an origin")
	}
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errString("must be an https origin of the form https://host[:port]")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

// validateUICoupling checks the client setup's token lifetime against the ceiling
// of personal tokens: the dialog creates a personal token.
func (c *Config) validateUICoupling(r *Report) {
	if c.UI.ClientSetup.TokenTTL > c.Auth.Tokens.PAT.MaxTTL {
		r.Add("ui.client_setup.token_ttl", "(%s) must not exceed auth.tokens.pat.max_ttl (%s)", c.UI.ClientSetup.TokenTTL, c.Auth.Tokens.PAT.MaxTTL)
	}
	// An image of trove's own origin would make the viewer's browser send authenticated
	// requests to package or file endpoints (R-AUTHZ-008): the list may not name one.
	own := map[string]bool{}
	for _, b := range append([]string{c.Server.BaseURL}, c.Server.AdditionalBaseURLs...) {
		own[originOfURL(b)] = true
	}
	for _, e := range c.Server.Endpoints {
		own[originOfURL(e.BaseURL)] = true
	}
	delete(own, "")
	for _, o := range c.UI.Readme.ExternalImageOrigins {
		if own[originOfURL(o)] {
			r.Add("ui.readme.external_image_origins", "%q is an origin of this installation: a README image from it could make a viewer's browser send authenticated requests to trove (R-AUTHZ-008)", o)
		}
	}
}

// originOfURL is scheme://host[:port] in lower case with a default port dropped, "" for
// text that is no URL with a host.
func originOfURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host
}
