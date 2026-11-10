package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// Q-VULN-145: what a crash left in the scan directory is removed at start, the unpacked database in use is not.
func TestQ_VULN_145_TheLeftoversOfACrashedScanAreRemovedAtStart(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"scan-1/layout", "db/unpack-7", "db/" + "0123", "other"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cleanScanDir(context.Background(), dir, slog.Default())
	for p, want := range map[string]bool{"scan-1": false, "db/unpack-7": false, "db/0123": true, "other": true} {
		_, err := os.Stat(filepath.Join(dir, p))
		if (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", p, err == nil, want)
		}
	}
}
