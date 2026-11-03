package bastionmain

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// capture is an audit.Recorder that keeps the asynchronous events it is given.
type capture struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *capture) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (c *capture) Record(_ context.Context, ev audit.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *capture) named(name string) []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []audit.Event
	for _, e := range c.events {
		if e.Type == name {
			out = append(out, e)
		}
	}
	return out
}

func details(t *testing.T, ev audit.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Details, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var (
	now      = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	recogn   = Recognition{Bastion: "partners", BastionID: "0192f1c4-0000-7000-8000-0000000000b1", TokenID: "0192f1c4-0000-7000-8000-000000000001", Replica: "bastion-0", Interface: 1, Route: true}
	digest   = strings.Repeat("9f", 32)
	oneEvent = func(at time.Time) iface.DownloadEvent {
		return iface.DownloadEvent{
			At: at, TokenID: "0192f1c4-0000-7000-8000-0000000000e1", PrincipalID: "0192f1c4-0000-7000-8000-0000000000e2", ClientAddress: "203.0.113.7",
			Repository: "npm-all", Path: "/api/npm/npm-all/@acme/sdk/-/sdk-2.1.0.tgz", SHA256: digest, UserAgent: "npm/11.20.0 node/v24.0.0", Replica: "bastion-1",
		}
	}
)

func intake(rec audit.Recorder, record bool) *Intake {
	return NewIntake(IntakeOptions{Audit: rec, MaxBatch: 3, MaxAge: 24 * time.Hour, Record: record, Now: func() time.Time { return now }})
}

// TestR_BAS_016_AnEventBecomesAnArtifactDownloadedOfTheBastionOrigin: the fields the specification lists.
func TestR_BAS_016_AnEventBecomesAnArtifactDownloadedOfTheBastionOrigin(t *testing.T) {
	c := &capture{}
	at := now.Add(-time.Minute)
	out, err := intake(c, true).Accept(context.Background(), recogn, iface.DownloadsRequest{Events: []iface.DownloadEvent{oneEvent(at)}})
	if err != nil || out.Recorded != 1 {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	evs := c.named(audit.EventArtifactDownloaded)
	if len(evs) != 1 {
		t.Fatalf("%d events", len(evs))
	}
	ev := evs[0]
	if ev.Actor.ID != "0192f1c4-0000-7000-8000-0000000000e2" || ev.Actor.TokenID != "0192f1c4-0000-7000-8000-0000000000e1" || ev.Actor.Kind != "user" {
		t.Errorf("actor = %+v", ev.Actor)
	}
	if ev.Source.ClientAddr != "203.0.113.7" || ev.Source.UserAgent != "npm/11.20.0 node/v24.0.0" {
		t.Errorf("source = %+v", ev.Source)
	}
	if ev.Repository != "npm-all" || ev.Path != "/api/npm/npm-all/@acme/sdk/-/sdk-2.1.0.tgz" || !ev.At.Equal(at) {
		t.Errorf("repository %q path %q at %v", ev.Repository, ev.Path, ev.At)
	}
	d := details(t, ev)
	want := map[string]any{"path": ev.Path, "sha256": digest, "origin": "bastion", "bastion": "partners", "replica": "bastion-1"}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("details.%s = %v, want %v", k, d[k], v)
		}
	}
	if len(c.named(audit.EventBastionDownloadsDropped)) != 0 {
		t.Error("a batch that lost nothing wrote bastion.downloads.dropped")
	}
	typ, _ := audit.Lookup(audit.EventArtifactDownloaded)
	if typ.Mode != audit.Async {
		t.Errorf("artifact.downloaded is %s", typ.Mode)
	}
}

func TestR_BAS_016_AnAnonymousCallerIsAnonymous(t *testing.T) {
	c := &capture{}
	e := oneEvent(now)
	e.TokenID, e.PrincipalID = "", ""
	if _, err := intake(c, true).Accept(context.Background(), recogn, iface.DownloadsRequest{Events: []iface.DownloadEvent{e}}); err != nil {
		t.Fatal(err)
	}
	if ev := c.named(audit.EventArtifactDownloaded)[0]; ev.Actor.Kind != "anonymous" || ev.Actor.ID != "" {
		t.Errorf("actor = %+v", ev.Actor)
	}
}

// TestR_BAS_016_TimesAreCheckedAgainstTheMainInstallationsClock: a future time is recorded at the time of receipt,
// an event older than max_age is dropped and counted.
func TestR_BAS_016_TimesAreCheckedAgainstTheMainInstallationsClock(t *testing.T) {
	c := &capture{}
	req := iface.DownloadsRequest{Events: []iface.DownloadEvent{oneEvent(now.Add(3 * time.Hour)), oneEvent(now.Add(-25 * time.Hour)), oneEvent(now.Add(-23 * time.Hour))}}
	out, err := intake(c, true).Accept(context.Background(), recogn, req)
	if err != nil || out.Recorded != 2 || out.Expired != 1 {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	evs := c.named(audit.EventArtifactDownloaded)
	if !evs[0].At.Equal(now) {
		t.Errorf("a future event is recorded at %v, want the time of receipt %v", evs[0].At, now)
	}
	if !evs[1].At.Equal(now.Add(-23 * time.Hour)) {
		t.Errorf("an old event keeps its time: %v", evs[1].At)
	}
	drop := c.named(audit.EventBastionDownloadsDropped)
	if len(drop) != 1 || details(t, drop[0])["count"] != float64(1) || details(t, drop[0])["expired"] != float64(1) {
		t.Errorf("dropped events = %+v", drop)
	}
}

// TestR_BAS_016_ADroppedCountWritesOneEventWithTheCountAndThePeriod: the second acceptance criterion.
func TestR_BAS_016_ADroppedCountWritesOneEventWithTheCountAndThePeriod(t *testing.T) {
	c := &capture{}
	since := now.Add(-time.Hour)
	if _, err := intake(c, true).Accept(context.Background(), recogn, iface.DownloadsRequest{Dropped: 7, DroppedSince: &since}); err != nil {
		t.Fatal(err)
	}
	drop := c.named(audit.EventBastionDownloadsDropped)
	if len(drop) != 1 {
		t.Fatalf("%d bastion.downloads.dropped events", len(drop))
	}
	d := details(t, drop[0])
	if d["count"] != float64(7) || d["reported"] != float64(7) || d["since"] != "2026-11-03T11:00:00Z" || d["replica"] != "bastion-0" {
		t.Errorf("details = %v", d)
	}
	if drop[0].Actor.Kind != "bastion" || drop[0].Actor.Name != "partners" || drop[0].Actor.TokenID != recogn.TokenID {
		t.Errorf("actor = %+v", drop[0].Actor)
	}
}

func TestR_BAS_016_WithoutRecordDownloadsNothingIsRecorded(t *testing.T) {
	c := &capture{}
	since := now
	out, err := intake(c, false).Accept(context.Background(), recogn, iface.DownloadsRequest{Events: []iface.DownloadEvent{oneEvent(now)}, Dropped: 4, DroppedSince: &since})
	if err != nil || !out.Disabled || len(c.events) != 0 {
		t.Fatalf("outcome %+v, error %v, %d events", out, err, len(c.events))
	}
	if intake(c, false).Records() || !intake(c, true).Records() {
		t.Error("Records does not report audit.record_downloads")
	}
}

func TestR_BAS_016_ABatchAboveMaxBatchIsRefusedAsAWhole(t *testing.T) {
	c := &capture{}
	evs := make([]iface.DownloadEvent, 4)
	for i := range evs {
		evs[i] = oneEvent(now)
	}
	_, err := intake(c, true).Accept(context.Background(), recogn, iface.DownloadsRequest{Events: evs})
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Invalid || len(e.Fields) != 1 || e.Fields[0].Pointer != "/events" || len(c.events) != 0 {
		t.Fatalf("error %v, %d events", err, len(c.events))
	}
	if _, err := intake(c, true).Accept(context.Background(), recogn, iface.DownloadsRequest{Dropped: -1}); err == nil {
		t.Error("a negative count was accepted")
	}
}

// TestR_BAS_016_AnEventThatBreaksItsContractIsDroppedAndCountedNotTheBatch: a poison event must not hold a
// Bastion's buffer forever.
func TestR_BAS_016_AnEventThatBreaksItsContractIsDroppedAndCountedNotTheBatch(t *testing.T) {
	c := &capture{}
	bad := []iface.DownloadEvent{oneEvent(now), oneEvent(now), oneEvent(now)}
	bad[0].SHA256 = "nothex"
	bad[1].Repository = ""
	bad[2].At = time.Time{}
	good := oneEvent(now)
	out, err := NewIntake(IntakeOptions{Audit: c, MaxBatch: 10, MaxAge: time.Hour, Record: true, Now: func() time.Time { return now }}).
		Accept(context.Background(), recogn, iface.DownloadsRequest{Events: append(bad, good)})
	if err != nil || out.Recorded != 1 || out.Invalid != 3 {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	if d := details(t, c.named(audit.EventBastionDownloadsDropped)[0]); d["count"] != float64(3) || d["invalid"] != float64(3) {
		t.Errorf("details = %v", d)
	}
}

func TestR_BAS_010_TheReleaseComesFromTheUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"trove-bastion/0.14.0":                     "0.14.0",
		"trove-bastion/0.14.0 (linux; go1.27)":     "0.14.0",
		"trove-bastion/":                           "",
		"curl/8.9":                                 "",
		"npm/11.20.0 trove-bastion/0.1.0":          "",
		"trove-bastion/" + strings.Repeat("9", 90): strings.Repeat("9", 64),
	} {
		if got := Release(ua); got != want {
			t.Errorf("Release(%q) = %q, want %q", ua, got, want)
		}
	}
}
