package oidcstub

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

const maxControlBody = 1 << 20

// ControlHandler is the control API, for the end-to-end tests that cannot call the Go methods.
// It needs the bearer token of Config.ControlToken (without one it answers 404 everywhere), is
// never counted and is not affected by faults. All bodies are JSON.
//
//	GET  /counts                  the counters (Counts)
//	POST /reset[?all=true]        zero the counters; with all, also heal faults, restore the caching
//	                              and replace the keys by a fresh default key set
//	GET  /info                    {"issuer", "discovery_url", "jwks_url", "keys": [Info]}
//	GET  /keys                    [Info]
//	POST /keys                    add a key (KeyOptions; {} is a default RSA-2048 key): the rotation
//	POST /keys/remove             {"kid": "..."}: take the key out of the JWKS; 404 when none matches
//	POST /mint                    MintOptions in, {"token": "..."} out
//	GET  /fault, POST /fault      the fault set (Fault; {} heals)
//	GET  /cache, POST /cache      the JWKS caching (Cache)
func (s *Server) ControlHandler() http.Handler {
	return http.HandlerFunc(s.serveControl)
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="oidcstub-control"`)
		writeError(w, http.StatusUnauthorized, "control token required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)

	switch r.Method + " " + r.URL.Path {
	case "GET /counts":
		writeJSON(w, s.Counts())
	case "POST /reset":
		if r.URL.Query().Get("all") == "true" {
			if err := s.ResetAll(); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			s.Reset()
		}
		w.WriteHeader(http.StatusNoContent)
	case "GET /info":
		base := s.controlIssuer()
		writeJSON(w, map[string]any{"issuer": base, "discovery_url": base + DiscoveryPath, "jwks_url": base + JWKSPath, "keys": s.Keys()})
	case "GET /keys":
		writeJSON(w, s.Keys())
	case "POST /keys":
		var o KeyOptions
		if !decodeStrict(w, r, &o) {
			return
		}
		k, err := s.AddKey(o)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, k.info())
	case "POST /keys/remove":
		var req struct {
			KID string `json:"kid"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		if !s.RemoveKey(req.KID) {
			writeError(w, http.StatusNotFound, "no key with that kid")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "POST /mint":
		var o MintOptions
		if !decodeStrict(w, r, &o) {
			return
		}
		tok, err := s.mint(o, s.controlIssuer())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]string{"token": tok})
	case "GET /fault":
		writeJSON(w, s.Fault())
	case "POST /fault":
		var f Fault
		if !decodeStrict(w, r, &f) {
			return
		}
		if err := s.SetFault(f); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, s.Fault())
	case "GET /cache":
		s.mu.Lock()
		c := s.cache
		s.mu.Unlock()
		writeJSON(w, c)
	case "POST /cache":
		var c Cache
		if !decodeStrict(w, r, &c) {
			return
		}
		s.SetCache(c)
		writeJSON(w, c)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// controlIssuer is the issuer a control request mints for: the one the issuer listener announces.
// The control port is another address, so the request's Host says nothing about it.
func (s *Server) controlIssuer() string { return s.Issuer() }

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

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
