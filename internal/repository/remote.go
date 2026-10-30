package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
)

// The settings of a remote repository (R-REPO-024) live in the `remote` section of
// the configuration document. The section is stored in repositories.config in its
// canonical form: every member present, the defaults of the format catalog
// applied, the URL normalized, and every secret field reduced to the marker
// {"set": true} or to its reference. The secret values themselves are in
// repository_secrets, sealed (R-REPO-033, secrets_store.go).

// The values of the enumerated members.
const (
	AuthNone   = "none"
	AuthBasic  = "basic"
	AuthBearer = "bearer"

	// FillRead and FillDeploy are the values of cacheFillAction: the action a
	// caller needs to make the repository fetch from the upstream (R-AUTHZ-008).
	FillRead   = "read"
	FillDeploy = "deploy"

	ChecksumVerifyIfAvailable = "verify-if-available"
	ChecksumRequire           = "require"
	ChecksumIgnore            = "ignore"
	ChecksumPassThrough       = "pass-through"

	AvailabilityOnline  = "online"
	AvailabilityOffline = "offline"

	ProxyDefault = "default"
	ProxyDirect  = "direct"
	ProxyNamed   = "named"
)

// RemoteSettings is the effective `remote` section of a remote repository: what
// the stored document holds, parsed once per snapshot entry (Repository.Remote).
// The JSON form is the stored and the returned one, so a secret field never
// carries its value in it.
type RemoteSettings struct {
	URL        string      `json:"url"`
	ExtraHosts []ExtraHost `json:"extraHosts"`
	AuthMode   string      `json:"authMode"`
	Username   string      `json:"username"`
	// Password is nil when unset. In the stored form it is {"set": true} or a
	// reference; the value of a request that carries one is only in Value.
	Password *SecretField `json:"password"`
	// ClientCertificate and Headers arrive with R-REPO-034 and R-REPO-036 (P1); a
	// document that sets them is refused with not_applicable until then.
	ClientCertificate     *string         `json:"clientCertificate"`
	Headers               []Header        `json:"headers"`
	ForwardAuthOnRedirect bool            `json:"forwardAuthOnRedirect"`
	OutboundProxy         OutboundProxy   `json:"outboundProxy"`
	ConnectTimeoutMs      int             `json:"connectTimeoutMs"`
	ReadTimeoutMs         int             `json:"readTimeoutMs"`
	SourceNetwork         *netip.Prefix   `json:"sourceNetwork"`
	CacheFillAction       string          `json:"cacheFillAction"`
	Cache                 RemoteCache     `json:"cache"`
	Integrity             RemoteIntegrity `json:"integrity"`
	Availability          Availability    `json:"availability"`
	Limits                RemoteLimits    `json:"limits"`
}

// ExtraHost is an entry of remote.extraHosts: a host name pattern (path.Match
// syntax over the lower-case host, for example "*.nuget.org") that upstream
// metadata or redirects may point to (R-REPO-043). SendCredentials lets the
// upstream credentials go there too (R-REPO-033).
type ExtraHost struct {
	Pattern         string `json:"pattern"`
	SendCredentials bool   `json:"sendCredentials"`
}

// Header is a custom upstream request header (R-REPO-036, P1). The member exists
// so that a document naming headers is read strictly and refused with a reason.
type Header struct {
	Name      string          `json:"name"`
	Value     json.RawMessage `json:"value"`
	Sensitive bool            `json:"sensitive"`
}

// OutboundProxy selects the outbound proxy of the repository (R-REPO-037): default
// resolves per request, direct uses none. Named proxies are P1.
type OutboundProxy struct {
	Mode string  `json:"mode"`
	Name *string `json:"name,omitempty"`
}

// RemoteCache is remote.cache (R-REPO-027 to R-REPO-029, R-REPO-044, R-REPO-046).
type RemoteCache struct {
	StoreArtifacts                bool `json:"storeArtifacts"`
	MetadataTTLSeconds            int  `json:"metadataTtlSeconds"`
	MetadataRefreshTimeoutSeconds int  `json:"metadataRefreshTimeoutSeconds"`
	NotFoundTTLSeconds            int  `json:"notFoundTtlSeconds"`
	MaxStaleSeconds               int  `json:"maxStaleSeconds"`
	UnusedRetentionHours          int  `json:"unusedRetentionHours"`
}

// RemoteIntegrity is remote.integrity (R-REPO-041, R-REPO-042).
type RemoteIntegrity struct {
	ChecksumPolicy              string   `json:"checksumPolicy"`
	BlockUnexpectedContentTypes bool     `json:"blockUnexpectedContentTypes"`
	AllowedContentTypes         []string `json:"allowedContentTypes"`
}

// Availability is remote.availability (R-REPO-029, R-REPO-030).
type Availability struct {
	Mode               string `json:"mode"`
	FailureThreshold   int    `json:"failureThreshold"`
	CircuitOpenSeconds int    `json:"circuitOpenSeconds"`
}

// RemoteLimits is remote.limits (R-REPO-040). A nil size limit means the system
// value (proxy.max_artifact_bytes, proxy.max_metadata_bytes).
type RemoteLimits struct {
	MaxArtifactBytes     *int64 `json:"maxArtifactBytes"`
	MaxMetadataBytes     *int64 `json:"maxMetadataBytes"`
	MaxConcurrentFetches int    `json:"maxConcurrentFetches"`
}

// SecretRef points at a secret that lives outside the database (R-REPO-033,
// R-OPS-029): a file directly under config_as_code.secrets_dir, or an
// environment variable named TROVE_CAC_<NAME>. Exactly one member is set.
type SecretRef struct {
	File string `json:"file,omitempty"`
	Env  string `json:"env,omitempty"`
}

// SecretField is a secret member of the document (R-REPO-033). A request carries
// either {"value": …}, which is sealed at once, or {"secretRef": …}; a read shows
// {"set": true} for a sealed value and the reference itself. A client that sends
// back what it read therefore keeps the secret and neither blanks nor leaks it.
type SecretField struct {
	// Value is the plaintext of a request. It is never part of the JSON form.
	Value *observability.Secret
	Ref   *SecretRef
	// Set is the read form of a sealed value, and the request form that keeps the
	// stored one.
	Set bool

	// badSet records "set": false or a request with several forms, which the
	// validation reports as invalid_secret.
	badSet bool
}

// MarshalJSON renders the read form; the plaintext is never written.
func (f SecretField) MarshalJSON() ([]byte, error) {
	if f.Ref != nil {
		return json.Marshal(struct {
			SecretRef *SecretRef `json:"secretRef"`
		}{f.Ref})
	}
	return []byte(`{"set":true}`), nil
}

// UnmarshalJSON reads a request or a stored form. Unknown members are errors.
func (f *SecretField) UnmarshalJSON(b []byte) error {
	var w struct {
		Value     *string    `json:"value"`
		SecretRef *SecretRef `json:"secretRef"`
		Set       *bool      `json:"set"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return err
	}
	*f = SecretField{Ref: w.SecretRef}
	if w.Value != nil {
		v := observability.Secret(*w.Value)
		f.Value = &v
	}
	if w.Set != nil {
		if !*w.Set {
			f.badSet = true
		}
		f.Set = *w.Set
	}
	return nil
}

var (
	secretFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
	secretEnvName  = regexp.MustCompile(`^TROVE_CAC_[A-Z0-9_]{1,200}$`)
)

// SecretEnvPrefix is the prefix of the only environment variables a secretRef may
// name, so that trove's own secrets can never be routed to an upstream
// (R-REPO-033, R-OPS-029).
const SecretEnvPrefix = "TROVE_CAC_" //nolint:gosec // G101: the prefix of a variable name, not a credential

// Valid reports whether the reference follows the rules of R-OPS-029: exactly one
// of a file name without path separators and without "..", and a variable with the
// TROVE_CAC_ prefix. The reason is empty when it does.
func (r SecretRef) Valid() (reason string) {
	switch {
	case r.File != "" && r.Env != "":
		return "name either file or env, not both"
	case r.File == "" && r.Env == "":
		return "name a file or an env variable"
	case r.File != "":
		if !secretFileName.MatchString(r.File) || strings.Contains(r.File, "..") {
			return "file must be a plain name directly under config_as_code.secrets_dir: no path separators, no \"..\""
		}
	case !secretEnvName.MatchString(r.Env):
		return "env must be a variable named " + SecretEnvPrefix + "<NAME>; trove's own variables cannot be referenced"
	}
	return ""
}

// remoteInput is the request form of the `remote` section: every member a
// pointer or a nil-able slice, so that an omitted member takes its default. The
// shape is checked strictly when a document is decoded (checkRemoteShape); the
// values are checked by validateRemote.
type remoteInput struct {
	URL                   *string               `json:"url"`
	ExtraHosts            []ExtraHost           `json:"extraHosts"`
	AuthMode              *string               `json:"authMode"`
	Username              *string               `json:"username"`
	Password              *SecretField          `json:"password"`
	ClientCertificate     *string               `json:"clientCertificate"`
	Headers               []Header              `json:"headers"`
	ForwardAuthOnRedirect *bool                 `json:"forwardAuthOnRedirect"`
	OutboundProxy         *outboundProxyInput   `json:"outboundProxy"`
	ConnectTimeoutMs      *int                  `json:"connectTimeoutMs"`
	ReadTimeoutMs         *int                  `json:"readTimeoutMs"`
	SourceNetwork         *string               `json:"sourceNetwork"`
	CacheFillAction       *string               `json:"cacheFillAction"`
	Cache                 *remoteCacheInput     `json:"cache"`
	Integrity             *remoteIntegrityInput `json:"integrity"`
	Availability          *availabilityInput    `json:"availability"`
	Limits                *remoteLimitsInput    `json:"limits"`
}

type outboundProxyInput struct {
	Mode *string `json:"mode"`
	Name *string `json:"name"`
}

type remoteCacheInput struct {
	StoreArtifacts                *bool `json:"storeArtifacts"`
	MetadataTTLSeconds            *int  `json:"metadataTtlSeconds"`
	MetadataRefreshTimeoutSeconds *int  `json:"metadataRefreshTimeoutSeconds"`
	NotFoundTTLSeconds            *int  `json:"notFoundTtlSeconds"`
	MaxStaleSeconds               *int  `json:"maxStaleSeconds"`
	UnusedRetentionHours          *int  `json:"unusedRetentionHours"`
}

type remoteIntegrityInput struct {
	ChecksumPolicy              *string  `json:"checksumPolicy"`
	BlockUnexpectedContentTypes *bool    `json:"blockUnexpectedContentTypes"`
	AllowedContentTypes         []string `json:"allowedContentTypes"`
}

type availabilityInput struct {
	Mode               *string `json:"mode"`
	FailureThreshold   *int    `json:"failureThreshold"`
	CircuitOpenSeconds *int    `json:"circuitOpenSeconds"`
}

type remoteLimitsInput struct {
	MaxArtifactBytes     *int64 `json:"maxArtifactBytes"`
	MaxMetadataBytes     *int64 `json:"maxMetadataBytes"`
	MaxConcurrentFetches *int   `json:"maxConcurrentFetches"`
}

// decodeRemote reads the `remote` section strictly: an object without a duplicate
// or an unknown member (R-REPO-008). An absent section is the empty one.
func decodeRemote(raw json.RawMessage) (remoteInput, error) {
	var in remoteInput
	if len(raw) == 0 {
		return in, nil
	}
	if err := decodeStrict(raw, &in); err != nil {
		return remoteInput{}, err
	}
	return in, nil
}

// checkRemoteShape is the 400 half of the validation of a remote section: its
// members are known and typed. The values follow in validate.
func checkRemoteShape(raw json.RawMessage) error {
	_, err := decodeRemote(raw)
	if e, ok := errs.As(err); ok {
		return invalidDocument("the remote section: %s", e.Detail)
	}
	return err
}

// Remote returns the parsed `remote` section of a remote repository: the effective
// settings, every member present. It is false for a repository of another class
// and for one whose stored section does not load (such a repository is disabled).
func (r Repository) Remote() (RemoteSettings, bool) {
	if r.remote == nil {
		return RemoteSettings{}, false
	}
	return *r.remote, true
}

// parseStoredRemote reads the canonical section of a stored document.
func parseStoredRemote(raw json.RawMessage) (*RemoteSettings, error) {
	var s RemoteSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if _, err := url.Parse(s.URL); err != nil {
		return nil, err
	}
	return &s, nil
}

// upstreamIdentityChanged reports whether an update changed what the cached
// answers of a remote repository are evidence of (R-REPO-011): the upstream, the
// hosts it may point to, or the credentials it is asked with.
func upstreamIdentityChanged(before, after Document, secretWritten bool) bool {
	if secretWritten {
		return true
	}
	b, errB := parseStoredRemote(before.Remote)
	a, errA := parseStoredRemote(after.Remote)
	if errB != nil || errA != nil {
		return true // what cannot be compared is treated as changed
	}
	return b.URL != a.URL || b.AuthMode != a.AuthMode || b.Username != a.Username ||
		!reflect.DeepEqual(b.ExtraHosts, a.ExtraHosts) || !reflect.DeepEqual(b.ClientCertificate, a.ClientCertificate) ||
		!reflect.DeepEqual(b.Headers, a.Headers) || !reflect.DeepEqual(b.Password, a.Password)
}

// upstreamChanged calls the invalidation hook inside the transaction of an update.
func (s *Service) upstreamChanged(ctx context.Context, tx meta.Tx, c Caller, before, after Document, repo Repository) error {
	hook := s.remoteDeps().OnUpstreamChanged
	if hook == nil || !upstreamIdentityChanged(before, after, hasSecretWrites(after.pending)) {
		return nil
	}
	return hook(ctx, tx, c, repo)
}
