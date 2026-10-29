package cluster

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/valkey-io/valkey-go"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"nil", nil, kindNone},
		{"deadline", context.DeadlineExceeded, KindTimeout},
		{"wrapped deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), KindTimeout},
		{"os deadline", os.ErrDeadlineExceeded, KindTimeout},
		{"canceled", context.Canceled, kindCanceled},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, KindConnection},
		{"closing", valkey.ErrClosing, KindConnection},
		{"unknown authority", x509.UnknownAuthorityError{}, KindTLS},
		{"hostname", x509.HostnameError{}, KindTLS},
		{"no primary", fmt.Errorf("lookup: %w", errNoPrimary), KindNoPrimary},
	}
	for _, c := range cases {
		t.Run("R-HA-057 kind of "+c.name, func(t *testing.T) {
			if got := classify(c.err); got != c.want {
				t.Fatalf("classify = %q, want %q", got, c.want)
			}
		})
	}
	replies := map[string]ErrorKind{
		"READONLY You can't write against a read only replica.":  KindReadonly,
		"WRONGPASS invalid username-password pair":               KindAuth,
		"NOAUTH Authentication required.":                        KindAuth,
		"NOPERM this user has no permissions to run the 'del'":   KindAuth,
		"OOM command not allowed when used memory > 'maxmemory'": KindOOM,
		"LOADING Valkey is loading the dataset in memory":        KindConnection,
		"WRONGTYPE Operation against a key holding the wrong":    KindOther,
	}
	for msg, want := range replies {
		t.Run("R-HA-057 server reply "+msg[:6], func(t *testing.T) {
			if got := classifyReply(msg); got != want {
				t.Fatalf("classifyReply(%q) = %q, want %q", msg, got, want)
			}
		})
	}
	t.Run("R-HA-057 only availability errors trip the breaker; READONLY does not", func(t *testing.T) {
		for kind, want := range map[ErrorKind]outcome{
			kindNone: outcomeSuccess, kindCanceled: outcomeNeutral, KindOther: outcomeNeutral,
			KindTimeout: outcomeFailure, KindConnection: outcomeFailure, KindOOM: outcomeFailure, KindReadonly: outcomeNeutral,
		} {
			if got := breakerOutcome(kind); got != want {
				t.Errorf("breakerOutcome(%q) = %v, want %v", kind, got, want)
			}
		}
	})
	t.Run("Q-HA-13 configuration error kinds", func(t *testing.T) {
		for _, k := range []ErrorKind{KindAuth, KindTLS, KindNoPrimary} {
			if !isConfigError(k) {
				t.Errorf("%q is not a configuration error", k)
			}
		}
		for _, k := range []ErrorKind{KindTimeout, KindConnection, KindReadonly, KindOOM, KindOther} {
			if isConfigError(k) {
				t.Errorf("%q is a configuration error", k)
			}
		}
	})
}
