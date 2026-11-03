package formats

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// restorable is a format with the optional restore hook.
type restorable struct{ fake }

func (restorable) OnItemsRestored(context.Context, Tx, ItemsEvent) error {
	return errors.New("restored")
}

// TestQ_ARCH_256_AnOptionalCapabilityIsFoundOnceAndRecordedAtRegister: a format that has the
// restore hook has it in its capabilities, one without it has none, and the registry answers
// from what Register recorded, so the core never asks the format value.
func TestQ_ARCH_256_AnOptionalCapabilityIsFoundOnceAndRecordedAtRegister(t *testing.T) {
	if c := CapabilitiesOf(fake{d: valid("cap-plain")}); c.ItemsRestorer != nil {
		t.Fatal("a format without the hook has one")
	}
	r := restorable{fake{d: valid("cap-restore")}}
	c := CapabilitiesOf(r)
	if c.ItemsRestorer == nil || c.ItemsRestorer.OnItemsRestored(context.Background(), nil, ItemsEvent{}) == nil {
		t.Fatalf("capabilities = %+v", c)
	}
	Register(r)
	Register(fake{d: valid("cap-registered-plain")})
	if CapabilitiesFor("cap-restore").ItemsRestorer == nil {
		t.Fatal("Register did not record the capability")
	}
	if CapabilitiesFor("cap-registered-plain").ItemsRestorer != nil || CapabilitiesFor("cap-unknown").ItemsRestorer != nil {
		t.Fatal("a capability the format lacks, or a format that is not registered, has one")
	}
}

// TestQ_ARCH_259_NotifyAreasAreSignalAreasOfTheCatalog: a descriptor may list the areas of
// catalog.FormatAreas and no other.
func TestQ_ARCH_259_NotifyAreasAreSignalAreasOfTheCatalog(t *testing.T) {
	d := valid("notify-ok")
	d.NotifyAreas = []string{catalog.MetadataChangedArea}
	if err := Validate(fake{d: d}); err != nil {
		t.Fatal(err)
	}
	d = valid("notify-bad")
	d.NotifyAreas = []string{"authz"}
	err := Validate(fake{d: d})
	if err == nil || !strings.Contains(err.Error(), `notification area "authz"`) {
		t.Fatalf("Validate = %v", err)
	}
	mustPanic(t, fake{d: d}, "notification area")
}

// scannable is a format with the optional vulnerability adapter (R-VULN-009, ADR-150).
type scannable struct{ fake }

func (scannable) Ecosystem() string                        { return "Test" }
func (scannable) AdvisoryName(n string) (string, bool)     { return n, n != "" }
func (scannable) AdvisoryVersion(v string) (string, bool)  { return v, v != "" }
func (scannable) CompareVersions(a, b string) (int, error) { return strings.Compare(a, b), nil }

// R-VULN-009 (contract test, ADR-150): CapabilitiesOf finds the vulnerability adapter, Register records it and derives the
// catalog entry's ecosystem from it; a format without it has neither.
func TestVulnerabilityAdapterIsFoundRecordedAndReachesTheCatalog(t *testing.T) {
	if c := CapabilitiesOf(fake{d: valid("vuln-plain")}); c.Vulnerability != nil {
		t.Fatal("a format without the adapter has one")
	}
	s := scannable{fake{d: valid("vuln-scanned")}}
	if c := CapabilitiesOf(s); c.Vulnerability == nil || c.Vulnerability.Ecosystem() != "Test" {
		t.Fatalf("capabilities = %+v", c)
	}
	if e := CatalogEntry(s); e.Ecosystem != "Test" {
		t.Errorf("the catalog entry's ecosystem = %q, want Test", e.Ecosystem)
	}
	if e := CatalogEntry(fake{d: valid("vuln-plain")}); e.Ecosystem != "" {
		t.Errorf("a format without the adapter has the ecosystem %q", e.Ecosystem)
	}
	Register(s)
	if CapabilitiesFor("vuln-scanned").Vulnerability == nil || CapabilitiesFor("vuln-unknown").Vulnerability != nil {
		t.Fatal("Register did not record the capability, or an unregistered format has one")
	}
}

// R-VULN-017: the host that has not wired the scanning reports every ecosystem as not complete and refuses reads with
// vulnerability_data_unavailable.
func TestNoVulnerabilityDataIsNotCompleteAndRefusesReads(t *testing.T) {
	d := NoVulnerabilityData()
	if st := d.State("npm"); st.Complete {
		t.Errorf("State = %+v", st)
	}
	ctx := context.Background()
	for name, err := range map[string]error{
		"ForNames":      func() error { _, err := d.ForNames(ctx, "npm", []string{"a"}); return err }(),
		"All":           func() error { _, err := d.All(ctx, "npm"); return err }(),
		"InternalNames": func() error { _, err := d.InternalNames(ctx, "npm", []string{"a"}); return err }(),
	} {
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeVulnerabilityDataUnavailable || e.Kind != errs.Unavailable {
			t.Errorf("%s: %v", name, err)
		}
	}
	e := advisory.Affected{Versions: []string{"1.0.0"}}
	if r := d.Affects(scannable{}, e, "1.0.0"); !r.Affected {
		t.Errorf("Affects = %+v", r)
	}
}
