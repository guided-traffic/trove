//go:build integration

package files

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// TestR_HA_052_SpoolExhaustionAnswers503AndEvictsNothing is the spool rows of R-HA-007 and R-HA-052 at the HTTP surface of a
// replica: concurrent uploads above the spool budget answer 503 with Retry-After, an upload larger than the largest upload
// answers 413, the upload that holds the budget is not evicted and commits, and the refused upload succeeds when the space
// frees. The scenario is not an e2e run because the dev profile's budget is 2 GiB per replica; the contract is the replica's.
func TestR_HA_052_SpoolExhaustionAnswers503AndEvictsNothing(t *testing.T) {
	const budget, maxUpload, size = 6 << 20, 5 << 20, 5 << 20
	te := newTransferEnv(t, func(o *replicaOpts) {
		o.storage = func(c *config.Storage) {
			c.Spool.MaxBytes = budget
			c.Upload.MaxSize = maxUpload
		}
	})
	a := te.a

	// the holder declares 5 MiB, sends 2 MiB and waits: its reservation is the budget's greater part
	hold := newGenerator(size, 31)
	pr, pw := io.Pipe()
	type outcome struct {
		status int
		err    error
	}
	held := make(chan outcome, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPut, a.baseURL+"/files/it/generic-local/spool/held.bin", pr)
		req.ContentLength = size
		req.Header.Set("Authorization", "Bearer maint")
		res, err := client.Do(req)
		if err != nil {
			held <- outcome{err: err}
			return
		}
		_ = res.Body.Close()
		held <- outcome{status: res.StatusCode}
	}()
	first := make([]byte, 2<<20)
	if _, err := io.ReadFull(hold, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(first); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the holder's reservation", 10*time.Second, func() bool { return len(spoolFiles(t, a.spoolDir)) == 1 })

	t.Run("R-HA-007 R-HA-052 R-STOR-004 an upload that does not fit the remaining budget answers 503 with Retry-After and leaves no spool file", func(t *testing.T) {
		res := putSized(t, a, "/files/it/generic-local/spool/refused.bin", newGenerator(size, 32), size)
		if res.status != http.StatusServiceUnavailable {
			t.Fatalf("answered %d %s, want 503", res.status, res.body)
		}
		if ra := res.header.Get("Retry-After"); ra == "" {
			t.Errorf("503 without Retry-After")
		}
		if n := len(spoolFiles(t, a.spoolDir)); n != 1 {
			t.Errorf("%d spool files, want the holder's only", n)
		}
	})
	t.Run("R-HA-007 R-HA-052 R-STOR-004 an upload larger than the largest upload answers 413", func(t *testing.T) {
		res := putSized(t, a, "/files/it/generic-local/spool/huge.bin", newGenerator(size+1, 33), size+1)
		if res.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("answered %d %s, want 413", res.status, res.body)
		}
	})

	// the holder finishes: nothing was evicted, and the refused upload goes through when the space frees
	rest := make([]byte, size-len(first))
	if _, err := io.ReadFull(hold, rest); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(rest); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	got := <-held
	if got.err != nil || got.status != http.StatusCreated {
		t.Fatalf("the upload that held the budget ended %d %v, want 201 (no eviction)", got.status, got.err)
	}
	t.Run("R-HA-007 R-HA-052 R-STOR-004 the refused upload succeeds when the space frees, and both files return their bytes", func(t *testing.T) {
		retry := newGenerator(size, 32)
		res := putSized(t, a, "/files/it/generic-local/spool/refused.bin", retry, size)
		if res.status != http.StatusCreated {
			t.Fatalf("the retry answered %d %s", res.status, res.body)
		}
		for path, want := range map[string]string{"held.bin": hold.sum(), "refused.bin": retry.sum()} {
			r := a.get("reader", "/files/it/generic-local/spool/"+path)
			sum := sha256.Sum256(r.body)
			if r.status != 200 || hex.EncodeToString(sum[:]) != want {
				t.Errorf("%s: %d, sha256 %x, want %s", path, r.status, sum, want)
			}
		}
	})
}

// spoolFiles lists the files in a spool directory.
func spoolFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// putSized uploads n bytes of body with a declared length: the replica admits the upload against its spool budget before it
// reads the body.
func putSized(t *testing.T, r *replica, path string, body io.Reader, n int64) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, r.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = n
	req.Header.Set("Authorization", "Bearer maint")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return response{status: res.StatusCode, header: res.Header, body: b}
}
