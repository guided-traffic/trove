//go:build integration

package debremote

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/debstub"
)

func TestR_DEB_019_TheInReleaseIsPassedThroughByteForByteAndServedFromTheCache(t *testing.T) {
	e := newEnv(t)
	want := e.stubGet(inRelease)
	e.stub.Reset()

	a := e.get(rKeyed, suitePath+"InRelease")
	if a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if sum(a.Body) != sum(want) {
		t.Errorf("the InRelease differs from the stub's file: %d bytes against %d", len(a.Body), len(want))
	}
	if got := a.Header.Get("ETag"); got != `"`+sum(want)+`"` {
		t.Errorf("ETag = %s", got)
	}
	// A second request within the period sends the stub nothing; a conditional request of the client is a 304.
	if b := e.get(rKeyed, suitePath+"InRelease", "If-None-Match", a.Header.Get("ETag")); b.Status != http.StatusNotModified {
		t.Errorf("conditional request = %d", b.Status)
	}
	if n := e.requests(inRelease); n != 1 {
		t.Errorf("the stub saw %d requests for the InRelease, want 1", n)
	}
	// The stored release names the files of the directory, and the directory is verified by the key a.
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND document = 'InRelease' AND verified_by IS NOT NULL AND floor_date IS NOT NULL`, e.repoID(rKeyed)); n != 1 {
		t.Errorf("verified InRelease rows = %d", n)
	}
	if n := e.rows("deb_remote_files", rKeyed); n < 10 {
		t.Errorf("the release lists %d files in deb_remote_files", n)
	}
}

func TestR_DEB_019_AReleaseAfterAPeriodIsRevalidatedWithOneConditionalRequest(t *testing.T) {
	e := newEnv(t)
	if a := e.get(rFast, suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	e.stub.Reset()
	time.Sleep(2500 * time.Millisecond)
	if a := e.get(rFast, suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	c := e.stub.Counts()
	if got := c.Conditional("release"); got != 1 || c.NotModified() != 1 {
		t.Errorf("conditional release requests %d, 304 answers %d, want one each", got, c.NotModified())
	}
}

// The verification of R-DEB-020, case by case, each in a repository of its own so that nothing is stored yet.
func TestR_DEB_020_ARepositoryWithKeysStoresAReleaseOnlyWhenAKeyOfTheSetVerifiesIt(t *testing.T) {
	cases := []struct {
		name    string
		mode    debstub.Mode
		status  int
		code    string
		stored  int64
		keyless bool
	}{
		{name: "signed by the key a", mode: debstub.Mode{}, status: http.StatusOK, stored: 1},
		{name: "signed by an unknown key", mode: debstub.Mode{SignWith: []string{"b"}}, status: http.StatusBadGateway, code: "deb_upstream_signature_invalid"},
		{name: "co-signed by a and an unknown key", mode: debstub.Mode{SignWith: []string{"a", "c"}}, status: http.StatusOK, stored: 1},
		{name: "signed with SHA-1 only", mode: debstub.Mode{SignHash: "sha1"}, status: http.StatusBadGateway, code: "deb_upstream_signature_invalid"},
		{name: "a broken signature", mode: debstub.Mode{BrokenSignature: true}, status: http.StatusBadGateway, code: "deb_upstream_signature_invalid"},
		{name: "a signature from the future", mode: debstub.Mode{SignatureSkewSeconds: 1200}, status: http.StatusBadGateway, code: "deb_upstream_signature_invalid"},
		{name: "a Date twenty minutes ahead", mode: debstub.Mode{Date: debstub.DateAhead}, status: http.StatusBadGateway, code: "deb_release_invalid"},
		{name: "a Valid-Until that has passed", mode: debstub.Mode{ValidUntil: debstub.ValidUntilPassed}, status: http.StatusBadGateway, code: "deb_release_invalid"},
		{name: "an InRelease without a Hash header", mode: debstub.Mode{NoHashHeader: true}, status: http.StatusOK, stored: 1},
		{name: "a broken signature without keys is stored", mode: debstub.Mode{BrokenSignature: true}, status: http.StatusOK, stored: 1, keyless: true},
		{name: "a Date twenty minutes ahead is refused without keys too", mode: debstub.Mode{Date: debstub.DateAhead}, status: http.StatusBadGateway, code: "deb_release_invalid", keyless: true},
	}
	e := newEnv(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := e.stub.SetMode(c.mode); err != nil {
				t.Fatal(err)
			}
			repo := e.fresh(!c.keyless)
			a := e.get(repo, suitePath+"InRelease")
			if a.Status != c.status {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
			if c.code != "" && a.Code(t) != c.code {
				t.Errorf("code = %s, want %s", a.Code(t), c.code)
			}
			if n := e.rows("deb_remote_releases", repo); n != c.stored {
				t.Errorf("stored release rows = %d, want %d", n, c.stored)
			}
		})
	}
}

func TestR_DEB_019_AnOlderReleaseDoesNotReplaceAVerifiedOneButReplacesAnUnverifiedOne(t *testing.T) {
	e := newEnv(t)
	first := map[string][]byte{}
	for _, repo := range []string{rFast, rPlain} {
		a := e.get(repo, suitePath+"InRelease")
		if a.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", repo, a.Status, a.Body)
		}
		first[repo] = a.Body
	}
	if err := e.stub.SetMode(debstub.Mode{Date: debstub.DateOlder}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // the period of rFast; rPlain keeps its 300 s, so it is changed by a stored copy below
	if a := e.get(rFast, suitePath+"InRelease"); a.Status != http.StatusOK || sum(a.Body) != sum(first[rFast]) {
		t.Errorf("the keyed remote served another release than the verified one: %d", a.Status)
	}
	if !strings.Contains(e.r.Metrics(t), "trove_deb_release_rollbacks_total") {
		t.Error("the rollback was not counted")
	}
}

func TestR_DEB_019_ReleaseAndReleaseGpgAreOnePairAndVerifyBeforeTheyAreServed(t *testing.T) {
	e := newEnv(t)
	rel := e.get(rKeyed, suitePath+"Release")
	gpg := e.get(rKeyed, suitePath+"Release.gpg")
	if rel.Status != http.StatusOK || gpg.Status != http.StatusOK {
		t.Fatalf("Release %d, Release.gpg %d: %s %s", rel.Status, gpg.Status, rel.Body, gpg.Body)
	}
	if !strings.Contains(string(gpg.Body), "BEGIN PGP SIGNATURE") {
		t.Errorf("Release.gpg: %.60s", gpg.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND document = 'Release' AND verified_by IS NOT NULL`, e.repoID(rKeyed)); n != 1 {
		t.Errorf("the Release is recorded verified %d times", n)
	}
}

func TestR_DEB_019_APairThatDoesNotVerifyIsRefused(t *testing.T) {
	e := newEnv(t)
	if err := e.stub.SetMode(debstub.Mode{BrokenSignature: true}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rKeyed, suitePath+"Release.gpg")
	if a.Status != http.StatusBadGateway || a.Code(t) != "deb_upstream_signature_invalid" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND verified_by IS NOT NULL`, e.repoID(rKeyed)); n != 0 {
		t.Errorf("%d releases are recorded verified", n)
	}
}

func TestR_DEB_019_ASuiteTheUpstreamDoesNotHaveIsRecordedAbsent(t *testing.T) {
	e := newEnv(t)
	if err := e.stub.SetMode(debstub.Mode{NotFound: []string{"testing"}}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(rPlain, "dists/testing/InRelease"); a.Status != http.StatusNotFound {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	id := e.repoID(rPlain)
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND dir = 'dists/testing' AND document = 'absent'`, id); n != 1 {
		t.Fatalf("absent records = %d", n)
	}
	// A later successful fetch deletes the record.
	if err := e.stub.SetMode(debstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 90*time.Second, "the negative entry to expire", func() bool {
		return e.get(rPlain, "dists/testing/InRelease").Status == http.StatusOK
	})
	if n := e.st.Number(`SELECT count(*) FROM deb_remote_releases WHERE repo_id = $1 AND dir = 'dists/testing' AND document = 'absent'`, id); n != 0 {
		t.Errorf("the absent record stays: %d", n)
	}
}

func TestR_DEB_018_SigningKeyOfARemoteIsTheKeysOfTheUpstream(t *testing.T) {
	e := newEnv(t)
	a := e.get(rKeyed, "signing-key.asc")
	if a.Status != http.StatusOK || a.Header.Get("Content-Type") != "application/pgp-keys" || !strings.Contains(string(a.Body), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("%d %s %.40s", a.Status, a.Header.Get("Content-Type"), a.Body)
	}
	if b := e.get(rKeyed, "signing-key.asc", "If-None-Match", a.Header.Get("ETag")); b.Status != http.StatusNotModified {
		t.Errorf("conditional = %d", b.Status)
	}
	n := e.get(rPlain, "signing-key.asc")
	if n.Status != http.StatusNotFound || n.Code(t) != "deb_no_upstream_keys" {
		t.Errorf("a remote without keys = %d %s", n.Status, n.Body)
	}
}
