// Command osvexpect writes the frozen expected results of the real-record corpus of the version matching (R-VULN-009): for
// every package of the frozen OSV records (test/osvstub/testdata/real) and a set of versions around the versions the records
// name, whether osv-scanner's offline mode finds each record affecting that package version.
//
// The tests of the matcher (internal/formats/npm and internal/formats/nuget, vuln_corpus_test.go) read the frozen file and need
// no network and no tool. `make osv-expected` runs this command with the osv-scanner of the pin in tools/versions.env:
//
//	go run ./internal/tools/osvexpect -osv-scanner .bin/osv-scanner-<pin>-<go> -real test/osvstub/testdata/real \
//	    -out internal/vuln/match/testdata/osv-scanner-expected.json
//
// The command builds a local advisory database of the records in the layout osv-scanner's offline mode reads
// (<dir>/osv-scalibr/<ecosystem>/all.zip), lists the candidate package versions in a CycloneDX document and runs
// `osv-scanner scan source --offline-vulnerabilities`; it is a developer tool, not part of the product.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// record is the part of an OSV record the command reads.
type record struct {
	ID       string `json:"id"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string              `json:"type"`
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
		Versions []string `json:"versions"`
	} `json:"affected"`
}

// Expected is the frozen file.
type Expected struct {
	// OSVScanner is the version of osv-scanner that produced the results.
	OSVScanner string `json:"osvScanner"`
	// Ecosystems maps an OSV ecosystem to its packages.
	Ecosystems map[string]map[string]*Package `json:"ecosystems"`
}

// Package is one package: the versions the results were asked for and, per record, the ones osv-scanner finds affected.
type Package struct {
	Candidates []string            `json:"candidates"`
	Affected   map[string][]string `json:"affected"`
}

var numeric = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

func main() {
	scanner := flag.String("osv-scanner", "", "path of the osv-scanner binary")
	real := flag.String("real", "test/osvstub/testdata/real", "directory of the frozen records (one subdirectory per ecosystem)")
	out := flag.String("out", "", "file to write")
	flag.Parse()
	if *scanner == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: osvexpect -osv-scanner <binary> [-real <dir>] -out <file>")
		os.Exit(2)
	}
	if err := run(*scanner, *real, *out); err != nil {
		fmt.Fprintln(os.Stderr, "osvexpect:", err)
		os.Exit(1)
	}
}

func run(scanner, realDir, out string) error {
	raw, byEco, err := load(realDir)
	if err != nil {
		return err
	}
	exp := &Expected{Ecosystems: map[string]map[string]*Package{}}
	tmp, err := os.MkdirTemp("", "osvexpect")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	version, err := scannerVersion(scanner)
	if err != nil {
		return err
	}
	exp.OSVScanner = version
	db := filepath.Join(tmp, "db")
	for eco, records := range byEco {
		if err := writeDB(filepath.Join(db, "osv-scalibr", eco, "all.zip"), raw[eco]); err != nil {
			return err
		}
		exp.Ecosystems[eco] = candidates(eco, records)
	}
	for eco, pkgs := range exp.Ecosystems {
		found, err := scan(scanner, db, tmp, eco, pkgs)
		if err != nil {
			return err
		}
		for name, p := range pkgs {
			p.Affected = map[string][]string{}
			for _, v := range p.Candidates {
				for _, id := range found[name+"@"+v] {
					p.Affected[id] = append(p.Affected[id], v)
				}
			}
		}
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(b, '\n'), 0o644) //nolint:gosec // a committed test file
}

// load reads the records of every ecosystem directory: the raw bytes by ecosystem and file name, and the decoded records.
func load(dir string) (map[string]map[string][]byte, map[string][]record, error) {
	raw := map[string]map[string][]byte{}
	recs := map[string][]record{}
	ecos, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ecos {
		if !e.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, e.Name(), "*.json"))
		if err != nil {
			return nil, nil, err
		}
		sort.Strings(files)
		raw[e.Name()] = map[string][]byte{}
		for _, f := range files {
			b, err := os.ReadFile(f) //nolint:gosec // the frozen records named by the flag
			if err != nil {
				return nil, nil, err
			}
			var r record
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", f, err)
			}
			raw[e.Name()][r.ID+".json"] = b
			recs[e.Name()] = append(recs[e.Name()], r)
		}
	}
	return raw, recs, nil
}

func writeDB(path string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			return err
		}
		if _, err := w.Write(files[n]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

// candidates lists, per package of the ecosystem, the versions the results are asked for: every version a record names (events
// and versions[]), the versions next to each (a patch below and above, the next minor and major, a pre-release of it) and a
// few fixed ones, so that both sides of every boundary and the pre-releases are covered.
func candidates(eco string, recs []record) map[string]*Package {
	set := map[string]map[string]bool{}
	add := func(name, v string) {
		if set[name] == nil {
			set[name] = map[string]bool{}
		}
		set[name][v] = true
	}
	for _, r := range recs {
		for _, a := range r.Affected {
			if a.Package.Ecosystem != eco {
				continue
			}
			name := a.Package.Name
			for _, v := range a.Versions {
				add(name, v)
			}
			for _, rg := range a.Ranges {
				for _, ev := range rg.Events {
					for _, v := range ev {
						if v == "0" || v == "*" {
							continue
						}
						for _, c := range around(v) {
							add(name, c)
						}
					}
				}
			}
		}
	}
	out := map[string]*Package{}
	for name, vs := range set {
		for _, v := range []string{"0.0.1", "0.1.0", "1.0.0", "2.0.0", "10.0.0", "9999.0.0"} {
			vs[v] = true
		}
		p := &Package{}
		for v := range vs {
			p.Candidates = append(p.Candidates, v)
		}
		sort.Strings(p.Candidates)
		out[name] = p
	}
	return out
}

// around returns a version and its neighbours: a numeric version with its last part one below and one above, the next higher
// part, and a pre-release of it; any other text as it is.
func around(v string) []string {
	out := []string{v}
	if !numeric.MatchString(v) {
		return out
	}
	parts := strings.Split(v, ".")
	n := make([]int, len(parts))
	for i, p := range parts {
		n[i], _ = strconv.Atoi(p)
	}
	join := func(ns []int) string {
		s := make([]string, len(ns))
		for i, x := range ns {
			s[i] = strconv.Itoa(x)
		}
		return strings.Join(s, ".")
	}
	last := len(n) - 1
	up := append([]int(nil), n...)
	up[last]++
	out = append(out, join(up), join(n)+"-alpha.1")
	if n[last] > 0 {
		down := append([]int(nil), n...)
		down[last]--
		out = append(out, join(down))
	}
	if last > 0 {
		next := append([]int(nil), n...)
		next[last-1]++
		next[last] = 0
		out = append(out, join(next))
	}
	return out
}

func scannerVersion(scanner string) (string, error) {
	b, err := exec.CommandContext(context.Background(), scanner, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w\n%s", scanner, err, b)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "osv-scanner version: "); ok {
			return "v" + strings.TrimPrefix(strings.TrimSpace(v), "v"), nil
		}
	}
	return "", errors.New("osv-scanner --version names no version: " + string(b))
}

// purl is the package URL of a package of an ecosystem.
func purl(eco, name, version string) (string, error) {
	switch eco {
	case "npm":
		return "pkg:npm/" + strings.ReplaceAll(url.PathEscape(name), "%2F", "/") + "@" + version, nil
	case "NuGet":
		return "pkg:nuget/" + url.PathEscape(name) + "@" + version, nil
	}
	return "", fmt.Errorf("no package URL for the ecosystem %q", eco)
}

// scan asks osv-scanner's offline mode which records affect each package version of one ecosystem; it returns the identifiers by
// "<name>@<version>".
func scan(scanner, db, tmp, eco string, pkgs map[string]*Package) (map[string][]string, error) {
	type component struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
		PURL    string `json:"purl"`
	}
	var comps []component
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, v := range pkgs[n].Candidates {
			p, err := purl(eco, n, v)
			if err != nil {
				return nil, err
			}
			comps = append(comps, component{Type: "library", Name: n, Version: v, PURL: p})
		}
	}
	bom, err := json.Marshal(map[string]any{"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1, "components": comps})
	if err != nil {
		return nil, err
	}
	sbom := filepath.Join(tmp, strings.ToLower(eco)+".cdx.json")
	if err := os.WriteFile(sbom, bom, 0o600); err != nil {
		return nil, err
	}
	report := filepath.Join(tmp, strings.ToLower(eco)+"-report.json")
	cmd := exec.CommandContext( //nolint:gosec // the binary is the pinned tool named by the flag
		context.Background(), scanner, "scan", "source", "--offline-vulnerabilities", "--local-db-path", db, "--all-vulns", "--all-packages", "--format", "json", "--output-file", report, "-S", sbom)
	cmd.Env = append(os.Environ(), "OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY="+db)
	if b, err := cmd.CombinedOutput(); err != nil {
		var ee *exec.ExitError
		// Exit status 1 says "vulnerabilities found": the report is written.
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil, fmt.Errorf("osv-scanner for %s: %w\n%s", eco, err, b)
		}
	}
	rb, err := os.ReadFile(report) //nolint:gosec // written by the tool just above
	if err != nil {
		return nil, err
	}
	var rep struct {
		Results []struct {
			Packages []struct {
				Package struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"package"`
				Vulnerabilities []struct {
					ID string `json:"id"`
				} `json:"vulnerabilities"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rb, &rep); err != nil {
		return nil, fmt.Errorf("the report of osv-scanner for %s: %w", eco, err)
	}
	found := map[string][]string{}
	seen := 0
	for _, r := range rep.Results {
		for _, p := range r.Packages {
			seen++
			key := p.Package.Name + "@" + p.Package.Version
			for _, v := range p.Vulnerabilities {
				found[key] = append(found[key], v.ID)
			}
		}
	}
	if seen == 0 {
		return nil, fmt.Errorf("the report of osv-scanner for %s lists no package: %s", eco, rb[:min(len(rb), 400)])
	}
	return found, nil
}
