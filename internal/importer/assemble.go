package importer

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
)

// Authentication modes of the source, the values of a remote repository's authMode (R-REPO-033).
const (
	AuthNone   = "none"
	AuthBasic  = "basic"
	AuthBearer = "bearer"
)

// Params is what the command line gives (R-IMP-001, R-IMP-002).
type Params struct {
	// Format is "npm", "nuget" or "ansible".
	Format string
	// From is the source: the registry address of npm, the service index of NuGet, the API root of a Galaxy. To is the target repository's address
	// in the same form, as a package manager is given it.
	From, To string
	// SourceAuth, SourceUsername and SourcePassword are the credential for the source; the password is the token for bearer.
	SourceAuth, SourceUsername, SourcePassword string
	// ExtraHosts are host patterns, besides the source's origin, that may serve files; they are asked without a credential.
	ExtraHosts []string
	// Token is the trove token the target is written with.
	Token string
	// Rate is the requests per second towards the source, 0 for no limit.
	Rate float64
}

// FormatByID returns the format of the importer for a command-line name.
func FormatByID(id string) (Format, error) {
	switch id {
	case "npm":
		return NPM{}, nil
	case "nuget":
		return &NuGet{}, nil
	case "ansible":
		return &Ansible{}, nil
	}
	return nil, fmt.Errorf("the format %q is not supported; the formats are npm, nuget and ansible", id)
}

// Build checks the addresses with the rule of a remote repository's upstream URL and builds the two clients from the
// outbound factory, so that the trust store, the egress address policy, the timeouts and the redirect policy of the
// factory apply to both (R-IMP-002). The credential of the source goes to its origin only, by the hop policy of a remote
// repository; the token goes to the target's origin only.
func Build(f *outbound.Factory, p Params) (Format, *SourceClient, *TargetClient, error) {
	format, err := FormatByID(p.Format)
	if err != nil {
		return nil, nil, nil, err
	}
	from, why := repository.NormalizeUpstreamURL(p.From)
	if why != "" {
		return nil, nil, nil, fmt.Errorf("the source address %s", why)
	}
	to, why := repository.NormalizeUpstreamURL(p.To)
	if why != "" {
		return nil, nil, nil, fmt.Errorf("the target address %s", why)
	}
	fromURL, _ := url.Parse(from)
	toURL, _ := url.Parse(to)
	if toURL.RawQuery != "" || fromURL.RawQuery != "" {
		return nil, nil, nil, fmt.Errorf("an address must not carry a query")
	}
	if p.Token == "" {
		return nil, nil, nil, fmt.Errorf("no token for the target: give --token-file or set TROVE_IMPORT_TOKEN")
	}
	var srcHeaders func() http.Header
	switch p.SourceAuth {
	case "", AuthNone:
	case AuthBasic:
		if p.SourcePassword == "" {
			return nil, nil, nil, fmt.Errorf("--source-auth basic needs a password: --source-password-file or TROVE_IMPORT_SOURCE_PASSWORD")
		}
		value := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.SourceUsername+":"+p.SourcePassword))
		srcHeaders = func() http.Header { return http.Header{"Authorization": {value}} }
	case AuthBearer:
		if p.SourcePassword == "" {
			return nil, nil, nil, fmt.Errorf("--source-auth bearer needs a token: --source-password-file or TROVE_IMPORT_SOURCE_PASSWORD")
		}
		value := "Bearer " + p.SourcePassword
		srcHeaders = func() http.Header { return http.Header{"Authorization": {value}} }
	default:
		return nil, nil, nil, fmt.Errorf("--source-auth %q: want none, basic or bearer", p.SourceAuth)
	}
	for _, h := range p.ExtraHosts {
		if h = strings.TrimSpace(h); h == "" || strings.ContainsAny(h, "/: ") {
			return nil, nil, nil, fmt.Errorf("--source-extra-host %q is not a host name pattern", h)
		}
	}
	srcHTTP := f.Client(outbound.PurposeRemote, outbound.WithHopPolicy(&outbound.CredentialPolicy{Origin: fromURL, Headers: srcHeaders}))
	token := p.Token
	dstHeaders := func() http.Header {
		h := http.Header{"Authorization": {"Bearer " + token}}
		if p.Format == "nuget" {
			h.Set("X-NuGet-ApiKey", token)
		}
		return h
	}
	dstHTTP := f.Client(outbound.PurposeRemote, outbound.WithHopPolicy(&outbound.CredentialPolicy{Origin: toURL, Headers: dstHeaders}))
	return format,
		&SourceClient{HTTP: srcHTTP, Origin: fromURL, Extra: p.ExtraHosts, Limiter: NewLimiter(p.Rate)},
		&TargetClient{HTTP: dstHTTP, Base: toURL},
		nil
}
