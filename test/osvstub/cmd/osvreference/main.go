// Command osvreference records the public answer for the reference lockfile and the OSV records behind it, on one day:
// it builds the bulk body of every installed (name, version) of perf/npm/reference/package-lock.json, posts it gzipped to the
// public registry's bulk advisory endpoint as npm 7 and later do, stores the answer as <dir>/public-bulk.json, fetches the OSV
// record of every GHSA identifier the answer names from api.osv.dev (and the MAL- records those records alias) into
// <dir>/records/, and writes <dir>/SOURCE.json with the SHA-256 of every file. It is run by `make osv-reference-refresh`;
// the tests never need the network.
//
//	osvreference [-lock perf/npm/reference/package-lock.json] [-dir test/osvstub/testdata/reference] [-date 2026-11-03]
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	bulkURL = "https://registry.npmjs.org/-/npm/v1/security/advisories/bulk"
	osvURL  = "https://api.osv.dev/v1/vulns/"
)

var ghsaURL = regexp.MustCompile(`^https://github\.com/advisories/(GHSA-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})$`)

type file struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	License string `json:"license,omitempty"`
}

type source struct {
	Taken    string `json:"taken"`
	Lockfile string `json:"lockfile"`
	Names    int    `json:"names"`
	Versions int    `json:"versions"`
	Control  int    `json:"controlNames"`
	Bulk     string `json:"bulk"`
	Records  string `json:"records"`
	Files    []file `json:"files"`
}

func main() {
	lock := flag.String("lock", "perf/npm/reference/package-lock.json", "the reference lockfile")
	dir := flag.String("dir", "test/osvstub/testdata/reference", "the output directory")
	date := flag.String("date", time.Now().UTC().Format(time.DateOnly), "the day the data is taken, in SOURCE.json")
	flag.Parse()
	if err := run(*lock, *dir, *date); err != nil {
		fmt.Fprintln(os.Stderr, "osvreference:", err)
		os.Exit(1)
	}
}

// Requested reads the (name, version) pairs of the lockfile as npm audit does: every installed package with a version.
// It is the body the integration test posts as well (the test rebuilds it from the same lockfile).
func requested(lock string) (map[string][]string, int, error) {
	raw, err := os.ReadFile(lock) //nolint:gosec // a project file
	if err != nil {
		return nil, 0, err
	}
	var doc struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, 0, err
	}
	set := map[string]map[string]bool{}
	for path, p := range doc.Packages {
		i := strings.LastIndex(path, "node_modules/")
		if path == "" || i < 0 || p.Link || p.Version == "" {
			continue
		}
		name := path[i+len("node_modules/"):]
		if set[name] == nil {
			set[name] = map[string]bool{}
		}
		set[name][p.Version] = true
	}
	out := map[string][]string{}
	n := 0
	for name, vs := range set {
		for v := range vs {
			out[name] = append(out[name], v)
			n++
		}
		sort.Strings(out[name])
	}
	return out, n, nil
}

func run(lock, dir, date string) error {
	body, versions, err := requested(lock)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "records"), 0o755); err != nil { //nolint:gosec // project files
		return err
	}
	src := source{Taken: date, Lockfile: lock, Names: len(body), Versions: versions, Bulk: bulkURL, Records: osvURL + "{id}"}
	ids := map[string]bool{}
	reference, err := json.Marshal(body) // map keys are sorted: the same body for the same lockfile
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "public-request.json"), append(reference, '\n'), 0o644); err != nil { //nolint:gosec // project files
		return err
	}
	src.Files = append(src.Files, file{Path: "public-request.json", SHA256: digest(append(reference, '\n'))})
	if err := record(dir, "public-bulk.json", reference, &src, ids); err != nil {
		return err
	}
	// The control: real packages with known advisories, so that the comparison is not one of two empty answers.
	control, err := os.ReadFile(filepath.Join(dir, "control-request.json")) //nolint:gosec // a project file
	if err != nil {
		return err
	}
	var cn map[string][]string
	if err := json.Unmarshal(control, &cn); err != nil {
		return err
	}
	src.Control = len(cn)
	src.Files = append(src.Files, file{Path: "control-request.json", SHA256: digest(control)})
	if err := record(dir, "control-bulk.json", control, &src, ids); err != nil {
		return err
	}
	queue := sortedKeys(ids)
	if old, err := filepath.Glob(filepath.Join(dir, "records", "*.json")); err == nil {
		for _, f := range old {
			_ = os.Remove(f)
		}
	}
	done := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if done[id] {
			continue
		}
		done[id] = true
		rec, err := fetch(id)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, rec, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		if err := os.WriteFile(filepath.Join(dir, "records", id+".json"), buf.Bytes(), 0o644); err != nil { //nolint:gosec // project files
			return err
		}
		src.Files = append(src.Files, file{Path: "records/" + id + ".json", SHA256: digest(buf.Bytes()), License: license(id)})
		var probe struct {
			Aliases []string `json:"aliases"`
		}
		_ = json.Unmarshal(rec, &probe)
		for _, a := range probe.Aliases {
			if strings.HasPrefix(a, "MAL-") && !done[a] {
				queue = append(queue, a)
			}
		}
	}
	sort.Slice(src.Files, func(i, j int) bool { return src.Files[i].Path < src.Files[j].Path })
	raw, _ := json.MarshalIndent(src, "", "  ")
	return os.WriteFile(filepath.Join(dir, "SOURCE.json"), append(raw, '\n'), 0o644) //nolint:gosec // project file
}

// record posts the body, stores the indented answer under the name and collects the GHSA identifiers it names.
func record(dir, name string, body []byte, src *source, ids map[string]bool) error {
	answer, err := post(body)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var parsed map[string][]struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(answer, &parsed); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, answer, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	if err := os.WriteFile(filepath.Join(dir, name), pretty.Bytes(), 0o644); err != nil { //nolint:gosec // project files
		return err
	}
	src.Files = append(src.Files, file{Path: name, SHA256: digest(pretty.Bytes())})
	for _, advs := range parsed {
		for _, a := range advs {
			m := ghsaURL.FindStringSubmatch(a.URL)
			if m == nil {
				return fmt.Errorf("%s: an advisory url that is no GHSA url: %q", name, a.URL)
			}
			ids[m[1]] = true
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func license(id string) string {
	switch {
	case strings.HasPrefix(id, "GHSA-"):
		return "CC-BY-4.0 (GitHub Advisory Database)"
	case strings.HasPrefix(id, "MAL-"):
		return "Apache-2.0 (OpenSSF malicious-packages)"
	}
	return "unknown"
}

// post sends the body gzipped, as npm does.
func post(body []byte) ([]byte, error) {
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	_, _ = w.Write(body)
	if err := w.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bulkURL, &z)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "npm/10.9.0 node/v22.0.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %.200s", resp.StatusCode, out)
	}
	if !json.Valid(out) {
		return nil, errors.New("not JSON")
	}
	return out, nil
}

func fetch(id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, osvURL+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("not JSON")
	}
	return body, nil
}
