//go:build integration

// Package vulnperf is the performance run of the vulnerability scanning (R-VULN-028, scenarios V-1 to V-9): 100,000 stored
// versions seeded in SQL, the npm and NuGet dumps of the test advisory source at the size of the real ones, one web and one
// worker replica of the composition root against the integration stack. It runs in the nightly workflow only
// (.github/workflows/performance.yml, job vuln-perf); Test and Release never starts it (owner, 2026-11-01: long tests out of
// the release path). perf/README.md says how to run it by hand and how to read a run.
package vulnperf

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The size of the installation (R-VULN-028): 90,000 npm versions in 9,000 packages across npm-local and npm-remote, 10,000
// NuGet versions in 1,000 packages in nuget-local. The versions of a package are 1.0.0 to 1.9.0.
const (
	npmPackages      = 9000
	nugetPackages    = 1000
	versionsPerPkg   = 10
	npmFiller        = 231000 // the records of the real npm archive, about (R-VULN-028 V-1)
	nugetFiller      = 7400   // the records of the real NuGet archive, about
	changedRecords   = 500    // V-3
	publishes        = 300    // V-4
	cachedDownloads  = 2000   // V-5
	auditPackages    = 1000   // V-6
	auditRepeats     = 60
	listRepeats      = 100
	nugetPageRepeats = 200
)

// row is one value of R-VULN-028: what it measures, its unit and its target. Every target is an upper bound.
type row struct {
	name   string
	unit   string
	target float64
}

// rows are the values of the run by ID. A scenario records one row, V-5 and V-7 two each.
var rows = map[string]row{
	"V-1":  {"full import of the npm archive into empty tables", "min", 15},
	"V-2":  {"full evaluation of the 100,000 versions", "min", 5},
	"V-3":  {"incremental sync with 500 changed records and their evaluation", "min", 2},
	"V-4":  {"evaluation added to a publish commit, p99 (exact, per call)", "ms", 5},
	"V-5a": {"latency added by the block check on a cached download, p99", "ms", 1},
	"V-5b": {"database statements of every cached download after the cache refills R-ARCH-027 exempts, the largest (both replicas, counted per request; each must be exactly 1)", "statements", 1.0},
	"V-6":  {"npm local audit of a 1,000-package lockfile, p99", "ms", 500},
	"V-7a": {"first page of the findings list for an admin, p99", "ms", 300},
	"V-7b": {"first page of the findings list for a caller with package-pattern grants, p99", "ms", 300},
	"V-8":  {"NuGet vulnerability page from the replica's cache, p99", "ms", 25},
	"V-9":  {"size of the advisory tables with indexes", "GiB", 2},
}

// The outcomes of a row. Only a measured value is compared with its target; a row that did not finish or was not measured
// misses it.
const (
	// outcomeMeasured: the value is the scenario's measurement.
	outcomeMeasured = "measured"
	// outcomeNotFinished: the scenario did not finish. Its wait ran out, or what it waited for ended without the result (the
	// value is the time it waited), or it failed before it measured (no value).
	outcomeNotFinished = "not finished"
	// outcomeNotMeasured: a scenario it needs did not finish, so it did not run (no value).
	outcomeNotMeasured = "not measured"
)

// result is one recorded row.
type result struct {
	ID      string `json:"id"`
	Name    string `json:"scenario"`
	Outcome string `json:"outcome"`
	// Value is null when the row has none: a scenario that failed before it measured, or one that did not run.
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`
	Target float64  `json:"target"`
	// Met is whether a measured value is within the target; a row that is not measured never meets it.
	Met  bool   `json:"met"`
	Note string `json:"note,omitempty"`
}

// report collects the results of a run and writes them to .out/perf-vuln.json, which the workflow uploads. With
// TROVE_PERF_GATE=1 a measured value above its target fails the test (the workflow sets it); without it the run reports only,
// which is how a run on a developer machine is read. A scenario that does not finish fails the test either way.
type report struct {
	t    *testing.T
	mu   sync.Mutex
	rows []result
}

// rowOf returns the row of an ID; an ID that is not in rows is a mistake of the run, found by its first run.
func (r *report) rowOf(id string) row {
	w, ok := rows[id]
	if !ok {
		panic("perf/vuln: no row " + id)
	}
	return w
}

func (r *report) add(res result) {
	r.mu.Lock()
	r.rows = append(r.rows, res)
	r.mu.Unlock()
}

// lookup returns the recorded row of an ID.
func (r *report) lookup(id string) (result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range r.rows {
		if res.ID == id {
			return res, true
		}
	}
	return result{}, false
}

// record records a measured value.
func (r *report) record(id string, value float64, note string) {
	r.t.Helper()
	w := r.rowOf(id)
	met := value <= w.target
	v := round(value)
	r.add(result{ID: id, Name: w.name, Outcome: outcomeMeasured, Value: &v, Unit: w.unit, Target: w.target, Met: met, Note: note})
	r.t.Logf("%s %s: %.3f %s (target %.3f %s) met=%t %s", id, w.name, value, w.unit, w.target, w.unit, met, note)
	if !met && os.Getenv("TROVE_PERF_GATE") == "1" {
		r.t.Errorf("%s %s: %.3f %s misses the target of %.3f %s", id, w.name, value, w.unit, w.target, w.unit)
	}
}

// recordCount records a count every sample of which must equal the target (V-5b): the value is the largest sample, and the
// row meets its target only when no sample missed, whatever the value. A miss fails the test with or without the gate,
// because a count does not depend on the host; its message names the first 20 samples that missed and how many there are
// (the note counts their statements by name).
func (r *report) recordCount(id string, value float64, misses []string, note string) {
	r.t.Helper()
	w := r.rowOf(id)
	met := len(misses) == 0 && value == w.target
	v := round(value)
	r.add(result{ID: id, Name: w.name, Outcome: outcomeMeasured, Value: &v, Unit: w.unit, Target: w.target, Met: met, Note: note})
	r.t.Logf("%s %s: %.3f %s (target exactly %.3f %s for every sample) met=%t %s", id, w.name, value, w.unit, w.target, w.unit, met, note)
	if !met {
		shown := misses
		if len(shown) > 20 {
			shown = append(append([]string{}, shown[:20]...), fmt.Sprintf("and %d more", len(misses)-20))
		}
		r.t.Errorf("%s %s: %d samples are not exactly %.0f %s:\n%s", id, w.name, len(misses), w.target, w.unit, strings.Join(shown, "\n"))
	}
}

// notFinished records a row whose scenario did not finish; waited is the time it waited, nil when it has none to report. The
// note says why in words of the run, never with data of the installation, because the regression issue prints it.
func (r *report) notFinished(id string, waited *time.Duration, note string) {
	r.t.Helper()
	w := r.rowOf(id)
	var v *float64
	if waited != nil && w.unit == "min" {
		m := round(waited.Minutes())
		v = &m
	}
	r.add(result{ID: id, Name: w.name, Outcome: outcomeNotFinished, Value: v, Unit: w.unit, Target: w.target, Note: note})
	r.t.Logf("%s %s: not finished (target %.3f %s): %s", id, w.name, w.target, w.unit, note)
}

// notMeasured records a row whose scenario did not run because the scenario of the row it needs did not finish.
func (r *report) notMeasured(id, note string) {
	r.t.Helper()
	w := r.rowOf(id)
	r.add(result{ID: id, Name: w.name, Outcome: outcomeNotMeasured, Unit: w.unit, Target: w.target, Note: note})
	r.t.Logf("%s %s: not measured (target %.3f %s): %s", id, w.name, w.target, w.unit, note)
}

// scenario runs one scenario as a subtest that records the rows ids. It does not run when a row it needs was not measured:
// its rows are recorded as not measured, naming the first such row, and the subtest is skipped (the scenario that did not
// finish has failed the test already). A row the scenario leaves unrecorded, because it failed before it measured, is recorded
// as not finished, so that the report and the regression issue list every row of the run.
func (r *report) scenario(t *testing.T, title string, ids, needs []string, fn func(t *testing.T)) {
	t.Helper()
	t.Run(title, func(t *testing.T) {
		for _, n := range needs {
			res, ok := r.lookup(n)
			if ok && res.Outcome == outcomeMeasured {
				continue
			}
			why := n + " did not finish"
			if ok && res.Outcome == outcomeNotMeasured {
				why = n + " was not measured"
			}
			for _, id := range ids {
				r.notMeasured(id, why)
			}
			t.Skipf("not measured: %s", why)
		}
		defer func() {
			for _, id := range ids {
				if _, ok := r.lookup(id); !ok {
					r.notFinished(id, nil, "the scenario failed before it measured; the test log says why")
				}
			}
		}()
		fn(t)
	})
}

func (r *report) write() {
	r.t.Helper()
	root := repoRoot()
	if err := os.MkdirAll(filepath.Join(root, ".out"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sort.Slice(r.rows, func(i, j int) bool { return r.rows[i].ID < r.rows[j].ID })
	doc := map[string]any{
		"requirement": "R-VULN-028",
		"environment": fmt.Sprintf("integration stack, in-process web and worker replica, %s/%s, GOMAXPROCS %d", runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0)),
		"gate":        os.Getenv("TROVE_PERF_GATE") == "1",
		"takenAt":     time.Now().UTC().Format(time.RFC3339),
		"results":     r.rows,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".out", "perf-vuln.json"), append(raw, '\n'), 0o644); err != nil { //nolint:gosec // a report, not a secret
		r.t.Fatal(err)
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found above " + file)
		}
		dir = parent
	}
}

func round(v float64) float64 { return math.Round(v*1000) / 1000 }

// percentile returns the p-th percentile (0 to 1) of the samples, by the nearest rank.
func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// refills are the only statements V-5b takes out of a download (Q-VULN-99): the refills R-ARCH-027 exempts. Its database
// budget per request reads "token and policy checks: 0 queries while their caches are valid" (docs/spec/architecture.md,
// R-ARCH-027). The token check reads two of the replica's caches, `auth_tokens` (the token row and its owner's status) and
// `auth_principals` (status and group memberships), which refill after their SafetyTTL, auth.tokens.cache_ttl (R-AUTH-023).
// The policy check reads the replica's grant index, which no request refills: it has no statement here. The repository
// snapshot is not here either: neither R-ARCH-027 nor its decisions exempt it. A statement that is not listed counts, and a
// download whose count is not exactly one fails the run with the statement's name: a false alarm at worst, never a hidden cost.
var refills = map[string]string{
	"IdentityTokenBySecret":  "auth_tokens (internal/auth/token/validate.go, Service.load)",
	"IdentityPrincipalByID":  "auth_principals (internal/auth/principal/store.go, PGStore.loadByID)",
	"IdentityGroupsOfMember": "auth_principals (internal/auth/principal/store.go, groupsOf)",
}

// authCacheTTL is auth.tokens.cache_ttl, the SafetyTTL of the caches of refills; the run keeps its default. A refill comes once
// per TTL and cache: a refill statement beyond ceil(window / TTL) + 1 in one replica is no refill of the TTL and counts.
const authCacheTTL = 15 * time.Second

// download is one download of V-5b: its replica, its request ID and what it ran in the database (testenv.RequestTransactions).
type download struct {
	replica, id string
	ran         testenv.RequestCost
}

// downloadCost is what V-5b counts of one download, after the refills are taken out: its statements and its transactions
// outside the refills, the statements that count by name, and the refill statements it ran.
type downloadCost struct {
	statements, transactions int
	counted, refilled        []string
}

// value is the download's count: its statements, or its transactions where they are more. Exactly 1 is the item lookup.
func (c downloadCost) value() int { return max(c.statements, c.transactions) }

// downloadCosts is what the downloads of one replica ran, in the order they ran, over a window of the given length.
type downloadCosts struct {
	replica string
	window  time.Duration
	all     []download
}

// costs takes the refills out of every download: a statement of refills is taken out while its cache has a refill left in
// the window (ceil(window / authCacheTTL) + 1 for each statement), and counts like any other statement after that. A
// transaction counts when it ran a statement that counts, or none at all.
func (c downloadCosts) costs() []downloadCost {
	left := map[string]int{}
	for name := range refills {
		left[name] = int(math.Ceil(float64(c.window)/float64(authCacheTTL))) + 1
	}
	out := make([]downloadCost, len(c.all))
	for i, d := range c.all {
		var dc downloadCost
		for _, tx := range d.ran.Transactions {
			counts := len(tx) == 0
			for _, s := range tx {
				if _, ok := refills[s]; ok && left[s] > 0 {
					left[s]--
					dc.refilled = append(dc.refilled, s)
					continue
				}
				dc.statements++
				dc.counted = append(dc.counted, s)
				counts = true
			}
			if counts {
				dc.transactions++
			}
		}
		out[i] = dc
	}
	return out
}

// largest is the largest count of a download of the replica; with no download the largest int, a miss of every target.
func (c downloadCosts) largest() int {
	if len(c.all) == 0 {
		return math.MaxInt
	}
	m := 0
	for _, dc := range c.costs() {
		m = max(m, dc.value())
	}
	return m
}

// misses names every download whose count is not exactly 1, with what counted in it; empty when there is none.
func (c downloadCosts) misses() []string {
	var out []string
	for i, dc := range c.costs() {
		if dc.value() != 1 {
			d := c.all[i]
			out = append(out, fmt.Sprintf("%s, request %s: %d statements and %d transactions after the refills (%s; refills %s)",
				d.replica, d.id, dc.statements, dc.transactions, orNone(dc.counted), orNone(dc.refilled)))
		}
	}
	return out
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// String names the replica's count, how many downloads ran a refill, the means before the refills are taken out, and every
// statement the downloads ran by name.
func (c downloadCosts) String() string {
	names, refilled := map[string]int{}, 0
	stmts, txs := 0, 0
	for _, dc := range c.costs() {
		if len(dc.refilled) > 0 {
			refilled++
		}
	}
	for _, d := range c.all {
		txs += len(d.ran.Transactions)
		for k, v := range d.ran.Statements() {
			names[k] += v
			stmts += v
		}
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ran := make([]string, 0, len(keys))
	for _, k := range keys {
		ran = append(ran, fmt.Sprintf("%s x%d", k, names[k]))
	}
	n := float64(max(len(c.all), 1))
	return fmt.Sprintf("%s: %d downloads, the largest count %d, %d not exactly 1, %d ran a refill; before the refills are taken out "+
		"%.4f statements and %.4f transactions per download; statements %s",
		c.replica, len(c.all), c.largest(), len(c.misses()), refilled, float64(stmts)/n, float64(txs)/n, strings.Join(ran, ", "))
}

// logExcerpt returns the last n lines of a replica's JSON log that say why a sync or a job ended: every WARN and ERROR line,
// the lines of the components vulnerabilities and jobs, and the requests to the advisory source. The rest, mostly the probes
// of the object store every 300 ms, is left out; the whole log is in .out/perf-vuln-logs.
func logExcerpt(log string, n int) string {
	var keep []string
	for _, line := range strings.Split(strings.TrimRight(log, "\n"), "\n") {
		for _, mark := range []string{`"level":"WARN"`, `"level":"ERROR"`, `"component":"vulnerabilities"`, `"component":"jobs"`, `"purpose":"vuln_source"`} {
			if strings.Contains(line, mark) {
				keep = append(keep, line)
				break
			}
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// histogramBound returns the smallest bucket bound, in seconds, at or below which at least the share q of the observations
// of the histogram series fall: an upper bound of the q-quantile that is exact to the bucket. -1 when the series has no
// observations. series is the metric name and the label selector inside the braces, for example
// `trove_vuln_evaluation_duration_seconds{trigger="publish"`.
func histogramBound(text, name, selector string, q float64) float64 {
	type bucket struct{ le, n float64 }
	var buckets []bucket
	var total float64
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, name+"_bucket{") || !strings.Contains(line, selector) {
			continue
		}
		i := strings.Index(line, `le="`)
		if i < 0 {
			continue
		}
		rest := line[i+4:]
		j := strings.Index(rest, `"`)
		le := math.Inf(1)
		if rest[:j] != "+Inf" {
			le, _ = strconv.ParseFloat(rest[:j], 64)
		}
		n, _ := strconv.ParseFloat(strings.TrimSpace(line[strings.LastIndex(line, " ")+1:]), 64)
		buckets = append(buckets, bucket{le, n})
		if math.IsInf(le, 1) {
			total = n
		}
	}
	if total == 0 {
		return -1
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].le < buckets[j].le })
	for _, b := range buckets {
		if b.n >= q*total {
			return b.le
		}
	}
	return math.Inf(1)
}

// histogramTotals returns the sum (in the unit of the metric) and the count of the observations of the histogram series
// whose line holds selector; ok is false when the series has no _count line yet. Scraped before and after one call, the
// differences are that call's own observation: the count rises by one and the sum by its duration, exactly and not to
// a bucket.
func histogramTotals(text, name, selector string) (sum float64, count int64, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, selector) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[strings.LastIndex(line, " ")+1:]), 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, name+"_sum{"):
			sum = v
		case strings.HasPrefix(line, name+"_count{"):
			count, ok = int64(v), true
		}
	}
	return sum, count, ok
}

// publishDoc is the body of an npm publish of name@version: a tarball with a package.json and a README, announced the way
// libnpmpublish does.
func publishDoc(t *testing.T, name, version string) []byte {
	t.Helper()
	pj, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct{ n, c string }{{"package.json", string(pj)}, {"README.md", "# " + name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + f.n, Mode: 0o644, Size: int64(len(f.c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.c))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tgz := buf.Bytes()
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	file := fmt.Sprintf("%s-%s.tgz", name, version)
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version}, "access": nil,
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "_id": name + "@" + version,
			"dist": map[string]any{
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:]),
				"tarball": "http://x/" + name + "/-/" + file,
			},
		}},
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
