package npm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

func TestR_NPM_047_TheUnpublishHandlerIsRegistered(t *testing.T) {
	h, ok := journal.HandlerFor(audit.EventNPMUnpublish)
	if !ok || h.Domain != "npm" {
		t.Fatalf("handler = %+v, %v", h, ok)
	}
	// npm.reinstate restores access and is not journaled (R-NPM-044).
	if _, ok := journal.HandlerFor(audit.EventNPMReinstate); ok {
		t.Error("npm.reinstate has a replay handler")
	}
}

// An entry that is not complete is a claim that names nothing; the replay skips it before it reads the database, so a
// forged object cannot reach a statement with an empty or oversized value.
func TestR_NPM_047_AnIncompleteEntryIsSkipped(t *testing.T) {
	long := string(make([]byte, maxEntryText+1))
	good := map[string]any{"repositoryId": 7, "package": "@acme/ui", "version": "1.4.0", "integrity": "sha512-AAAA", "actorId": noActor}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for name, entry := range map[string]any{
		"no repository":    with("repositoryId", 0),
		"negative":         with("repositoryId", -3),
		"no package":       with("package", ""),
		"long package":     with("package", long),
		"no version":       with("version", ""),
		"no integrity":     with("integrity", ""),
		"weak integrity":   with("integrity", "sha1-AAAA"),
		"not an object":    []int{1},
		"wrong type":       with("repositoryId", "seven"),
		"long integrity":   with("integrity", "sha512-"+long),
		"empty":            map[string]any{},
		"integrity as int": with("integrity", 5),
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			res, err := replayUnpublish(context.Background(), nil, journal.Env{Now: time.Now()},
				journal.Object{V: 1, ID: "0197c2a4-1111-7000-8000-000000000001", At: time.Now(), Event: audit.EventNPMUnpublish, Entry: raw})
			if err != nil || res.Outcome != journal.Skipped {
				t.Fatalf("result %+v, error %v", res, err)
			}
		})
	}
}
