package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all" // the formats the replica registers
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
)

// noRunner is a transaction runner that runs nothing: the data ports are built, not used.
type noRunner struct{}

func (noRunner) InTx(context.Context, func(meta.Tx) error) error { return nil }

type portStore interface{ Name() string }

type portImpl struct{}

func (portImpl) Name() string { return "p" }

// sidecar is a format that declares a data port.
type sidecar struct {
	formats.Base
}

func (sidecar) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts:           catalog.Facts{ID: "sidecar", Classes: []formats.Class{formats.Local}, Layout: "x", PatternSubject: formats.SubjectPath},
		ContractVersion: formats.ContractVersion, DataPort: reflect.TypeFor[portStore](),
	}
}
func (sidecar) Routes() []formats.Route { return nil }
func (sidecar) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (sidecar) Classify(formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.Content}
}

func (sidecar) PackageDocumentPath(string) (formats.Path, bool) {
	return formats.Path{}, false
}

// TestQ_ARCH_193_AMissingDataPortBindingIsAStartupError: the composition root binds
// the data port of every format that declares one; the replica does not start with
// a binding missing or wrong (typed wiring).
func TestQ_ARCH_193_AMissingDataPortBindingIsAStartupError(t *testing.T) {
	t.Run("the formats of this build pass with the bindings the root makes", func(t *testing.T) {
		ports, err := formatPorts(noRunner{}, audit.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bindFormatPorts(formats.List(), ports); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a format with a data port and no binding stops the start-up and names the format", func(t *testing.T) {
		_, err := bindFormatPorts([]formats.Format{sidecar{}}, nil)
		if err == nil || !strings.Contains(err.Error(), `"sidecar"`) || !strings.Contains(err.Error(), "none is bound") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a binding that does not implement the port stops the start-up", func(t *testing.T) {
		_, err := bindFormatPorts([]formats.Format{sidecar{}}, map[formats.ID]any{"sidecar": struct{}{}})
		if err == nil || !strings.Contains(err.Error(), "does not implement") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a binding that fits passes", func(t *testing.T) {
		if _, err := bindFormatPorts([]formats.Format{sidecar{}}, map[formats.ID]any{"sidecar": portImpl{}}); err != nil {
			t.Fatal(err)
		}
	})
}
