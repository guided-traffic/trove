package npmstub

import (
	"net/http"
	"testing"
)

// The log of the registry's requests carries the times and statuses the failure suite of the mirror asserts on (R-REPO-054,
// R-REPO-059).
func TestRequestLogRecordsTimesStatusesAndRetryAfter(t *testing.T) {
	f := newFixture(t, Config{})
	_, name := names(t)
	if err := f.stub.Publish(name, "9.9.9", nil); err != nil {
		t.Fatal(err)
	}
	f.setThrottle(Throttle{Kind: Throttle429, Class: KindTarball, RetryAfterSeconds: 7})
	file := "/" + name + "/-/" + name + "-9.9.9.tgz"
	if st := f.status(file); st != http.StatusTooManyRequests {
		t.Fatalf("first tarball request: %d", st)
	}
	if st := f.status(file); st != http.StatusOK {
		t.Fatalf("second tarball request: %d", st)
	}
	if st := f.status(docPath(name)); st != http.StatusOK {
		t.Fatalf("document request: %d", st)
	}
	log := f.stub.Requests()
	if len(log.Requests) != 3 {
		t.Fatalf("%d records, want 3: %+v", len(log.Requests), log.Requests)
	}
	first, second, doc := log.Requests[0], log.Requests[1], log.Requests[2]
	if first.Kind != KindTarball || first.Name != name || first.Status != 429 || first.RetryAfterSeconds != 7 {
		t.Errorf("first record %+v, want a tarball of %s answered 429 with Retry-After 7", first, name)
	}
	if second.Status != 200 || second.RetryAfterSeconds != 0 || doc.Kind != KindDocument || doc.Status != 200 {
		t.Errorf("records %+v and %+v", second, doc)
	}
	if first.At.IsZero() || first.Done.Before(first.At) || second.At.Before(first.At) || doc.At.Before(second.At) {
		t.Errorf("times out of order: %v %v %v", first.At, second.At, doc.At)
	}
	f.stub.Reset()
	if n := len(f.stub.Requests().Requests); n != 0 {
		t.Errorf("%d records after a reset", n)
	}
}
