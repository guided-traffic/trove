//go:build integration

package bastion

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// cacheStore is the cache backend's storage seen from outside, to damage it the way a failing disk or somebody
// with write access to the bucket would: keys are relative to the root of the layout (R-BAS-030).
type cacheStore struct {
	b *bastion
}

func (c cacheStore) list(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	if c.b.sh.backend == "volume" {
		root := c.b.sh.volume
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
	} else {
		token := ""
		for {
			objs, next, err := c.b.sh.raw.ListPlain(context.Background(), c.b.sh.prefix+"/"+dir+"/", token, 1000)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range objs {
				out = append(out, strings.TrimPrefix(o.Key, c.b.sh.prefix+"/"))
			}
			if next == "" {
				break
			}
			token = next
		}
	}
	sort.Strings(out)
	return out
}

func (c cacheStore) read(t *testing.T, key string) []byte {
	t.Helper()
	if c.b.sh.backend == "volume" {
		b, err := os.ReadFile(filepath.Join(c.b.sh.volume, key))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rc, _, err := c.b.sh.raw.OpenPlain(context.Background(), c.b.sh.prefix+"/"+key, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (c cacheStore) write(t *testing.T, key string, data []byte) {
	t.Helper()
	if c.b.sh.backend == "volume" {
		if err := os.WriteFile(filepath.Join(c.b.sh.volume, key), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := c.b.sh.raw.PutPlain(context.Background(), c.b.sh.prefix+"/"+key, bytes.NewReader(data), int64(len(data)), nil); err != nil {
		t.Fatal(err)
	}
}

// tryGet is a GET that reports a transport error (a cut connection) instead of failing the test.
func (r *replica) tryGet(path, token, addr string) (reply, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, r.client+path, nil)
	if err != nil {
		return reply{}, err
	}
	req.Host = baseHost
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", addr)
	res, err := clientHTTP.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return reply{Status: res.StatusCode, Header: res.Header, Body: b}, err
}

// TestR_BAS_032_CorruptCacheContentAndForgedEntries: bytes of a cached artifact changed in the storage are never
// delivered as complete (the transfer is cut, the object and its entry are deleted, the corruption is counted)
// and the next request is served from the main installation; an entry whose fields were changed fails its
// signature, is deleted and is a miss. For both backends.
func TestR_BAS_032_CorruptCacheContentAndForgedEntries(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.publishNPM("integ-lib", "1.0.0")
	path := tarballPath(npmAll, "integ-lib", "1.0.0")
	want := w.atBastionHost(path, w.full.token).Body

	for _, v := range []variant{{"volume", bastionOptions{Backend: "volume"}}, {"bucket", bastionOptions{Backend: "bucket", Valkey: true}}} {
		t.Run(v.name, func(t *testing.T) {
			b := w.newBastion(t, v.o).wait()
			r := b.reps[0]
			st := cacheStore{b}
			addr := "198.51.100.151"
			if got, err := r.tryGet(path, w.full.token, addr); err != nil || got.Status != http.StatusOK || !bytes.Equal(got.Body, want) {
				t.Fatalf("cold: %v %d", err, got.Status)
			}
			objects := st.list(t, "sha256")
			if len(objects) != 1 {
				t.Fatalf("cache objects: %v", objects)
			}

			// 1. The bytes change in place, the size does not.
			bad := st.read(t, objects[0])
			bad[len(bad)/2] ^= 0xff
			st.write(t, objects[0], bad)
			got, err := r.tryGet(path, w.full.token, addr)
			if err == nil && got.Status == http.StatusOK && bytes.Equal(got.Body, bad) {
				t.Fatal("a damaged artifact was delivered as a complete response")
			}
			if sumMetric(r.metrics(t), "trove_bastion_cache_corrupt_total") < 1 {
				t.Fatal("the corruption was not counted")
			}
			if left := st.list(t, "sha256"); len(left) != 0 {
				t.Fatalf("the damaged object was kept: %v", left)
			}
			m := w.front.Mark()
			if got, err := r.tryGet(path, w.full.token, addr); err != nil || got.Status != http.StatusOK || !bytes.Equal(got.Body, want) {
				t.Fatalf("after the corruption: %v %d", err, got.Status)
			}
			if n := len(w.front.Artifacts(m)); n != 1 {
				t.Fatalf("the artifact was fetched %d times after the corruption, want 1", n)
			}

			// 2. A forged entry: a field changed, the signature kept.
			entries := st.list(t, "entries")
			if len(entries) == 0 {
				t.Fatal("no entry was written")
			}
			for _, e := range entries {
				doc := st.read(t, e)
				forged := bytes.Replace(doc, []byte(`"repository":"it/npm-all"`), []byte(`"repository":"it/npm-xxx"`), 1)
				if bytes.Equal(forged, doc) {
					forged = bytes.Replace(doc, []byte(`"size":`), []byte(`"size":1`), 1)
				}
				st.write(t, e, forged)
			}
			time.Sleep(50 * time.Millisecond)
			if got, err := r.tryGet(path, w.full.token, addr); err != nil || got.Status != http.StatusOK || !bytes.Equal(got.Body, want) {
				t.Fatalf("with a forged entry: %v %d", err, got.Status)
			}
			if sumMetric(r.metrics(t), "trove_bastion_cache_evictions_total", `stale`) < 1 {
				t.Fatalf("the forged entry was not deleted as stale")
			}
			// The signature check is not a one-off: a good entry is written again and is served without the main
			// installation.
			m = w.front.Mark()
			if got, err := r.tryGet(path, w.full.token, addr); err != nil || got.Status != http.StatusOK {
				t.Fatalf("after the repair: %v %d", err, got.Status)
			}
			if n := len(w.front.Forwards(m, "/api/")); n != 0 {
				t.Fatalf("the repaired entry costs %d requests", n)
			}
		})
	}
}
