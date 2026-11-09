package release_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/openpgp"
)

const file = "0000000000000000000000000000000000000000000000000000000000000001"

func text(extra string) []byte {
	return []byte("Origin: test\nSuite: stable\nCodename: ratel\nDate: Mon, 09 Nov 2026 10:00:00 UTC\nAcquire-By-Hash: yes\n" + extra +
		"SHA256:\n " + file + " 1234 main/binary-amd64/Packages.xz\n " + file + " 99 InRelease.gpg\n")
}

func TestParseReadsTheFieldsAndTheChecksumSection(t *testing.T) {
	r, err := release.Parse(text("Valid-Until: Mon, 16 Nov 2026 10:00:00 UTC\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Suite != "stable" || r.Codename != "ratel" || !r.ByHash || !r.Date.Equal(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("fields = %+v", r)
	}
	if r.ValidUntil.IsZero() || len(r.Files) != 2 || r.Files[0].Path != "main/binary-amd64/Packages.xz" || r.Files[0].Size != 1234 {
		t.Errorf("files = %+v until %v", r.Files, r.ValidUntil)
	}
}

func TestParseRefusesWhatIsNoRelease(t *testing.T) {
	for name, doc := range map[string]string{
		"no Date":              "Suite: stable\nSHA256:\n " + file + " 1 a\n",
		"no SHA256 section":    "Suite: stable\nDate: Mon, 09 Nov 2026 10:00:00 UTC\n",
		"a line of two fields": "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n " + file + " 1\n",
		"a short digest":       "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n abcd 1 a\n",
		"a path above":         "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n " + file + " 1 ../a\n",
		"an absolute path":     "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n " + file + " 1 /a\n",
		"a negative size":      "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n " + file + " -1 a\n",
		"a second paragraph":   "Date: Mon, 09 Nov 2026 10:00:00 UTC\nSHA256:\n " + file + " 1 a\n\nOrigin: x\n",
		"a date that is none":  "Date: yesterday\nSHA256:\n " + file + " 1 a\n",
	} {
		if _, err := release.Parse([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := release.Parse(make([]byte, release.MaxBytes+1)); err == nil {
		t.Error("a document over 16 MiB was accepted")
	}
}

func TestParseTimeReadsTheFormsOfTheArchives(t *testing.T) {
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, s := range []string{"Fri, 02 Jan 2026 03:04:05 UTC", "Fri, 2 Jan 2026 03:04:05 UTC", "Fri, 02 Jan 2026 04:04:05 +0100"} {
		got, err := release.ParseTime(s)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q = %v, %v", s, got, err)
		}
	}
	if release.Format(want) != "Fri, 02 Jan 2026 03:04:05 UTC" {
		t.Errorf("Format = %s", release.Format(want))
	}
}

// signed is the InRelease of a document, signed by a key made for the test.
func signed(t *testing.T, doc []byte) (inrelease []byte, certs []openpgp.Certificate) {
	t.Helper()
	now := time.Now()
	secret, public, _, err := openpgp.Generate("test", now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key, err := openpgp.ParseSecret(secret, nil, now, openpgp.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	inrelease, err = openpgp.ClearSign(doc, []*openpgp.Key{key}, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	certs, err = openpgp.ParseCertificates(public, now, openpgp.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return inrelease, certs
}

func TestSignedVerifiesWithKeysAndReadsStructureWithout(t *testing.T) {
	doc, certs := signed(t, text(""))
	r, signer, err := release.Signed(doc, certs, time.Now())
	if err != nil || signer != certs[0].Fingerprint || r.Suite != "stable" {
		t.Fatalf("with keys: %v %s %v", r, signer, err)
	}
	r, signer, err = release.Signed(doc, nil, time.Now())
	if err != nil || signer != "" || r.Codename != "ratel" {
		t.Fatalf("without keys: %v %q %v", r, signer, err)
	}
	// Another key does not verify, and the failure is a verification error with a label.
	_, other := signed(t, text(""))
	if _, _, err = release.Signed(doc, other, time.Now()); err == nil || openpgp.Reason(err) == "" {
		t.Errorf("a foreign key verified: %v", err)
	}
	var ve *openpgp.VerifyError
	if !errors.As(err, &ve) {
		t.Errorf("the error is %T", err)
	}
	// A text that is no Release is the document's error, not a signature's.
	bad, certs := signed(t, []byte("not: a release\n"))
	var re *release.Error
	if _, _, err := release.Signed(bad, certs, time.Now()); !errors.As(err, &re) {
		t.Errorf("a signed non-release = %v", err)
	}
}

func TestCheckSuiteNamesTheSuiteAndTheValidity(t *testing.T) {
	r, err := release.Parse(text("Valid-Until: Mon, 09 Nov 2026 11:00:00 UTC\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 10, 30, 0, 0, time.UTC)
	for _, suite := range []string{"stable", "ratel", ""} {
		if err := r.CheckSuite(suite, now); err != nil {
			t.Errorf("%q: %v", suite, err)
		}
	}
	if err := r.CheckSuite("testing", now); err == nil || !strings.Contains(err.Error(), "testing") {
		t.Errorf("another suite: %v", err)
	}
	if err := r.CheckSuite("stable", now.Add(time.Hour)); err == nil {
		t.Error("an expired Valid-Until was accepted")
	}
}

func TestRecordCarriesTheDocumentToTheRows(t *testing.T) {
	r, _ := release.Parse(text(""))
	rec := r.Record(7, "dists/stable", "InRelease", [32]byte{1}, "ABCD", true)
	if rec.RepoID != 7 || rec.Dir != "dists/stable" || rec.Document != "InRelease" || !rec.ByHash || !rec.KeysSet || rec.VerifiedBy != "ABCD" || len(rec.Files) != 2 {
		t.Errorf("record = %+v", rec)
	}
	if !rec.Usable() {
		t.Error("a verified record is unusable")
	}
	rec.VerifiedBy = ""
	if rec.Usable() {
		t.Error("an unverified record of a keyed remote is usable")
	}
}
