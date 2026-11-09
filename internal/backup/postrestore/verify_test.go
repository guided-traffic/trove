package postrestore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestR_HA_025_StorageVerifyLine: step 4 prints the count of referenced blobs without their object (Q-HA-213), points to the repair when
// there are some, and reports a check that could not run as a line.
func TestR_HA_025_StorageVerifyLine(t *testing.T) {
	run := func(c StorageCheck, err error) string {
		p := New(Options{VerifyStorage: func(context.Context) (StorageCheck, error) { return c, err }})
		var lines []string
		p.verifyStorage(context.Background(), func(s string) { lines = append(lines, s) })
		return strings.Join(lines, "\n")
	}
	if got := run(StorageCheck{Checked: 40}, nil); got != "storage verify: 40 blobs checked, 0 without object, 0 of another size, 0 not checked, 0 items affected" {
		t.Errorf("clean: %q", got)
	}
	if got := run(StorageCheck{Checked: 40, Missing: 2, ItemsAffected: 3}, nil); !strings.Contains(got, "2 without object") || !strings.Contains(got, "step 5") {
		t.Errorf("missing: %q", got)
	}
	if got := run(StorageCheck{}, errors.New("no store")); !strings.Contains(got, "storage verify failed: no store") {
		t.Errorf("error: %q", got)
	}
	if got := func() string {
		var n int
		New(Options{}).verifyStorage(context.Background(), func(string) { n++ })
		return string(rune('0' + n))
	}(); got != "0" {
		t.Errorf("a post-restore without the check printed a line")
	}
}
