package app

import (
	"slices"
	"testing"
)

// TestR_OPS_004_FormatsComeFromTheRegistry: the formats of /system/version and
// /system/info are the registered ones, not a literal; npm is not among them while
// no npm format is registered.
func TestR_OPS_004_FormatsComeFromTheRegistry(t *testing.T) {
	got := formatNames()
	if !slices.Contains(got, "generic") {
		t.Fatalf("formats = %v, want generic among them", got)
	}
	if slices.Contains(got, "npm") || slices.Contains(got, "nuget") {
		t.Fatalf("formats = %v, a format that is not registered is reported", got)
	}
}
