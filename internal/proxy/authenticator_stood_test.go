package proxy

import (
	"net/http"
	"strings"
	"testing"
)

// stoodAuth is a handshake whose format reads a stood 401 with error="insufficient_scope" as a missing resource.
type stoodAuth struct{ hsAuth }

func (*stoodAuth) StoodMissing(_ string, header http.Header) bool {
	return strings.Contains(header.Get("Www-Authenticate"), `error="insufficient_scope"`)
}

// A stood 401 is the format's to read only when the handshake sent a token and the upstream refused it: with a static bearer token, with a
// challenge the format has no handshake for and with a request that carried no token, the same header says nothing of the resource.
func TestR_OCI_021_AStood401IsReadAsMissingOnlyAfterTheEnginesOwnTokenWasRefused(t *testing.T) {
	missing := func(h *handshake, status int) bool {
		resp := &http.Response{
			StatusCode: status, Body: http.NoBody,
			Header: http.Header{"Www-Authenticate": {`Bearer realm="https://a.example.com/t",error="insufficient_scope"`}},
		}
		defer resp.Body.Close()
		return h.stoodMissing(resp)
	}
	h := &handshake{auth: &stoodAuth{}}
	if missing(h, http.StatusUnauthorized) {
		t.Error("read as missing although no token of the handshake was refused")
	}
	h.refused = true
	if !missing(h, http.StatusUnauthorized) {
		t.Error("not read as missing after the token was refused")
	}
	if missing(h, http.StatusForbidden) {
		t.Error("a 403 was read as missing")
	}
	if missing(&handshake{auth: &hsAuth{}, refused: true}, http.StatusUnauthorized) {
		t.Error("a format that reads no stood challenge had its 401 read as missing")
	}
}
