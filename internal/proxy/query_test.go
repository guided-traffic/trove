package proxy

import (
	"encoding/json"
	"testing"
)

// TestQueryEviction: a change of repo_config drops the copies of the repository's configuration
// versions below the one the change produced, every copy on a deletion and on a payload without a
// version, and an unreadable payload names no repository, which flushes the cache (Q-NUGET-94).
func TestQueryEviction(t *testing.T) {
	for _, c := range []struct {
		name, payload string
		ok            bool
		id            int64
		// dropped is what the change drops of the versions 1, 2 and 3.
		dropped [3]bool
	}{
		{"a create keeps every copy", `{"id":"7","key":"r","op":"created","version":1}`, true, 7, [3]bool{false, false, false}},
		{"an update drops the older versions", `{"id":"7","key":"r","op":"updated","version":2}`, true, 7, [3]bool{true, false, false}},
		{"the mark for deletion drops every version", `{"id":"7","key":"r","op":"deleting","version":3}`, true, 7, [3]bool{true, true, true}},
		{"the removal of the row drops every version", `{"id":"7","key":"r","op":"deleted","version":2}`, true, 7, [3]bool{true, true, true}},
		{"a payload without a version drops every version", `{"id":"7","key":"r","op":"updated"}`, true, 7, [3]bool{true, true, true}},
		{"a payload without an id names no repository", `{"type":"repository"}`, false, 0, [3]bool{}},
		{"an id that is a number names no repository", `{"id":7,"op":"updated","version":2}`, false, 0, [3]bool{}},
		{"an unreadable payload names no repository", `[`, false, 0, [3]bool{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, older, ok := queryEviction(json.RawMessage(c.payload))
			if ok != c.ok || id != c.id {
				t.Fatalf("queryEviction = %d, %v, want %d, %v", id, ok, c.id, c.ok)
			}
			if !ok {
				return
			}
			for i, want := range c.dropped {
				if got := older(int64(i + 1)); got != want {
					t.Errorf("version %d dropped = %v, want %v", i+1, got, want)
				}
			}
		})
	}
}
