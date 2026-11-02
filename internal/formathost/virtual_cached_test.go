package formathost

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// fillOnly declares the fill hook for one artifact path, as NuGet declares it for the .nupkg of a
// version and for no other file of it.
type fillOnly struct{ path string }

func (f fillOnly) InspectsFill(p formats.Path) bool { return p.Path == f.path }
func (fillOnly) InspectFill(context.Context, formats.RemoteContext, formats.Path, formats.Spooled) (*formats.Inspection, error) {
	return nil, nil //nolint:nilnil // the test never inspects
}

// TestR_AUTHZ_008_AVersionCountsAsCachedOnlyWhenTheArtifactItsFillRecordsIsCached (Q-NUGET-82): a
// remote member that has cached another file of a version (NuGet: its .nuspec) has not cached the
// version's package; the merge must not list the version to a caller who may not fill, nor rank
// the member as holding it. For a format with a fill hook the artifact the hook inspects decides;
// a format without one keeps the format's classification. Before, any file of the version counted.
func TestR_AUTHZ_008_AVersionCountsAsCachedOnlyWhenTheArtifactItsFillRecordsIsCached(t *testing.T) {
	b := newBridge(t)
	b.r.(*resolver).caps = func(formats.ID) formats.RemoteCaps {
		return formats.RemoteCaps{Fill: fillOnly{path: "lodash/-/4.17.21.tgz"}}
	}
	p := formats.Path{Path: "lodash"}
	if _, err := b.r.Metadata(t.Context(), b.rq, p, b.format.Classify(p), "full"); err != nil {
		t.Fatal(err)
	}
	mg := b.eng.merger
	if got := mg.CachedVersion("lodash/-/4.17.21.tgz"); got != "4.17.21" {
		t.Errorf("the inspected artifact names no version: %q", got)
	}
	if got := mg.CachedVersion("lodash/-/4.17.22.tgz"); got != "" {
		t.Errorf("another file of a version made the version cached: %q", got)
	}

	b2 := newBridge(t)
	b2.r.(*resolver).caps = func(formats.ID) formats.RemoteCaps { return formats.RemoteCaps{} }
	if _, err := b2.r.Metadata(t.Context(), b2.rq, p, b2.format.Classify(p), "full"); err != nil {
		t.Fatal(err)
	}
	if got := b2.eng.merger.CachedVersion("lodash/-/4.17.22.tgz"); got != "4.17.22" {
		t.Errorf("a format without a fill hook: %q", got)
	}
}
