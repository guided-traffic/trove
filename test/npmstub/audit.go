package npmstub

import (
	"bytes"
	"compress/gzip"
	_ "embed" // the recorded advisories
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// The advisory endpoint (R-NPM-038): POST /-/npm/v1/security/advisories/bulk takes the bulk
// body npm, pnpm and Yarn send ({"<name>": ["<version>", ...]}, plain or gzip), logs the names
// it was asked about, and answers every recorded advisory of every name it was asked about, in
// the shape of the public registry. Which installed versions an advisory affects is the
// client's to decide from vulnerable_versions, as it is against the public registry.

// auditPath is the endpoint below the stub's root.
const auditPath = "/-/npm/v1/security/advisories/bulk"

// maxAuditBody bounds what the stub reads of an audit body, decompressed.
const maxAuditBody = 64 << 20

// AuditRequest is one bulk advisory request the stub received: the names and versions, the
// credential scheme (as in the counters, never the credential) and the content coding.
type AuditRequest struct {
	Names    map[string][]string `json:"names"`
	Auth     string              `json:"auth"`
	Encoding string              `json:"encoding"`
}

// Asked reports whether any request of the log asked about a name.
func Asked(log []AuditRequest, name string) bool {
	for _, r := range log {
		if _, ok := r.Names[name]; ok {
			return true
		}
	}
	return false
}

// The advisories the stub answers: synthetic, with ids and addresses no real advisory has.
//
//go:embed testdata/advisories.json
var advisoriesJSON []byte

// Advisories returns the recorded advisories by package name.
func Advisories() map[string][]json.RawMessage {
	var out map[string][]json.RawMessage
	if err := json.Unmarshal(advisoriesJSON, &out); err != nil {
		panic("npmstub: testdata/advisories.json: " + err.Error())
	}
	return out
}

// Audits returns the log of the audit requests, oldest first.
func (s *Server) Audits() []AuditRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(make([]AuditRequest, 0, len(s.audits)), s.audits...)
}

func (s *Server) serveAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	enc := strings.ToLower(r.Header.Get("Content-Encoding"))
	var rd io.Reader = http.MaxBytesReader(w, r.Body, maxAuditBody)
	if enc == "gzip" {
		zr, err := gzip.NewReader(rd)
		if err != nil {
			writeError(w, http.StatusBadRequest, "the body is no gzip stream")
			return
		}
		rd = zr
	}
	raw, err := io.ReadAll(io.LimitReader(rd, maxAuditBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the body cannot be read")
		return
	}
	var names map[string][]string
	if err := json.Unmarshal(raw, &names); err != nil || names == nil {
		writeError(w, http.StatusBadRequest, "the body is no object of names and versions")
		return
	}
	s.mu.Lock()
	s.audits = append(s.audits, AuditRequest{Names: names, Auth: authScheme(r.Header.Get("Authorization")), Encoding: enc})
	s.mu.Unlock()

	known := Advisories()
	keys := make([]string, 0, len(names))
	for n := range names {
		if _, ok := known[n]; ok {
			keys = append(keys, n)
		}
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, n := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(n)
		v, _ := json.Marshal(known[n])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b.Bytes())
}
