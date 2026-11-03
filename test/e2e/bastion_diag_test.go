//go:build e2e

package e2e

// Diagnostics of the Bastion tests: when a Bastion scenario fails, the logs of every pod of the Bastions' namespace and of the main
// installation (the previous containers too) and their events are written into the artifact directory before anything is torn down,
// because the next step of a scenario (a restore, an upgrade) replaces the pods whose logs explain the failure.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const basNamespace = "trove-bastion"

// basDumpLogs writes the logs of the pods of the namespaces trove-bastion and the main installation's, and their events, to
// <artifacts>/bastion-diag-<label>/. It never fails a test.
func basDumpLogs(t testing.TB, label string) {
	t.Helper()
	dir := filepath.Join(cfg.out, "bastion-diag-"+strings.NewReplacer("/", "_", " ", "_").Replace(label))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("diagnostics: %v", err)
		return
	}
	for _, ns := range []string{basNamespace, cfg.namespace} {
		out, err := kubectl("-n", ns, "get", "pods", "-o", "wide")
		_ = os.WriteFile(filepath.Join(dir, ns+"-pods.txt"), []byte(out+errText(err)), 0o644)
		ev, err := kubectl("-n", ns, "get", "events", "--sort-by=.lastTimestamp")
		_ = os.WriteFile(filepath.Join(dir, ns+"-events.txt"), []byte(ev+errText(err)), 0o644)
		names, err := kubectl("-n", ns, "get", "pods", "-o", "name")
		if err != nil {
			continue
		}
		for _, n := range strings.Fields(names) {
			base := ns + "-" + strings.TrimPrefix(n, "pod/")
			for _, prev := range []bool{false, true} {
				args := []string{"-n", ns, "logs", n, "--all-containers", "--timestamps"}
				suffix := ".log"
				if prev {
					args, suffix = append(args, "--previous"), ".previous.log"
				}
				l, err := kubectl(args...)
				if l != "" || (err != nil && !prev) {
					_ = os.WriteFile(filepath.Join(dir, base+suffix), []byte(l+errText(err)), 0o644)
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "e2e: Bastion diagnostics written to %s (%s)\n", dir, time.Now().UTC().Format(time.RFC3339))
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return "\n" + err.Error() + "\n"
}
