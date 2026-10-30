package repository

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ownershipCatalog is testCatalog with a format whose local repositories enforce
// package ownership by default, as npm and NuGet declare it (Q-AUTHZ-2).
func ownershipCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "ownfmt", Classes: []Class{Local, Remote, Virtual}, Layout: "ownfmt", ProtocolBasePath: "/api/ownfmt/{key}/",
		PatternSubject: SubjectName, PackageNames: true, OwnershipDefault: catalog.OwnershipEnforce,
	}, Names: lower{}})
	return r
}

func ownershipOf(d Document) string {
	if d.Local == nil || d.Local.PackageOwnership == nil {
		return ""
	}
	return *d.Local.PackageOwnership
}

// TestR_AUTHZ_018_OwnershipDefault: a local repository of a format with an ownership
// default holds it while its document sets none, on a create and on an update that
// removes it, and the stored document says so (Q-AUTHZ-155); off stays an explicit
// choice, and nothing is added where ownership does not apply.
func TestR_AUTHZ_018_OwnershipDefault(t *testing.T) {
	check := func(t *testing.T, body string, create bool) Document {
		t.Helper()
		d, err := DecodeDocument([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(ownershipCatalog(), &d, create)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Run("R-AUTHZ-018 a local repository created without packageOwnership reads back enforce", func(t *testing.T) {
		d := check(t, `{"key":"own-local","class":"local","format":"ownfmt"}`, true)
		if got := ownershipOf(d); got != catalog.OwnershipEnforce {
			t.Fatalf("packageOwnership = %q", got)
		}
		cfg, err := d.marshalConfig()
		if err != nil || !strings.Contains(string(cfg), `"local":{"packageOwnership":"enforce"}`) {
			t.Fatalf("stored config %s, %v", cfg, err)
		}
	})
	t.Run("R-AUTHZ-018 off is kept as the explicit choice", func(t *testing.T) {
		if got := ownershipOf(check(t, `{"key":"own-local","class":"local","format":"ownfmt","local":{"packageOwnership":"off"}}`, true)); got != catalog.OwnershipOff {
			t.Fatalf("packageOwnership = %q", got)
		}
	})
	t.Run("R-REPO-011 an update without the member reverts it to the default", func(t *testing.T) {
		if got := ownershipOf(check(t, `{"key":"own-local","class":"local","format":"ownfmt","local":{"autoClaim":null}}`, false)); got != catalog.OwnershipEnforce {
			t.Fatalf("packageOwnership = %q", got)
		}
	})
	t.Run("R-AUTHZ-018 remote and virtual repositories have no ownership", func(t *testing.T) {
		for class, section := range map[string]string{
			"remote":  `"remote":{"url":"https://upstream.example.test/"}`,
			"virtual": `"virtual":{"members":["own-local"]}`,
		} {
			if d := check(t, `{"key":"own-x","class":"`+class+`","format":"ownfmt",`+section+`}`, true); d.Local != nil {
				t.Fatalf("%s: local = %+v", class, *d.Local)
			}
		}
	})
	t.Run("R-AUTHZ-018 a format without an ownership default adds nothing", func(t *testing.T) {
		if d := check(t, `{"key":"pkg-local","class":"local","format":"pkgfmt"}`, true); d.Local != nil {
			t.Fatalf("local = %+v", *d.Local)
		}
	})
	t.Run("R-AUTHZ-020 a shared ownership domain is refused until it is built", func(t *testing.T) {
		d, err := DecodeDocument([]byte(`{"key":"own-local","class":"local","format":"ownfmt","local":{"ownershipDomain":"acme-npm"}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(ownershipCatalog(), &d, true)
		if got := fieldsOf(v.err())["/local/ownershipDomain"]; got != errs.CodeNotApplicable {
			t.Fatalf("code %q", got)
		}
	})
}
