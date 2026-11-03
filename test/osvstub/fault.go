package osvstub

import "strings"

// Fault is what is switched on for one ecosystem (or "kev" for the catalog) through the control API. The zero value
// is a healthy source. Scope narrows Delay, Error, Truncate and Abort to one kind of object.
type Fault struct {
	// Scope is "", "all", "archive" (all.zip), "changes" (modified_id.csv) or "records" (<id>.json).
	Scope string `json:"scope,omitempty"`
	// DelayMS waits before answering.
	DelayMS int `json:"delayMs,omitempty"`
	// Error answers with this status (500, for example) and a short text body.
	Error int `json:"error,omitempty"`
	// Truncate cuts the body to half; length, ETag and digest describe the cut body, so the download succeeds and
	// the content is damaged (an archive without its central directory).
	Truncate bool `json:"truncate,omitempty"`
	// Abort declares the full length, sends half of the body and drops the connection: a transfer that fails.
	Abort bool `json:"abort,omitempty"`
	// DigestMismatch makes the md5 of x-goog-hash differ from the archive's bytes (the ETag stays right).
	DigestMismatch bool `json:"digestMismatch,omitempty"`
	// InvalidPercent replaces at least this share of the records, spread evenly over the sorted identifiers, by a
	// record whose modified is no timestamp, in the archive and when fetched singly.
	InvalidPercent int `json:"invalidPercent,omitempty"`
	// Invalid names records to replace in the same way.
	Invalid []string `json:"invalid,omitempty"`
	// ShrinkPercent leaves at least this share of the records out of the archive (the change file and the single
	// records still have them).
	ShrinkPercent int `json:"shrinkPercent,omitempty"`
	// NotFound lists records whose <id>.json answers 404 while the change file still names them.
	NotFound []string `json:"notFound,omitempty"`
	// NoRange turns Range support off: Range is ignored (200 with the full body) and Accept-Ranges is not sent.
	NoRange bool `json:"noRange,omitempty"`
}

// Kinds of object the stub serves.
const (
	KindArchive    = "archive"
	KindChanges    = "changes"
	KindRecord     = "record"
	KindEcosystems = "ecosystems"
	KindKEV        = "kev"
	KindOther      = "other"
)

func (f Fault) scoped(kind string) bool {
	switch strings.ToLower(f.Scope) {
	case "", "all":
		return true
	case "records":
		return kind == KindRecord
	}
	return strings.ToLower(f.Scope) == kind
}

func (f Fault) isZero() bool {
	return f.Scope == "" && f.DelayMS == 0 && f.Error == 0 && !f.Truncate && !f.Abort && !f.DigestMismatch &&
		f.InvalidPercent == 0 && len(f.Invalid) == 0 && f.ShrinkPercent == 0 && len(f.NotFound) == 0 && !f.NoRange
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// spread picks count indexes out of n, evenly spread and distinct (count is capped at n).
func spread(n, count int) map[int]bool {
	count = min(count, n)
	out := make(map[int]bool, count)
	for i := range count {
		out[(2*i+1)*n/(2*count)] = true
	}
	return out
}

// share is ceil(n*percent/100).
func share(n, percent int) int {
	if percent <= 0 || n == 0 {
		return 0
	}
	return min(n, (n*percent+99)/100)
}
