//go:build integration

package debvirtual

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/test/debstub"
)

// R-DEB-026, the delay bound: with a remote member whose index holds 70,000 paragraphs and deb.compression ["xz"], a package published to
// the local member is listed in the virtual repository within deb.virtual_index_interval + 60 s. The interval of the installation is 10 s.
func TestR_DEB_026_TheDelayOfAVirtualBuildOverALargeIndexStaysWithinItsBound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an index of 70,000 paragraphs")
	}
	e := newEnv(t, options{bulk: debstub.DefaultBulkParagraphs})
	const interval, slack = 10 * time.Second, 60 * time.Second

	// The first publication: the virtual repository merges the 70,000 paragraphs of the remote member.
	first := e.published(rBulk, "bulk", 4*time.Minute, nil)
	listed := e.xzPackages(rBulk, "bulk")
	if n := len(listed); n < debstub.DefaultBulkParagraphs {
		t.Fatalf("the merged index lists %d paragraphs, want at least %d", n, debstub.DefaultBulkParagraphs)
	}
	if a := e.get(rBulk, "dists/bulk/main/binary-amd64/Packages.gz", e.admin); a.Status != 200 {
		t.Errorf("Packages.gz, which is always built, = %d", a.Status)
	}

	start := time.Now()
	if a := e.upload(rLocal, e.admin, "bound-tool", "1.0-1", "bulk"); a.Status != 201 {
		t.Fatalf("upload = %d %s", a.Status, a.Body)
	}
	got := e.published(rBulk, "bulk", interval+slack, func(s suiteState) bool { return s.Generation > first.Generation })
	took := time.Since(start)
	if _, ok := find(e.xzPackages(rBulk, "bulk"), "bound-tool", "1.0-1"); !ok {
		t.Errorf("generation %d does not list the upload", got.Generation)
	}
	t.Logf("from the upload to the publication of the virtual repository: %s (bound %s)", took.Round(100*time.Millisecond), interval+slack)
	if took > interval+slack {
		t.Errorf("the publication took %s, the bound is %s", took, interval+slack)
	}
}

// xzPackages reads the merged Packages.xz of a suite as paragraphs.
func (e *venv) xzPackages(repo, suite string) []control.Paragraph {
	e.t.Helper()
	a := e.get(repo, "dists/"+suite+"/main/binary-amd64/Packages.xz", e.admin)
	if a.Status != 200 {
		e.t.Fatalf("Packages.xz of %s = %d %s", repo, a.Status, a.Body)
	}
	xr, err := xz.NewReader(bytes.NewReader(a.Body))
	if err != nil {
		e.t.Fatal(err)
	}
	r := control.NewReader(xr, false)
	var out []control.Paragraph
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
}
