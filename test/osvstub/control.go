package osvstub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// The control API lives below /-/stub/, needs the bearer token of Config.ControlToken and is not counted:
//
//	GET    /-/stub/requests[?log=false]   the request log and the counts per path and kind (Counts as JSON)
//	POST   /-/stub/reset[?all=true]       zero the counts; with all, also the dataset, toggles, faults and the catalog
//	GET    /-/stub/dataset                records, change rows, archive size and digests per ecosystem (Info), toggles
//	GET    /-/stub/faults                 the faults that are on, by ecosystem ("kev" for the catalog)
//	PUT    /-/stub/faults/{ecosystem}     replace the ecosystem's faults (a Fault as JSON; {} clears)
//	DELETE /-/stub/faults/{ecosystem}     clear them
//	POST   /-/stub/records                {"ecosystem","record":{…OSV record…},"modified"?}: add or replace a record
//	POST   /-/stub/withdraw               {"ecosystem","id"}: set withdrawn, new modified
//	POST   /-/stub/remove                 {"ecosystem","id"}: delete the record from archive and change file
//	PUT    /-/stub/toggles/{name}         {"on":bool}: switch a fixture group in or out
//	PUT    /-/stub/filler/{ecosystem}     {"count":n}: hold exactly n synthetic filler records
//	POST   /-/stub/kev                    a KEVEntry ({"cveID":"CVE-2026-0001"} suffices): add a CVE to the catalog
//	DELETE /-/stub/kev/{cve}              remove it
//
// Every answer to a mutation is 204, or the new modified of the record for POST /-/stub/records.
const maxControlBody = 32 << 20

func (s *Stub) serveControl(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.ControlToken
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="osvstub-control"`)
		writeError(w, http.StatusUnauthorized, "control token required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)
	s.control.ServeHTTP(w, r)
}

func (s *Stub) controlMux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /-/stub/requests", func(w http.ResponseWriter, r *http.Request) {
		c := s.Counts()
		if q := r.URL.Query().Get("log"); q == "false" || q == "0" {
			c.Log = nil
		}
		writeJSON(w, c)
	})
	m.HandleFunc("POST /-/stub/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") == "true" {
			s.ResetAll()
		} else {
			s.ResetCounts()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("GET /-/stub/dataset", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.Info()) })
	m.HandleFunc("GET /-/stub/faults", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.Faults()) })
	m.HandleFunc("PUT /-/stub/faults/{ecosystem}", func(w http.ResponseWriter, r *http.Request) {
		var f Fault
		if !decodeStrict(w, r, &f) {
			return
		}
		done(w, s.SetFault(r.PathValue("ecosystem"), f))
	})
	m.HandleFunc("DELETE /-/stub/faults/{ecosystem}", func(w http.ResponseWriter, r *http.Request) {
		done(w, s.SetFault(r.PathValue("ecosystem"), Fault{}))
	})
	m.HandleFunc("POST /-/stub/records", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Ecosystem string          `json:"ecosystem"`
			Record    json.RawMessage `json:"record"`
			Modified  string          `json:"modified"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		var at time.Time
		if req.Modified != "" {
			t, err := time.Parse(time.RFC3339Nano, req.Modified)
			if err != nil {
				writeError(w, http.StatusBadRequest, "modified: "+err.Error())
				return
			}
			at = t
		}
		mod, err := s.upsert(req.Ecosystem, req.Record, at)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, map[string]string{"modified": stamp(mod)})
	})
	m.HandleFunc("POST /-/stub/withdraw", func(w http.ResponseWriter, r *http.Request) {
		s.byID(w, r, s.Withdraw)
	})
	m.HandleFunc("POST /-/stub/remove", func(w http.ResponseWriter, r *http.Request) {
		s.byID(w, r, s.Remove)
	})
	m.HandleFunc("PUT /-/stub/toggles/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		done(w, s.Toggle(r.PathValue("name"), req.On))
	})
	m.HandleFunc("PUT /-/stub/filler/{ecosystem}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count int `json:"count"`
		}
		if !decodeStrict(w, r, &req) {
			return
		}
		if req.Count < 0 || req.Count > 100000 {
			writeError(w, http.StatusBadRequest, "count must be 0 to 100000")
			return
		}
		done(w, s.SetFiller(r.PathValue("ecosystem"), req.Count))
	})
	m.HandleFunc("POST /-/stub/kev", func(w http.ResponseWriter, r *http.Request) {
		var k KEVEntry
		if !decodeStrict(w, r, &k) {
			return
		}
		done(w, s.AddKEV(k))
	})
	m.HandleFunc("DELETE /-/stub/kev/{cve}", func(w http.ResponseWriter, r *http.Request) {
		done(w, s.RemoveKEV(r.PathValue("cve")))
	})
	return m
}

func (s *Stub) byID(w http.ResponseWriter, r *http.Request, act func(ecosystem, id string) error) {
	var req struct {
		Ecosystem string `json:"ecosystem"`
		ID        string `json:"id"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	done(w, act(req.Ecosystem, req.ID))
}

func done(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrUnknown) {
		status = http.StatusNotFound
	}
	writeError(w, status, err.Error())
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

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
