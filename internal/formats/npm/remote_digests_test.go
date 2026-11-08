package npm

import (
	"crypto/sha1" //nolint:gosec // the legacy digest an npm document announces
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// TestR_REPO_041_TheAnnouncedDigestIsTheStrongestTheSpoolComputes: sha512 of dist.integrity, else sha256, else sha1 of the
// integrity, else dist.shasum; an integrity with sha384 and nothing the spool computes is refused (R-NPM-025).
func TestR_REPO_041_TheAnnouncedDigestIsTheStrongestTheSpoolComputes(t *testing.T) {
	b := []byte("tarball")
	s512, s384, s256, s1 := sha512.Sum512(b), sha512.Sum384(b), sha256.Sum256(b), sha1.Sum(b) //nolint:gosec // see the import
	sri := func(algo string, sum []byte) string { return algo + "-" + base64.StdEncoding.EncodeToString(sum) }
	shasum := hex.EncodeToString(s1[:])

	cases := []struct {
		name      string
		facts     rows.TarballFacts
		sha512    []byte
		sha256    []byte
		sha1      []byte
		wantError string
	}{
		{name: "sha512 wins over a shasum", facts: rows.TarballFacts{Integrity: sri("sha512", s512[:]), Shasum: "0000000000000000000000000000000000000000"}, sha512: s512[:]},
		{name: "sha512 wins over the other entries", facts: rows.TarballFacts{Integrity: sri("sha256", s256[:]) + " " + sri("sha512", s512[:])}, sha512: s512[:]},
		{name: "sha256 when there is no sha512, whatever the shasum says", facts: rows.TarballFacts{Integrity: sri("sha256", s256[:]), Shasum: "0000000000000000000000000000000000000000"}, sha256: s256[:]},
		{name: "sha256 outvotes the sha384 it cannot compare", facts: rows.TarballFacts{Integrity: sri("sha384", s384[:]) + " " + sri("sha256", s256[:])}, sha256: s256[:]},
		{name: "sha1 of the integrity", facts: rows.TarballFacts{Integrity: sri("sha1", s1[:]), Shasum: "0000000000000000000000000000000000000000"}, sha1: s1[:]},
		{name: "the shasum when the integrity carries none", facts: rows.TarballFacts{Shasum: shasum}, sha1: s1[:]},
		{name: "nothing announced", facts: rows.TarballFacts{}},
		{name: "sha384 outvotes a sha1 and is refused", facts: rows.TarballFacts{Integrity: sri("sha384", s384[:]) + " " + sri("sha1", s1[:]), Shasum: shasum}, wantError: errs.CodeUpstreamDigestMissing},
		{name: "sha384 only is refused", facts: rows.TarballFacts{Integrity: sri("sha384", s384[:]), Shasum: shasum}, wantError: errs.CodeUpstreamDigestMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := announcedDigests(tc.facts)
			if tc.wantError != "" {
				if e, ok := errs.As(err); !ok || e.Code != tc.wantError {
					t.Fatalf("err = %v, want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(d.SHA512) != string(tc.sha512) || string(d.SHA256) != string(tc.sha256) || string(d.SHA1) != string(tc.sha1) {
				t.Errorf("digests = %x %x %x", d.SHA512, d.SHA256, d.SHA1)
			}
		})
	}
}
