package health

import (
	"encoding/json"
	"net/http"
	"time"
)

// LiveHandler serves GET /healthz: 200 {"status":"alive"} while every watched
// loop makes progress, otherwise 503 {"status":"stuck","stuck":[...]}. It never
// reads a readiness result (Q-OPS-1).
func (h *Health) LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if stuck := h.Stuck(); len(stuck) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "stuck", "stuck": stuck})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "alive"})
	})
}

// ReadyHandler serves GET /readyz from the cached results:
//
//	200 {"status":"ready"}
//	200 {"status":"ready","degraded":["valkey"]}
//	503 {"status":"unready","failing":["database"]}
//
// With ?verbose=1 the body also names the replica, its role, the time of the
// last evaluation and every check. A check shows its status, a short sanitised
// reason and its latency; it never shows the text of an error, because the
// listener is unauthenticated (R-OPS-001, H-41).
func (h *Health) ReadyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := h.Snapshot()
		ready := len(s.Failing) == 0
		body := map[string]any{"status": "ready"}
		code := http.StatusOK
		if !ready {
			body["status"], code = "unready", http.StatusServiceUnavailable
			body["failing"] = s.Failing
		}
		if len(s.Degraded) > 0 {
			body["degraded"] = s.Degraded
		}
		if r.URL.Query().Get("verbose") == "1" {
			body["replica"], body["role"] = h.replica, h.role
			if !s.CheckedAt.IsZero() {
				body["checkedAt"] = s.CheckedAt.UTC().Format("2006-01-02T15:04:05.000Z")
			}
			checks := make(map[string]any, len(s.Checks))
			for _, c := range s.Checks {
				m := map[string]any{"status": c.Status}
				if c.Reason != "" {
					m["reason"] = c.Reason
				}
				if c.ConfigError {
					m["configError"] = true
				}
				if !c.Since.IsZero() && c.Status != Pass {
					m["since"] = c.Since.UTC().Format("2006-01-02T15:04:05.000Z")
				}
				if c.Latency > 0 && c.Status == Pass {
					m["latencyMs"] = c.Latency / time.Millisecond
				}
				checks[c.Name] = m
			}
			body["checks"] = checks
		}
		writeJSON(w, code, body)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil { // cannot happen: the bodies hold strings and numbers
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}
