package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// slotWaitRecord sends one request with the call facts and returns its upstream log record.
func slotWaitRecord(t *testing.T, purpose string, call Call) map[string]any {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") }))
	defer srv.Close()
	var buf bytes.Buffer
	lg := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &buf)
	req, _ := http.NewRequestWithContext(WithCall(context.Background(), call), http.MethodGet, srv.URL, nil)
	resp, err := New(allowLoopback(), WithUpstreamLog(lg)).Client(purpose).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	return rec
}

func TestR_OPS_011_TheSlotWaitIsARemoteRecordsField(t *testing.T) {
	t.Run("a wait is logged in milliseconds with the precision of duration_ms", func(t *testing.T) {
		wait := 12500 * time.Microsecond
		rec := slotWaitRecord(t, PurposeRemote, Call{SlotWait: NewSlotWait(wait)})
		if rec["slot_wait_ms"] != 12.5 {
			t.Errorf("slot_wait_ms = %#v, want 12.5", rec["slot_wait_ms"])
		}
	})
	t.Run("a wait of zero is 0, not null", func(t *testing.T) {
		var wait time.Duration
		rec := slotWaitRecord(t, PurposeRemote, Call{SlotWait: NewSlotWait(wait)})
		if v, ok := rec["slot_wait_ms"]; !ok || v != float64(0) {
			t.Errorf("slot_wait_ms = %#v, %v", v, ok)
		}
	})
	t.Run("a request that took no slot logs null", func(t *testing.T) {
		rec := slotWaitRecord(t, PurposeRemote, Call{})
		if v, ok := rec["slot_wait_ms"]; !ok || v != nil {
			t.Errorf("slot_wait_ms = %#v, %v", v, ok)
		}
	})
	t.Run("a request that follows a redirect reports the wait on its first hop only", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") })
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/final", http.StatusFound) })
		srv := httptest.NewServer(mux)
		defer srv.Close()
		var buf bytes.Buffer
		lg := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &buf)
		wait := 12500 * time.Microsecond
		req, _ := http.NewRequestWithContext(WithCall(context.Background(), Call{SlotWait: NewSlotWait(wait)}), http.MethodGet, srv.URL+"/start", nil)
		resp, err := New(allowLoopback(), WithUpstreamLog(lg)).Client(PurposeRemote).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		var got []any
		for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
			var rec map[string]any
			if err := json.Unmarshal(l, &rec); err != nil {
				t.Fatalf("%v: %s", err, l)
			}
			got = append(got, rec["slot_wait_ms"])
		}
		if len(got) != 2 || got[0] != 12.5 || got[1] != float64(0) {
			t.Fatalf("slot_wait_ms of the hops = %#v, want 12.5 then 0", got)
		}
	})
	t.Run("another purpose has no slot_wait_ms", func(t *testing.T) {
		wait := 3 * time.Millisecond
		rec := slotWaitRecord(t, PurposeWebhook, Call{SlotWait: NewSlotWait(wait)})
		if _, ok := rec["slot_wait_ms"]; ok {
			t.Errorf("webhook record has slot_wait_ms = %#v", rec["slot_wait_ms"])
		}
	})
}
