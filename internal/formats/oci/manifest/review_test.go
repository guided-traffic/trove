package manifest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// The text of the annotations of a row is at most the limit, to the byte: an object of two entries whose text is exactly the limit is
// kept whole, and one byte more cuts the second entry (R-OCI-007).
//
// Mechanism of R-OCI-007, not a proof of its criteria: it checks a row, a bound or a query of the data port; the criteria are the
// digest of the stored bytes, the one object for tag and manifest and the layer shared by two names.
func TestRowAnnotationsAtTheExactBoundary(t *testing.T) {
	const limit = 200
	// {"a": "<x>", "b": "<y>"} is 2+3+2+... bytes; build the two values so the whole text is exactly limit bytes.
	frame := len(`{"a": "", "b": ""}`)
	for _, over := range []int{0, 1} {
		x := strings.Repeat("x", 50)
		y := strings.Repeat("y", limit-frame-len(x)+over)
		m := &Manifest{Annotations: map[string]string{"a": x, "b": y}}
		raw, cut := m.RowAnnotations(limit)
		if over == 0 && (cut || len(raw) != limit) {
			t.Fatalf("exact: %d bytes cut=%v, want %d whole", len(raw), cut, limit)
		}
		if over == 1 && (!cut || len(raw) > limit || strings.Contains(string(raw), `"b"`)) {
			t.Fatalf("one over: %d bytes cut=%v %s", len(raw), cut, raw)
		}
	}
	// A single entry that fills the limit exactly.
	one := &Manifest{Annotations: map[string]string{"a": strings.Repeat("v", limit-len(`{"a": ""}`))}}
	if raw, cut := one.RowAnnotations(limit); cut || len(raw) != limit {
		t.Fatalf("single: %d bytes cut=%v", len(raw), cut)
	}
}

// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestTheShapeKeysOfTheOtherTypeAreRefusedInEveryCase(t *testing.T) {
	d := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1}`, dg("x"))
	for name, tc := range map[string]struct{ doc, ct string }{
		"an image manifest with Manifests": {`{"schemaVersion":2,"config":` + d + `,"layers":[],"Manifests":[]}`, tImage},
		"an image manifest with MANIFESTS": {`{"schemaVersion":2,"config":` + d + `,"layers":[],"MANIFESTS":[]}`, tImage},
		"an index with Layers":             {`{"schemaVersion":2,"manifests":[],"Layers":[]}`, tIndex},
		"an index with CONFIG":             {`{"schemaVersion":2,"manifests":[],"CONFIG":` + d + `}`, tIndex},
		"an image manifest with fsLayers":  {`{"schemaVersion":2,"config":` + d + `,"layers":[],"fsLayers":[]}`, tImage},
		"an index with fsLayers":           {`{"schemaVersion":2,"manifests":[],"fsLayers":[]}`, tIndex},
		"an image manifest with FSLAYERS":  {`{"schemaVersion":2,"config":` + d + `,"layers":[],"FSLAYERS":[]}`, tImage},
	} {
		if _, err := parse(t, tc.doc, tc.ct); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestUnpairedSurrogateEscapesAreRefused(t *testing.T) {
	d := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1}`, dg("x"))
	doc := func(v string) string {
		return `{"schemaVersion":2,"config":` + d + `,"layers":[],"annotations":{"k":"` + v + `"}}`
	}
	for name, v := range map[string]string{
		"a lone high surrogate":          `\ud800`,
		"a lone low surrogate":           `\udc00`,
		"a high surrogate then a letter": `\ud800a`,
		"a high then a high":             `\ud800\ud800`,
		"a low then a high":              `\udc00\ud800`,
		"a high at the end":              `x\uD83D`,
	} {
		_, err := parse(t, doc(v), tImage)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		wantCode(t, err, errs.Invalid, errs.CodeOCIManifestInvalid)
	}
	for name, v := range map[string]string{
		"a pair":                       `😀`,
		"a pair after a backslash":     `\\😀`,
		"an escaped backslash and u":   `\\ud800`,
		"a BMP escape":                 `é€`,
		"a literal supplementary char": "\U0001F600",
	} {
		if _, err := parse(t, doc(v), tImage); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Mechanism of R-OCI-013, not a proof of its criteria: it checks the parser, one step of the commit transaction or a store query;
// the criteria are the refusals by size and media type, the foreign layer, the artifact round trip and the concurrent tag.
func TestUrlsWithCredentialsAreRefused(t *testing.T) {
	doc := func(u string) string {
		return fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"size":1},"layers":[{"mediaType":"a/b","digest":%q,"size":1,"urls":[%q]}]}`, dg("c"), dg("l"), u)
	}
	if _, err := parse(t, doc("https://user:pass@example.com/l"), tImage); err == nil {
		t.Error("credentials in a url were accepted")
	}
	if _, err := parse(t, doc("https://example.com/l"), tImage); err != nil {
		t.Error(err)
	}
}
