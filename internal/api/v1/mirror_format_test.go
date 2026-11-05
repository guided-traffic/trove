package v1

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the formats whose descriptors are served
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// TestR_UI_021_FormatDescriptorMirrorFact: the descriptor of GET /api/v1/formats says whether a remote of the format can run in
// mode mirror, the catalog fact Entry.Mirror (R-UI-021, R-REPO-050); a format the catalog does not know cannot, and the
// served document has the member for every format.
func TestR_UI_021_FormatDescriptorMirrorFact(t *testing.T) {
	reg := catalog.NewRegistry()
	npm, ok := catalog.Default().Lookup("npm")
	if !ok {
		t.Fatal("the npm format is not registered")
	}
	on, off := npm, npm
	on.ID, on.Mirror = "listing-fmt", true
	off.ID, off.Mirror = "plain-fmt", false
	reg.Register(on)
	reg.Register(off)
	for format, want := range map[string]bool{"listing-fmt": true, "plain-fmt": false, "unknown-fmt": false} {
		if got := mirrorFact(reg, format); got != want {
			t.Errorf("%s: %v, want %v", format, got, want)
		}
	}
	t.Run("R-UI-021 every served descriptor carries the boolean, validated against the document", func(t *testing.T) {
		val := apitest.New(t, EmbeddedYAML())
		s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
		s.handler = val.Handler(t, s.handler)
		w := s.get("/api/v1/formats")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var body struct{ Formats []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Formats) == 0 {
			t.Fatalf("%v %s", err, w.Body)
		}
		for _, f := range body.Formats {
			got, isBool := f["mirror"].(bool)
			e, _ := catalog.Default().Lookup(f["name"].(string))
			if !isBool || got != e.Mirror {
				t.Errorf("%v: mirror %v, the catalog says %v", f["name"], f["mirror"], e.Mirror)
			}
		}
	})
}
