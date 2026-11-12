package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// TestR_REPO_041_IgnoreAndPassThroughAreRefusedForAFormatThatAlwaysVerifies: the catalog fact
// ChecksumAlwaysVerified (npm) makes create and update answer 422 not_applicable for the two policies that
// would change nothing; verify-if-available and require are accepted.
func TestR_REPO_041_IgnoreAndPassThroughAreRefusedForAFormatThatAlwaysVerifies(t *testing.T) {
	cat := remoteCatalog()
	cat.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "alwaysverified", Classes: []Class{Local, Remote, Virtual}, Layout: "alwaysverified", ProtocolBasePath: "/api/alwaysverified/{project}/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		UpstreamPresets: []catalog.UpstreamPreset{{ID: "public", DisplayName: "Public", URL: "https://feed.example.org/", Default: true}}, ChecksumAlwaysVerified: true,
	}, Names: lower{}})
	validateWith := func(t *testing.T, policy string, create bool) error {
		t.Helper()
		d, err := DecodeDocument([]byte(`{"key":"feed","class":"remote","format":"alwaysverified","remote":{"integrity":{"checksumPolicy":"` + policy + `"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(cat, &d, create)
		return v.err()
	}
	for _, create := range []bool{true, false} {
		name := map[bool]string{true: "create", false: "update"}[create]
		for _, policy := range []string{ChecksumIgnore, ChecksumPassThrough} {
			t.Run("a "+name+" with "+policy+" answers 422 not_applicable at the member", func(t *testing.T) {
				err := validateWith(t, policy, create)
				e, ok := errs.As(err)
				if !ok || e.Status() != 422 || e.Code != errs.CodeValidation {
					t.Fatalf("err = %v", err)
				}
				if got := fieldsOf(err)["/remote/integrity/checksumPolicy"]; got != errs.CodeNotApplicable {
					t.Fatalf("fields = %v", fieldsOf(err))
				}
			})
		}
		for _, policy := range []string{ChecksumVerifyIfAvailable, ChecksumRequire} {
			t.Run("a "+name+" with "+policy+" is accepted", func(t *testing.T) {
				if err := validateWith(t, policy, create); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
