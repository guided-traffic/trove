package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type bucketRecorder struct{ seen []string }

func (b *bucketRecorder) Allow(_ context.Context, bucket string) (bool, time.Duration) {
	b.seen = append(b.seen, bucket)
	return true, 0
}

func TestR_AUTH_038_FlowStartsCountAnIPv6NetworkAsOneAddress(t *testing.T) {
	rec := &bucketRecorder{}
	addr := ""
	s := &Service{o: Options{Limiter: rec, ClientAddr: func(*http.Request) string { return addr }}}
	for _, a := range []string{"2001:db8:5:6::1", "2001:db8:5:6::2:3", "198.51.100.4"} {
		addr = a
		if !s.allow(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/oidc/corp/start", nil)) {
			t.Fatal("refused")
		}
	}
	if len(rec.seen) != 3 || rec.seen[0] != "2001:db8:5:6::/64" || rec.seen[1] != rec.seen[0] || rec.seen[2] != "198.51.100.4" {
		t.Fatalf("buckets %v", rec.seen)
	}
}
