package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	written, skipped, rejected, err := s.fetchApply(context.Background(), "npm", testAdapter{}, []string{"GHSA-ASKED-0001"}, s.newRejectLog("npm"))
	if err != nil || written != 0 || skipped != 0 || rejected != 1 {
		t.Fatalf("written %d, skipped %d, rejected %d, err %v", written, skipped, rejected, err)
	}
}

// R-VULN-004, S11 of the fix wave, Q-VULN-122: a fetched record whose modified time is older than the stored one does not replace
// it; the same record, or a newer one, is judged by digest and time, and a row an older adapter wrote is never skipped by its digest.
func TestR_VULN_004_AnOlderFetchedRecordDoesNotReplaceANewerOne(t *testing.T) {
	const current = 3
	stored := store.Stored{Modified: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), SHA256: bytes.Repeat([]byte{1}, 32), AdapterVersion: current}
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
		{"the same record, written by an older adapter", rec(stored.Modified, 1), false},
		{"the same time, another digest", rec(stored.Modified, 2), false},
		{"newer", rec(stored.Modified.Add(time.Hour), 2), false},
	} {
		row := stored
		if strings.Contains(tt.name, "older adapter") {
			row.AdapterVersion = current - 1
		}
		if got := fetchedNotNewer(row, tt.rec, current); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

// R-VULN-004, Q-VULN-116, Q-VULN-119: a fetched record above max_record_size is rejected and counted, and logged with the name it
// has in an archive, the length the response declared, the limit and the key.
func TestR_VULN_004_AFetchedRecordAboveTheRecordSizeIsRejectedAndLogged(t *testing.T) {
	const limit = 4096
	body := `{"id":"GHSA-BIG-0001","modified":"2026-10-01T00:00:00Z","summary":"` + strings.Repeat("x", 3*limit) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = srv.URL + "/"
	cfg.Sources.OSV.MaxRecordSize = limit
	var logs bytes.Buffer
	s := &Service{o: Options{HTTP: srv.Client(), Config: cfg}, log: slog.New(slog.NewJSONHandler(&logs, nil))}
	rl := s.newRejectLog("npm")
	written, skipped, rejected, err := s.fetchApply(context.Background(), "npm", testAdapter{}, []string{"GHSA-BIG-0001"}, rl)
	if err != nil || written != 0 || skipped != 0 || rejected != 1 {
		t.Fatalf("written %d, skipped %d, rejected %d, err %v", written, skipped, rejected, err)
	}
	var l map[string]any
	if err := json.Unmarshal(logs.Bytes(), &l); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if l["level"] != "WARN" || l["msg"] != "an advisory record was rejected" || l["source"] != "osv" || l["ecosystem"] != "npm" ||
		l["entry"] != "GHSA-BIG-0001.json" || l["id"] != "GHSA-BIG-0001" || l["size"] != float64(len(body)) ||
		l["limit"] != float64(limit) || l["key"] != "vulnerabilities.sources.osv.max_record_size" {
		t.Errorf("log line %v", l)
	}
}

// R-VULN-004, Q-VULN-119: a body that fails a rule of R-VULN-006 is logged with the rule, and a body with another identifier with
// that fact; neither has a size or a key.
func TestR_VULN_004_AFetchedRecordThatFailsAnyRuleIsLoggedWithItsRule(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ASKED") {
			_, _ = io.WriteString(w, `{"id":"GHSA-OTHER-0001","modified":"2026-10-01T00:00:00Z"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"GHSA-BAD-0001","modified":"yesterday"}`)
	}))
	defer srv.Close()
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = srv.URL + "/"
	cfg.Sources.OSV.MaxRecordSize = config.MiB
	var logs bytes.Buffer
	s := &Service{o: Options{HTTP: srv.Client(), Config: cfg}, log: slog.New(slog.NewJSONHandler(&logs, nil))}
	_, _, rejected, err := s.fetchApply(context.Background(), "npm", testAdapter{}, []string{"GHSA-ASKED-0001", "GHSA-BAD-0001"}, s.newRejectLog("npm"))
	if err != nil || rejected != 2 {
		t.Fatalf("rejected %d, err %v", rejected, err)
	}
	out := logs.String()
	for _, want := range []string{`"entry":"GHSA-ASKED-0001.json"`, `carries the identifier`, `"entry":"GHSA-BAD-0001.json"`, `modified is not an RFC 3339 time`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
	if strings.Contains(out, `"key"`) || strings.Contains(out, `"size"`) {
		t.Errorf("a rule rejection names a size or a key: %s", out)
	}
}

// R-VULN-004, Q-VULN-120: the bodies that wait for the decoder are bounded by bytes: with a gate of 1 MiB and 400 KiB bodies, no
// more than the gate and the body that passed it are ever counted at once, however many records are requested, and every record
// arrives.
func TestR_VULN_004_FetchedBodiesWaitingForTheDecoderAreBoundedByBytes(t *testing.T) {
	body := `{"id":"GHSA-X","modified":"2026-10-01T00:00:00Z","summary":"` + strings.Repeat("x", 400<<10) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = srv.URL + "/"
	cfg.Sources.OSV.MaxRecordSize = config.MiB
	s := &Service{o: Options{HTTP: srv.Client(), Config: cfg}, log: slog.New(slog.DiscardHandler)}
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = "GHSA-" + strconv.Itoa(i)
	}
	stream := s.fetchStream(context.Background(), "npm", ids, 1<<20)
	n := 0
	for f := range stream.ch {
		if f.err != nil || !f.found || len(f.body) != len(body) {
			t.Fatalf("record %s: %v, found %v, %d bytes", f.id, f.err, f.found, len(f.body))
		}
		time.Sleep(2 * time.Millisecond) // the decoder is slower than the source
		stream.gate.release(f.held)
		n++
	}
	if n != len(ids) {
		t.Fatalf("%d of %d records arrived", n, len(ids))
	}
	if limit := int64(1<<20) + int64(len(body)); stream.gate.peak > limit {
		t.Errorf("%d bytes were counted at once, more than the gate and one body (%d)", stream.gate.peak, limit)
	}
}

// R-VULN-004, Q-VULN-120: the gate lets a body larger than its limit pass alone, makes the next wait until it is released, and
// gives up when the run's context ends.
func TestR_VULN_004_TheByteGateWaitsAndLetsALoneBodyPass(t *testing.T) {
	g := newByteGate(100)
	if err := g.acquire(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := g.acquire(ctx, 1); err == nil {
		t.Fatal("a body was admitted beside one that fills the gate")
	}
	g.release(500)
	if err := g.acquire(context.Background(), 60); err != nil {
		t.Fatal(err)
	}
	if err := g.acquire(context.Background(), 40); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.acquire(context.Background(), 10) }()
	select {
	case err := <-done:
		t.Fatalf("admitted above the limit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	g.release(60)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// R-VULN-004, Q-VULN-120: the timeout of a record covers its request and its body, not the wait at the byte gate: a decoder that
// stalls for several times the timeout makes the waiting requests neither fail nor end as upstream_timeout.
func TestR_VULN_004_AStallAtTheByteGateIsNoUpstreamTimeout(t *testing.T) {
	body := `{"id":"GHSA-X","modified":"2026-10-01T00:00:00Z","summary":"` + strings.Repeat("x", 400<<10) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = srv.URL + "/"
	cfg.Sources.OSV.MaxRecordSize = config.MiB
	s := &Service{o: Options{HTTP: srv.Client(), Config: cfg}, log: slog.New(slog.DiscardHandler), partTimeoutOverride: 100 * time.Millisecond}
	ids := []string{"GHSA-1", "GHSA-2", "GHSA-3", "GHSA-4", "GHSA-5", "GHSA-6"}
	stream := s.fetchStream(context.Background(), "npm", ids, 500<<10) // one body fits; the others wait
	n := 0
	for f := range stream.ch {
		if f.err != nil || !f.found {
			t.Fatalf("record %s: %v (found %v)", f.id, f.err, f.found)
		}
		if n == 0 {
			time.Sleep(500 * time.Millisecond) // five times the timeout, with the other requests waiting at the gate
		}
		stream.gate.release(f.held)
		n++
	}
	if n != len(ids) {
		t.Fatalf("%d of %d records arrived", n, len(ids))
	}
}
