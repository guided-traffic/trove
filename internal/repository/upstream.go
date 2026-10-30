package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/observability"
)

// The upstream test of a remote repository (R-REPO-024): one request to the
// format's probe resource through the client, the egress rules, the proxy and the
// credentials that real fetches use. The probing itself is in
// internal/proxy/upstream, which imports this package; the service reaches it
// through [UpstreamProber], bound by the composition root.

// UpstreamTestResult is the answer of both upstream-test operations. A probe that
// fails is a 200 with Reachable false and the reason in Error: the question the
// operation answers is whether the upstream works, and "no" is an answer.
type UpstreamTestResult struct {
	// Reachable is true when the upstream answered with an HTTP response, whatever
	// its status.
	Reachable  bool `json:"reachable"`
	HTTPStatus *int `json:"httpStatus"`
	// Authenticated is false for 401 and 403, the upstream's refusal of the
	// credentials sent (or of none), and true for any other response.
	Authenticated *bool `json:"authenticated"`
	DurationMs    int64 `json:"durationMs"`
	// TLS describes the certificate the upstream presented; nil over plain http.
	TLS *UpstreamTLS `json:"tls"`
	// OutboundProxy names the proxy the request went through; always nil until
	// named proxies exist (R-REPO-037).
	OutboundProxy *string `json:"outboundProxy"`
	// SourceAddress is the local address the probing replica bound (R-REPO-024,
	// sourceNetwork); nil when the kernel chose.
	SourceAddress *string            `json:"sourceAddress"`
	Error         *UpstreamTestError `json:"error"`
}

// UpstreamTLS is the leaf certificate of the upstream.
type UpstreamTLS struct {
	PeerSubject string    `json:"peerSubject"`
	NotAfter    time.Time `json:"notAfter"`
}

// UpstreamTestError is why no response came, or why the probe was not sent. Code
// is one of the stable codes of internal/proxy/upstream (dns_error,
// connection_refused, timeout, tls_untrusted, egress_denied,
// source_network_unavailable, upstream_secret_unavailable, offline,
// too_many_redirects, insecure_redirect, request_failed).
type UpstreamTestError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// UnsavedProbe is a `remote` section that has not been saved, validated and
// resolved against the format's defaults. Password is the inline secret of the
// request, used once and never stored; a reference in Settings is resolved on
// this replica like a saved one.
type UnsavedProbe struct {
	Format   string
	Settings RemoteSettings
	Password *observability.Secret
}

// UpstreamProber sends the probe request. The implementation reports every failure
// in the result.
type UpstreamProber interface {
	// TestSaved probes the saved configuration of r; the secrets are those of its
	// repository_secrets rows.
	TestSaved(ctx context.Context, r Repository, s RemoteSettings) UpstreamTestResult
	// TestUnsaved probes a section that is not saved.
	TestUnsaved(ctx context.Context, p UnsavedProbe) UpstreamTestResult
}

func (s *Service) prober() (UpstreamProber, error) {
	p := s.remoteDeps().Prober
	if p == nil {
		return nil, errs.E(errs.Unavailable, errs.CodeUnavailable, "The upstream test is not available on this replica.", errs.WithRetryAfter(time.Second))
	}
	return p, nil
}

// UpstreamTest implements POST /api/v1/repositories/{key}/upstream-test: it probes
// the saved configuration of a remote repository. The state is read from
// PostgreSQL, so a configuration saved through another replica is tested at once.
func (s *Service) UpstreamTest(ctx context.Context, key string) (UpstreamTestResult, error) {
	p, err := s.prober()
	if err != nil {
		return UpstreamTestResult{}, err
	}
	st, err := s.Get(ctx, key)
	if err != nil {
		return UpstreamTestResult{}, err
	}
	switch {
	case st.Repository.Lifecycle == Deleting:
		return UpstreamTestResult{}, errs.E(errs.Conflict, errs.CodeConflict, "the repository is being deleted")
	case st.Repository.Class != Remote:
		return UpstreamTestResult{}, errs.E(errs.Invalid, errs.CodeNotApplicable, "The upstream test applies to remote repositories; this one is "+string(st.Repository.Class)+".")
	}
	rs, ok := st.Repository.Remote()
	if !ok {
		return UpstreamTestResult{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithOp("repository.UpstreamTest"))
	}
	return p.TestSaved(ctx, st.Repository, rs), nil
}

// UnsavedTestRequest is the body of POST /api/v1/upstream-tests.
type UnsavedTestRequest struct {
	Format string          `json:"format"`
	Remote json.RawMessage `json:"remote"`
}

// UpstreamTestUnsaved implements POST /api/v1/upstream-tests: the section is
// validated like the one of a create, with the same defaults of the format, and
// then probed. A secret value in it is used for the probe and not stored; the
// marker {"set": true} of a stored value has nothing to refer to here.
func (s *Service) UpstreamTestUnsaved(ctx context.Context, body []byte) (UpstreamTestResult, error) {
	p, err := s.prober()
	if err != nil {
		return UpstreamTestResult{}, err
	}
	var req UnsavedTestRequest
	if err := decodeStrict(body, &req); err != nil {
		return UpstreamTestResult{}, err
	}
	d := Document{Key: "upstream-test", Class: Remote, Format: req.Format, Settings: Settings{Remote: emptyToNil(req.Remote)}}
	d.normalize()
	if err := checkRemoteShape(d.Remote); err != nil {
		return UpstreamTestResult{}, err
	}
	_, v := validate(s.cat, &d, false)
	probe := UnsavedProbe{Format: d.Format}
	for _, ps := range d.pending {
		switch ps.op {
		case secretSeal:
			val := ps.value
			probe.Password = &val
		case secretKeep:
			v.add(ps.field, codeInvalidSecret, `{"set": true} refers to a stored value, and an unsaved section has none`)
		}
	}
	if err := v.err(); err != nil {
		return UpstreamTestResult{}, err
	}
	rs, err := parseStoredRemote(d.Remote)
	if err != nil {
		return UpstreamTestResult{}, err
	}
	probe.Settings = *rs
	return p.TestUnsaved(ctx, probe), nil
}
