package v1

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_API_012_TheTagOfARepositoryReadFollowsItsActivityTime: lastActivityAt is in
// the representation and changes without a new revision, so the strong tag of a
// read changes with it and a conditional read after a download is not answered
// 304; a write accepts the tag of a read and still compares the revision only.
func TestR_API_012_TheTagOfARepositoryReadFollowsItsActivityTime(t *testing.T) {
	at := time.Date(2026, 10, 29, 9, 14, 3, 117000000, time.UTC)
	st := func(version int64, last time.Time) repository.Stored {
		return repository.Stored{Repository: repository.Repository{Version: version, LastActivityAt: last}}
	}
	t.Run("R-API-012 no activity: the plain revision tag", func(t *testing.T) {
		if got := repositoryETag(st(4, time.Time{}), ""); got != `"4"` {
			t.Fatalf("%s", got)
		}
	})
	first := repositoryETag(st(4, at), "")
	second := repositoryETag(st(4, at.Add(time.Minute)), "")
	t.Run("R-API-012 the tag changes when only the activity time does", func(t *testing.T) {
		if first == second || first == `"4"` {
			t.Fatalf("%s %s", first, second)
		}
		if view := repositoryETag(st(4, at), consumerView); view == first {
			t.Fatalf("the view is part of the tag: %s", view)
		}
	})
	t.Run("R-API-012 a conditional read with the earlier tag is answered 200 once the activity moved", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("If-None-Match", first)
		if NotModified(httptest.NewRecorder(), r, second) {
			t.Fatal("304 with a stale activity time")
		}
		if !NotModified(httptest.NewRecorder(), r, first) {
			t.Fatal("no 304 for an unchanged representation")
		}
	})
	t.Run("R-API-012 a write with the tag of a read compares the revision", func(t *testing.T) {
		for tag, want := range map[string]int64{first: 4, `"4"`: 4, `"0"`: 0, `"x.1"`: 0} {
			r := httptest.NewRequest("PUT", "/", nil)
			r.Header.Set("If-Match", tag)
			pre, err := ParseIfMatch(r)
			if err != nil {
				t.Fatal(err)
			}
			got := repositoryPrecondition(pre)
			if want == 0 && got.Version != 9223372036854775807 {
				t.Errorf("%s: version %d", tag, got.Version)
			}
			if want != 0 && got.Version != want {
				t.Errorf("%s: version %d, want %d", tag, got.Version, want)
			}
		}
	})
}
