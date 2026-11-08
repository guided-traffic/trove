package main

import (
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

// handlerPending are the journaled events whose replay handler is not built yet, each with the gap that says so. The list
// keeps itself honest: an event that has a handler must leave it.
var handlerPending = map[string]string{
	"deb.package.deleted":      "the replay of a Debian deletion is built with the local class (T-17 WP3, R-DEB-032); nothing emits the event before that",
	"deb.coordinates.removed":  "the replay of removed coordinates is built with the local class (T-17 WP3, R-DEB-032); nothing emits the event before that",
	"deb.signing_key.promoted": "the replay of a key promotion is built with the signing keys (T-17 WP2, R-DEB-032); nothing emits the event before that",
	"deb.signing_key.retired":  "the replay of a key retirement is built with the signing keys (T-17 WP2, R-DEB-032); nothing emits the event before that",
}

// TestR_HA_054_EveryJournaledEventHasAReplayHandler: the trove binary, which runs both replays (`trove backup restore`,
// `trove admin post-restore`), holds a replay handler for every event the audit registry flags for the revocation journal.
// An entry without a handler is reported skipped, so a flagged event without one is a removal a restore undoes.
func TestR_HA_054_EveryJournaledEventHasAReplayHandler(t *testing.T) {
	journaled := 0
	for _, et := range audit.Types() {
		if et.Journal != audit.JournalRevocation {
			continue
		}
		journaled++
		_, has := journal.HandlerFor(et.Name)
		why, pending := handlerPending[et.Name]
		switch {
		case has && pending:
			t.Errorf("%s has a replay handler: remove it from handlerPending", et.Name)
		case !has && pending:
			t.Logf("%s has no replay handler yet (%s)", et.Name, why)
		case !has:
			t.Errorf("%s is journaled and has no replay handler: register one with journal.Register in the owning package's journal_replay.go", et.Name)
		}
	}
	if journaled == 0 {
		t.Fatal("the registry holds no journaled event: the test reads the wrong registry")
	}
	for name := range handlerPending {
		if et, ok := audit.Lookup(name); !ok || et.Journal != audit.JournalRevocation {
			t.Errorf("%s is listed as pending but is no journaled event", name)
		}
	}
}
