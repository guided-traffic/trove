package sync

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The key that sets the size bound of one record; the log line and the problem of a run name it.
const recordSizeKey = "vulnerabilities.sources.osv.max_record_size"

// maxRejectLines is the number of rejections of one run and ecosystem that are logged one by one (Q-VULN-119).
const maxRejectLines = 100

// maxName is the length, in bytes, at which a name that came from a source is cut before it is logged or put into a problem.
const maxName = 80

// rejection is why one record was rejected: a bound of its entry or a rule of R-VULN-006 (Q-VULN-116).
type rejection struct {
	// reason is a sentence without the entry's name.
	reason string
	// size is the declared size of the entry or fetched body; 0 when not known.
	size int64
	// limit and key are set for the size bound only; the ratio bound is fixed.
	limit int64
	key   string
	// validation is true for a record that failed a rule of R-VULN-006 or a row of the change file that does not parse, false
	// for a bound of one entry.
	validation bool
}

// sizeRejection is the rejection of an entry above max_record_size.
func sizeRejection(size, limit int64) *rejection {
	return &rejection{reason: fmt.Sprintf("larger than %d bytes", limit), size: size, limit: limit, key: recordSizeKey}
}

// ratioRejection is the rejection of an entry that compresses by more than the fixed ratio.
func ratioRejection(size int64) *rejection {
	return &rejection{reason: fmt.Sprintf("compresses by more than %d:1", maxRatio), size: size}
}

// ruleRejection is the rejection of a record that fails a rule of R-VULN-006; err wraps osv.ErrRejected.
func ruleRejection(err error) *rejection {
	return &rejection{reason: clean(strings.TrimPrefix(err.Error(), osv.ErrRejected.Error()+": "), 200), validation: true}
}

// rejectLog logs the rejections of one run of one ecosystem: the first maxRejectLines one by one, then one line that counts the
// rest (Q-VULN-119). The count includes the rejections that were counted without a line of their own.
type rejectLog struct {
	s   *Service
	eco string
	n   int
	// bound, invalid and malicious are the counts of the metric (Q-VULN-122): the rejections by a bound of one entry, the
	// ones by validation, and the rejections, of either cause, whose entry is named like a malicious-package record (MAL-).
	bound, invalid, malicious int
}

func (s *Service) newRejectLog(eco string) *rejectLog { return &rejectLog{s: s, eco: eco} }

// add logs one rejected record: entry is its name in the archive or the name it would have, id its identifier when known.
func (l *rejectLog) add(entry, id string, r *rejection) {
	l.n++
	if r.validation {
		l.invalid++
	} else {
		l.bound++
	}
	// A record named like a malicious-package record counts here whatever rejected it: a MAL- record that is not applied is the
	// case the critical alert is for (Q-VULN-122).
	if strings.HasPrefix(entry, "MAL-") {
		l.malicious++
	}
	if l.n > maxRejectLines {
		return
	}
	attrs := []any{"source", store.SourceOSV, "ecosystem", l.eco, "entry", escaped(entry)}
	if id != "" {
		attrs = append(attrs, "id", escaped(id))
	}
	if r.size > 0 {
		attrs = append(attrs, "size", r.size)
	}
	if r.key != "" {
		attrs = append(attrs, "limit", r.limit, "key", r.key)
	}
	attrs = append(attrs, "reason", r.reason)
	l.s.log.Warn("an advisory record was rejected", attrs...)
}

// counted counts n rejections by validation that get no line of their own.
func (l *rejectLog) counted(n int) {
	l.n += n
	l.invalid += n
}

// finish adds the rejections to the counters of the metric, whatever ended the run, and writes the line that counts the
// rejections beyond the ones logged one by one.
func (l *rejectLog) finish() {
	l.s.count(store.SourceOSV, l.eco, vmetrics.RecordRejected, l.bound)
	l.s.count(store.SourceOSV, l.eco, vmetrics.RecordInvalid, l.invalid)
	l.s.count(store.SourceOSV, l.eco, vmetrics.RecordRejectedMalicious, l.malicious)
	if l.n > maxRejectLines {
		l.s.log.Warn("further advisory records were rejected and are not logged one by one",
			"source", store.SourceOSV, "ecosystem", l.eco, "count", l.n-maxRejectLines)
	}
}

// cut keeps a name from a source from filling a log line or a problem: at most maxName bytes, on a character boundary.
func cut(s string) string {
	if len(s) <= maxName {
		return s
	}
	i := maxName
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "..."
}

// quoted is a name from a source as a Go string literal with the quotes: control, format and bidirectional characters and
// invalid bytes are escaped.
func quoted(name string) string { return strconv.Quote(cut(name)) }

// escaped is quoted without the quotes, for a log attribute whose encoder quotes what needs it.
func escaped(name string) string {
	q := quoted(name)
	return q[1 : len(q)-1]
}

// clean removes the control and format characters of a text that came out of a source's record and cuts it.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		i := max
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		s = s[:i] + "..."
	}
	return s
}

// entryID is the identifier an archive entry is named after: the name without ".json" when it has no "/" and satisfies the
// identifier rule of R-VULN-006. A rejected entry with such a name keeps the stored record of that identifier (Q-VULN-117).
func entryID(name string) (string, bool) {
	id, ok := strings.CutSuffix(name, ".json")
	if !ok || strings.Contains(id, "/") || !osv.ValidID(id) {
		return "", false
	}
	return id, true
}

// isRejected reports an error of osv.Decode that is a rejected record rather than a fault.
func isRejected(err error) bool { return errors.Is(err, osv.ErrRejected) }
