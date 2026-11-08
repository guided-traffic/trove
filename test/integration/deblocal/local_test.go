//go:build integration

package deblocal

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/debstub/debgen"
)

func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	i := strings.Index(string(body), `"code":"`)
	if i < 0 {
		t.Fatalf("no code in %s", body)
	}
	rest := string(body)[i+len(`"code":"`):]
	return rest[:strings.IndexByte(rest, '"')]
}

// R-DEB-006: a new file answers 201, the same bytes again answer 200 and add coordinates, other bytes for the version answer 409, and the
// rows, the suites' numbers and the item's properties follow.
func TestR_DEB_006_UploadStoresTheFileWithItsCoordinates(t *testing.T) {
	e := newEnv(t)
	body := build(t, "hello", "2.10-3", nil)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable"})
	if a["path"] != poolHello || a["package"] != "hello" || a["version"] != "2.10-3" {
		t.Errorf("answer = %v", a)
	}
	if got := e.coordinates(poolHello); got != "stable/main" {
		t.Errorf("coordinates = %q", got)
	}
	if got := e.changeSeq("stable"); got != 1 {
		t.Errorf("change_seq of stable = %d", got)
	}
	if got := e.st.Text(`SELECT COALESCE((SELECT p.value FROM item_properties p JOIN items i ON i.id = p.item_id JOIN repositories r ON r.id = i.repo_id
		WHERE r.key = $1 AND i.path = $2 AND p.key = 'deb.coordinate'), '')`, repo, poolHello); got != "stable/main" {
		t.Errorf("deb.coordinate = %q", got)
	}

	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 200, []string{"testing"}, "main", "contrib")
	if got := e.coordinates(poolHello); got != "stable/main,testing/contrib,testing/main" {
		t.Errorf("coordinates after the second upload = %q", got)
	}
	if got := e.changeSeq("stable"); got != 1 {
		t.Errorf("a suite the second upload did not touch has change_seq %d", got)
	}

	other := build(t, "hello", "2.10-3", func(s *debgen.Spec) { s.Line = "other bytes" })
	if r := e.upload(e.tokA, "hello_2.10-3_amd64.deb", other, []string{"stable"}); r.Status != 409 || codeOf(t, r.Body) != "deb_version_exists" {
		t.Errorf("other bytes = %d %s", r.Status, r.Body)
	}
	if r := e.upload(e.tokA, "hello_2.10-3_amd64.deb", body, nil); r.Status != 400 || codeOf(t, r.Body) != "deb_distribution_required" {
		t.Errorf("no distribution = %d %s", r.Status, r.Body)
	}
	if r := e.upload(e.tokA, "bad.deb", []byte("garbage"), []string{"stable"}); r.Status != 422 || codeOf(t, r.Body) != "deb_invalid_archive" {
		t.Errorf("garbage = %d %s", r.Status, r.Body)
	}
	if r := e.upload(e.tokC, "hello_2.11-1_amd64.deb", build(t, "hello", "2.11-1", nil), []string{"stable"}); r.Status != 403 {
		t.Errorf("a reader's upload = %d %s", r.Status, r.Body)
	}
	if r := e.st.Do(e.r, "GET", "/api/deb/"+project+"/"+repo+"/"+poolHello, e.tokC, nil); r.Status != 200 || string(r.Body) != string(body) {
		t.Errorf("download = %d, %d bytes", r.Status, len(r.Body))
	}
}

// R-DEB-009: PUT .../deb/coordinates replaces the coordinates; an added one needs deploy, a removed one delete, and a removal is journaled.
func TestR_DEB_009_CoordinatesChangeNeedsDeployAndDelete(t *testing.T) {
	e := newEnv(t)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})

	// bob may deploy, not delete: adding a coordinate is allowed, removing one is not.
	if a := e.setCoordinates(e.tokB, poolHello, [2]string{"stable", "main"}, [2]string{"testing", "main"}); a.Status != 200 {
		t.Fatalf("add = %d %s", a.Status, a.Body)
	}
	if got := e.coordinates(poolHello); got != "stable/main,testing/main" {
		t.Errorf("coordinates = %q", got)
	}
	if a := e.setCoordinates(e.tokB, poolHello, [2]string{"testing", "main"}); a.Status != 403 {
		t.Errorf("a removal by a deployer = %d %s", a.Status, a.Body)
	}
	if a := e.setCoordinates(e.tokC, poolHello, [2]string{"stable", "main"}, [2]string{"testing", "main"}, [2]string{"unstable", "main"}); a.Status != 403 {
		t.Errorf("a change by a reader = %d %s", a.Status, a.Body)
	}
	if got := e.coordinates(poolHello); got != "stable/main,testing/main" {
		t.Errorf("a refused change altered the coordinates: %q", got)
	}

	seqBefore := e.changeSeq("stable")
	a := e.setCoordinates(e.tokA, poolHello, [2]string{"testing", "main"})
	if a.Status != 200 {
		t.Fatalf("move = %d %s", a.Status, a.Body)
	}
	if got := e.coordinates(poolHello); got != "testing/main" {
		t.Errorf("coordinates after the move = %q", got)
	}
	if e.changeSeq("stable") <= seqBefore {
		t.Error("the suite the file left was not raised")
	}
	if e.events("deb.coordinates.changed") != 2 || e.events("deb.coordinates.removed") != 1 {
		t.Errorf("events: changed %d, removed %d", e.events("deb.coordinates.changed"), e.events("deb.coordinates.removed"))
	}

	if a := e.setCoordinates(e.tokA, poolHello); a.Status != 422 {
		t.Errorf("an empty list = %d %s", a.Status, a.Body)
	}
	if a := e.setCoordinates(e.tokA, poolHello, [2]string{"Bad Suite", "main"}); a.Status != 400 {
		t.Errorf("a suite outside the grammar = %d %s", a.Status, a.Body)
	}
	if a := e.setCoordinates(e.tokA, "pool/h/hello/missing_1.0-1_amd64.deb", [2]string{"stable", "main"}); a.Status != 404 {
		t.Errorf("a missing file = %d %s", a.Status, a.Body)
	}
}

// R-DEB-009: 64 suites per repository, 32 components per suite, 32 coordinates per file; more answers 422.
func TestR_DEB_009_BoundsAnswer422(t *testing.T) {
	e := newEnv(t)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"s0"})
	for n := 1; n < 64; n++ {
		a := e.setCoordinates(e.tokA, poolHello, [2]string{"s0", "main"}, [2]string{suiteName(n), "main"})
		if a.Status != 200 {
			t.Fatalf("suite %d = %d %s", n, a.Status, a.Body)
		}
	}
	a := e.setCoordinates(e.tokA, poolHello, [2]string{"s0", "main"}, [2]string{"one-too-many", "main"})
	if a.Status != 422 {
		t.Errorf("the 65th suite = %d %s", a.Status, a.Body)
	}
	if got := e.st.Number(`SELECT count(*) FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1`, repo); got != 64 {
		t.Errorf("suites = %d", got)
	}
}

func suiteName(n int) string { return "s" + string(rune('a'+n/26)) + string(rune('a'+n%26)) }

// R-DEB-013: deleting a file through its route needs delete, removes the rows, raises the suites and writes the journaled event.
func TestR_DEB_013_DeleteRemovesTheFileAndJournalsIt(t *testing.T) {
	e := newEnv(t)
	body := build(t, "hello", "2.10-3", nil)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable"})
	path := "/api/deb/" + project + "/" + repo + "/" + poolHello
	if a := e.st.Do(e.r, "DELETE", path, e.tokB, nil); a.Status != 403 {
		t.Errorf("a deployer's delete = %d %s", a.Status, a.Body)
	}
	seq := e.changeSeq("stable")
	if a := e.st.Do(e.r, "DELETE", path, e.tokA, nil); a.Status != 204 {
		t.Fatalf("delete = %d %s", a.Status, a.Body)
	}
	if got := e.st.Number(`SELECT count(*) FROM deb_binaries b JOIN repositories r ON r.id = b.repo_id WHERE r.key = $1`, repo); got != 0 {
		t.Errorf("rows left = %d", got)
	}
	if e.changeSeq("stable") <= seq {
		t.Error("the suite was not raised by the deletion")
	}
	if e.events("deb.package.deleted") != 1 {
		t.Errorf("deb.package.deleted events = %d", e.events("deb.package.deleted"))
	}
	if a := e.st.Do(e.r, "GET", path, e.tokA, nil); a.Status != 404 {
		t.Errorf("get after delete = %d", a.Status)
	}
	// The bytes can be uploaded again as a new file: only a restore withholds them.
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable"})
}

// R-AUTHZ-018: the ownership of a name is claimed by the first upload; another deployer cannot add a version or move the file.
func TestR_AUTHZ_018_OwnershipHoldsForUploadAndCoordinates(t *testing.T) {
	e := newOwnedEnv(t)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	if a := e.upload(e.tokB, "hello_2.11-1_amd64.deb", build(t, "hello", "2.11-1", nil), []string{"stable"}); a.Status != 403 || codeOf(t, a.Body) != "package_owned_by_other_owner" {
		t.Errorf("a version by another deployer = %d %s", a.Status, a.Body)
	}
	if a := e.setCoordinates(e.tokB, poolHello, [2]string{"stable", "main"}, [2]string{"testing", "main"}); a.Status != 403 {
		t.Errorf("coordinates by another deployer = %d %s", a.Status, a.Body)
	}
	if got := e.coordinates(poolHello); got != "stable/main" {
		t.Errorf("coordinates = %q", got)
	}
}
