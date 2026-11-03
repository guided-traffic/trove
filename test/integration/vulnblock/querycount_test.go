//go:build integration

package vulnblock

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The statements of a download, counted from the PostgreSQL log: the test turns log_statement on for its own database (every
// test has a database of its own, so no other test's statement is in the window) before its replicas connect, and reads the
// container's log between two instants. A statement is counted by its sqlc name.

var statementLine = regexp.MustCompile(`(?:statement|execute [^:]*): (.*)$`)

// statementsSince returns the statements the database logged since an instant, without the transaction control.
func statementsSince(t *testing.T, at time.Time) []string {
	t.Helper()
	out, err := exec.Command("docker", "logs", "--since", at.UTC().Format(time.RFC3339Nano), "trove-it-postgres").CombinedOutput()
	if err != nil {
		t.Fatalf("docker logs: %v\n%s", err, out)
	}
	var stmts []string
	for _, line := range strings.Split(string(out), "\n") {
		m := statementLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		s := strings.TrimSpace(m[1])
		switch strings.ToUpper(strings.Fields(s + " x")[0]) {
		case "BEGIN", "COMMIT", "ROLLBACK", "SET", "RESET", "SHOW", "DISCARD", "DEALLOCATE":
			continue
		}
		stmts = append(stmts, s)
	}
	return stmts
}

// nameOf is the sqlc name of a statement ("-- name: GetLiveItem :many"), or its first words.
func nameOf(s string) string {
	if i := strings.Index(s, "-- name: "); i >= 0 {
		if f := strings.Fields(s[i+len("-- name: "):]); len(f) > 0 {
			return f[0]
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// downloadStatements runs n downloads of a path against a replica and returns the statements the window logged, by name.
func downloadStatements(t *testing.T, e *env, r *testenv.Replica, path string, n int) map[string]int {
	t.Helper()
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Fatalf("warm-up %s: %d %s", path, a.Status, a.Body)
	}
	time.Sleep(time.Second)
	start := time.Now()
	for range n {
		if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
			t.Fatalf("%s: %d", path, a.Status)
		}
	}
	time.Sleep(300 * time.Millisecond)
	counts := map[string]int{}
	for _, s := range statementsSince(t, start.Add(-100*time.Millisecond)) {
		name := nameOf(s)
		counts[name]++
		if strings.Contains(s, "vuln_") && name != "GetLiveItem" {
			t.Errorf("a statement of the scanning's tables runs on a download: %s", name)
		}
	}
	return counts
}

func describe(c map[string]int) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-50s x%d\n", k, c[k])
	}
	return b.String()
}

// background are the statements of the replica itself (its health probes and its registration), which run whatever is served.
var background = map[string]bool{
	"GetSchemaState": true, "TableExists": true, "SELECT NOT pg_is_in_recovery()": true, "-- ping": true, "ping": true, "UpsertReplica": true,
	"ListClusterEpochs": true, "EndpointsCountServingReplicas": true,
}

// statementsPerDownload checks that a path costs the same statements with scanning (the replica on) and without (the replica
// off), that the item lookup runs once per download, and that no statement of the scanning's tables runs but the item lookup's join. A statement of the request runs once
// per download; the batched and the cached ones (download statistics, token and principal reads) run less often.
func statementsPerDownload(t *testing.T, e *env, on, off *testenv.Replica, path string) {
	t.Helper()
	const n = 30
	withScan := downloadStatements(t, e, on, path, n)
	without := downloadStatements(t, e, off, path, n)
	perRequest := func(c map[string]int) map[string]int {
		out := map[string]int{}
		for k, v := range c {
			if !background[k] && v >= n {
				out[k] = v
			}
		}
		return out
	}
	pw, po := perRequest(withScan), perRequest(without)
	if fmt.Sprint(pw) != fmt.Sprint(po) || pw["GetLiveItem"] != n {
		t.Errorf("per download with scanning %v, without %v; want the same statements, one GetLiveItem per download\nwith:\n%swithout:\n%s",
			pw, po, describe(withScan), describe(without))
	}
	sum := func(c map[string]int) (total int) {
		for _, v := range c {
			total += v
		}
		return total
	}
	t.Logf("statements per download: %.2f with scanning, %.2f without\n%s", float64(sum(withScan))/n, float64(sum(without))/n, describe(withScan))
}

// logStatements turns the statement log on for the database of a test; replicas started after it log their statements.
func logStatements(t *testing.T, e *env) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available: the statements are counted from the PostgreSQL log")
	}
	e.st.Exec("ALTER DATABASE " + e.st.Database() + " SET log_statement = 'all'")
}

// R-VULN-015 (R-ARCH-027): scanning adds no query to a download. One replica scans (a dismissed and a resolved finding exist,
// so the lateral join runs and skips them) and one does not (vulnerabilities.enabled false); both serve the same stored
// versions of one database, and the statements the log shows per download are the same: the verdict is read by the item
// lookup, not by a query of its own.
func TestR_VULN_015_ScanningAddsNoQueryToADownload(t *testing.T) {
	e := newEnv(t, options{})
	logStatements(t, e)
	on := e.replica("scan-on")
	off := e.replica("scan-off", "TROVE_VULNERABILITIES_ENABLED=false")
	e.publish(on, rNPM, mal, "1.0.0")
	e.publish(on, rNPM, mal, "1.0.1")
	e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "dismissed")
	e.finding(rNPM, "npm", mal, "1.0.1", malA, "malicious", "resolved")
	for name, path := range map[string]string{
		"a local npm tarball":     tgzPath(rNPM, mal, "1.0.0"),
		"a local npm tarball (2)": tgzPath(rNPM, mal, "1.0.1"),
		"the raw view":            "/files/" + rNPM + "/" + e.itemPath(rNPM, "-1.0.0.tgz"),
	} {
		t.Run(name, func(t *testing.T) { statementsPerDownload(t, e, on, off, path) })
	}
}

// The same for the remote hit, which reads the cache entry, the item and the format's facts: the verdict joins the item lookup.
func TestR_VULN_015_ScanningAddsNoQueryToARemoteHit(t *testing.T) {
	e := newEnv(t, options{})
	logStatements(t, e)
	on := e.replica("scan-on")
	off := e.replica("scan-off", "TROVE_VULNERABILITIES_ENABLED=false")
	if a := e.do(on, "GET", "/api/npm/"+rRemote+"/"+e.pkg, e.tokReader); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	if a := e.do(on, "GET", tgzPath(rRemote, e.pkg, e.ver), e.tokReader); a.Status != 200 {
		t.Fatalf("fill: %d %s", a.Status, a.Body)
	}
	e.requireLinked(rRemote, e.pkg, e.ver)
	e.finding(rRemote, "npm", e.pkg, e.ver, malA, "malicious", "dismissed")
	statementsPerDownload(t, e, on, off, tgzPath(rRemote, e.pkg, e.ver))
}
