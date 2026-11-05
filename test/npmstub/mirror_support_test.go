package npmstub

import (
	"crypto/sha512"
	"encoding/base64"
	"strings"
	"testing"
)

func integrityOfBytes(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// The two additions the mirror's integration suite needs (R-REPO-052 byte bound, R-REPO-051 a name a feed missed).

func TestPublishPaddedServesALargeTarballWithItsOwnIntegrity(t *testing.T) {
	// R-REPO-052, R-REPO-059: a version whose tarball is as large as the test wants, with the integrity of those bytes.
	f := newFixture(t, Config{})
	const pad = 1 << 20
	if err := f.stub.PublishPadded("@acme/big", "1.0.0", pad); err != nil {
		t.Fatal(err)
	}
	r := f.get("/@acme/big/-/big-1.0.0.tgz")
	if r.status != 200 || len(r.body) < pad || len(r.body) > pad+16384 {
		t.Fatalf("%d, %d bytes", r.status, len(r.body))
	}
	again := f.get("/@acme/big/-/big-1.0.0.tgz")
	if string(again.body) != string(r.body) {
		t.Error("the padded tarball is not reproducible")
	}
	v, ok := f.stub.Served("@acme/big", "1.0.0")
	if !ok {
		t.Fatal("the version is not listed")
	}
	if got := integrityOfBytes(r.body); got != v.Integrity {
		t.Errorf("the document promises %s, the tarball is %s", v.Integrity, got)
	}
}

func TestUnlistLeavesAPackageOutOfTheListingButServesIt(t *testing.T) {
	// R-REPO-051, R-REPO-059: "a name a feed missed".
	f := newFixture(t, Config{})
	for _, n := range []string{"@acme/listed", "@acme/missed"} {
		if err := f.stub.Publish(n, "1.0.0", nil); err != nil {
			t.Fatal(err)
		}
	}
	f.stub.Unlist("@acme/missed")
	got, st := f.allDocs(keyQuery("@acme/", "@acme/￰", "10000"))
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	var ids []string
	for _, r := range got.Rows {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "@acme/listed" {
		t.Fatalf("the listing holds %v", ids)
	}
	if st := f.status("/@acme/missed"); st != 200 {
		t.Errorf("the registry answers %d for the document of an unlisted package", st)
	}
}
