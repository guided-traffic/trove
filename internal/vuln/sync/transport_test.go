package sync

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// R-OPS-019, Q-OPS-186: the error of a failed request reaches the error of the job, so it carries scheme and host of the source and
// nothing else of the URL: not the userinfo, not a token in the path, not a query.
func TestR_OPS_019_ATransportErrorNamesNeitherUserinfoNorPathNorQuery(t *testing.T) {
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.BaseURL = "https://ci-user:hunter2@source.example:8443/tok-3f9a77c1/"
	s := &Service{
		o: Options{Config: cfg, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}},
		log: slog.New(slog.DiscardHandler),
	}
	rawURLs := []string{s.osvURL("npm", "all.zip"), s.osvURL("npm", "GHSA-x.json") + "?key=query-secret-77"}
	for _, raw := range rawURLs {
		resp, err := s.get(context.Background(), raw)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("the request did not fail")
		}
		text := err.Error()
		for _, secret := range []string{"ci-user", "hunter2", "tok-3f9a77c1", "query-secret-77", "all.zip", "GHSA-x"} {
			if strings.Contains(text, secret) {
				t.Errorf("the error of %s names %q: %s", raw, secret, text)
			}
		}
		if !strings.Contains(text, "https://source.example:8443") || !strings.Contains(text, "connection refused") {
			t.Errorf("the error lost the host or the cause: %s", text)
		}
		var f *failure
		if !errors.As(err, &f) || strings.Contains(asError(err).Error(), "hunter2") {
			t.Errorf("the problem error: %v", err)
		}
	}

	// A URL that does not parse is not echoed either.
	cfg.Sources.OSV.BaseURL = "https://u:pw@bad host/tok-9"
	s.o.Config = cfg
	resp, err := s.get(context.Background(), s.osvURL("npm", "all.zip"))
	if err == nil {
		_ = resp.Body.Close()
	}
	if err == nil || strings.Contains(err.Error(), "tok-9") || strings.Contains(err.Error(), "pw@") {
		t.Errorf("an invalid URL: %v", err)
	}
}

// R-VULN-004, Q-VULN-122: the "unchanged" shortcuts and the incremental run are skipped when the configured record limit is not the
// one the last full sync completed with and that sync rejected records; a sync that rejected none is not repeated for a new limit.
func TestR_VULN_004_ALimitThatChangedAfterARejectionReadsTheArchiveAgain(t *testing.T) {
	var cfg config.Vulnerabilities
	cfg.Sources.OSV.MaxRecordSize = 8 * config.MiB
	s := &Service{o: Options{Config: cfg}}
	for _, tt := range []struct {
		name string
		st   store.SyncState
		want bool
	}{
		{"the limit was raised after a rejection", store.SyncState{FullRejected: 2, RecordLimit: int64(config.MiB)}, true},
		{"the limit was lowered after a rejection", store.SyncState{FullRejected: 2, RecordLimit: int64(64 * config.MiB)}, true},
		{"the same limit", store.SyncState{FullRejected: 2, RecordLimit: int64(8 * config.MiB)}, false},
		{"another limit, nothing rejected", store.SyncState{FullRejected: 0, RecordLimit: int64(config.MiB)}, false},
		{"no full sync yet", store.SyncState{}, false},
	} {
		if got := s.limitChanged(tt.st); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}
