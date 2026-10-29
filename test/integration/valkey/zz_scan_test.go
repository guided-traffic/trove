//go:build integration

package valkey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestZKeysHavePrefixAndTTL scans the primary after writing through every
// method of the API. It runs last (file order) but does not depend on it: it
// writes its own keys first.
func TestZKeysHavePrefixAndTTL(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := newClient(t, stackConfig(t, 2*time.Second))
	waitAvailable(t, c)
	exerciseAPI(ctx, t, c)
	time.Sleep(time.Second) // a probe

	t.Run("R-ARCH-030 every key starts with the prefix and has a TTL", func(t *testing.T) {
		admin := adminAt(t, v, c.Status().Primary)
		cursor := "0"
		var mine, total int
		for {
			arr, err := admin.Do(ctx, admin.B().Scan().Cursor(parseCursor(cursor)).Count(1000).Build()).ToArray()
			if err != nil || len(arr) != 2 {
				t.Fatalf("SCAN: %v", err)
			}
			cursor, _ = arr[0].ToString()
			keys, _ := arr[1].AsStrSlice()
			for _, k := range keys {
				total++
				if !strings.HasPrefix(k, "trove:") {
					t.Errorf("key %q does not start with the prefix trove:", k)
				}
				if !strings.HasPrefix(k, runPrefix) {
					continue
				}
				mine++
				ttl, err := admin.Do(ctx, admin.B().Pttl().Key(k).Build()).AsInt64()
				if err != nil {
					t.Fatal(err)
				}
				if ttl == -1 {
					t.Errorf("key %q has no TTL", k)
				}
			}
			if cursor == "0" {
				break
			}
		}
		if mine == 0 {
			t.Fatalf("no key under %s in %d keys: the scan checked nothing", runPrefix, total)
		}
		t.Logf("%d keys under %s, %d in all, all with a TTL", mine, runPrefix, total)
	})
}

func parseCursor(s string) uint64 {
	var n uint64
	for _, r := range s {
		n = n*10 + uint64(r-'0')
	}
	return n
}
