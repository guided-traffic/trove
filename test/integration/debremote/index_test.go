//go:build integration

package debremote

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub"
)

// listing reads what a signed InRelease lists: the path of each file below the directory and its SHA-256.
func listing(t *testing.T, inrelease []byte) map[string]string {
	t.Helper()
	text, err := openpgp.ClearText(inrelease, openpgp.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := release.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range rel.Files {
		out[f.Path] = hex.EncodeToString(f.SHA256[:])
	}
	return out
}

func stubPath(dir, rel string) string { return debstub.ArchiveBase + "/" + dir + rel }

func TestR_DEB_021_AnIndexIsServedWhenTheStoredReleaseListsItAndNotOtherwise(t *testing.T) {
	e := newEnv(t)
	rel := e.get(rKeyed, suitePath+"InRelease")
	if rel.Status != http.StatusOK {
		t.Fatalf("%d %s", rel.Status, rel.Body)
	}
	files := listing(t, rel.Body)
	const index = packages + ".gz"
	want, ok := files[index]
	if !ok {
		t.Fatalf("the release does not list %s: %v", index, files)
	}
	e.stub.Reset()
	a := e.get(rKeyed, suitePath+index)
	if a.Status != http.StatusOK || sum(a.Body) != want {
		t.Fatalf("%d, digest %s, listed %s", a.Status, sum(a.Body), want)
	}
	if b := e.get(rKeyed, suitePath+index); b.Status != http.StatusOK || sum(b.Body) != want {
		t.Errorf("second read: %d", b.Status)
	}
	if n := e.requests(stubPath(suitePath, index)); n != 1 {
		t.Errorf("the stub saw %d requests for the index, want 1", n)
	}
	// A file the release does not list answers 404 without a request to the upstream. The fill of the index enqueued its parse
	// (Q-DEB-90), which reads through the upstream in the background: wait until it is done, so the count below is the request's.
	id := e.repoID(rKeyed)
	eventually(t, 30*time.Second, "the parse of main/binary-amd64", func() bool {
		return e.st.Number(`SELECT count(*) FROM deb_remote_indexes WHERE repo_id = $1 AND path = $2`, id, packages) > 0
	})
	e.stub.Reset()
	c := e.get(rKeyed, suitePath+packages+".bz2")
	if c.Status != http.StatusNotFound || c.Code(t) != "deb_not_in_release" {
		t.Errorf("unlisted index = %d %s", c.Status, c.Body)
	}
	if n := e.stub.Counts().Total(); n != 0 {
		t.Errorf("the stub saw %d requests for an unlisted index", n)
	}
}

func TestR_DEB_021_ByHashFilesAreContentFetchedOnceAndTheOtherHashesAre404WithoutARequest(t *testing.T) {
	e := newEnv(t)
	rel := e.get(rKeyed, suitePath+"InRelease")
	files := listing(t, rel.Body)
	digest := files[packages+".gz"]
	byHash := suitePath + "main/binary-amd64/by-hash/SHA256/" + digest
	e.stub.Reset()
	for i := 0; i < 2; i++ {
		a := e.get(rKeyed, byHash)
		if a.Status != http.StatusOK || sum(a.Body) != digest {
			t.Fatalf("read %d: %d", i, a.Status)
		}
	}
	if n := e.requests(debstub.ArchiveBase + "/" + byHash); n != 1 {
		t.Errorf("the stub saw %d requests for the by-hash file, want 1", n)
	}
	e.stub.Reset()
	for _, algo := range []string{"MD5Sum", "SHA1"} {
		if a := e.get(rKeyed, suitePath+"main/binary-amd64/by-hash/"+algo+"/"+digest[:32]); a.Status != http.StatusNotFound {
			t.Errorf("%s by-hash = %d", algo, a.Status)
		}
	}
	if n := e.stub.Counts().Total(); n != 0 {
		t.Errorf("the stub saw %d requests for by-hash files of MD5Sum and SHA1", n)
	}
	// A hash that is not the file's own is refused by the verification, whatever the policy says about other files.
	bad := suitePath + "main/binary-amd64/by-hash/SHA256/" + strings.Repeat("0", 64)
	if a := e.get(rKeyed, bad); a.Status == http.StatusOK {
		t.Errorf("a by-hash path of no file = %d", a.Status)
	}
}

func TestR_DEB_021_AnIndexThatMatchesNoReleaseIsRefusedAndNothingIsStoredForIt(t *testing.T) {
	e := newEnv(t)
	repo := e.fresh(true)
	if err := e.stub.SetMode(debstub.Mode{IndexMismatch: true}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(repo, suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	a := e.get(repo, suitePath+packages+".gz")
	if a.Status != http.StatusBadGateway || a.Code(t) != "deb_index_mismatch" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.rows("deb_remote_indexes", repo); n != 0 {
		t.Errorf("%d parsed indexes", n)
	}
}

func TestR_DEB_021_WithoutByHashANewGenerationIsServedOnlyOnceTheReleaseIsRefreshed(t *testing.T) {
	e := newEnv(t)
	if err := e.stub.SetMode(debstub.Mode{NoByHash: true}); err != nil {
		t.Fatal(err)
	}
	const index = packages + ".gz"
	first := e.get(rFast, suitePath+"InRelease")
	oldSum := listing(t, first.Body)[index]
	if a := e.get(rFast, suitePath+index); a.Status != http.StatusOK || sum(a.Body) != oldSum {
		t.Fatalf("the first read: %d", a.Status)
	}
	if _, err := e.stub.NewGeneration("stable"); err != nil {
		t.Fatal(err)
	}
	// Both documents are still fresh: the old release is stored and the old bytes are what it names.
	if a := e.get(rFast, suitePath+index); a.Status != http.StatusOK || sum(a.Body) != oldSum {
		t.Errorf("before the refresh: %d, %s", a.Status, sum(a.Body))
	}
	time.Sleep(2500 * time.Millisecond)
	second := e.get(rFast, suitePath+"InRelease")
	newSum := listing(t, second.Body)[index]
	if newSum == oldSum {
		t.Fatal("the new generation lists the old index")
	}
	if a := e.get(rFast, suitePath+index); a.Status != http.StatusOK || sum(a.Body) != newSum {
		t.Errorf("after the refresh: %d, %s, want %s", a.Status, sum(a.Body), newSum)
	}
}

func TestR_DEB_021_TheFlatDirectoryAndTheFlatRootAreServed(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		repo, dir, prefix string
	}{{rFlat, "binary/", "/flat/"}, {rFlatRoot, "", "/flatroot/"}} {
		path := c.dir
		if c.dir == "" {
			path = "./"
		}
		rel := e.get(c.repo, path+"InRelease")
		if rel.Status != http.StatusOK {
			t.Fatalf("%s: InRelease %d %s", c.repo, rel.Status, rel.Body)
		}
		files := listing(t, rel.Body)
		a := e.get(c.repo, path+"Packages.gz")
		if a.Status != http.StatusOK || sum(a.Body) != files["Packages.gz"] {
			t.Errorf("%s: Packages.gz %d", c.repo, a.Status)
		}
		if g := e.get(c.repo, path+"Release.gpg"); g.Status != http.StatusOK {
			t.Errorf("%s: Release.gpg %d", c.repo, g.Status)
		}
		// Without the dot segment the same file is the same answer.
		if b := e.get(c.repo, c.dir+"InRelease"); b.Status != http.StatusOK || sum(b.Body) != sum(rel.Body) {
			t.Errorf("%s: InRelease without the dot segment %d", c.repo, b.Status)
		}
	}
	// A package file of the flat directory is content.
	var deb string
	for _, f := range e.stub.Files() {
		if strings.HasPrefix(f.Path, "/flat/binary/") {
			deb = strings.TrimPrefix(f.Path, "/flat/")
		}
	}
	if deb == "" {
		t.Fatal("the stub lists no package of the flat directory")
	}
	if a := e.get(rFlat, deb); a.Status != http.StatusOK {
		t.Errorf("package of the flat directory = %d", a.Status)
	}
	// Any other dot segment is refused.
	if a := e.get(rFlat, "./../InRelease"); a.Status != http.StatusBadRequest {
		t.Errorf("a second dot segment = %d", a.Status)
	}
}
