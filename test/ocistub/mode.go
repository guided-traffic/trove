package ocistub

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Down modes: how the stub behaves as an upstream that is not working. Stopping the listener itself is Running.Close.
const (
	DownUnavailable = "503"   // every request is answered 503
	DownReset       = "reset" // the connection is cut without an answer
	DownHang        = "hang"  // the request is held until the client gives up or MaxHang passes
)

// MaxHang bounds how long a request is held in DownHang or by a delay, so a test that forgets to clear the mode does not leave
// connections open for good.
const MaxHang = 2 * time.Minute

// Auth modes of the registry.
const (
	// AuthAnonymous (also the empty value) challenges every request and lets the realm issue a token to a client with no
	// credentials, as Docker Hub does; credentials that are given must be right.
	AuthAnonymous = "anonymous"
	// AuthBasic challenges every request and lets the realm issue a token only to a client with the right credentials.
	AuthBasic = "basic"
	// AuthNone does not challenge: the registry answers without a token.
	AuthNone = "none"
)

// Classes of request, for Mode.DelayMS, Mode.FailStatus and Throttle.Class. ClassAll adds to the class of the request itself.
const (
	ClassBase      = "base"      // GET /v2/
	ClassCatalog   = "catalog"   // GET /v2/_catalog
	ClassTags      = "tags"      // tags lists
	ClassManifest  = "manifest"  // manifests by tag and by digest
	ClassBlob      = "blob"      // blobs on the registry host
	ClassReferrers = "referrers" // the referrers API
	ClassToken     = "token"     // the realm
	ClassCDN       = "cdn"       // blobs on the CDN host
	ClassOther     = "other"
	ClassAll       = "all"
)

var classes = []string{ClassBase, ClassCatalog, ClassTags, ClassManifest, ClassBlob, ClassReferrers, ClassToken, ClassCDN, ClassOther, ClassAll}

// Mode is the set of switches. The zero value is a healthy, Docker-Hub-like registry: anonymous token handshake, blobs by
// redirect to the CDN host, no catalog and no referrers API. Setting a mode replaces the previous one. A repository entry
// is a repository name; the reference entries of Schema1, WrongDigest and WrongBlob are "name" (everything of the
// repository), "name:tag" or "name@sha256:...".
type Mode struct {
	// Down makes the whole registry fail: DownUnavailable, DownReset or DownHang.
	Down string `json:"down,omitempty"`
	// DelayMS delays the answer by class (the Class constants). {"all": 2000} delays every response by 2 s.
	DelayMS map[string]int `json:"delay_ms,omitempty"`
	// FailStatus answers the requests of a class with this 5xx status instead of the content: {"blob": 502}.
	FailStatus map[string]int `json:"fail_status,omitempty"`
	// Auth is AuthAnonymous (default), AuthBasic or AuthNone.
	Auth string `json:"auth,omitempty"`
	// PrivateRepos lists repositories whose scope the realm grants only to a client with credentials.
	PrivateRepos []string `json:"private_repos,omitempty"`
	// StaticToken, when set, is a bearer token the registry accepts for every repository without a handshake.
	StaticToken string `json:"static_token,omitempty"`
	// TokenTTLSeconds is the lifetime of the tokens the realm issues from now on; 0 means DefaultTokenTTL.
	TokenTTLSeconds int `json:"token_ttl_seconds,omitempty"`
	// TokenOmitExpiry leaves expires_in out of the realm's answer.
	TokenOmitExpiry bool `json:"token_omit_expiry,omitempty"`
	// RefuseToken makes the realm answer this status (400 to 599) instead of a token.
	RefuseToken int `json:"refuse_token,omitempty"`
	// BlobsDirect serves a blob from the registry host, GET and HEAD; by default both answer a redirect to the CDN host, as
	// distribution does when its storage driver redirects (the S3 driver presigns HEAD too).
	BlobsDirect bool `json:"blobs_direct,omitempty"`
	// HeadDirect answers a blob HEAD from the registry host while a GET still redirects, as a registry does that checks existence
	// itself.
	HeadDirect bool `json:"head_direct,omitempty"`
	// CDNAcceptAuth lets the CDN host serve a request that carries an Authorization header. By default it answers 400 to one, as an
	// object store does for a signed URL, so a client that leaks its token to the redirect target fails visibly.
	CDNAcceptAuth bool `json:"cdn_accept_auth,omitempty"`
	// Catalog serves GET /v2/_catalog; without it the path answers 404, as distribution does when its catalog is disabled.
	Catalog bool `json:"catalog,omitempty"`
	// Referrers serves the referrers API; without it the path answers 404 and a client falls back to the tag schema.
	Referrers bool `json:"referrers,omitempty"`
	// NotFound lists repositories that answer 404 NAME_UNKNOWN.
	NotFound []string `json:"not_found,omitempty"`
	// Schema1 lists tags that answer a schema 1 manifest.
	Schema1 []string `json:"schema1,omitempty"`
	// WrongDigest lists manifests whose bytes do not match their Docker-Content-Digest.
	WrongDigest []string `json:"wrong_digest,omitempty"`
	// WrongBlob lists blobs ("name" or "name@digest") whose bytes do not match their digest.
	WrongBlob []string `json:"wrong_blob,omitempty"`
	// PullQuota counts manifest GET requests against a quota and answers 429 with Docker Hub's headers when it is used up.
	PullQuota *PullQuota `json:"pull_quota,omitempty"`
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
	}
	switch m.Auth {
	case "", AuthAnonymous, AuthBasic, AuthNone:
	default:
		return fmt.Errorf("auth %q: want anonymous, basic or none", m.Auth)
	}
	for class, ms := range m.DelayMS {
		if !slices.Contains(classes, class) {
			return fmt.Errorf("delay_ms class %q: want one of %s", class, strings.Join(classes, ", "))
		}
		if ms < 0 || ms > int(MaxHang/time.Millisecond) {
			return fmt.Errorf("delay_ms %q: %d out of range", class, ms)
		}
	}
	for class, status := range m.FailStatus {
		if !slices.Contains(classes, class) {
			return fmt.Errorf("fail_status class %q: want one of %s", class, strings.Join(classes, ", "))
		}
		if status < 500 || status > 599 {
			return fmt.Errorf("fail_status %q: %d is no 5xx status", class, status)
		}
	}
	if m.RefuseToken != 0 && (m.RefuseToken < 400 || m.RefuseToken > 599) {
		return fmt.Errorf("refuse_token %d: want 400 to 599", m.RefuseToken)
	}
	if m.TokenTTLSeconds < 0 || m.TokenTTLSeconds > 86400 {
		return fmt.Errorf("token_ttl_seconds %d out of range", m.TokenTTLSeconds)
	}
	if q := m.PullQuota; q != nil {
		return q.Validate()
	}
	return nil
}

func (m Mode) auth() string {
	if m.Auth == "" {
		return AuthAnonymous
	}
	return m.Auth
}

// delay is the hold for a request of a class.
func (m Mode) delay(class string) time.Duration {
	return time.Duration(m.DelayMS[class]+m.DelayMS[ClassAll]) * time.Millisecond
}

// failStatus is the status a class fails with, or 0.
func (m Mode) failStatus(class string) int {
	if s := m.FailStatus[class]; s != 0 {
		return s
	}
	return m.FailStatus[ClassAll]
}

// matches reports whether an entry list names a repository, or a reference of it. An entry is "name", "name:tag" or "name@digest";
// ref is the reference of the request (a tag or a digest), empty for a request that has none.
func matches(list []string, name, ref string) bool {
	for _, e := range list {
		if e == name || ref != "" && (e == name+":"+ref || e == name+"@"+ref) {
			return true
		}
	}
	return false
}

// clone copies the maps, slices and the quota, so that a caller of Mode or SetMode cannot change the stub's mode afterwards.
func (m Mode) clone() Mode {
	m.DelayMS = maps.Clone(m.DelayMS)
	m.FailStatus = maps.Clone(m.FailStatus)
	m.PrivateRepos = slices.Clone(m.PrivateRepos)
	m.NotFound = slices.Clone(m.NotFound)
	m.Schema1 = slices.Clone(m.Schema1)
	m.WrongDigest = slices.Clone(m.WrongDigest)
	m.WrongBlob = slices.Clone(m.WrongBlob)
	if m.PullQuota != nil {
		q := *m.PullQuota
		m.PullQuota = &q
	}
	return m
}
