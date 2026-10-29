package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/valkey-io/valkey-go"
)

// ErrUnavailable is returned by every Valkey call while Valkey cannot be used:
// the circuit is open, no primary is connected, or the server is unsupported.
// It is never shown to a client (R-ARCH-031); the caller runs its fallback.
var ErrUnavailable = errors.New("valkey is unavailable")

// ErrNoKeyspace is returned when a key or channel is formed before the
// keyspace ID is known.
var ErrNoKeyspace = errors.New("valkey keyspace is not set")

// ErrValueTooLarge is returned for a value or payload above [MaxValueSize]
// (R-ARCH-030: small values).
var ErrValueTooLarge = errors.New("valkey value exceeds the 1 KiB limit")

// errNoPrimary means the Sentinels do not know the configured primary name.
var errNoPrimary = errors.New("no Sentinel knows the primary name")

// ErrorKind is the value of the kind label of trove_valkey_errors_total
// (R-HA-057).
type ErrorKind string

// The error kinds. kindNone and kindCanceled are not counted.
const (
	kindNone     ErrorKind = ""
	kindCanceled ErrorKind = "canceled"
	// KindTimeout is a deadline that passed.
	KindTimeout ErrorKind = "timeout"
	// KindConnection is a broken, refused or closed connection.
	KindConnection ErrorKind = "connection"
	// KindReadonly is a READONLY reply: the node is no longer the primary.
	KindReadonly ErrorKind = "readonly"
	// KindAuth is a rejected credential (WRONGPASS, NOAUTH, NOPERM).
	KindAuth ErrorKind = "auth"
	// KindTLS is a failed handshake or certificate verification.
	KindTLS ErrorKind = "tls"
	// KindOOM is a write refused at the memory limit.
	KindOOM ErrorKind = "oom"
	// KindNoPrimary is a Sentinel answer without a primary for the name.
	KindNoPrimary ErrorKind = "no_primary"
	// KindOther is any other server reply (a type error, a syntax error). It
	// is counted but does not trip the breaker: it says nothing about
	// availability.
	KindOther ErrorKind = "other"
)

// classify maps an error of a Valkey call to its kind.
func classify(err error) ErrorKind {
	if err == nil || errors.Is(err, valkey.Nil) {
		return kindNone
	}
	if errors.Is(err, errNoPrimary) {
		return KindNoPrimary
	}
	if errors.Is(err, context.Canceled) {
		return kindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return KindTimeout
	}
	if isTLSError(err) {
		return KindTLS
	}
	var verr *valkey.ValkeyError
	if errors.As(err, &verr) && verr != valkey.Nil {
		return classifyReply(verr.Error())
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return KindTimeout
	}
	// Refused, reset and closed connections, EOF and everything else the
	// transport reports.
	return KindConnection
}

// classifyReply maps the text of a server error reply.
func classifyReply(msg string) ErrorKind {
	word, _, _ := strings.Cut(msg, " ")
	switch word {
	case "READONLY":
		return KindReadonly
	case "WRONGPASS", "NOAUTH", "NOPERM":
		return KindAuth
	case "OOM":
		return KindOOM
	case "LOADING", "MASTERDOWN", "BUSY", "TRYAGAIN", "CLUSTERDOWN":
		return KindConnection
	}
	if strings.HasPrefix(msg, "ERR invalid password") || strings.HasPrefix(msg, "ERR AUTH") {
		return KindAuth
	}
	return KindOther
}

// isTLSError reports certificate and handshake failures.
func isTLSError(err error) bool {
	var (
		ua  x509.UnknownAuthorityError
		he  x509.HostnameError
		ci  x509.CertificateInvalidError
		cv  *tls.CertificateVerificationError
		rh  tls.RecordHeaderError
		al  tls.AlertError
		sip x509.SystemRootsError
	)
	if errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci) || errors.As(err, &cv) ||
		errors.As(err, &rh) || errors.As(err, &al) || errors.As(err, &sip) {
		return true
	}
	return strings.Contains(err.Error(), "tls: ")
}

// breakerOutcome decides how a call ending in err counts for the breaker.
func breakerOutcome(kind ErrorKind) outcome {
	switch kind {
	case kindNone:
		return outcomeSuccess
	case kindCanceled, KindOther, KindReadonly:
		// READONLY says the node was demoted and the client is about to move
		// on, not that Valkey is down. Opening the circuit for it would keep a
		// replica away from the new primary for open_duration on top of the
		// refresh that finds it (R-ARCH-031's bound is refresh_interval + 1 s).
		return outcomeNeutral
	default:
		return outcomeFailure
	}
}

// isConfigError reports kinds that mean the replica's own configuration is
// wrong rather than Valkey being unreachable (Q-HA-13).
func isConfigError(kind ErrorKind) bool {
	return kind == KindAuth || kind == KindTLS || kind == KindNoPrimary
}
