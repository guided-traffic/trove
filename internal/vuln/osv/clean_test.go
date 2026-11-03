package osv_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// R-VULN-006 (S10 of the fix wave): the bidirectional controls, the zero-width characters, the byte order mark and the line and
// paragraph separators are removed from the texts of a record, as the control characters are; the event versions of a range the
// format cannot evaluate are stored cleaned and cut, and a name, a URL or a version that holds such a character is not evaluable.
func TestR_VULN_006_FormatCharactersAreRemoved(t *testing.T) {
	const spoof = "safe\u202egnp\u2066x\u2069\u200b\u200d\ufeff\u2028\u2029 title\tkept\nnewline\u0007bell"
	js, _ := json.Marshal(spoof)
	r := decode(t, fmt.Sprintf(`{%s,"summary":%s,"details":%s}`, base, js, js))
	const want = "safegnpx title\tkept\nnewlinebell"
	if r.Advisory.Summary != want || r.Advisory.Details != want {
		t.Errorf("summary %q, details %q, want %q", r.Advisory.Summary, r.Advisory.Details, want)
	}
	// A reference's type is a text of the record too.
	r = decode(t, fmt.Sprintf(`{%s,"references":[{"type":"WE\u202eB","url":"https://example.org/a"}]}`, base))
	if len(r.Advisory.References) != 1 || r.Advisory.References[0].Type != "WEB" {
		t.Errorf("references %+v", r.Advisory.References)
	}
	// A URL with a format character is refused, not cleaned.
	r = decode(t, fmt.Sprintf(`{%s,"references":[{"type":"WEB","url":"https://exa\u202emple.org/a"}]}`, base))
	if len(r.Advisory.References) != 0 {
		t.Errorf("a URL with a bidirectional control was kept: %+v", r.Advisory.References)
	}
	r = decode(t, fmt.Sprintf(`{%s,"affected":[{"package":{"ecosystem":"npm","name":"a\u200bb"},"versions":["1.0.0"]}]}`, base))
	if len(r.Advisory.Affected) != 0 || r.Unevaluable != 1 {
		t.Errorf("a name with a zero-width character: %+v, unevaluable %d", r.Advisory.Affected, r.Unevaluable)
	}
	// The events of an unevaluable range are cleaned and cut before they are stored.
	long := strings.Repeat("9", 300)
	r = decode(t, fmt.Sprintf(`{%s,"affected":[{"package":{"ecosystem":"npm","name":"a"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.\u202e2\u0000x%s"}]}]}]}`, base, long))
	if r.Unevaluable != 1 || len(r.Advisory.Affected) != 1 {
		t.Fatalf("unevaluable %d, affected %+v", r.Unevaluable, r.Advisory.Affected)
	}
	ev := r.Advisory.Affected[0].Ranges[0].Events
	if strings.ContainsAny(ev[1].Version, "\u202e\x00") || !strings.HasPrefix(ev[1].Version, "1.2x9") || len(ev[1].Version) > 128 {
		t.Errorf("the stored version %q (%d bytes)", ev[1].Version, len(ev[1].Version))
	}
	if ev[0] != (advisory.Event{Kind: advisory.EventIntroduced, Version: "0"}) {
		t.Errorf("introduced %+v", ev[0])
	}
}
