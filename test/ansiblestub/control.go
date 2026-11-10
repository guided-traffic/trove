package ansiblestub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
)

// The control API lives below /-/stub/ on any host, needs the bearer token of Config.ControlToken and is not counted:
//
//	GET  /-/stub/counts                 the counters (Counts as JSON)
//	GET  /-/stub/inflight               {"open","max","requests"}: the requests open now and the most open at once since the last reset
//	POST /-/stub/reset[?all=true]       zero the counters; with all, also the modes, the throttle, the realm and the corpus changes
//	GET  /-/stub/mode                   the switches
//	POST /-/stub/mode                   replace them (a Mode as JSON; {} clears)
//	GET  /-/stub/throttle               the rate-limit switch with the requests it counted and throttled (ThrottleState)
//	POST /-/stub/throttle               replace it (a Throttle as JSON; {} clears)
//	GET  /-/stub/realm                  the realm with its counters (RealmState)
//	POST /-/stub/realm                  replace the realm configuration (a Realm as JSON; {} clears) and forget its tokens
//	POST /-/stub/realm/expire           revoke every access token issued; answers {"revoked": n}
//	GET  /-/stub/corpus                 the collections and roles with the digests of their original archives (CorpusInfo)
//	POST /-/stub/versions/add           {"collection":"ns.name","version":"1.2.3"}: the upstream publishes a version
//	POST /-/stub/versions/drop          {"collection":"ns.name","version":"1.2.3"}: the upstream removes a version
const maxControlBody = 1 << 20

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ansiblestub-control"`)
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
	case "GET /-/stub/realm":
		writeJSON(w, http.StatusOK, s.RealmState())
	case "POST /-/stub/realm":
		var rm Realm
		if !decodeStrict(w, r, &rm) {
			return
		}
		if err := s.SetRealm(rm); err != nil {
			writeText(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.RealmState())
	case "POST /-/stub/realm/expire":
		writeJSON(w, http.StatusOK, map[string]int{"revoked": s.ExpireAccessTokens()})
	case "GET /-/stub/corpus":
		info, err := s.Corpus()
		if err != nil {
			writeText(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, info)
	case "POST /-/stub/versions/add", "POST /-/stub/versions/drop":
		var req struct {
			Collection string `json:"collection"`
			Version    string `json:"version"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		var err error
		if r.URL.Path == "/-/stub/versions/add" {
			err = s.AddVersion(req.Collection, req.Version)
		} else {
			err = s.DropVersion(req.Collection, req.Version)
		}
		switch {
		case errors.Is(err, ErrUnknown):
			writeText(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeText(w, http.StatusBadRequest, err.Error())
		default:
			w.WriteHeader(http.StatusNoContent)
		}
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
