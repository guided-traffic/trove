package nupkg

import (
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
)

// The defaults of Limits: the nuget.archive settings of R-NUGET-005 and the
// bounds of R-ARCH-028 for what is read into memory.
const (
	DefaultMaxEntries           = 65535
	DefaultMaxUncompressedBytes = 4 << 30
	DefaultMaxRatio             = 100
	DefaultMaxNuspecBytes       = nuspec.DefaultMaxBytes
	DefaultMaxReadBytes         = 64 << 20
	DefaultDeadline             = 30 * time.Second

	// maxCentralBytes bounds the central directory itself, so that entries with
	// large extra fields or comments cannot make the scan read gigabytes.
	maxCentralBytes = 64 << 20
	// maxNameTotal bounds the bookkeeping of the names.
	maxNameTotal = 16 << 20
	// ratioSlack is added to the allowance of the ratio check: a tiny entry
	// can legitimately compress by more than the ratio.
	ratioSlack = 4 << 10
)

// Limits bound what one archive may cost. A zero field takes its default. The
// bounds on what is read into memory (MaxRatio, MaxNuspecBytes, MaxReadBytes,
// Deadline) can only be tightened (R-ARCH-028); MaxEntries and
// MaxUncompressedBytes bound what the central directory may declare and are the
// nuget.archive settings.
type Limits struct {
	// MaxEntries caps the number of entries (nuget.archive.max_entries).
	MaxEntries int
	// MaxUncompressedBytes caps the sum of the declared sizes of all entries
	// (nuget.archive.max_uncompressed_bytes). No entry other than the ones the
	// caller reads is inflated.
	MaxUncompressedBytes int64
	// MaxRatio is the compression ratio an entry read into memory may show
	// (nuget.archive.max_compression_ratio).
	MaxRatio int
	// MaxNuspecBytes caps the .nuspec (nuget.archive.max_nuspec_bytes).
	MaxNuspecBytes int64
	// MaxReadBytes caps what one Archive reads into memory in total.
	MaxReadBytes int64
	// Deadline bounds Open and Inspect; the caller's context applies as well.
	Deadline time.Duration
}

// WithDefaults returns l with every unset field at its default and the
// tighten-only fields clamped to theirs.
func (l Limits) WithDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.MaxUncompressedBytes <= 0 {
		l.MaxUncompressedBytes = DefaultMaxUncompressedBytes
	}
	if l.MaxRatio <= 0 || l.MaxRatio > DefaultMaxRatio {
		l.MaxRatio = DefaultMaxRatio
	}
	if l.MaxNuspecBytes <= 0 || l.MaxNuspecBytes > DefaultMaxNuspecBytes {
		l.MaxNuspecBytes = DefaultMaxNuspecBytes
	}
	if l.MaxReadBytes <= 0 || l.MaxReadBytes > DefaultMaxReadBytes {
		l.MaxReadBytes = DefaultMaxReadBytes
	}
	if l.Deadline <= 0 || l.Deadline > DefaultDeadline {
		l.Deadline = DefaultDeadline
	}
	return l
}
