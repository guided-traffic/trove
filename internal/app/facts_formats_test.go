package app

import (
	"slices"
	"testing"
)

// TestR_OPS_004_FormatsComeFromTheRegistry: the formats of /system/version and
// /system/info are the registered ones, not a literal; npm and nuget are among them since
// their formats are registered; a name nobody registered is not.
func TestR_OPS_004_FormatsComeFromTheRegistry(t *testing.T) {
	got := formatNames()
	for _, want := range []string{"generic", "npm", "nuget"} {
		if !slices.Contains(got, want) {
			t.Fatalf("formats = %v, want %s among them", got, want)
		}
	}
	if slices.Contains(got, "maven") {
		t.Fatalf("formats = %v, a format that is not registered is reported", got)
	}
}
