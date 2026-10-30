//go:build integration

package files

import (
	"testing"
)

// content returns n deterministic bytes that differ by seed.
func content(n int, seed byte) []byte {
	b := make([]byte, n)
	x := uint32(seed)*2654435761 + 12345
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

// hdr returns a header of a response.
func (r response) hdr(k string) string { return r.header.Get(k) }

func problemOf(t testing.TB, res response, status int, code string) map[string]any {
	t.Helper()
	m := res.json(t)
	if res.status != status || m["code"] != code {
		t.Fatalf("answer %d %v, want %d %s\n%s", res.status, m["code"], status, code, res.body)
	}
	return m
}

// violation asserts the 422 of R-API-009: the top-level code is validation_failed
// and the resource-specific code is one of errors[].code (Q-REPO-84).
func violation(t testing.TB, res response, code string) {
	t.Helper()
	m := problemOf(t, res, 422, "validation_failed")
	list, _ := m["errors"].([]any)
	for _, e := range list {
		if e.(map[string]any)["code"] == code {
			return
		}
	}
	t.Fatalf("errors %v hold no %q\n%s", m["errors"], code, res.body)
}
