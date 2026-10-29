package errs

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProblemExtensions(t *testing.T) {
	t.Run("R-API-012 a 412 carries currentEtag and a 428 has its own status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		Write(rec, httptest.NewRequest("PATCH", "/x", nil),
			E(PreconditionFailed, CodePreconditionFailed, "stale", WithExtension("currentEtag", `"5"`)), "id", nil)
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 412 || m["currentEtag"] != `"5"` || m["code"] != CodePreconditionFailed {
			t.Fatalf("%d %v", rec.Code, m)
		}
		rec = httptest.NewRecorder()
		Write(rec, httptest.NewRequest("PATCH", "/x", nil), E(PreconditionRequired, CodePreconditionRequired, "send If-Match"), "id", nil)
		if rec.Code != 428 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("R-API-009 an extension never replaces a standard member", func(t *testing.T) {
		rec := httptest.NewRecorder()
		Write(rec, httptest.NewRequest("GET", "/x", nil),
			E(NotFound, CodeNotFound, "gone", WithExtension("code", "evil"), WithExtension("status", 1)), "id", nil)
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m["code"] != CodeNotFound || m["status"] != float64(404) {
			t.Fatalf("%v", m)
		}
	})
	t.Run("R-API-014 a conflict can carry Retry-After", func(t *testing.T) {
		rec := httptest.NewRecorder()
		Write(rec, httptest.NewRequest("POST", "/x", nil),
			E(Conflict, CodeIdempotencyInFlight, "wait", WithRetryAfter(time.Second)), "id", nil)
		if rec.Code != 409 || rec.Header().Get("Retry-After") != "1" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
}
