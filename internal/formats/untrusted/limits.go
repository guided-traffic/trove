// Package untrusted holds the resource limits a format applies to untrusted
// package content (R-ARCH-028): the bounds of Inspect, of a rewritten document and
// of every decoder that reads an upload or an upstream response. It imports the
// standard library and errs only, and format packages may import it.
package untrusted

import "time"

// Limits are the bounds one read of untrusted content runs under. A zero field
// is "not set"; [DefaultLimits] gives the defaults of R-ARCH-028 and
// [Limits.Tighten] only ever lowers them.
type Limits struct {
	// Entries is the number of archive entries a reader accepts.
	Entries int
	// UncompressedBytes is what a reader reads into memory in total.
	UncompressedBytes int64
	// Ratio is the largest compression ratio of one entry.
	Ratio int
	// ManifestEntryBytes bounds one manifest entry.
	ManifestEntryBytes int64
	// Deadline bounds the whole inspection.
	Deadline time.Duration
	// JSONDepth is the deepest nesting a JSON decoder accepts (Q-ARCH-217).
	JSONDepth int
	// DocumentBytes bounds a document held in memory: the smaller of the format's
	// MaxDocumentBytes and the proxy's metadata size limit. The core sets it.
	DocumentBytes int64
}

// DefaultLimits returns the defaults of R-ARCH-028.
func DefaultLimits() Limits {
	return Limits{
		Entries:            10_000,
		UncompressedBytes:  64 << 20,
		Ratio:              100,
		ManifestEntryBytes: 1 << 20,
		Deadline:           30 * time.Second,
		JSONDepth:          64,
	}
}

// Tighten returns l with every field that by sets to a smaller value than l
// holds replaced by it. It never loosens: a format may lower a default, not
// raise it. A field of l that is unset takes the value of by.
func (l Limits) Tighten(by Limits) Limits {
	l.Entries = lowerInt(l.Entries, by.Entries)
	l.UncompressedBytes = lowerInt64(l.UncompressedBytes, by.UncompressedBytes)
	l.Ratio = lowerInt(l.Ratio, by.Ratio)
	l.ManifestEntryBytes = lowerInt64(l.ManifestEntryBytes, by.ManifestEntryBytes)
	l.JSONDepth = lowerInt(l.JSONDepth, by.JSONDepth)
	l.DocumentBytes = lowerInt64(l.DocumentBytes, by.DocumentBytes)
	if by.Deadline > 0 && (l.Deadline <= 0 || by.Deadline < l.Deadline) {
		l.Deadline = by.Deadline
	}
	return l
}

func lowerInt(cur, by int) int {
	if by > 0 && (cur <= 0 || by < cur) {
		return by
	}
	return cur
}

func lowerInt64(cur, by int64) int64 {
	if by > 0 && (cur <= 0 || by < cur) {
		return by
	}
	return cur
}
