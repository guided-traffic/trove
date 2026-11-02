package oidcstub

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that the control API writes as "4s".
type Duration time.Duration

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON reads a string such as "250ms".
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration is a string such as \"4s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Redirect makes the answer a chain of redirects first.
type Redirect struct {
	// Hops is the number of 302 answers before the content (all to https on the same host).
	Hops int `json:"hops"`
	// ToHTTP makes the last answer a redirect to the plain-HTTP listener, which answers the
	// content: a client that follows it has left TLS.
	ToHTTP bool `json:"to_http,omitempty"`
}

// Fault is what the double does wrong. The zero value is a healthy issuer. Faults are switched
// at run time with SetFault or the control API and replace each other: one call sets the whole
// set.
type Fault struct {
	// Target limits Delay, Status, NotJSON, OversizeBytes and Redirect to "discovery" or
	// "jwks"; empty means both.
	Target string `json:"target,omitempty"`

	// Delay holds every answer back by this long (or until the client gives up).
	Delay Duration `json:"delay,omitempty"`
	// Unreachable closes every connection before the TLS handshake and refuses to answer on
	// kept-alive ones.
	Unreachable bool `json:"unreachable,omitempty"`
	// Status answers with this HTTP status and a short error body, for example 500.
	Status int `json:"status,omitempty"`
	// NotJSON answers 200 with an HTML body that contains NonJSONMarker.
	NotJSON bool `json:"not_json,omitempty"`
	// OversizeBytes answers 200 with a valid JSON document of at least this many bytes.
	OversizeBytes int `json:"oversize_bytes,omitempty"`
	// Redirect answers through a redirect chain.
	Redirect *Redirect `json:"redirect,omitempty"`

	// IssuerMismatch is the "issuer" the discovery document names instead of its own base URL.
	IssuerMismatch string `json:"issuer_mismatch,omitempty"`
	// JWKSURI is the "jwks_uri" of the discovery document, for example
	// "https://169.254.169.254/keys".
	JWKSURI string `json:"jwks_uri,omitempty"`
	// Algorithms replace id_token_signing_alg_values_supported.
	Algorithms []string `json:"algorithms,omitempty"`
	// NoCommonAlgorithm offers only HS256, which no CI issuer allows (algorithms_not_offered).
	NoCommonAlgorithm bool `json:"no_common_algorithm,omitempty"`
}

// The two endpoints a Fault can target.
const (
	TargetDiscovery = "discovery"
	TargetJWKS      = "jwks"
)

// NonJSONMarker is in the body of the NotJSON fault. A test asserts that it appears nowhere in
// what trove records about the failure (R-CI-003: no byte of a body in last_error).
const NonJSONMarker = "oidcstub-body-marker-4f9c1e"

func (f Fault) validate() error {
	switch f.Target {
	case "", TargetDiscovery, TargetJWKS:
	default:
		return fmt.Errorf("target %q: use %q, %q or empty", f.Target, TargetDiscovery, TargetJWKS)
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		return fmt.Errorf("status %d is not an HTTP status", f.Status)
	}
	if f.Delay < 0 || f.OversizeBytes < 0 || (f.Redirect != nil && f.Redirect.Hops < 0) {
		return fmt.Errorf("delay, oversize_bytes and hops must not be negative")
	}
	return nil
}

// applies reports whether the response faults hit the endpoint.
func (f Fault) applies(target string) bool { return f.Target == "" || f.Target == target }

// Cache is the caching the JWKS answer announces.
type Cache struct {
	// NoHeader leaves Cache-Control out (trove then uses its default refresh interval).
	NoHeader bool `json:"no_header,omitempty"`
	// MaxAge is the max-age of Cache-Control.
	MaxAge Duration `json:"max_age"`
	// ETag adds an ETag over the key set and answers If-None-Match with 304.
	ETag bool `json:"etag,omitempty"`
}

// Counts are the requests the issuer endpoints received since the last Reset.
type Counts struct {
	// Discovery and JWKS count requests on the TLS listener, answered or faulted.
	Discovery int `json:"discovery"`
	JWKS      int `json:"jwks"`
	// Hops counts the requests that arrived through a redirect (Fault.Redirect); they are not
	// counted again as Discovery or JWKS, so one fetch is one count however long the chain is.
	Hops int `json:"hops"`
	// NotModified counts the JWKS requests answered 304.
	NotModified int `json:"not_modified"`
	// Plain counts requests on the plain-HTTP listener: a client that got there followed a
	// redirect out of TLS.
	Plain int `json:"plain"`
	// Other counts requests to any other path.
	Other int `json:"other"`
	// Refused counts connections closed by the Unreachable fault.
	Refused int `json:"refused"`
}
