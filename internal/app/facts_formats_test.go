package app

import (
	"slices"
	"testing"
)

// TestR_OPS_004_FormatsComeFromTheRegistry: the formats of /system/version and
// /system/info are the registered ones, not a literal; npm is among them since its
// format is registered; nuget is not while it is not.
func TestR_OPS_004_FormatsComeFromTheRegistry(t *testing.T) {
	got := formatNames()
	if !slices.Contains(got, "generic") || !slices.Contains(got, "npm") {
		t.Fatalf("formats = %v, want generic and npm among them", got)
	}
	if slices.Contains(got, "nuget") {
		t.Fatalf("formats = %v, a format that is not registered is reported", got)
	}
}
