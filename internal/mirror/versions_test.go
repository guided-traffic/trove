package mirror

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// R-REPO-051 (Q-OCI-180): the engine counts versions, not items. A version of several items is stored whole, an artifact two versions
// name is filled once, and an artifact the plan stored itself (discover.Upstream.Artifact, reported with ReportStored) is counted in
// the pass's items and bytes.
func TestR_REPO_051_AVersionOfSeveralItemsIsStoredWholeAndASharedArtifactOnce(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"maxVersionsPerPackage":2}`)
	g.names("acme/a")
	g.lister.plans = map[string]discover.Plan{"acme/a": {Name: "acme/a", Items: []discover.Item{
		{Version: "1.2", Path: "acme/a/_blobs/config-1.2", Size: 100},
		{Version: "1.2", Path: "acme/a/_blobs/base", Size: 1000},
		{Version: "1.2", Path: "acme/a/_blobs/layer-1.2", Size: 1000},
		{Version: "1.1", Path: "acme/a/_blobs/config-1.1", Size: 100},
		{Version: "1.1", Path: "acme/a/_blobs/base", Size: 1000},
		{Version: "1.0", Path: "acme/a/_blobs/config-1.0", Size: 100},
		{Version: "latest", Path: "acme/a/_blobs/config-latest", Size: 100, Pinned: true},
		{Version: "latest", Path: "acme/a/_blobs/base", Size: 1000, Pinned: true},
	}}}
	g.lister.planHook = func(ctx context.Context, _ string) {
		ReportStored(ctx, FillResult{Bytes: 400})
		ReportStored(ctx, FillResult{Present: true}) // present already: not reported, not counted
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stored", g.host.stored(),
		"acme/a/_blobs/config-1.2", "acme/a/_blobs/base", "acme/a/_blobs/layer-1.2", "acme/a/_blobs/config-1.1",
		"acme/a/_blobs/config-latest")
	if n := len(g.host.stored()); n != 5 {
		t.Errorf("%d artifacts stored, want 5: %v", n, g.host.stored())
	}
	if n := g.host.perPath["acme/a/_blobs/base"]; n != 1 {
		t.Errorf("the shared layer was filled %d times, want once", n)
	}
	last := g.lastPass()
	if last["itemsStored"] != float64(6) || last["bytesStored"] != float64(400+100+1000+1000+100+100) {
		t.Errorf("summary %v: the five fills and the artifact the plan stored are counted", last)
	}
	if g.item(ResultStored) != 6 {
		t.Errorf("trove_mirror_items_total{result=stored} = %v, want 6", g.item(ResultStored))
	}
}
