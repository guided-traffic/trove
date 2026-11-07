package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
)

// vulnCatalog holds the generic format (no adapter), an npm-like format whose catalog entry carries the ecosystem of its
// vulnerability adapter (formats.Register derives it, ADR-150) and a package format without one.
func vulnCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npm", Classes: []Class{Local, Remote, Virtual}, Layout: "npm", ProtocolBasePath: "/api/npm/{project}/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
	}, Names: lower{}, Ecosystem: "npm"})
	return r
}

func vulnValidate(t *testing.T, body string) (Document, error) {
	t.Helper()
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(vulnCatalog(), &d, true)
	return d, v.err()
}

// R-VULN-002 acceptance, at the level of the model: PATCH npm-all {"vulnerabilities": {"enabled": false}} on a virtual
// repository answers 422 not_applicable at /vulnerabilities. (The merge patch itself is the service's; this is the document it
// validates.)
func TestR_VULN_002_AVirtualRepositoryHasNoVulnerabilitiesMember(t *testing.T) {
	_, err := vulnValidate(t, `{"key":"npm-all","class":"virtual","format":"npm","vulnerabilities":{"enabled":false}}`)
	e, ok := errs.As(err)
	if !ok || e.Status() != 422 {
		t.Fatalf("err = %v", err)
	}
	if got := fieldsOf(err)["/vulnerabilities"]; got != errs.CodeNotApplicable {
		t.Fatalf("fields = %v, want not_applicable at /vulnerabilities", fieldsOf(err))
	}
}

func TestR_VULN_002_AFormatWithoutTheAdapterHasNoVulnerabilitiesMember(t *testing.T) {
	for name, body := range map[string]string{
		"generic":                              `{"key":"files","class":"local","format":"generic","vulnerabilities":{"enabled":true}}`,
		"a package format without the adapter": `{"key":"p-local","class":"local","format":"pkgfmt","vulnerabilities":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := vulnValidate(t, body)
			if got := fieldsOf(err)["/vulnerabilities"]; got != errs.CodeNotApplicable {
				t.Fatalf("fields = %v (%v), want not_applicable at /vulnerabilities", fieldsOf(err), err)
			}
		})
	}
}

func TestR_VULN_002_TheMemberOnLocalAndRemoteRepositoriesOfAScannedFormat(t *testing.T) {
	for class, section := range map[string]string{"local": "", "remote": `,"remote":{"url":"https://registry.npmjs.org/"}`} {
		t.Run(class+" accepts a partial member and fills the defaults", func(t *testing.T) {
			d, err := vulnValidate(t, `{"key":"npm-x","class":"`+class+`","format":"npm"`+section+`,"vulnerabilities":{"blockMalicious":false}}`)
			if err != nil {
				t.Fatal(err)
			}
			want := vuln.DefaultSettings()
			want.BlockMalicious = false
			if d.Vulnerabilities == nil || *d.Vulnerabilities != want {
				t.Fatalf("member = %+v, want %+v", d.Vulnerabilities, want)
			}
		})
	}
	t.Run("a value that is not one of the choices answers 422 at the member's field", func(t *testing.T) {
		_, err := vulnValidate(t, `{"key":"npm-x","class":"local","format":"npm","vulnerabilities":{"onMissingData":"maybe","matchPublicAdvisories":"sometimes"}}`)
		f := fieldsOf(err)
		if f["/vulnerabilities/onMissingData"] != "invalid_value" || f["/vulnerabilities/matchPublicAdvisories"] != "invalid_value" {
			t.Fatalf("fields = %v (%v)", f, err)
		}
	})
	t.Run("a misspelt or unknown member is a malformed document, never dropped (R-REPO-008)", func(t *testing.T) {
		_, err := DecodeDocument([]byte(`{"key":"npm-x","class":"local","format":"npm","vulnerabilities":{"enable":false}}`))
		if e, ok := errs.As(err); !ok || e.Status() != 400 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the member is a reserved name: no format is called vulnerabilities", func(t *testing.T) {
		if !IsReservedMember("vulnerabilities") {
			t.Fatal("vulnerabilities is not reserved")
		}
	})
}

// The read model: the member with its defaults, the ecosystem from the catalog, and the policy of the download path.
func TestR_VULN_002_TheReadModelCarriesTheMemberAndThePolicy(t *testing.T) {
	cat := vulnCatalog()
	row := func(class, format, config string) q.Repository {
		return q.Repository{ID: 7, Key: "r", Class: class, Format: format, Config: []byte(config), Lifecycle: "active"}
	}
	t.Run("a scanned format without the member has the defaults", func(t *testing.T) {
		r, err := toRepository(row("remote", "npm", `{}`), "it", cat)
		if err != nil && r.Class != Remote {
			t.Logf("err = %v", err)
		}
		if got := r.VulnerabilitySettings(); got != vuln.DefaultSettings() || r.VulnerabilityEcosystem() != "npm" {
			t.Fatalf("settings %+v ecosystem %q", got, r.VulnerabilityEcosystem())
		}
		if p := r.VulnerabilityPolicy(true, vuln.EnforceBlocks); !p.Enabled || p.Ecosystem != "npm" || !p.BlockMalicious || p.OnMissingData != vuln.MissingAllow {
			t.Fatalf("policy %+v", p)
		}
	})
	t.Run("the stored member is read, and the installation's switches apply", func(t *testing.T) {
		r, _ := toRepository(row("local", "npm", `{"vulnerabilities":{"enabled":true,"matchPublicAdvisories":"always","blockMalicious":false,"onMissingData":"block"}}`), "it", cat)
		s := r.VulnerabilitySettings()
		if s.MatchPublicAdvisories != vuln.MatchAlways || s.BlockMalicious || s.OnMissingData != vuln.MissingBlock {
			t.Fatalf("settings %+v", s)
		}
		if p := r.VulnerabilityPolicy(false, vuln.EnforceBlocks); p.Enabled {
			t.Error("the installation switch off leaves the repository scanned")
		}
		if p := r.VulnerabilityPolicy(true, vuln.EnforceReport); !p.Enabled || p.Enforcement != vuln.EnforceReport {
			t.Errorf("policy %+v", p)
		}
	})
	t.Run("enabled false disables the policy", func(t *testing.T) {
		r, _ := toRepository(row("local", "npm", `{"vulnerabilities":{"enabled":false}}`), "it", cat)
		if p := r.VulnerabilityPolicy(true, vuln.EnforceBlocks); p.Enabled || p.QueryEnforce() {
			t.Errorf("policy %+v", p)
		}
	})
	for name, rw := range map[string]q.Repository{
		"a virtual repository":         row("virtual", "npm", `{}`),
		"a format without the adapter": row("local", "pkgfmt", `{}`),
		"generic":                      row("local", "generic", `{}`),
	} {
		t.Run(name+" is not scanned", func(t *testing.T) {
			r, _ := toRepository(rw, "it", cat)
			if r.VulnerabilityEcosystem() != "" || r.VulnerabilityPolicy(true, vuln.EnforceBlocks).Enabled {
				t.Errorf("ecosystem %q policy %+v", r.VulnerabilityEcosystem(), r.VulnerabilityPolicy(true, vuln.EnforceBlocks))
			}
		})
	}
}
