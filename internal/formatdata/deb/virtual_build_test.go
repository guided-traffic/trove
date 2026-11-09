package deb

import (
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

func at(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// R-DEB-025: a member release past its Valid-Until is not merged; an unexpired one hands its Valid-Until on.
func TestAdmitReleaseHoldsAMemberReleaseToItsValidUntil(t *testing.T) {
	now := *at("2026-11-09T12:00:00Z")
	m := VirtualMember{Key: "debian", URL: "https://deb.example/debian/"}
	rel := rows.Release{ReleaseDate: at("2026-11-09T10:00:00Z")}

	_, _, err := admitRelease(m, rel, *at("2026-11-09T11:00:00Z"), virtualInputs{}, now)
	var me *memberError
	if !errors.As(err, &me) || me.reason != ReasonMemberExpired || me.member != "debian" {
		t.Fatalf("an expired release: %v", err)
	}
	_, until, err := admitRelease(m, rel, *at("2026-11-12T00:00:00Z"), virtualInputs{}, now)
	if err != nil || until == nil || !until.Equal(*at("2026-11-12T00:00:00Z")) {
		t.Fatalf("an unexpired release: %v %v", until, err)
	}
	_, until, err = admitRelease(m, rel, time.Time{}, virtualInputs{}, now)
	if err != nil || until != nil {
		t.Fatalf("a release without Valid-Until: %v %v", until, err)
	}
}

// R-DEB-025, Q-DEB-43: a release older than the Date the last build recorded for the same upstream is a rollback; the record counts for
// the upstream it came from only, and a member without a record (new, or one that left and came back) has no floor.
func TestAdmitReleaseRefusesARollbackOfTheRecordedDate(t *testing.T) {
	now := *at("2026-11-09T12:00:00Z")
	m := VirtualMember{Key: "debian", URL: "https://deb.example/debian/"}
	older := rows.Release{ReleaseDate: at("2026-11-09T08:00:00Z")}
	prev := virtualInputs{Members: map[string]memberRecord{"debian": {Identity: m.URL, Date: "2026-11-09T09:00:00Z"}}}

	_, _, err := admitRelease(m, older, time.Time{}, prev, now)
	var me *memberError
	if !errors.As(err, &me) || me.reason != ReasonMemberRollback {
		t.Fatalf("an older Date: %v", err)
	}
	same := rows.Release{ReleaseDate: at("2026-11-09T09:00:00Z")}
	if rec, _, err := admitRelease(m, same, time.Time{}, prev, now); err != nil || rec.Date != "2026-11-09T09:00:00Z" || rec.Identity != m.URL {
		t.Fatalf("the same Date: %+v %v", rec, err)
	}
	other := m
	other.URL = "https://mirror.example/debian/"
	if _, _, err := admitRelease(other, older, time.Time{}, prev, now); err != nil {
		t.Fatalf("another upstream shares no floor with the record: %v", err)
	}
	if _, _, err := admitRelease(m, older, time.Time{}, virtualInputs{}, now); err != nil {
		t.Fatalf("no record, no floor: %v", err)
	}
}
