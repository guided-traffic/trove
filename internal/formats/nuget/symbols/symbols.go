// Package symbols validates the content of a NuGet symbol package (.snupkg, R-NUGET-020):
// the extensions its entries may have and the file signature of every .pdb. It reads
// through the opened archive of internal/formats/nuget/nupkg and takes nothing out of
// it: a symbol push validates and stores, it extracts nothing (Q-NUGET-11). The package is
// a leaf: standard library, internal/errs and its siblings nuspec and nupkg.
//
// A refusal is a 400 *errs.Error with the code nuspec.CodeInvalidPackage and one of the
// Err reasons below as its cause, so a caller and a test tell the cases apart with
// errors.Is.
package symbols

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
)

// Kind is the format of a .pdb file.
type Kind string

const (
	// Portable is a Portable PDB: the metadata root signature "BSJB".
	Portable Kind = "portable"
	// Windows is a Windows PDB: an MSF 7.00 container.
	Windows Kind = "windows"
)

// HeadBytes is how much of a .pdb is read to decide its kind.
const HeadBytes = 32

const (
	portableMagic = "BSJB"
	windowsMagic  = "Microsoft C/C++ MSF 7.00"
)

// The reasons of a refusal.
var (
	// ErrNotSymbols: the .nuspec does not declare the package type SymbolsPackage. A legacy
	// .symbols.nupkg is one of these.
	ErrNotSymbols = errors.New("the package does not declare the package type SymbolsPackage")
	// ErrEntryType: an entry has an extension a symbol package may not hold.
	ErrEntryType = errors.New("an entry of the symbol package has an extension that is not allowed")
	// ErrSignature: a .pdb is neither a Portable PDB nor a Windows PDB.
	ErrSignature = errors.New("a .pdb file is neither a Portable PDB nor a Windows PDB")
)

// allowed reports whether a lower-cased extension is one nuget.org accepts in a symbol package.
func allowed(ext string) bool {
	switch ext {
	case ".pdb", ".nuspec", ".xml", ".psmdcp", ".rels", ".p7s":
		return true
	}
	return false
}

// Sniff decides the kind of a .pdb by the start of its content.
func Sniff(head []byte) (Kind, bool) {
	switch {
	case bytes.HasPrefix(head, []byte(portableMagic)):
		return Portable, true
	case bytes.HasPrefix(head, []byte(windowsMagic)):
		return Windows, true
	}
	return "", false
}

// Summary is what a valid symbol package holds.
type Summary struct {
	// Portable and Windows count the .pdb files of each kind.
	Portable, Windows int
}

// Files is the number of .pdb files.
func (s Summary) Files() int { return s.Portable + s.Windows }

// Validate checks the entries of an opened symbol package: every file entry has one of the
// allowed extensions, and every .pdb starts with the signature of a Portable PDB or of a
// Windows PDB. The first entry that breaks a rule is named in the refusal. A cancelled
// context returns its own error.
func Validate(ctx context.Context, a *nupkg.Archive) (Summary, error) {
	var s Summary
	for _, e := range a.Entries() {
		if e.Dir {
			continue
		}
		ext := strings.ToLower(path.Ext(e.Path))
		if !allowed(ext) {
			return s, nuspec.Invalid(ErrEntryType, fmt.Sprintf(
				"The symbol package holds %q: a symbol package may hold only .pdb, .nuspec, .xml, .psmdcp, .rels and .p7s files; assemblies belong to the package.", clip(e.Name)))
		}
		if ext != ".pdb" {
			continue
		}
		head, err := a.Head(ctx, e, HeadBytes)
		if err != nil {
			return s, err
		}
		switch kind, ok := Sniff(head); {
		case !ok:
			return s, nuspec.Invalid(ErrSignature, fmt.Sprintf(
				"%q is neither a Portable PDB nor a Windows PDB (MSF 7.00) by its file signature.", clip(e.Name)))
		case kind == Portable:
			s.Portable++
		default:
			s.Windows++
		}
	}
	return s, nil
}

// clip bounds a name that goes into a message.
func clip(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "..."
	}
	return s
}
