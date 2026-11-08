package debstub

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// The control API lives below /-/stub/ on any path, needs the bearer token of Config.ControlToken and is not counted:
//
//	GET  /-/stub/counts                 the counters (Counts as JSON)
//	GET  /-/stub/inflight               {"open","max","requests"}: the requests open now and the most open at once since the last reset
//	POST /-/stub/reset[?all=true]       zero the counters; with all, also the modes, the throttle and every archive's generations
//	GET  /-/stub/mode                   the switches
//	POST /-/stub/mode                   replace them (a Mode as JSON; {} clears)
//	GET  /-/stub/throttle               the rate-limit switch with the requests it counted and throttled (ThrottleState)
//	POST /-/stub/throttle               replace it (a Throttle as JSON; {} clears)
//	GET  /-/stub/archives               the archives with their generation, date and Release digest
//	POST /-/stub/generation             {"archive"}: publish a new release of the archive; answers {"generation","date"}
//	GET  /-/stub/release?archive=       the unsigned Release text of the archive under the mode in force
//	GET  /-/stub/keys                   the test keys with fingerprints and public certificates
//	GET  /-/stub/keyring?names=a,b      the named public certificates as one armored block
//	GET  /-/stub/files                  the package and source files with size and SHA-256
const maxControlBody = 1 << 20

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="debstub-control"`)
		writeText(w, http.StatusUnauthorized, "control token required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)

	switch r.Method + " " + r.URL.Path {
	case "GET /-/stub/counts":
		writeJSON(w, http.StatusOK, s.Counts())
	case "GET /-/stub/inflight":
		writeJSON(w, http.StatusOK, s.Inflight())
	case "POST /-/stub/reset":
		if r.URL.Query().Get("all") == "true" {
			s.ResetAll()
		} else {
			s.Reset()
		}
		w.WriteHeader(http.StatusNoContent)
	case "GET /-/stub/mode":
		writeJSON(w, http.StatusOK, s.Mode())
	case "POST /-/stub/mode":
		var m Mode
		if !decodeStrict(w, r, &m) {
			return
		}
		if err := s.SetMode(m); err != nil {
			writeText(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.Mode())
	case "GET /-/stub/throttle":
		writeJSON(w, http.StatusOK, s.ThrottleState())
	case "POST /-/stub/throttle":
		var t Throttle
		if !decodeStrict(w, r, &t) {
			return
		}
		if err := s.SetThrottle(t); err != nil {
			writeText(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.ThrottleState())
	case "GET /-/stub/archives":
		writeJSON(w, http.StatusOK, s.Archives())
	case "POST /-/stub/generation":
		var req struct {
			Archive string `json:"archive"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		date, err := s.NewGeneration(req.Archive)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrUnknown) {
				status = http.StatusNotFound
			}
			writeText(w, status, err.Error())
			return
		}
		n, _, _ := s.Generation(req.Archive)
		writeJSON(w, http.StatusOK, map[string]any{"generation": n, "date": date.Format(releaseTimeFormat)})
	case "GET /-/stub/release":
		text, err := s.Release(r.URL.Query().Get("archive"))
		if err != nil {
			writeText(w, http.StatusNotFound, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(text) //nolint:gosec // a text/plain test document of the stub's own making
	case "GET /-/stub/keys":
		writeJSON(w, http.StatusOK, s.Keys())
	case "GET /-/stub/keyring":
		ring, err := s.Keyring(strings.Split(r.URL.Query().Get("names"), ",")...)
		if err != nil {
			writeText(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(ring)
	case "GET /-/stub/files":
		writeJSON(w, http.StatusOK, s.Files())
	default:
		writeText(w, http.StatusNotFound, "not found")
	}
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeText(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// gunzip decompresses a gzip stream the stub made itself.
func gunzip(data []byte) []byte {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	out, _ := io.ReadAll(zr)
	return out
}
