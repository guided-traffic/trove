package create

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup"
)

func TestR_BAK_026_KMSNeedsAStoreThatNamesTheKey(t *testing.T) {
	for name, tc := range map[string]struct {
		reported string
		ok       bool
	}{
		"the configured key": {"arn:aws:kms:eu:1:key/abc", true},
		"no key reported":    {"", false},
		"another key":        {"arn:aws:kms:eu:1:key/zzz", false},
	} {
		t.Run("R-BAK-026 "+name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg.Target.SSE.Mode, e.cfg.Target.SSE.KMSKeyID = "kms", "key/abc"
			e.mem.SSE, e.mem.KMSKeyID = "aws:kms", tc.reported
			_, err := e.create(backup.CreateOptions{})
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			exitOf(t, err, backup.ExitUsage, backup.CodeEncryptionMismatch)
			if !strings.Contains(err.Error(), "backup.target.sse") {
				t.Fatalf("the error does not name the setting: %v", err)
			}
		})
	}
}
