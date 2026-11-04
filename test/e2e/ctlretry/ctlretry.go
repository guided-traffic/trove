// Package ctlretry is the retry rule of the e2e control clients: an idempotent call through a long-lived
// `kubectl port-forward` that fails on the transport (a reset, an EOF, a refused connection) is repeated once per new
// forward, a bounded number of times (docs/developer/e2e.md, port-forwards). It has no build tag so that the rule is
// unit-tested without a cluster.
package ctlretry

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
)

// Transport reports whether err is a failure of the connection (the forward) and not an answer of the server: a
// reset, a broken pipe, an end of stream, a refused connection or a network error of the dialer. An HTTP status is
// never an error here, so it is never repeated.
func Transport(err error) bool {
	if err == nil {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// Do runs attempt; when it fails on the transport it calls renew (which returns once a new forward listens) and runs
// attempt again, at most renewals times. The error of the last attempt is returned, with the failure of renew when
// that is why the call ended. Only an idempotent attempt may be passed.
func Do(renewals int, attempt func() error, renew func() error) error {
	err := attempt()
	for i := 0; i < renewals && Transport(err); i++ {
		if rerr := renew(); rerr != nil {
			return fmt.Errorf("%w (the forward could not be renewed: %w)", err, rerr)
		}
		err = attempt()
	}
	return err
}
