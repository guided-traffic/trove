package buildinfo

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	t.Run("R-ARCH-001 version line names version, commit and date", func(t *testing.T) {
		s := Info{Version: "v0.1.0", Commit: "abc123", Date: "2026-10-29T00:00:00Z", GoVersion: "go1.27.3"}.String()
		for _, want := range []string{"trove v0.1.0", "abc123", "2026-10-29T00:00:00Z", "go1.27.3"} {
			if !strings.Contains(s, want) {
				t.Errorf("%q does not contain %q", s, want)
			}
		}
	})
	t.Run("R-ARCH-001 Get never returns empty fields", func(t *testing.T) {
		i := Get()
		if i.Version == "" || i.Commit == "" || i.Date == "" || i.GoVersion == "" {
			t.Errorf("empty field in %+v", i)
		}
	})
}
