package archtest

import "testing"

// TestFormatAllowlistSplit: the contract package, the conformance kit and a format
// package have three allowlists (R-ARCH-008, Q-ARCH-189, Q-ARCH-216). The contract
// spells core types for the formats, so only it imports them; a format reaches the
// core through the aliases and Host.
func TestFormatAllowlistSplit(t *testing.T) {
	t.Run("R-ARCH-008 the contract imports the subject, the repository model, the transaction type and the content headers", func(t *testing.T) {
		wantClean(t, CheckModuleImportBans(mod, []Package{
			pkg("internal/formats", "internal/authz", "internal/repository", "internal/meta", "internal/httpserver/content",
				"internal/formats/catalog", "internal/formats/untrusted", "internal/errs"),
		}))
	})
	t.Run("R-ARCH-008 a format importing internal/authz is named", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/npm", "internal/authz")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/authz")
	})
	t.Run("R-ARCH-008 a format importing internal/meta is named: it spells formats.Tx", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/npm", "internal/meta")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/meta")
	})
	t.Run("R-ARCH-008 a format importing the repository model or the content headers is named", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/npm", "internal/repository", "internal/httpserver/content")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/repository")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/httpserver/content")
	})
	t.Run("R-ARCH-008 a format may import the untrusted-content limits", func(t *testing.T) {
		wantClean(t, CheckModuleImportBans(mod, []Package{pkg("internal/formats/npm", "internal/formats/untrusted", "internal/formats")}))
	})
	t.Run("R-ARCH-008 the contract importing the object store is named", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats", "internal/blob", "internal/meta/postgres")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats", "internal/blob")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats", "internal/meta/postgres")
	})
	t.Run("R-ARCH-010 the kit imports what the contract imports and its own subpackages", func(t *testing.T) {
		wantClean(t, CheckModuleImportBans(mod, []Package{
			pkg("internal/formats/formattest", "internal/formats", "internal/authz", "internal/repository", "internal/meta",
				"internal/httpserver/content", "internal/formats/untrusted", "internal/formats/formattest/testformat"),
			pkg("internal/formats/formattest/testformat", "internal/formats", "internal/formats/formattest"),
		}))
	})
	t.Run("R-ARCH-010 the kit importing meta/postgres or a concrete format is named", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/formattest", "internal/meta/postgres", "internal/formats/npm", "internal/blob")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/formattest", "internal/meta/postgres")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/formattest", "internal/formats/npm")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/formattest", "internal/blob")
	})
}

func TestDispatcherAndHostBans(t *testing.T) {
	t.Run("R-ARCH-002 the dispatcher and the host importing the management API name the edge", func(t *testing.T) {
		vs := CheckModuleBans(mod, []Package{
			pkg("internal/api/dispatch", "internal/api/v1"),
			pkg("internal/formathost", "internal/api/v1"),
		})
		wantEdge(t, vs, "R-ARCH-002", "internal/api/dispatch", "internal/api/v1")
		wantEdge(t, vs, "R-ARCH-002", "internal/formathost", "internal/api/v1")
	})
	t.Run("Q-ARCH-193 the host importing a format's data package names the edge", func(t *testing.T) {
		vs := CheckModuleBans(mod, []Package{pkg("internal/formathost", "internal/formatdata/npm")})
		wantEdge(t, vs, "R-ARCH-002", "internal/formathost", "internal/formatdata/npm")
	})
	t.Run("R-ARCH-002 the dispatcher and the host sharing the edge package are clean", func(t *testing.T) {
		wantClean(t, CheckModuleBans(mod, []Package{
			pkg("internal/api/dispatch", "internal/api/edge", "internal/formathost", "internal/formats"),
			pkg("internal/formathost", "internal/api/edge", "internal/packages", "internal/items"),
		}))
	})
	t.Run("R-ARCH-002 the new packages have layers", func(t *testing.T) {
		if l, ok := layerOf("internal/packages"); !ok || l != layerDomain {
			t.Fatalf("internal/packages: %v %v", l, ok)
		}
		if l, ok := layerOf("internal/formathost"); !ok || l != layerEdge {
			t.Fatalf("internal/formathost: %v %v", l, ok)
		}
		wantEdge(t, CheckLayers(mod, []Package{pkg("internal/packages", "internal/formathost")}), "R-ARCH-002", "internal/packages", "internal/formathost")
	})
}
