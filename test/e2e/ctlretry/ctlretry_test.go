package ctlretry

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

func TestTransport(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":     {nil, false},
		"reset":   {fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		"eof":     {io.EOF, true},
		"refused": {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		"other":   {errors.New("bad request body"), false},
	} {
		if got := Transport(tc.err); got != tc.want {
			t.Errorf("%s: Transport = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDoRepeatsOncePerNewForward(t *testing.T) {
	attempts, renews := 0, 0
	err := Do(2, func() error {
		attempts++
		if attempts < 3 {
			return syscall.ECONNRESET
		}
		return nil
	}, func() error { renews++; return nil })
	if err != nil || attempts != 3 || renews != 2 {
		t.Fatalf("err %v, %d attempts, %d renewals; want nil, 3, 2", err, attempts, renews)
	}
}

func TestDoIsBounded(t *testing.T) {
	attempts := 0
	err := Do(2, func() error { attempts++; return io.EOF }, func() error { return nil })
	if !errors.Is(err, io.EOF) || attempts != 3 {
		t.Fatalf("err %v after %d attempts; want EOF after 3", err, attempts)
	}
}

func TestDoDoesNotRepeatAnAnswer(t *testing.T) {
	attempts, renews := 0, 0
	want := errors.New("404 not found")
	err := Do(2, func() error { attempts++; return want }, func() error { renews++; return nil })
	if !errors.Is(err, want) || attempts != 1 || renews != 0 {
		t.Fatalf("err %v, %d attempts, %d renewals; want one attempt and no renewal", err, attempts, renews)
	}
}

func TestDoReportsAFailedRenewal(t *testing.T) {
	err := Do(2, func() error { return syscall.ECONNRESET }, func() error { return errors.New("did not listen") })
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("err %v does not wrap the transport error", err)
	}
}

// A real client: the first connection is reset by the server, the second is answered.
func TestDoWithAClientThatIsReset(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			c, _, _ := w.(http.Hijacker).Hijack()
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetLinger(0) // close sends a reset
			}
			_ = c.Close()
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	var body string
	err := Do(1, func() error {
		resp, err := cl.Get(srv.URL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		body = string(b)
		return err
	}, func() error { return nil })
	if err != nil || body != "ok" {
		t.Fatalf("err %v, body %q", err, body)
	}
}

func TestStateShown(t *testing.T) {
	doc := []byte(`{"status":"dismissed","n":2,"vulnerabilities":{"blockMalicious":false,"other":1},"gone":null}`)
	for name, tc := range map[string]struct {
		want    map[string]any
		wantErr bool
	}{
		"a member":                  {map[string]any{"status": "dismissed"}, false},
		"a number":                  {map[string]any{"n": 2}, false},
		"a nested member":           {map[string]any{"vulnerabilities": map[string]any{"blockMalicious": false}}, false},
		"null for an absent member": {map[string]any{"missing": nil, "gone": nil}, false},
		"another value":             {map[string]any{"status": "confirmed"}, true},
		"another nested value":      {map[string]any{"vulnerabilities": map[string]any{"blockMalicious": true}}, true},
		"null for a present member": {map[string]any{"status": nil}, true},
		"an object for a scalar":    {map[string]any{"status": map[string]any{"a": 1}}, true},
		"a member that is absent":   {map[string]any{"missing": "x"}, true},
	} {
		if err := StateShown(doc, tc.want); (err != nil) != tc.wantErr {
			t.Errorf("%s: err %v, want error %v", name, err, tc.wantErr)
		}
	}
	if err := StateShown([]byte("nope"), map[string]any{}); err == nil {
		t.Error("a body that is no JSON passed")
	}
}
