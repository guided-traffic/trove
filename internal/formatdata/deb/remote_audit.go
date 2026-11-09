package deb

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

// signatureEventEvery is the least time between two security events of one repository and release directory (R-DEB-020).
const signatureEventEvery = time.Hour

// signatureEvents remembers when each repository and directory last wrote the event. It is per replica: a second replica may write the
// event once more within the hour, which the rule's "at most once an hour" tolerates as one event per replica.
type signatureEvents struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (e *signatureEvents) due(key string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.last == nil {
		e.last = map[string]time.Time{}
	}
	if t, ok := e.last[key]; ok && now.Sub(t) < signatureEventEvery {
		return false
	}
	e.last[key] = now
	// Entries older than the period are of no use; the map is bounded by the repositories and directories that failed in an hour.
	for k, t := range e.last {
		if now.Sub(t) >= signatureEventEvery {
			delete(e.last, k)
		}
	}
	return true
}

// RecordSignatureInvalid writes the security audit event deb.upstream_signature_invalid for a release document of a remote that failed
// the verification against deb.upstreamKeys (R-DEB-020), at most once an hour for the repository and the release directory. reason is
// one of the labels of openpgp.Reason. It reports whether it wrote the event.
func (s remoteStore) RecordSignatureInvalid(ctx context.Context, repoID int64, project, repoKey, dir, reason string) bool {
	if !s.sig.due(strconv.FormatInt(repoID, 10)+"|"+dir, time.Now()) {
		return false
	}
	raw, err := json.Marshal(map[string]string{"dir": dir, "reason": reason})
	if err != nil {
		return false
	}
	name := repoKey
	if project != "" {
		name = project + "/" + repoKey
	}
	ev := audit.Event{
		Type: audit.EventDebUpstreamSignatureError, Outcome: audit.Failure,
		Subject: audit.Subject{Kind: "repository", ID: strconv.FormatInt(repoID, 10), Name: name}, Repository: name, Project: project, Details: raw,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	s.o.Recorder.Record(ctx, ev)
	return true
}
