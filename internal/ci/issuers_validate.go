package ci

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// The validation and the defaults of an issuer (R-CI-001, R-CI-002). The functions here are pure: the
// service calls them before it opens a transaction or contacts the issuer, and `trove migrate` calls the
// same ones for a declared issuer, so the API and the configuration agree on what an issuer is.

var issuerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// maxDescription bounds the free text of an issuer.
const maxDescription = 1024

// gitlabPinClaims are the immutable ID claims a gitlab issuer may pin on, and its default (R-CI-001):
// a path or name claim in pin_claims would satisfy the pinning rule with the claims that rule exists to
// exclude.
func gitlabPinClaims() []string {
	return []string{"project_id", "namespace_id", "job_project_id", "job_namespace_id"}
}

// gitlabRequiredClaims is the preset of required_claims of a gitlab issuer (R-CI-006): the claims the
// table marks as required in the preset.
func gitlabRequiredClaims() []string {
	return []string{
		"iss", "aud", "exp", "iat", "jti", "sub", "project_id", "namespace_id", "project_path", "namespace_path",
		"ref", "ref_type", "ref_protected", "pipeline_source", "pipeline_id", "job_id", "user_id", "user_login",
	}
}

// issuerLimits are the installation facts the defaults and bounds of an issuer come from.
type issuerLimits struct {
	// BaseURL is server.base_url; the default audiences are made from it.
	BaseURL string
	// TokenMaxTTL and TokenMinTTL are ci.token.max_ttl and ci.token.min_ttl, the bounds of max_ttl_seconds: a bound
	// below the floor would refuse every exchange of the issuer with ttl_below_minimum (R-CI-016, Q-CI-47). A zero
	// TokenMinTTL bounds at 1 s.
	TokenMaxTTL, TokenMinTTL time.Duration
}

// publicByConstruction reports whether an issuer URL is public by construction: anyone can obtain a
// token from it, so public_issuer false is refused (R-CI-001).
func publicByConstruction(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "gitlab.com", "token.actions.githubusercontent.com":
		return true
	}
	return false
}

// defaultAudiences are the audiences of an issuer created without any (R-CI-001): the base URL and
// npm: followed by its host name as new URL(...).hostname yields it, without the port.
func defaultAudiences(baseURL string) ([]string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("server.base_url %q has no host name", baseURL)
	}
	return []string{baseURL, "npm:" + u.Hostname()}, nil
}

func field(pointer, code, detail string) errs.FieldError {
	return errs.FieldError{Pointer: pointer, Code: code, Detail: detail}
}

// managementCodes are the codes of R-CI-002 that are the top-level code of a 422 when one field carries
// them; any other field error answers validation_failed with errors[].
func managementCodes() []string {
	return []string{
		string(reason.PinClaimNotImmutable), string(reason.PublicIssuerRequired), string(reason.KindNotAvailable),
		string(reason.JobTokenFallbackNotAvailable),
	}
}

// validationError builds the 422 of a list of field errors, in the shape of the rest of the management
// API (R-API-009, Q-CI-30): errors[] lists every violation with its pointer and code, and a rule that the
// specification names by its code (the codes of managementCodes) is also the problem's `code`; the
// first such violation decides it. Any other violation answers validation_failed.
func validationError(fields []errs.FieldError) error {
	if len(fields) == 0 {
		return nil
	}
	code, detail := errs.CodeValidation, fields[0].Detail
	if len(fields) > 1 {
		detail = fmt.Sprintf("%d fields are invalid.", len(fields))
	}
	for _, f := range fields {
		if slices.Contains(managementCodes(), f.Code) {
			code, detail = f.Code, f.Detail
			break
		}
	}
	return errs.E(errs.Invalid, code, detail, errs.WithFields(fields...))
}

// buildIssuer applies the defaults of R-CI-001 to an input and checks every rule of R-CI-001 and
// R-CI-002 that needs no database: the name, the closed kind, the https URLs, the algorithms, the
// bounds, the pin claims, the public issuers and the P2 fallback. A nil optional member takes its
// default, so a PUT is a full replacement. The result has no ID, version or source.
func buildIssuer(in IssuerInput, lim issuerLimits) (Issuer, []errs.FieldError) {
	var fe []errs.FieldError
	is := Issuer{Name: in.Name, Kind: in.Kind, Issuer: in.Issuer, Description: in.Description}

	if !issuerNamePattern.MatchString(in.Name) {
		fe = append(fe, field("/name", "invalid_name", "The name matches ^[a-z0-9][a-z0-9._-]{1,63}$."))
	}
	switch in.Kind {
	case KindGitLab:
	case KindGitHub, KindGeneric:
		fe = append(fe, field("/kind", string(reason.KindNotAvailable), "The kinds github and generic are not available in this release."))
	default:
		fe = append(fe, field("/kind", "invalid_kind", "The kind is one of the literals gitlab, github and generic."))
	}
	if err := checkIssuerURL(in.Issuer); err != "" {
		fe = append(fe, field("/issuer", "invalid_issuer", err))
	}
	if utf8.RuneCountInString(in.Description) > maxDescription {
		fe = append(fe, field("/description", "invalid_description", fmt.Sprintf("The description has at most %d characters.", maxDescription)))
	}
	if v := derefStr(in.DiscoveryURL); v != "" {
		if !isHTTPS(v) {
			fe = append(fe, field("/discoveryUrl", "invalid_url", "The discovery URL is an https URL."))
		}
		is.DiscoveryURL = &v
	}
	if v := derefStr(in.JWKSURI); v != "" {
		if !isHTTPS(v) {
			fe = append(fe, field("/jwksUri", "invalid_url", "The key set URL is an https URL."))
		}
		is.JWKSURI = &v
	}

	fe = append(fe, buildPolicy(in, lim, &is)...)
	is.SingleUseSubjectTokens = pick(in.SingleUseSubjectTokens, false)

	if in.MaxTTLSeconds != nil {
		v := *in.MaxTTLSeconds
		floor := max(lim.TokenMinTTL, time.Second)
		if d := time.Duration(v) * time.Second; d < floor || d > lim.TokenMaxTTL {
			fe = append(fe, field("/maxTtlSeconds", "invalid_max_ttl",
				fmt.Sprintf("The bound is ci.token.min_ttl (%d) to ci.token.max_ttl (%d) seconds, or null.",
					int64(floor/time.Second), int64(lim.TokenMaxTTL/time.Second))))
		}
		is.MaxTTLSeconds = &v
	}
	if ref := derefStr(in.CABundleRef); ref != "" {
		if err := validBundleRef(ref); err != nil {
			fe = append(fe, field("/caBundleRef", "invalid_ca_bundle_ref", err.Error()))
		}
		is.CABundleRef = &ref
	}
	is.UseOutboundProxy = pick(in.UseOutboundProxy, false)
	if in.JobTokenFallback != nil && in.JobTokenFallback.Enabled {
		fe = append(fe, field("/jobTokenFallback/enabled", string(reason.JobTokenFallbackNotAvailable),
			"The CI_JOB_TOKEN fallback is not built in this release."))
	}
	is.Enabled = pick(in.Enabled, true)
	return is, fe
}

// buildPolicy applies the defaults and rules of the members that decide which tokens are accepted: the
// audiences, the algorithms, the typ values, the clock skew, the required claims, the public flag and the
// pin claims. It fills is and returns the violations.
func buildPolicy(in IssuerInput, lim issuerLimits, is *Issuer) []errs.FieldError {
	var fe []errs.FieldError
	switch {
	case in.AllowedAudiences == nil:
		auds, err := defaultAudiences(lim.BaseURL)
		if err != nil {
			fe = append(fe, field("/allowedAudiences", "required", "Name the audiences: server.base_url is not set, so there are no defaults."))
		}
		is.AllowedAudiences = auds
	case len(in.AllowedAudiences) == 0 || slices.Contains(in.AllowedAudiences, ""):
		fe = append(fe, field("/allowedAudiences", "invalid_audiences", "At least one audience, none of them empty."))
	default:
		is.AllowedAudiences = dedupe(in.AllowedAudiences)
	}
	is.ExclusiveAudience = pick(in.ExclusiveAudience, true)

	switch {
	case in.AllowedAlgorithms == nil:
		is.AllowedAlgorithms = []string{"RS256"}
	case len(in.AllowedAlgorithms) == 0:
		fe = append(fe, field("/allowedAlgorithms", "invalid_algorithms", "At least one algorithm."))
	default:
		for _, a := range in.AllowedAlgorithms {
			if !slices.Contains(config.CIAlgorithms(), a) {
				fe = append(fe, field("/allowedAlgorithms", "algorithm_not_allowed",
					fmt.Sprintf("%q is not accepted; the entries are %s, never none or an HMAC algorithm (RFC 8725).", a, strings.Join(config.CIAlgorithms(), ", "))))
				break
			}
		}
		is.AllowedAlgorithms = dedupe(in.AllowedAlgorithms)
	}
	switch {
	case in.AllowedTyp == nil:
		is.AllowedTyp = []string{"JWT"}
	case slices.Contains(in.AllowedTyp, ""):
		fe = append(fe, field("/allowedTyp", "invalid_typ", "A typ value is not empty."))
	default:
		is.AllowedTyp = dedupe(in.AllowedTyp)
	}
	is.AllowMissingTyp = pick(in.AllowMissingTyp, true)

	is.ClockSkewSeconds = 60
	if in.ClockSkewSeconds != nil {
		is.ClockSkewSeconds = *in.ClockSkewSeconds
		if is.ClockSkewSeconds < 0 || is.ClockSkewSeconds > 300 {
			fe = append(fe, field("/clockSkewSeconds", "invalid_clock_skew", "The clock skew is 0 to 300 seconds."))
		}
	}
	if in.RequiredClaims == nil {
		is.RequiredClaims = gitlabRequiredClaims()
	} else {
		is.RequiredClaims = dedupe(in.RequiredClaims)
		if slices.Contains(in.RequiredClaims, "") {
			fe = append(fe, field("/requiredClaims", "invalid_claims", "A claim name is not empty."))
		}
	}
	is.PublicIssuer = pick(in.PublicIssuer, true)
	if !is.PublicIssuer && publicByConstruction(in.Issuer) {
		fe = append(fe, field("/publicIssuer", string(reason.PublicIssuerRequired),
			"This issuer URL is public by construction: anyone can obtain a token from it, so it is a public issuer."))
	}
	if in.PinClaims == nil {
		is.PinClaims = gitlabPinClaims()
	} else {
		is.PinClaims = dedupe(in.PinClaims)
		for _, c := range in.PinClaims {
			if !slices.Contains(gitlabPinClaims(), c) {
				fe = append(fe, field("/pinClaims", string(reason.PinClaimNotImmutable),
					fmt.Sprintf("%q is not an immutable ID claim; pin_claims names %s.", c, strings.Join(gitlabPinClaims(), ", "))))
				break
			}
		}
	}
	return fe
}

// checkIssuerURL returns the reason an issuer URL is refused, "" when it is an https URL. The URL is
// compared exactly with `iss` (R-CI-001), so it is stored as written: the check refuses what could never
// equal an `iss` or would smuggle a credential into a URL.
func checkIssuerURL(raw string) string {
	if !strings.HasPrefix(raw, "https://") {
		return "The issuer is an https URL."
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Hostname() == "":
		return "The issuer is not a URL with a host name."
	case u.User != nil || u.Fragment != "" || u.RawQuery != "" || strings.ContainsAny(raw, " \t\r\n"):
		return "The issuer has no user information, query, fragment or white space."
	}
	return ""
}

func pick(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// dedupe returns the list without repeated entries, in order.
func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
