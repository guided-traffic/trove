package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob/verify"
)

// TestR_HA_024_StorageVerifyRefusesWhatIsIncomplete: an unknown command, a malformed --since and a non-positive
// concurrency print the usage and open no connection.
func TestR_HA_024_StorageVerifyRefusesWhatIsIncomplete(t *testing.T) {
	s := streams{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	for _, args := range [][]string{
		{"storage"},
		{"storage", "frobnicate"},
		{"storage", "verify", "--since", "yesterday"},
		{"storage", "verify", "--concurrency", "0"},
	} {
		err := adminCmd(args, s)
		var u usageError
		if !errors.As(err, &u) || !strings.Contains(u.Error(), "trove admin storage verify") {
			t.Errorf("%v: err = %v, want the usage of trove admin storage verify", args, err)
		}
	}
	if !strings.Contains(adminUsage, "storage verify") {
		t.Error("the usage of trove admin does not list storage verify")
	}
}

// TestR_HA_025_StorageVerifyPrintsOneLinePerItem: the text report names the item, its blob and the totals.
func TestR_HA_025_StorageVerifyPrintsOneLinePerItem(t *testing.T) {
	var out bytes.Buffer
	rep := &verify.Report{
		Checked: 5, Missing: 1, ItemsAffected: 3,
		Findings: []verify.Finding{{
			Kind: verify.KindMissing, SHA256: "ab", Size: 3, ItemCount: 3,
			Items: []verify.Item{{Project: "p", Repository: "npm-local", Path: "a/b.tgz"}, {Project: "p", Repository: "npm-local", Path: "c.tgz", Trashed: true}},
		}},
	}
	if err := writeVerifyReport(&out, rep); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"item p/npm-local:a/b.tgz: object missing, blob ab",
		"item p/npm-local:c.tgz (trashed): object missing, blob ab",
		"blob ab: and 1 more items",
		"5 blobs checked, 1 without object, 0 of another size, 0 not checked, 3 items affected",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}
