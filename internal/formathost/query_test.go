package formathost

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The wrapper that turns a formats.Query into the engine's request needs a repository with
// parsed remote settings, which only the repository snapshot builds; its test runs against the
// real snapshot in test/integration/proxy (TestR_ARCH_013_QueryThroughTheHost).

func TestR_ARCH_013_AFormatThatEmbedsTheDefaultsRefusesToBuildAQuery(t *testing.T) {
	if _, err := (formats.RemoteBase{}).UpstreamQuery(t.Context(), formats.RemoteContext{}, formats.Query{}); errs.KindOf(err) != errs.Internal {
		t.Fatalf("err = %v", err)
	}
}

func TestR_ARCH_013_AHostWithoutTheEngineRefusesAQuery(t *testing.T) {
	if _, err := (unsupportedRemote{}).Query(t.Context(), nil, formats.Query{}); errs.KindOf(err) != errs.Internal {
		t.Fatalf("err = %v", err)
	}
	if err := (unsupportedRemote{}).Gate(t.Context(), nil, formats.Path{}); errs.KindOf(err) != errs.Internal {
		t.Fatalf("a Host without the engine let an artifact pass its gate: %v", err)
	}
}
