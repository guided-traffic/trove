package webui

import (
	"fmt"
	"net/url"
	"strings"
)

// buildCSP returns the Content-Security-Policy of UI documents with a %s slot for the per-response
// nonce (R-UI-001). Each image origin is preceded by a space; no wildcard is ever emitted.
func buildCSP(imageOrigins []string) (string, error) {
	var img strings.Builder
	for _, origin := range imageOrigins {
		if err := validateOrigin(origin); err != nil {
			return "", fmt.Errorf("webui: image origin %q: %w", origin, err)
		}
		img.WriteString(" ")
		img.WriteString(origin)
	}
	return "default-src 'self'; script-src 'self'; style-src 'self' 'nonce-%s'; " +
		"img-src 'self' data:" + img.String() + "; font-src 'self'; connect-src 'self'; " +
		"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; " +
		"require-trusted-types-for 'script'; trusted-types angular angular#bundler", nil
}

// validateOrigin accepts exactly scheme://host[:port] with an https scheme, no path, no
// wildcard and no credentials.
func validateOrigin(origin string) error {
	if strings.ContainsAny(origin, "*; ,'\"\r\n") {
		return fmt.Errorf("contains a character that is not allowed in an origin")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("must be an https origin of the form https://host[:port]")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("host is empty")
	}
	return nil
}
