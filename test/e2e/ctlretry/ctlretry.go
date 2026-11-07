// Package ctlretry is the retry rule of the e2e control clients: an idempotent call through a long-lived
// `kubectl port-forward` that fails on the transport (a reset, an EOF, a refused connection) is repeated once per new
// forward, a bounded number of times (docs/developer/e2e.md, port-forwards). It has no build tag so that the rule is
// unit-tested without a cluster.
package ctlretry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
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

// StateShown checks that the JSON document a resource answers with shows the state a write set. want is the members
// the write meant to set, in the shape of a JSON merge patch: a member that is an object is checked member by member
// (the other members of the document are not looked at), a nil member must be absent or null, any other value must be
// equal after a JSON round trip. A write repeated after a transport error is accepted only when this holds, because
// the first attempt may or may not have reached the server.
func StateShown(doc []byte, want map[string]any) error {
	var have any
	if err := json.Unmarshal(doc, &have); err != nil {
		return fmt.Errorf("the resource is no JSON: %w", err)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return err
	}
	var norm any
	if err := json.Unmarshal(raw, &norm); err != nil {
		return err
	}
	return shown("", have, norm)
}

func shown(path string, have, want any) error {
	if wm, ok := want.(map[string]any); ok {
		hm, ok := have.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, have %v", pathOf(path), have)
		}
		for k, w := range wm {
			if err := shown(path+"/"+k, hm[k], w); err != nil {
				return err
			}
		}
		return nil
	}
	if !reflect.DeepEqual(have, want) {
		return fmt.Errorf("%s: have %v, want %v", pathOf(path), have, want)
	}
	return nil
}

func pathOf(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
