//go:build integration

// Package vulnperf is the performance run of the vulnerability scanning (R-VULN-028, scenarios V-1 to V-9): 100,000 stored
// versions seeded in SQL, the npm and NuGet dumps of the test advisory source at the size of the real ones, one web and one
// worker replica of the composition root against the integration stack. It runs in the nightly workflow only
// (.github/workflows/performance.yml, job vuln-perf); Test and Release never starts it (owner, 2026-11-01: long tests out of
// the release path). perf/vuln/README.md says how to run it by hand.
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

// result is one scenario's recorded value.
type result struct {
	ID     string  `json:"id"`
	Name   string  `json:"scenario"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Target float64 `json:"target"`
	// Met is whether the value is within the target (every target of R-VULN-028 is an upper bound).
	Met  bool   `json:"met"`
	Note string `json:"note,omitempty"`
}

// report collects the results of a run and writes them to .out/perf-vuln.json, which the workflow uploads. With
// TROVE_PERF_GATE=1 a miss fails the test (the workflow sets it); without it the run reports only, which is how a run on
// a developer machine is read.
type report struct {
	t    *testing.T
	mu   sync.Mutex
	rows []result
}

func (r *report) record(id, name string, value float64, unit string, target float64, note string) {
	r.t.Helper()
	met := value <= target
	r.mu.Lock()
	r.rows = append(r.rows, result{ID: id, Name: name, Value: round(value), Unit: unit, Target: target, Met: met, Note: note})
	r.mu.Unlock()
	r.t.Logf("%s %s: %.3f %s (target %.3f %s) met=%t %s", id, name, value, unit, target, unit, met, note)
	if !met && os.Getenv("TROVE_PERF_GATE") == "1" {
		r.t.Errorf("%s %s: %.3f %s misses the target of %.3f %s", id, name, value, unit, target, unit)
	}
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
