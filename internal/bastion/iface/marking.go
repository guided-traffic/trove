package iface

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Class is the kind of a response, which says what the Bastion may keep of it
// (R-BAS-013, R-BAS-021).
type Class string

// The values of [HeaderClass].
const (
	// ClassArtifact is a 200 or 304 of a Download route or of a file under /files/:
	// immutable bytes, kept by content address (R-BAS-022).
	ClassArtifact Class = "artifact"
	// ClassShared is a 200 or 304 of a Metadata or Computed route (or a /files/
	// folder listing) when the caller's shape on the addressed repository is full:
	// the same bytes for every full reader (R-BAS-023).
	ClassShared Class = "shared"
	// ClassPrivate is the same routes for any other caller: a document filtered
	// for that caller, never shared.
	ClassPrivate Class = "private"
	// ClassAbsent is a 404 of a read route when the item does not exist in the
	// repository for any subject (R-BAS-024). A refusal, a hidden item and a
	// serve-time refusal are never absent.
	ClassAbsent Class = "absent"
	// ClassPass is every other answer: Volatile and other routes, errors,
	// redirects. The Bastion keeps nothing of it.
	ClassPass Class = "pass"
)

// Valid reports whether c is one of the five classes.
func (c Class) Valid() bool {
	switch c {
	case ClassArtifact, ClassShared, ClassPrivate, ClassAbsent, ClassPass:
		return true
	}
	return false
}

// Read is the read shape of a subject on a repository (R-BAS-012, Q-BAS-28).
type Read string

// The values of [HeaderRead] and of the shape members of the check answer.
const (
	// ReadFull: the subject may read every package and version reachable through
	// the repository, and every response of a read route is the same for it as for
	// every other full subject.
	ReadFull Read = "full"
	// ReadPartial is every other case in which the subject may read something.
	ReadPartial Read = "partial"
	// ReadNone: the subject may read nothing through the repository.
	ReadNone Read = "none"
)

// Valid reports whether r is one of the three shapes.
func (r Read) Valid() bool { return r == ReadFull || r == ReadPartial || r == ReadNone }

// Marking is the set of response headers the main installation adds to the
// response of a recognized Bastion request (R-BAS-013).
type Marking struct {
	Class Class
	// Repository is the qualified name "<projectKey>/<repoKey>".
	Repository string
	Read       Read
	Subject    Subject
	// Interface is the main installation's interface version.
	Interface int
}

// Set writes the marking headers. A marking without a repository (a response
// that addresses none, class pass) leaves [HeaderRepository] and [HeaderRead]
// out.
func (m Marking) Set(h http.Header) {
	h.Set(HeaderClass, string(m.Class))
	if m.Repository != "" {
		h.Set(HeaderRepository, m.Repository)
	}
	if m.Read != "" {
		h.Set(HeaderRead, string(m.Read))
	}
	h.Set(HeaderSubject, m.Subject.Header())
	h.Set(HeaderInterface, strconv.Itoa(m.Interface))
}

// StripMarking removes every header with [HeaderPrefix] from a response header
// map. The main installation strips them from the response of every request that
// is not a recognized Bastion request (R-BAS-013), and the Bastion strips them
// before it replays a response to its caller (R-BAS-021).
func StripMarking(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), HeaderPrefix) {
			delete(h, k)
		}
	}
}

// ErrMarking is the error of a response whose marking the Bastion cannot read.
var ErrMarking = errors.New("bastion marking: malformed")

// ParseMarking reads the marking of a response. A response without [HeaderClass]
// is unmarked: ok is false and the Bastion treats the response as the answer of a
// proxy in front of the main installation (R-BAS-021). A marked response with a
// malformed header is an error. Repository and read may be absent.
func ParseMarking(h http.Header) (m Marking, ok bool, err error) {
	class := h.Get(HeaderClass)
	if class == "" {
		return Marking{}, false, nil
	}
	m.Class = Class(class)
	if !m.Class.Valid() {
		return Marking{}, true, fmt.Errorf("%w: %s %q", ErrMarking, HeaderClass, class)
	}
	m.Repository = h.Get(HeaderRepository)
	if r := h.Get(HeaderRead); r != "" {
		m.Read = Read(r)
		if !m.Read.Valid() {
			return Marking{}, true, fmt.Errorf("%w: %s %q", ErrMarking, HeaderRead, r)
		}
	}
	if s := h.Get(HeaderSubject); s != "" {
		sub, err := ParseSubjectHeader(s)
		if err != nil {
			return Marking{}, true, err
		}
		m.Subject = sub
	}
	if v := h.Get(HeaderInterface); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Marking{}, true, fmt.Errorf("%w: %s %q", ErrMarking, HeaderInterface, v)
		}
		m.Interface = n
	}
	return m, true, nil
}

// SubjectKind says who a credential turned out to be (R-BAS-011, R-BAS-013).
type SubjectKind string

// The subject kinds. The header [HeaderSubject] carries token, anonymous and
// invalid only; the check answer carries all five.
const (
	// SubjectToken: a valid token. ExpiresAt is its expiry, if it has one.
	SubjectToken SubjectKind = "token"
	// SubjectAnonymous: no credential and anonymous access on.
	SubjectAnonymous SubjectKind = "anonymous"
	// SubjectAnonymousRefused: no credential and anonymous access off.
	SubjectAnonymousRefused SubjectKind = "anonymous_refused"
	// SubjectInvalid: malformed, unknown, expired, revoked or owner disabled; the
	// main installation tells which to nobody (R-AUTH-002).
	SubjectInvalid SubjectKind = "invalid"
	// SubjectThrottled: the address is in the failed-credential throttle's
	// throttled mode and the credential is not exempt (R-AUTH-038). Check only.
	SubjectThrottled SubjectKind = "throttled"
)

// Subject is the main installation's statement about a credential. It is the
// subject member of the check answer (JSON) and, in the form of [Subject.Header],
// the value of [HeaderSubject].
type Subject struct {
	Kind        SubjectKind `json:"kind"`
	TokenID     string      `json:"tokenId,omitempty"`
	PrincipalID string      `json:"principalId,omitempty"`
	// ExpiresAt is the token's expiry; absent for a token that does not expire.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// RetryAfterSeconds is set with [SubjectThrottled] only.
	RetryAfterSeconds *int `json:"retryAfterSeconds,omitempty"`
}

// Header formats the subject for [HeaderSubject] (R-BAS-013):
//
//	token=<token ID>; principal=<principal ID>; expires=<unix seconds>
//	anonymous
//	invalid
//
// The expires member is left out for a token that does not expire (a service
// account token may). The refused and throttled kinds are never a header value;
// they format as invalid, which is what the response to the caller is.
func (s Subject) Header() string {
	switch s.Kind {
	case SubjectToken:
		out := "token=" + s.TokenID + "; principal=" + s.PrincipalID
		if s.ExpiresAt != nil {
			out += "; expires=" + strconv.FormatInt(s.ExpiresAt.Unix(), 10)
		}
		return out
	case SubjectAnonymous:
		return string(SubjectAnonymous)
	}
	return string(SubjectInvalid)
}

// ParseSubjectHeader parses a [HeaderSubject] value written by [Subject.Header].
func ParseSubjectHeader(v string) (Subject, error) {
	switch v {
	case string(SubjectAnonymous):
		return Subject{Kind: SubjectAnonymous}, nil
	case string(SubjectInvalid):
		return Subject{Kind: SubjectInvalid}, nil
	}
	s := Subject{Kind: SubjectToken}
	seen := map[string]bool{}
	for _, part := range strings.Split(v, ";") {
		k, val, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || val == "" || seen[k] {
			return Subject{}, fmt.Errorf("%w: %s %q", ErrMarking, HeaderSubject, v)
		}
		seen[k] = true
		switch k {
		case "token":
			s.TokenID = val
		case "principal":
			s.PrincipalID = val
		case "expires":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return Subject{}, fmt.Errorf("%w: %s %q", ErrMarking, HeaderSubject, v)
			}
			t := time.Unix(n, 0).UTC()
			s.ExpiresAt = &t
		default:
			return Subject{}, fmt.Errorf("%w: %s %q", ErrMarking, HeaderSubject, v)
		}
	}
	if s.TokenID == "" || s.PrincipalID == "" {
		return Subject{}, fmt.Errorf("%w: %s %q", ErrMarking, HeaderSubject, v)
	}
	return s, nil
}
