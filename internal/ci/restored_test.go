package ci

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestR_CI_018_TheRestoreCutOff(t *testing.T) {
	restored := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	skew := 60 * time.Second
	for name, tc := range map[string]struct {
		iat      time.Time
		restored time.Time
		refused  bool
	}{
		"R-CI-018 a token issued ten minutes before the restore finished is refused":  {restored.Add(-10 * time.Minute), restored, true},
		"R-CI-018 with the default 60 s skew, a token issued 30 s before is refused":  {restored.Add(-30 * time.Second), restored, true},
		"R-CI-018 the leeway lies on the refusing side: 30 s after is refused":        {restored.Add(30 * time.Second), restored, true},
		"R-CI-018 a token issued exactly clock_skew after the restore is refused":     {restored.Add(skew), restored, true},
		"R-CI-018 a token issued more than clock_skew after the restore is exchanged": {restored.Add(skew + time.Second), restored, false},
		"R-CI-018 an installation never restored refuses nothing":                     {restored.Add(-time.Hour), time.Time{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := predatesRestore(tc.iat, tc.restored, skew); got != tc.refused {
				t.Fatalf("predatesRestore = %v, want %v", got, tc.refused)
			}
		})
	}
	t.Run("R-CI-018 the stored cut-off is microseconds, and 0 means no restore", func(t *testing.T) {
		if !restoredAtOf(0).IsZero() || !restoredAtOf(-1).IsZero() {
			t.Fatal("0 is no restore")
		}
		if got := restoredAtOf(restored.UnixMicro()); !got.Equal(restored) {
			t.Fatalf("restoredAtOf = %v", got)
		}
	})
}

// R-CI-029's column "After a restore" against the registrations a backup and a restore read: the CI tables' classes
// (R-BAK-004) and the revision columns a restore lifts (R-BAK-022).
func TestR_CI_029_CIStateAcrossARestore(t *testing.T) {
	for table, want := range map[string]tables.Class{
		"ci_issuers":            tables.Data,
		"ci_mappings":           tables.Data,
		"ci_token_issuances":    tables.Data,
		"ci_subject_token_uses": tables.Data,      // R-CI-018: a jti used before the snapshot stays used
		"ci_issuer_keys":        tables.Transient, // Q-CI-12: a removed key is never revived
	} {
		got, ok := tables.Lookup(table)
		if !ok || got.Class != want {
			t.Errorf("R-CI-029 %s: class %v (registered %v), want %v", table, got.Class, ok, want)
		}
	}
	for _, col := range [][2]string{{"ci_issuers", "version"}, {"ci_issuers", "mappings_version"}, {"ci_mappings", "version"}} {
		if !meta.IsRevisionColumn(col[0], col[1]) {
			t.Errorf("R-CI-029 %s.%s is not a revision column: a restore must lift it above the old installation's values", col[0], col[1])
		}
	}
}
