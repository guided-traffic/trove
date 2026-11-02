package symbols_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/symbols"
)

const nuspec = `<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>` +
	`<id>A.B</id><version>1.0.0</version><authors>x</authors><description>d</description>` +
	`<packageTypes><packageType name="SymbolsPackage"/></packageTypes></metadata></package>`

func open(t *testing.T, entries map[string]string) *nupkg.Archive {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	all := map[string]string{"A.B.nuspec": nuspec}
	for k, v := range entries {
		all[k] = v
	}
	for name, body := range all {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := nupkg.Open(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()), nupkg.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestR_NUGET_020_ThePdbKindIsDecidedByItsSignature: BSJB is a Portable PDB, the MSF 7.00
// banner a Windows PDB, anything else (a prefix of either included) is neither.
func TestR_NUGET_020_ThePdbKindIsDecidedByItsSignature(t *testing.T) {
	for _, c := range []struct {
		head string
		kind symbols.Kind
		ok   bool
	}{
		{"BSJB\x01\x00", symbols.Portable, true},
		{"Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00", symbols.Windows, true},
		{"Microsoft C/C++ MSF 7.0", "", false},
		{"BSJ", "", false},
		{"", "", false},
		{"MZ\x90\x00", "", false},
		{" BSJB", "", false},
	} {
		kind, ok := symbols.Sniff([]byte(c.head))
		if kind != c.kind || ok != c.ok {
			t.Errorf("Sniff(%q) = %q, %v; want %q, %v", c.head, kind, ok, c.kind, c.ok)
		}
	}
}

// TestR_NUGET_020_ASymbolPackageHoldsOnlyTheAllowedEntries: the extensions nuget.org accepts
// pass, case-insensitively and in any folder; every other extension, none at all included,
// is refused with the entry named.
func TestR_NUGET_020_ASymbolPackageHoldsOnlyTheAllowedEntries(t *testing.T) {
	good := open(t, map[string]string{
		"lib/net8.0/A.B.pdb": "BSJB....", "lib/net48/A.B.PDB": "Microsoft C/C++ MSF 7.00\r\n\x1aDS", "doc/A.B.xml": "<x/>",
		"package/services/metadata/core-properties/1.psmdcp": "x", "_rels/.rels": "x", ".signature.p7s": "x", "docs/": "",
	})
	sum, err := symbols.Validate(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Portable != 1 || sum.Windows != 1 || sum.Files() != 2 {
		t.Errorf("summary = %+v", sum)
	}
	for _, name := range []string{"lib/A.dll", "lib/A.DLL", "src/A.cs", "LICENSE", "lib/exe", "tools/run.ps1", "a.pdb.txt"} {
		_, err := symbols.Validate(context.Background(), open(t, map[string]string{name: "x"}))
		if !errors.Is(err, symbols.ErrEntryType) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want ErrEntryType naming it", name, err)
		}
	}
}

// TestR_NUGET_020_EveryPdbIsChecked: one bad .pdb among good ones refuses the package, a
// .pdb shorter than a signature included.
func TestR_NUGET_020_EveryPdbIsChecked(t *testing.T) {
	for name, body := range map[string]string{"text": "hello world", "empty": "", "short": "BS"} {
		a := open(t, map[string]string{"a.pdb": "BSJB....", "b/" + name + ".pdb": body})
		if _, err := symbols.Validate(context.Background(), a); !errors.Is(err, symbols.ErrSignature) || !strings.Contains(err.Error(), name+".pdb") {
			t.Errorf("%s: err = %v, want ErrSignature naming the file", name, err)
		}
	}
	_, err := symbols.Validate(context.Background(), open(t, map[string]string{"a.pdb": "no"}))
	if !nupkg.IsRefusal(err) {
		t.Errorf("err = %v, want a 400 refusal of the package", err)
	}
}

// TestR_NUGET_020_ValidationStopsWithItsContext: a cancelled context is its own error, not a
// refusal of the package.
func TestR_NUGET_020_ValidationStopsWithItsContext(t *testing.T) {
	a := open(t, map[string]string{"a.pdb": "BSJB...."})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := symbols.Validate(ctx, a)
	if !errors.Is(err, context.Canceled) || nupkg.IsRefusal(err) {
		t.Errorf("err = %v", err)
	}
}
