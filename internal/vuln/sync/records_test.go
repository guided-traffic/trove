package sync

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

type testAdapter struct{}

func (testAdapter) Ecosystem() string                        { return "npm" }
func (testAdapter) AdvisoryName(n string) (string, bool)     { return n, n != "" }
func (testAdapter) AdvisoryVersion(v string) (string, bool)  { return v, v != "" }
func (testAdapter) CompareVersions(a, b string) (int, error) { return strings.Compare(a, b), nil }

// R-VULN-004, S11 of the fix wave: the body served for <id>.json is that record's; a body with another identifier is rejected and
// nothing of it is written, however valid it is.
func TestR_VULN_004_AFetchedBodyWithAnotherIdentifierIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"GHSA-OTHER-0001","modified":"2026-10-01T00:00:00Z","summary":"not the one asked for"}`)
	}))
	defer srv.Close()
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = srv.URL + "/"
	cfg.Sources.OSV.MaxRecordSize = config.MiB
	s := &Service{o: Options{HTTP: srv.Client(), Config: cfg}, log: slog.New(slog.DiscardHandler)}
	written, skipped, rejected, err := s.fetchApply(context.Background(), "npm", testAdapter{}, []string{"GHSA-ASKED-0001"})
	if err != nil || written != 0 || skipped != 0 || rejected != 1 {
		t.Fatalf("written %d, skipped %d, rejected %d, err %v", written, skipped, rejected, err)
	}
}

// R-VULN-004, S11 of the fix wave: a fetched record whose modified time is older than the stored one does not replace it; the
// same record, or a newer one, is judged by digest and time as before.
func TestR_VULN_004_AnOlderFetchedRecordDoesNotReplaceANewerOne(t *testing.T) {
	stored := store.Stored{Modified: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), SHA256: bytes.Repeat([]byte{1}, 32)}
	rec := func(mod time.Time, sha byte) *osv.Record {
		r := &osv.Record{}
		copy(r.SHA256[:], bytes.Repeat([]byte{sha}, 32))
		r.Advisory.Modified = mod
		return r
	}
	for _, tt := range []struct {
		name string
		rec  *osv.Record
		want bool
	}{
		{"older", rec(stored.Modified.Add(-time.Hour), 2), true},
		{"older with the stored digest", rec(stored.Modified.Add(-time.Hour), 1), true},
		{"the same record", rec(stored.Modified, 1), true},
		{"the same time, another digest", rec(stored.Modified, 2), false},
		{"newer", rec(stored.Modified.Add(time.Hour), 2), false},
	} {
		if got := fetchedNotNewer(stored, tt.rec); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}
