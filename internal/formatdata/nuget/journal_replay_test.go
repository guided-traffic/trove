package nuget

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

func TestR_NUGET_033_TheHandlerIsRegisteredForBothEvents(t *testing.T) {
	for _, ev := range []string{audit.EventPackagePublished, audit.EventPackageDeleted} {
		if h, ok := journal.HandlerFor(ev); !ok || h.Domain != "nuget" {
			t.Errorf("%s: handler = %+v, %v", ev, h, ok)
		}
	}
	// An unlist or a relist reserves and withdraws nothing (R-NUGET-033).
	if _, ok := journal.HandlerFor(audit.EventPackageMetadataChanged); ok {
		t.Error("package.metadata_changed has a replay handler")
	}
}

// An entry that is not complete names nothing; the replay skips it before it reads the database.
func TestR_NUGET_033_AnIncompleteEntryIsSkipped(t *testing.T) {
	sum := strings.Repeat("ab", 64)
	good := map[string]any{"repository": 7, "id": "acme.widgets", "version": "1.2.3", "sha512": sum}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for name, entry := range map[string]any{
		"no repository": with("repository", 0),
		"no id":         with("id", ""),
		"long id":       with("id", strings.Repeat("a", maxEntryText+1)),
		"no version":    with("version", ""),
		"no sum":        with("sha512", ""),
		"short sum":     with("sha512", sum[:126]),
		"upper sum":     with("sha512", strings.ToUpper(sum)),
		"not hex":       with("sha512", strings.Repeat("zz", 64)),
		"not an object": []int{1},
		"wrong type":    with("repository", "seven"),
	} {
		for _, ev := range []string{audit.EventPackagePublished, audit.EventPackageDeleted} {
			t.Run(name+" "+ev, func(t *testing.T) {
				raw, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				res, err := replayPackage(context.Background(), nil, journal.Env{Now: time.Now()},
					journal.Object{V: 1, ID: "0197c2a4-1111-7000-8000-000000000001", At: time.Now(), Event: ev, Entry: raw})
				if err != nil || res.Outcome != journal.Skipped {
					t.Fatalf("result %+v, error %v", res, err)
				}
			})
		}
	}
}
