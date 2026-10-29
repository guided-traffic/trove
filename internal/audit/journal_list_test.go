package audit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// journalPending are the events of the R-HA-054 table whose producing area does not
// exist yet. The test keeps the list honest: an event that is registered must not
// stay on it, and every event the table names must be either registered with the
// flag or on it.
var journalPending = map[string]string{
	"ci.token.revoked":         "CI identity (M4, R-CI-017)",
	"authz.ownership.removed":  "package ownership (R-AUTHZ-019)",
	"authz.ownership.released": "package ownership (R-AUTHZ-019)",
	"npm.unpublish":            "npm format (M2, R-NPM-017)",
	"package.published":        "NuGet format (M2, R-NUGET-033)",
	"package.deleted":          "NuGet format (M2, R-NUGET-033)",
	"ci.issuer.*":              "CI identity (M4)",
	"ci.mapping.*":             "CI identity (M4)",
}

// r054Events reads the event names of the first column of the table of
// R-HA-054 ("What is journaled") from the specification page, so that the list in
// the test cannot drift from the spec.
func r054Events(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/spec/ha-and-helm.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "### R-HA-054")
	if start < 0 {
		t.Fatal("R-HA-054 is not in docs/spec/ha-and-helm.md")
	}
	text = text[start:]
	tbl := strings.Index(text, "| Audit event | Owner | Replay |")
	if tbl < 0 {
		t.Fatal("the table of R-HA-054 moved; update the test")
	}
	var names []string
	seen := map[string]bool{}
	tick := regexp.MustCompile("`([a-z][a-z0-9_.*]*)`")
	for _, line := range strings.Split(text[tbl:], "\n")[2:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cell := strings.Split(line, "|")[1]
		for _, m := range tick.FindAllStringSubmatch(cell, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
	}
	if len(names) < 15 {
		t.Fatalf("read only %d event names from the table: %v", len(names), names)
	}
	return names
}

// TestR_HA_054_EveryListedEventIsFlaggedAndEntryRequired: every event type that the
// table of R-HA-054 names is registered with the journal flag `revocation` and, as
// the default for a flagged type, a required entry (R-OPS-014); the shared package
// events may declare the entry optional.
func TestR_HA_054_EveryListedEventIsFlaggedAndEntryRequired(t *testing.T) {
	names := r054Events(t)
	for _, name := range names {
		t.Run("R-HA-054 "+name, func(t *testing.T) {
			if strings.Contains(name, "*") {
				// A family ("ci.issuer.*"): the disables, deletions and updates of one
				// area; its members register with their area.
				prefix := strings.TrimSuffix(name, "*")
				for _, et := range Types() {
					if strings.HasPrefix(et.Name, prefix) && et.Journal == JournalRevocation && et.Entry != EntryRequired && !sharedTypes[et.Name] {
						t.Errorf("%s is flagged with entry %q", et.Name, et.Entry)
					}
				}
				return
			}
			et, ok := Lookup(name)
			if !ok {
				if why, pending := journalPending[name]; pending {
					t.Skipf("not registered yet: %s", why)
				}
				t.Fatalf("%s is in the table of R-HA-054 but not in the event registry", name)
			}
			if _, pending := journalPending[name]; pending {
				t.Errorf("%s is registered; remove it from journalPending", name)
			}
			if et.Mode != Transactional {
				t.Errorf("%s has mode %q; the journal row is written in the transaction of the change", name, et.Mode)
			}
			if et.Journal != JournalRevocation {
				t.Errorf("%s has journal flag %q, want %q", name, et.Journal, JournalRevocation)
			}
			if et.Entry != EntryRequired && !sharedTypes[name] {
				t.Errorf("%s has entry rule %q, want %q", name, et.Entry, EntryRequired)
			}
		})
	}
}
