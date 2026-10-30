package edge

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// WriteJSON answers a JSON body with the headers of a management answer: no
// store, one trailing newline.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// jobReference is the JobReference of the OpenAPI document: the job an operation
// started, which the caller polls at Href.
type jobReference struct {
	Href  string `json:"href"`
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// JobHref is the path of a job in the management API.
func JobHref(id string) string { return "/api/v1/system/jobs/" + id }

// WriteJob answers 202 for an operation that continues as a job: the reference
// to the job in the body and its absolute URL in Location (R-API-015).
func WriteJob(w http.ResponseWriter, r *http.Request, id, kind, state string) {
	w.Header().Set("Location", strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/")+JobHref(id))
	WriteJSON(w, r, http.StatusAccepted, struct {
		Job jobReference `json:"job"`
	}{jobReference{Href: JobHref(id), ID: id, Kind: kind, State: state}})
}
