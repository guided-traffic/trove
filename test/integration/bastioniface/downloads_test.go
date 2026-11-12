//go:build integration

package bastioniface

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

func downloadEvent(i int, at time.Time) iface.DownloadEvent {
	return iface.DownloadEvent{
		At: at, TokenID: "0192f1c4-0000-7000-8000-0000000000e1", PrincipalID: "0192f1c4-0000-7000-8000-0000000000e2",
		ClientAddress: "203.0.113." + strconv.Itoa(10+i%5), Repository: "it/npm-all",
		Path:   "/api/npm/it/npm-all/pkg-" + strconv.Itoa(i) + "/-/pkg-" + strconv.Itoa(i) + "-1.0.0.tgz",
		SHA256: strings.Repeat("9f", 31) + strconv.Itoa(10+i), UserAgent: "npm/11.20.0 node/v24.0.0", Replica: "trove-bastion-7d9c-x2k4q",
	}
}

func downloadsFixture(t *testing.T, o siteOpts) (*site, *env, string) {
	e := newEnv(t)
	s := e.newSite(o)
	return s, e, s.registered("partners", e.registration("partners"))
}

// TestR_BAS_016_AWarmInstallIsTwentyArtifactDownloadedEventsOfTheBastionOrigin is the first acceptance criterion
// of R-BAS-016, as far as it needs no Bastion: twenty events in one batch become twenty artifact.downloaded rows
// with origin bastion, the callers' token ID and client address, the repository, the path and the time the
// Bastion reports.
func TestR_BAS_016_AWarmInstallIsTwentyArtifactDownloadedEventsOfTheBastionOrigin(t *testing.T) {
	s, e, bst := downloadsFixture(t, siteOpts{recordDownloads: true})
	at := time.Now().Add(-5 * time.Second).UTC().Truncate(time.Millisecond)
	events := make([]iface.DownloadEvent, 20)
	for i := range events {
		events[i] = downloadEvent(i, at)
	}
	rp := s.iface(bst, http.MethodPost, iface.PathDownloads, iface.DownloadsRequest{Events: events})
	if rp.Status != http.StatusNoContent || len(rp.Body) != 0 || rp.Header.Get(iface.HeaderInterface) != strconv.Itoa(iface.InterfaceVersion) {
		t.Fatalf("%d %s %v", rp.Status, rp.Body, rp.Header)
	}
	waitFor(t, "twenty artifact.downloaded events", 10*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND details->>'origin' = 'bastion'") == 20
	})
	if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND details->>'bastion' = 'partners'
		AND details->>'replica' = 'trove-bastion-7d9c-x2k4q' AND token_id = '0192f1c4-0000-7000-8000-0000000000e1'::uuid
		AND actor_id = '0192f1c4-0000-7000-8000-0000000000e2'::uuid AND repository = 'it/npm-all' AND user_agent = 'npm/11.20.0 node/v24.0.0'
		AND host(source_ip) LIKE '203.0.113.%' AND path LIKE '/api/npm/it/npm-all/pkg-%' AND details->>'sha256' ~ '^[0-9a-f]{64}$'
		AND details->>'path' = path`); n != 20 {
		t.Errorf("%d of 20 events have the fields of R-BAS-016", n)
	}
	if n := e.number("SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND abs(extract(epoch FROM occurred_at) - $1) < 0.01", float64(at.UnixMilli())/1000); n != 20 {
		t.Errorf("%d of 20 events carry the time the Bastion reported", n)
	}
	if n := e.number("SELECT count(*) FROM audit_event WHERE action = 'bastion.downloads.dropped'"); n != 0 {
		t.Errorf("%d bastion.downloads.dropped events for a batch that lost nothing", n)
	}
}

// TestR_BAS_016_ADroppedCountIsOneEventAndTimesAreChecked: "dropped": 7 yields one bastion.downloads.dropped with
// count 7 and the period; an event in the future is recorded at the time of receipt, one older than max_age is
// dropped and counted in the same event.
func TestR_BAS_016_ADroppedCountIsOneEventAndTimesAreChecked(t *testing.T) {
	s, e, bst := downloadsFixture(t, siteOpts{recordDownloads: true, maxAge: time.Hour})
	since := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	future := time.Now().Add(3 * time.Hour)
	rp := s.iface(bst, http.MethodPost, iface.PathDownloads, iface.DownloadsRequest{
		Events:  []iface.DownloadEvent{downloadEvent(1, future), downloadEvent(2, time.Now().Add(-2*time.Hour))},
		Dropped: 7, DroppedSince: &since,
	})
	if rp.Status != http.StatusNoContent {
		t.Fatalf("%d %s", rp.Status, rp.Body)
	}
	waitFor(t, "the events", 10*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action IN ('artifact.downloaded', 'bastion.downloads.dropped')") == 2
	})
	if d := e.text("SELECT extract(epoch FROM now() - occurred_at)::text FROM audit_event WHERE action = 'artifact.downloaded'"); !strings.HasPrefix(d, "0.") && !strings.HasPrefix(d, "1.") && !strings.HasPrefix(d, "2.") {
		t.Errorf("an event from the future was recorded %s s ago, want the time of receipt", d)
	}
	if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'bastion.downloads.dropped' AND actor_type = 'bastion' AND actor_name = 'partners'
		AND (details->>'count')::int = 8 AND (details->>'reported')::int = 7 AND (details->>'expired')::int = 1 AND details->>'since' = $1`, since.Format(time.RFC3339)); n != 1 {
		t.Errorf("the dropped event: %d rows match", n)
	}
}

// TestR_BAS_016_WithoutRecordDownloadsTheOperationAnswers204AndRecordsNothing.
func TestR_BAS_016_WithoutRecordDownloadsTheOperationAnswers204AndRecordsNothing(t *testing.T) {
	s, e, bst := downloadsFixture(t, siteOpts{recordDownloads: false})
	since := time.Now().Add(-time.Minute).UTC()
	rp := s.iface(bst, http.MethodPost, iface.PathDownloads, iface.DownloadsRequest{Events: []iface.DownloadEvent{downloadEvent(1, time.Now())}, Dropped: 3, DroppedSince: &since})
	if rp.Status != http.StatusNoContent {
		t.Fatalf("%d %s", rp.Status, rp.Body)
	}
	time.Sleep(2500 * time.Millisecond) // the writer flushes every second
	if n := e.number("SELECT count(*) FROM audit_event WHERE action IN ('artifact.downloaded', 'bastion.downloads.dropped')"); n != 0 {
		t.Errorf("%d events recorded with audit.record_downloads off", n)
	}
}

// TestR_BAS_016_ABatchAboveMaxBatchIsRefused: 422, nothing recorded; a body that does not read is 400.
func TestR_BAS_016_ABatchAboveMaxBatchIsRefused(t *testing.T) {
	s, e, bst := downloadsFixture(t, siteOpts{recordDownloads: true, maxBatch: 3})
	events := make([]iface.DownloadEvent, 4)
	for i := range events {
		events[i] = downloadEvent(i, time.Now())
	}
	if rp := s.iface(bst, http.MethodPost, iface.PathDownloads, iface.DownloadsRequest{Events: events}); rp.Status != http.StatusUnprocessableEntity {
		t.Errorf("four events with max_batch 3: %d %s", rp.Status, rp.Body)
	}
	if rp := s.iface(bst, http.MethodPost, iface.PathDownloads, iface.DownloadsRequest{Events: events[:3]}); rp.Status != http.StatusNoContent {
		t.Errorf("three events: %d %s", rp.Status, rp.Body)
	}
	if rp := s.iface(bst, http.MethodPost, iface.PathDownloads, `{"events":[],"dropped":0,"surprise":true}`); rp.Status != http.StatusBadRequest {
		t.Errorf("an unknown member: %d %s", rp.Status, rp.Body)
	}
	waitFor(t, "the three events", 10*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded'") == 3
	})
}
