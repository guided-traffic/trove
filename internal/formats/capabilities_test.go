package formats

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
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
