package journal

import (
	"slices"
	"testing"
)

// A result can carry the further objects its entry's replay changed (Result.Also): the later mappings of a tier that a
// replayed mapping disable suspends (R-CI-020, Q-CI-11). Each is counted and printed as a result of its own.
func TestR_CI_020_FurtherResultsAreCountedAndPrintedAsTheirOwn(t *testing.T) {
	r := Result{
		ID: id1, Event: "ci.mapping.deleted", Outcome: Applied, Subject: "gitlab/acme-mr-readonly",
		Also: []Result{
			{ID: id1, Event: "ci.mapping.deleted", Outcome: Suspended, Subject: "gitlab/acme-release-publish"},
			{ID: id1, Event: "ci.mapping.deleted", Outcome: Suspended, Subject: "gitlab/acme-read"},
		},
	}
	flat := r.Flat()
	if len(flat) != 3 || flat[0].Also != nil || flat[2].Subject != "gitlab/acme-read" {
		t.Fatalf("Flat = %+v", flat)
	}
	var lines []string
	for _, x := range flat {
		lines = append(lines, x.Line())
	}
	if !slices.Equal(lines, []string{
		"\"ci.mapping.deleted\" \"gitlab/acme-mr-readonly\" " + id1 + " applied",
		"\"ci.mapping.deleted\" \"gitlab/acme-release-publish\" " + id1 + " suspended",
		"\"ci.mapping.deleted\" \"gitlab/acme-read\" " + id1 + " suspended",
	}) {
		t.Fatalf("lines = %q", lines)
	}
	var s Summary
	s.Add(r)
	if s.Counts[Applied] != 1 || s.Counts[Suspended] != 2 || len(s.Results) != 3 || s.Results[0].Also != nil {
		t.Fatalf("Summary = %+v", s)
	}
}
