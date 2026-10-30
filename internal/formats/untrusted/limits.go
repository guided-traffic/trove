// Package untrusted holds the resource limits a format applies to untrusted
// package content (R-ARCH-028) and the bounded readers that enforce them: byte
// caps, JSON and XML decoders with depth and member limits, tar.gz and zip
// readers with entry counts, ratios and ambiguity checks, and the streaming
// counting pass. A format's Inspect, PrepareFill and Rewrite read untrusted
// bytes through these helpers and add only what is specific to its protocol
// (for npm, internal/formats/npm/tarball). It imports the standard library and
// errs only, and format packages may import it.
//
// A bound that is crossed is errs.TooLarge (413); a structure that two readers
// could resolve differently, or that the format forbids, is errs.Unprocessable
// (422). A format that has codes of its own maps both to them.
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
	// JSONMembers is the number of values (object members and array elements)
	// one JSON document may hold: the text of a document is small next to the
	// values a decoder builds from it (Q-ARCH-217).
	JSONMembers int
	// XMLDepth is the deepest element nesting an XML decoder accepts
	// (Q-ARCH-217).
	XMLDepth int
	// StreamBytes is what a pass that keeps nothing may inflate: the bytes of an
	// archive that are skipped or counted, not read into memory (Q-ARCH-217).
	StreamBytes int64
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
		JSONMembers:        2_000_000,
		XMLDepth:           64,
		StreamBytes:        2 << 30,
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
	l.JSONMembers = lowerInt(l.JSONMembers, by.JSONMembers)
	l.XMLDepth = lowerInt(l.XMLDepth, by.XMLDepth)
	l.StreamBytes = lowerInt64(l.StreamBytes, by.StreamBytes)
	l.DocumentBytes = lowerInt64(l.DocumentBytes, by.DocumentBytes)
	if by.Deadline > 0 && (l.Deadline <= 0 || by.Deadline < l.Deadline) {
		l.Deadline = by.Deadline
	}
	return l
}

// Bound returns the byte cap of a document held in memory under l: DocumentBytes
// when the core set it, else ManifestEntryBytes.
func (l Limits) Bound() int64 {
	if l.DocumentBytes > 0 {
		return l.DocumentBytes
	}
	return l.ManifestEntryBytes
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
