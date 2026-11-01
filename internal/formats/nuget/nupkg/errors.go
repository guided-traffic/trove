package nupkg

import (
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
)

// The reasons of a refusal, kept as the cause of the *errs.Error (see the
// package comment). The reasons of the .nuspec itself are those of package
// nuspec.
var (
	// ErrNotZip: the content is no ZIP archive or its structure is damaged.
	ErrNotZip = errors.New("the content is not a valid ZIP archive")
	// ErrMultiDisk: a split or multi-disk archive.
	ErrMultiDisk = errors.New("a ZIP archive in several parts is not accepted")
	// ErrEncrypted: an entry is encrypted.
	ErrEncrypted = errors.New("an entry of the archive is encrypted")
	// ErrLocalMismatch: a central-directory name differs from the local header
	// name, or the two readers of the archive would see different entries.
	ErrLocalMismatch = errors.New("the central directory and the local headers disagree")
	// ErrName: an entry name breaks the name rules.
	ErrName = errors.New("an entry name breaks the name rules")
	// ErrDuplicateName: two entries share a name compared case-insensitively.
	ErrDuplicateName = errors.New("two entries share a name")
	// ErrOverlap: the data of two entries overlap.
	ErrOverlap = errors.New("the data of two entries overlap")
	// ErrNuspecCount: no .nuspec, or several, at the archive root.
	ErrNuspecCount = errors.New("the archive does not hold exactly one .nuspec at its root")
	// ErrLimit: a bound on entries, sizes or compression ratio is crossed.
	ErrLimit = errors.New("a bound on the archive is crossed")
	// ErrEntry: an entry cannot be read as its header declares.
	ErrEntry = errors.New("an entry cannot be read as its header declares")
)

func invalid(reason error, detail string) *errs.Error { return nuspec.Invalid(reason, detail) }

func invalidName(detail string) *errs.Error { return invalid(ErrName, detail) }
