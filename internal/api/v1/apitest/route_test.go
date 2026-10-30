package apitest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestR_AUTHZ_019_AnEncodedSlashStaysInItsSegment: the validator finds the
// operation of a path whose package name holds an encoded "/" the way the server
// does, segment by segment, and the operation it finds is the one with that
// many segments.
func TestR_AUTHZ_019_AnEncodedSlashStaysInItsSegment(t *testing.T) {
	spec := []byte(`openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /things/{key}/packages/{name}/owners:
    parameters:
      - {name: key, in: path, required: true, schema: {type: string}}
      - {name: name, in: path, required: true, schema: {type: string}}
    get:
      operationId: owners
      responses:
        "200": {description: ok}
  /things/{key}/packages/{scope}/{name}/owners:
    parameters:
      - {name: key, in: path, required: true, schema: {type: string}}
      - {name: scope, in: path, required: true, schema: {type: string}}
      - {name: name, in: path, required: true, schema: {type: string}}
    get:
      operationId: twoSegments
      responses:
        "200": {description: ok}
`)
	v := New(t, spec)
	for path, op := range map[string]string{
		"/things/k/packages/@acme%2Ffrontend/owners": "owners",
		"/things/k/packages/@acme%2ffrontend/owners": "owners",
		"/things/k/packages/left-pad/owners":         "owners",
		"/things/k/packages/@acme/frontend/owners":   "twoSegments",
	} {
		got, err := v.Check(httptest.NewRequest(http.MethodGet, path, nil), 200, http.Header{}, nil)
		if err != nil || got != op {
			t.Errorf("%s: operation %q, %v; want %q", path, got, err, op)
		}
	}
}
