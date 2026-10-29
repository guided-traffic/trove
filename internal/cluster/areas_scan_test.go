package cluster

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var registerAreaCall = regexp.MustCompile(`\bRegisterArea\(`)

// TestAreasAreDeclaredCentrally: every area is declared in areas.go. A package
// that wants an area adds it there and, for a change area, seeds its
// cluster_epoch row in a migration (the integration schema test checks the row).
func TestAreasAreDeclaredCentrally(t *testing.T) {
	t.Run("R-ARCH-018 RegisterArea is called only from internal/cluster/areas.go", func(t *testing.T) {
		_, self, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(self), "..", "..")
		var bad []string
		err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if filepath.ToSlash(rel) == "internal/cluster/areas.go" {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if registerAreaCall.Match(src) {
				bad = append(bad, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(bad) > 0 {
			t.Fatalf("RegisterArea is called outside internal/cluster/areas.go: %v", bad)
		}
	})
}
