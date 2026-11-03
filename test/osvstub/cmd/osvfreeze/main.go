// Command osvfreeze fetches the real OSV records listed in testdata/real/ids.txt from api.osv.dev and writes them,
// indented, to testdata/real/<ecosystem>/<id>.json, with testdata/real/SOURCE.json (the day taken and the SHA-256
// of every file). It is run by `make osv-real-refresh`; the tests never need the network.
//
//	osvfreeze [-dir test/osvstub/testdata/real] [-date 2026-11-03]
package main

import (
	"bufio"
	"bytes"
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
	"strings"
	"time"
)

type file struct {
	Ecosystem string `json:"ecosystem"`
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	License   string `json:"license"`
}

type source struct {
	Taken   string `json:"taken"`
	From    string `json:"from"`
	Records []file `json:"records"`
}

func main() {
	dir := flag.String("dir", "test/osvstub/testdata/real", "the directory of ids.txt and the records")
	date := flag.String("date", time.Now().UTC().Format(time.DateOnly), "the day the records are taken, in SOURCE.json")
	flag.Parse()
	if err := run(*dir, *date); err != nil {
		fmt.Fprintln(os.Stderr, "osvfreeze:", err)
		os.Exit(1)
	}
}

func run(dir, date string) error {
	f, err := os.Open(filepath.Join(dir, "ids.txt")) //nolint:gosec // a project file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	src := source{Taken: date, From: "https://api.osv.dev/v1/vulns/{id}"}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return fmt.Errorf("bad line %q: want \"<ecosystem> <id>\"", line)
		}
		eco, id := parts[0], parts[1]
		body, err := fetch(id)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		pretty.WriteByte('\n')
		out := filepath.Join(dir, eco, id+".json")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil { //nolint:gosec // project files
			return err
		}
		if err := os.WriteFile(out, pretty.Bytes(), 0o644); err != nil { //nolint:gosec // project files
			return err
		}
		sum := sha256.Sum256(pretty.Bytes())
		src.Records = append(src.Records, file{Ecosystem: eco, ID: id, SHA256: hex.EncodeToString(sum[:]), License: license(id)})
	}
	if err := sc.Err(); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(src, "", "  ")
	return os.WriteFile(filepath.Join(dir, "SOURCE.json"), append(raw, '\n'), 0o644) //nolint:gosec // project file
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

func fetch(id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.osv.dev/v1/vulns/"+url.PathEscape(id), nil)
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
