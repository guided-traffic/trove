package iface

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// DecodeStrict decodes one JSON value into v and refuses an unknown member and
// trailing data. Equality of the interface versions is the rule (R-BAS-009), so
// a body with a member the reader does not know is a version problem, never
// something to skip. Both sides decode every body of the interface with it.
func DecodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bastion interface body: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("bastion interface body: trailing data after the JSON value")
	}
	return nil
}

// Marshal encodes a body of the interface.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// MediaType is the content type of every body of the interface.
const MediaType = "application/json"

// Handshake is the answer of GET /api/v1/bastion/handshake (R-BAS-010).
type Handshake struct {
	// InterfaceVersion is the main installation's interface version.
	InterfaceVersion int `json:"interfaceVersion"`
	// Bastion is the registration the token belongs to.
	Bastion HandshakeBastion `json:"bastion"`
	// Installation describes the main installation.
	Installation HandshakeInstallation `json:"installation"`
	// ResponseBaseURL is the response base URL the main installation derived for
	// this request (R-OPS-005, R-EP-003). The Bastion requires it to equal its own
	// server.base_url (R-BAS-010).
	ResponseBaseURL string `json:"responseBaseUrl"`
	// AnonymousAccess is the effective anonymous switch (R-AUTH-037).
	AnonymousAccess bool `json:"anonymousAccess"`
	// RecordDownloads is audit.record_downloads (R-BAS-016, R-BAS-039).
	RecordDownloads bool `json:"recordDownloads"`
	// Feed is the feed position at the time of the answer.
	Feed HandshakeFeed `json:"feed"`
	// ServerTime is the main installation's clock.
	ServerTime time.Time `json:"serverTime"`
}

// HandshakeBastion identifies the registration and the token in use.
type HandshakeBastion struct {
	Name           string    `json:"name"`
	TokenID        string    `json:"tokenId"`
	TokenExpiresAt time.Time `json:"tokenExpiresAt"`
}

// HandshakeInstallation describes the main installation.
type HandshakeInstallation struct {
	BaseURL string `json:"baseUrl"`
	Release string `json:"release"`
}

// HandshakeFeed carries the feed position of the handshake.
type HandshakeFeed struct {
	// Cursor is a [Cursor] in wire form.
	Cursor string `json:"cursor"`
}

// CheckRequest is the body of POST /api/v1/bastion/check (R-BAS-011).
type CheckRequest struct {
	// Credential is the token string the caller presented, or null for an
	// anonymous caller. The field is always present.
	Credential *string `json:"credential"`
	// ClientAddress is the caller's address as the Bastion determined it
	// (R-BAS-019); the failed-credential throttle and token.rejected count
	// against it.
	ClientAddress string `json:"clientAddress"`
	// Repositories are the qualified repository names "<projectKey>/<repoKey>" to
	// answer for, at most bastions.check.max_repositories. A name without a
	// project is refused with 400 malformed_request (R-BAS-011).
	Repositories []string `json:"repositories"`
}

// CheckAnswer is the answer of the check (R-BAS-011).
type CheckAnswer struct {
	Subject Subject `json:"subject"`
	// Repositories maps every requested qualified name to the caller's shape on it.
	Repositories map[string]RepositoryRead `json:"repositories"`
}

// RepositoryRead is the shape of a subject on one repository and, for none, the
// status the caller would receive for a read of it (R-BAS-011, R-BAS-012).
type RepositoryRead struct {
	Read Read `json:"read"`
	// Status is 401, 403 or 404, set with [ReadNone] only.
	Status int `json:"status,omitempty"`
}

// FeedKind is the kind of a feed entry (R-BAS-015).
type FeedKind string

// The feed entry kinds. A kind is a member of the interface: adding one raises
// [InterfaceVersion].
const (
	// FeedToken: the tokens listed in Tokens are revoked.
	FeedToken FeedKind = "token"
	// FeedPrincipal: the principals listed in Principals can no longer be used
	// as before (disabled, deleted, a member removed, a binding removed).
	FeedPrincipal FeedKind = "principal"
	// FeedGrants: every permission answer of every caller is stale.
	FeedGrants FeedKind = "grants"
	// FeedSettings: a setting the handshake reports changed; the Bastion
	// handshakes again. When anonymous access was switched off, the same audit
	// event also yields a grants entry.
	FeedSettings FeedKind = "settings"
	// FeedContent: content was withdrawn from the repositories in Repositories,
	// the repository itself and every virtual repository that includes it,
	// recursively.
	FeedContent FeedKind = "content"
)

// Valid reports whether k is one of the five kinds.
func (k FeedKind) Valid() bool {
	switch k {
	case FeedToken, FeedPrincipal, FeedGrants, FeedSettings, FeedContent:
		return true
	}
	return false
}

// FeedEntry is one entry of the feed. ID is the audit event's ID, so the Bastion
// skips the IDs it applied (the lookback re-reads entries, R-BAS-015). An event
// that yields a second entry (settings and grants) gives it the ID
// "<event ID>:1", a third "<event ID>:2", so every entry has an ID of its own
// and the same event always yields the same IDs. Entries
// carry IDs and qualified repository names, never names or secrets. Exactly one of the
// lists is set, by Kind: Tokens for token, Principals for principal,
// Repositories for content, none for grants and settings.
type FeedEntry struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	Kind         FeedKind  `json:"kind"`
	Tokens       []string  `json:"tokens,omitempty"`
	Principals   []string  `json:"principals,omitempty"`
	Repositories []string  `json:"repositories,omitempty"`
}

// FeedAnswer is the answer of GET /api/v1/bastion/feed (R-BAS-015).
type FeedAnswer struct {
	Entries []FeedEntry `json:"entries"`
	// Cursor names the last entry returned, or the request's cursor when there is
	// none; it is a [Cursor] in wire form.
	Cursor string `json:"cursor"`
	// More is true when the limit cut the answer.
	More bool `json:"more"`
	// Reset is true when the cursor is no longer valid: the Bastion drops every
	// answer and takes Cursor.
	Reset bool `json:"reset"`
}

// DownloadsRequest is the body of POST /api/v1/bastion/downloads (R-BAS-016).
type DownloadsRequest struct {
	// Events are at most bastions.downloads.max_batch events.
	Events []DownloadEvent `json:"events"`
	// Dropped counts the events the Bastion lost since DroppedSince.
	Dropped int `json:"dropped"`
	// DroppedSince is the start of the period Dropped counts, set with a
	// non-zero Dropped.
	DroppedSince *time.Time `json:"droppedSince,omitempty"`
}

// DownloadEvent is one artifact the Bastion served from its cache (R-BAS-016,
// R-BAS-039). The Bastion knows no item path; Path is the request path.
type DownloadEvent struct {
	At time.Time `json:"at"`
	// TokenID and PrincipalID are empty for an anonymous caller.
	TokenID       string `json:"tokenId,omitempty"`
	PrincipalID   string `json:"principalId,omitempty"`
	ClientAddress string `json:"clientAddress"`
	// Repository is the qualified name "<projectKey>/<repoKey>".
	Repository string `json:"repository"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	UserAgent  string `json:"userAgent,omitempty"`
	Replica    string `json:"replica"`
}
