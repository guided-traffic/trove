package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// noDigestCatalog adds a remote-capable format whose upstream announces no digest to the remote catalog.
func noDigestCatalog() *catalog.Registry {
	r := remoteCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "nodigest", Classes: []Class{Local, Remote, Virtual}, Layout: "nodigest", ProtocolBasePath: "/api/nodigest/{project}/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		DefaultUpstreamURL: "https://feed.example.org/index.json", ChecksumRequireUnsupported: true,
	}, Names: lower{}})
	return r
}

// TestR_REPO_041_ChecksumRequireIsRefusedForAFormatWithoutAnnouncedDigests: the catalog fact
// ChecksumRequireUnsupported makes create and update answer 422 for checksumPolicy: require, naming the
// member; every other policy, and every other format, is unaffected (WP3.0g).
func TestR_REPO_041_ChecksumRequireIsRefusedForAFormatWithoutAnnouncedDigests(t *testing.T) {
	validateWith := func(t *testing.T, format, policy string, create bool) error {
		t.Helper()
		d, err := DecodeDocument([]byte(`{"key":"feed","class":"remote","format":"` + format + `","remote":{"integrity":{"checksumPolicy":"` + policy + `"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(noDigestCatalog(), &d, create)
		return v.err()
	}
	for _, create := range []bool{true, false} {
		name := map[bool]string{true: "create", false: "update"}[create]
		t.Run("a "+name+" with require answers 422 not_applicable at the member", func(t *testing.T) {
			err := validateWith(t, "nodigest", ChecksumRequire, create)
			e, ok := errs.As(err)
			if !ok || e.Status() != 422 || e.Code != errs.CodeValidation {
				t.Fatalf("err = %v", err)
			}
			if got := fieldsOf(err)["/remote/integrity/checksumPolicy"]; got != errs.CodeNotApplicable {
				t.Fatalf("fields = %v", fieldsOf(err))
			}
		})
		for _, policy := range []string{ChecksumVerifyIfAvailable, ChecksumIgnore, ChecksumPassThrough} {
			t.Run("a "+name+" with "+policy+" is accepted", func(t *testing.T) {
				if err := validateWith(t, "nodigest", policy, create); err != nil {
					t.Fatal(err)
				}
			})
		}
		t.Run("a "+name+" of a format that announces digests accepts require", func(t *testing.T) {
			if err := validateWith(t, "npmlike", ChecksumRequire, create); err != nil {
				t.Fatal(err)
			}
		})
	}
}
