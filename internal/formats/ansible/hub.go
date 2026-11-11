package ansible

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The exchange of a hub behind single sign-on (R-ANS-019, Q-ANS-51). The Red Hat Automation Hub is called with an access token that
// the realm issues for a long-lived offline token, a refresh token: the stored credential of the repository is that token, and
// `ansible.authUrl` is the realm's token endpoint. The format reads the protocol and the engine does the I/O (formats.CredentialExchanger):
// ExchangeRequest builds the form of the refresh-token grant, ReadExchange reads the answer. The engine sends the request only to a host
// that may have the credential, keeps the token in the replica's cache until shortly before it expires, sends the upstream request with
// `Authorization: Bearer <access token>` and, when the upstream refuses it, exchanges once more and sends the request once more. A
// failed exchange is 502 upstream_auth_failed and never an empty list. Neither token is logged. The mechanism is specified from the
// documentation of the service and proven against the stub's realm only (H-842).

var _ formats.CredentialExchanger = remoteAdapter{}

// The lifetime margin: the token is used until hubTokenMargin before the realm's expires_in ends. The engine takes ten seconds of it
// as its own skew, so ReadExchange takes the rest off the lifetime it returns.
const (
	hubTokenMargin = 30 * time.Second
	engineSkew     = 10 * time.Second
	// maxAccessTokenBytes bounds the access token a realm may answer with.
	maxAccessTokenBytes = 8 << 10
)

// ExchangeRequest builds the refresh-token grant: a POST of grant_type, client_id and refresh_token, form encoded, to ansible.authUrl.
// A repository without authUrl does no exchange.
func (remoteAdapter) ExchangeRequest(ctx context.Context, rc formats.RemoteContext, credential string) (*http.Request, string, bool, error) {
	s := memberSettings(rc.Settings)
	if s.AuthURL == "" {
		return nil, "", false, nil
	}
	u, err := url.Parse(s.AuthURL)
	if err != nil || u.Host == "" {
		return nil, "", false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "ansible.authUrl is not a usable address.")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {s.ClientID}, "refresh_token": {credential}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", false, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return req, "hub/" + s.ClientID, true, nil
}

// ReadExchange reads the realm's answer: access_token and expires_in. A token that cannot be a header value, or a lifetime that is
// not positive, is a failed exchange.
func (remoteAdapter) ReadExchange(_ context.Context, _ formats.RemoteContext, _ int, body []byte) (string, time.Duration, error) {
	var a struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &a); err != nil || a.AccessToken == "" || len(a.AccessToken) > maxAccessTokenBytes || strings.ContainsAny(a.AccessToken, " \t\r\n") {
		return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamAuthFailed, "The single sign-on realm answered the exchange without a usable access_token.")
	}
	if a.TokenType != "" && !strings.EqualFold(a.TokenType, "bearer") {
		return "", 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamAuthFailed, "The single sign-on realm answered the exchange with a token that is not a bearer token.")
	}
	ttl := time.Duration(a.ExpiresIn)*time.Second - hubTokenMargin + engineSkew
	if a.ExpiresIn <= 0 {
		ttl = time.Second
	}
	return "Bearer " + a.AccessToken, max(ttl, time.Second), nil
}

// memberSettings reads the ansible member of the repository document as the engine hands it over (RemoteContext.Settings is the member
// itself, not the document), with the default client id.
func memberSettings(member json.RawMessage) Settings {
	s := Settings{ClientID: DefaultClientID, FetchDocumentation: true}
	var m struct {
		AuthURL            string `json:"authUrl"`
		ClientID           string `json:"clientId"`
		FetchDocumentation *bool  `json:"fetchDocumentation"`
	}
	if json.Unmarshal(member, &m) != nil {
		return s
	}
	s.AuthURL = m.AuthURL
	if m.ClientID != "" {
		s.ClientID = m.ClientID
	}
	if m.FetchDocumentation != nil {
		s.FetchDocumentation = *m.FetchDocumentation
	}
	return s
}
