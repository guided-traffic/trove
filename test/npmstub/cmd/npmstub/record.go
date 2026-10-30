package main

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// maxRecordedBody bounds what the recorder reads from a registry.
const maxRecordedBody = 64 << 20

// record trims a live package document to the listed versions and stores it with the real
// tarballs under -out. It runs by hand only: a test run never contacts a registry. The output is
// reviewed and committed under test/npmstub/testdata/recorded.
func record(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	registry := fs.String("registry", "https://registry.npmjs.org", "registry to record from")
	name := fs.String("package", "", "package name, for example @scope/name")
	versions := fs.String("versions", "", "comma-separated versions to keep")
	out := fs.String("out", "test/npmstub/testdata/recorded", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *versions == "" {
		return errors.New("record: -package and -versions are required")
	}
	keep := strings.Split(*versions, ",")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	raw, err := fetch(ctx, strings.TrimSuffix(*registry, "/")+"/"+url.PathEscape(*name))
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	trimmed, tarballs, err := trim(doc, *name, keep)
	if err != nil {
		return err
	}
	dir := filepath.Join(*out, npmstub.DirName(*name))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for file, u := range tarballs {
		data, integrity, err := fetchTarball(ctx, u.url)
		if err != nil {
			return err
		}
		if integrity != u.integrity {
			return fmt.Errorf("%s: integrity %s does not match the document's %s", file, integrity, u.integrity)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil { //nolint:gosec // recorded public data
			return err
		}
	}
	body, err := json.MarshalIndent(trimmed, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "document.json"), append(body, '\n'), 0o644) //nolint:gosec // recorded public data
}

type tarballRef struct{ url, integrity string }

// trim keeps the listed versions, the dist-tags and times that refer to them, and drops the
// readme and bookkeeping fields, which are large and of no use to a client test.
func trim(doc map[string]json.RawMessage, name string, keep []string) (map[string]any, map[string]tarballRef, error) {
	var versions map[string]json.RawMessage
	if err := json.Unmarshal(doc["versions"], &versions); err != nil {
		return nil, nil, fmt.Errorf("versions: %w", err)
	}
	var tags, times map[string]string
	if err := json.Unmarshal(doc["dist-tags"], &tags); err != nil {
		return nil, nil, fmt.Errorf("dist-tags: %w", err)
	}
	if err := json.Unmarshal(doc["time"], &times); err != nil {
		return nil, nil, fmt.Errorf("time: %w", err)
	}
	kept := map[string]json.RawMessage{}
	keptTimes := map[string]string{"created": times["created"], "modified": times["modified"]}
	keptTags := map[string]string{}
	tarballs := map[string]tarballRef{}
	if latest := tags["latest"]; latest != "" && !slices.Contains(keep, latest) {
		keep = append(slices.Clone(keep), latest) // a document without `latest` is not a registry's
	}
	for _, v := range keep {
		raw, ok := versions[v]
		if !ok {
			return nil, nil, fmt.Errorf("%s has no version %s", name, v)
		}
		var meta struct {
			Dist struct {
				Tarball   string `json:"tarball"`
				Integrity string `json:"integrity"`
			} `json:"dist"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil || meta.Dist.Integrity == "" {
			return nil, nil, fmt.Errorf("%s@%s: no dist.integrity", name, v)
		}
		kept[v], keptTimes[v] = raw, times[v]
		tarballs[corpus.TarballFile(name, v)] = tarballRef{meta.Dist.Tarball, meta.Dist.Integrity}
	}
	for tag, v := range tags {
		if _, ok := kept[v]; ok {
			keptTags[tag] = v
		}
	}
	out := map[string]any{"name": name, "dist-tags": keptTags, "versions": kept, "time": keptTimes}
	for _, k := range []string{"description", "license"} {
		if doc[k] != nil {
			out[k] = doc[k]
		}
	}
	return out, tarballs, nil
}

func fetch(ctx context.Context, u string) ([]byte, error) {
	data, _, err := get(ctx, u)
	return data, err
}

func fetchTarball(ctx context.Context, u string) ([]byte, string, error) {
	data, sum, err := get(ctx, u)
	if err != nil {
		return nil, "", err
	}
	return data, "sha512-" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func get(ctx context.Context, u string) ([]byte, [sha512.Size]byte, error) {
	var zero [sha512.Size]byte
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, zero, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, zero, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response
	if resp.StatusCode != http.StatusOK {
		return nil, zero, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordedBody))
	if err != nil {
		return nil, zero, err
	}
	return data, sha512.Sum512(data), nil
}
