package ocistub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
)

// The control API lives below /-/stub/ on any host, needs the bearer token of Config.ControlToken and is not counted:
//
//	GET  /-/stub/counts                    the counters (Counts as JSON)
//	GET  /-/stub/inflight                  {"open","max","requests"}: the requests open now and the most open at once since the last reset
//	POST /-/stub/reset[?all=true]          zero the counters; with all, also the modes, the throttle, the quota, the tokens and the changes to the corpus
//	GET  /-/stub/mode                      the switches
//	POST /-/stub/mode                      replace them (a Mode as JSON; {} clears)
//	GET  /-/stub/throttle                  the rate-limit switch with the requests it counted and throttled (ThrottleState)
//	POST /-/stub/throttle                  replace it (a Throttle as JSON; {} clears)
//	GET  /-/stub/quota                     the pull quota of the mode with its use (QuotaState)
//	GET  /-/stub/repositories              the repository names
//	GET  /-/stub/tags?name=                the tags of a repository with their digests
//	GET  /-/stub/image?name=&reference=    the manifest a tag or digest names (ImageInfo, with the manifests and blobs below it)
//	POST /-/stub/move-tag                  {"name","tag"}: point the tag at a new revision of its image; answers {"digest"}
//	POST /-/stub/set-tag                   {"name","tag","digest"}: point a tag at a manifest the repository holds
//	POST /-/stub/delete-tag                {"name","tag"}: remove a tag
//	POST /-/stub/revoke-tokens             invalidate every token issued so far
const maxControlBody = 1 << 20

type tagRequest struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ocistub-control"`)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "control token required")
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
			writeError(w, http.StatusBadRequest, "UNSUPPORTED", err.Error())
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
			writeError(w, http.StatusBadRequest, "UNSUPPORTED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.ThrottleState())
	case "GET /-/stub/quota":
		writeJSON(w, http.StatusOK, s.Quota())
	case "GET /-/stub/repositories":
		writeJSON(w, http.StatusOK, s.Repositories())
	case "GET /-/stub/tags":
		tags, ok := s.Tags(r.URL.Query().Get("name"))
		if !ok {
			writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "no such repository")
			return
		}
		writeJSON(w, http.StatusOK, tags)
	case "GET /-/stub/image":
		info, err := s.Image(r.URL.Query().Get("name"), r.URL.Query().Get("reference"))
		if err != nil {
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, info)
	case "POST /-/stub/move-tag":
		s.controlTag(w, r, func(t tagRequest) (any, error) {
			d, err := s.MoveTag(t.Name, t.Tag)
			return map[string]string{"digest": d}, err
		})
	case "POST /-/stub/set-tag":
		s.controlTag(w, r, func(t tagRequest) (any, error) { return nil, s.SetTag(t.Name, t.Tag, t.Digest) })
	case "POST /-/stub/delete-tag":
		s.controlTag(w, r, func(t tagRequest) (any, error) { return nil, s.DeleteTag(t.Name, t.Tag) })
	case "POST /-/stub/revoke-tokens":
		s.RevokeTokens()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "not found")
	}
}

func (s *Server) controlTag(w http.ResponseWriter, r *http.Request, act func(tagRequest) (any, error)) {
	var t tagRequest
	if !decodeStrict(w, r, &t) {
		return
	}
	if t.Name == "" || t.Tag == "" {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED", "name and tag are required")
		return
	}
	out, err := act(t)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUnknown) {
			status = http.StatusNotFound
		}
		writeError(w, status, "UNSUPPORTED", err.Error())
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "UNSUPPORTED", "invalid JSON: "+err.Error())
		return false
	}
	return true
}
