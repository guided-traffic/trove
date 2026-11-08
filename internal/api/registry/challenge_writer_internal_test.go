package registry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/registry/route"
)

type noMatch struct{}

func (noMatch) RegistryTarget(*http.Request) (route.Target, bool) { return route.Target{}, false }

// A 103 Early Hints before a bare-Bearer 401 does not count as the response: the 401 still gets the registry's challenge.
func TestTheChallengeWriterRewritesAfterAnInformationalStatus(t *testing.T) {
	e, err := NewEnforcer(EnforcerOptions{
		Matcher: noMatch{}, WriteError: func(http.ResponseWriter, *http.Request, error) {},
		Realm: func(*http.Request) (string, string) { return "https://trove.test/v2/_token", "trove.test" },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	w := &challengeWriter{ResponseWriter: rec, e: e, r: httptest.NewRequest(http.MethodGet, "/v2/web/docker-local/app/manifests/1", nil)}
	w.Header().Set("Link", "</style.css>; rel=preload")
	w.WriteHeader(http.StatusEarlyHints)
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer realm="https://trove.test/v2/_token",service="trove.test"`; got != want { // the recorder keeps the first status it saw, so only the header is asserted
		t.Fatalf("%q, want %q", got, want)
	}
}

// readerFromRecorder records that the underlying writer's ReadFrom was used.
type readerFromRecorder struct {
	*httptest.ResponseRecorder
	readFrom int
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.readFrom++
	return io.Copy(r.ResponseRecorder, src)
}

// A blob download copied with io.Copy keeps the fast path of the writer under the challenge wrapper, and the response controller
// still reaches the underlying writer.
func TestTheChallengeWriterKeepsTheReadFromFastPathAndUnwrap(t *testing.T) {
	under := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	cw := &challengeWriter{ResponseWriter: under}
	n, err := io.Copy(cw, struct{ io.Reader }{strings.NewReader("layer bytes")}) // no WriterTo: io.Copy asks the writer
	if err != nil || n != int64(len("layer bytes")) || under.readFrom != 1 {
		t.Fatalf("copied %d (%v), underlying ReadFrom called %d times", n, err, under.readFrom)
	}
	if under.Code != http.StatusOK || under.Body.String() != "layer bytes" {
		t.Fatalf("%d %q", under.Code, under.Body.String())
	}
	if err := http.NewResponseController(cw).Flush(); err != nil || !under.Flushed {
		t.Fatalf("the response controller does not reach the underlying writer: %v flushed %v", err, under.Flushed)
	}
	// A writer without ReadFrom still gets the bytes.
	plain := httptest.NewRecorder()
	if n, err := io.Copy(&challengeWriter{ResponseWriter: plain}, struct{ io.Reader }{strings.NewReader("abc")}); err != nil || n != 3 || plain.Body.String() != "abc" {
		t.Fatalf("%d %v %q", n, err, plain.Body.String())
	}
	// A status written first is kept, and the bare challenge is only replaced once, before the first write.
	first := httptest.NewRecorder()
	w := &challengeWriter{ResponseWriter: first}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("x"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("status %d", first.Code)
	}
}
