package npmstub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
)

// The control API lives below /-/stub/ (no package name can start there), needs the bearer token
// of Config.ControlToken and is not counted:
//
//	GET  /-/stub/counts             the counters (Counts as JSON)
//	GET  /-/stub/audits             the log of the advisory requests ({"requests": [AuditRequest]})
//	POST /-/stub/reset[?all=true]   zero the counters and the log; with all, also modes and changes to packages
//	GET  /-/stub/mode               the failure switches
//	POST /-/stub/mode               replace them (a Mode as JSON; {} clears)
//	POST /-/stub/publish            {"name","version","dependencies","tags"}: offer a version
//	POST /-/stub/drop               {"name","version"}: remove a version from its document
//	POST /-/stub/restore            {"name","version"}: list it again
const maxControlBody = 1 << 20

type versionRequest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
	Tags         []string          `json:"tags"`
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="npmstub-control"`)
		writeError(w, http.StatusUnauthorized, "control token required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)

	switch r.Method + " " + r.URL.Path {
	case "GET /-/stub/counts":
		writeJSON(w, s.Counts())
	case "GET /-/stub/audits":
		writeJSON(w, struct {
			Requests []AuditRequest `json:"requests"`
		}{s.Audits()})
	case "POST /-/stub/reset":
		if r.URL.Query().Get("all") == "true" {
			s.ResetAll()
		} else {
			s.Reset()
		}
		w.WriteHeader(http.StatusNoContent)
	case "GET /-/stub/mode":
		writeJSON(w, s.Mode())
	case "POST /-/stub/mode":
		var m Mode
		if !decodeStrict(w, r, &m) {
			return
		}
		if err := s.SetMode(m); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, s.Mode())
	case "POST /-/stub/publish":
		s.controlVersion(w, r, func(v versionRequest) error {
			return s.Publish(v.Name, v.Version, v.Dependencies, v.Tags...)
		})
	case "POST /-/stub/drop":
		s.controlVersion(w, r, func(v versionRequest) error { return s.Drop(v.Name, v.Version) })
	case "POST /-/stub/restore":
		s.controlVersion(w, r, func(v versionRequest) error { return s.Restore(v.Name, v.Version) })
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) controlVersion(w http.ResponseWriter, r *http.Request, act func(versionRequest) error) {
	var v versionRequest
	if !decodeStrict(w, r, &v) {
		return
	}
	if v.Name == "" || v.Version == "" {
		writeError(w, http.StatusBadRequest, "name and version are required")
		return
	}
	if err := act(v); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
