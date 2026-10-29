package v1

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
)

func TestR_API_004_ItemPathsFollowTheCanonicalRules(t *testing.T) {
	seg := strings.Repeat("s", repository.MaxSegmentBytes)
	long := strings.TrimSuffix(strings.Repeat(seg+"/", repository.MaxPathBytes/len(seg)+2), "/")
	for _, c := range []struct {
		name, path string
		ok         bool
	}{
		{"R-GENERIC-002 a backslash", `a\b`, false},
		{"R-GENERIC-002 a path of more than 4096 bytes", long, false},
		{"R-GENERIC-002 a segment of more than 255 bytes", "a/" + seg + "s", false},
		{"R-GENERIC-002 a segment of 255 bytes", "a/" + seg, true},
		{"R-API-004 a path of 1 MiB is refused by its length", strings.Repeat("a", 1<<20), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := CheckItemPath(c.path, PathOptions{})
			if c.ok != (err == nil) {
				t.Fatalf("CheckItemPath(%d bytes) = %v", len(c.path), err)
			}
			if err != nil {
				if _, code, _ := problemOf(t, err); code != codeInvalidPath {
					t.Fatalf("code %q", code)
				}
			}
		})
	}
}
