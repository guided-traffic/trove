//go:build drainfixture

package app

import (
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/httpserver"
)

// fixtureBuild marks the test build the process-level drain tests compile
// (`go build -tags drainfixture`, R-ARCH-024 acceptance criteria use test builds).
// It starts the HTTP shell without PostgreSQL and Valkey and adds a route that
// holds a request open. No release build contains either.
const fixtureBuild = true

func registerFixture(r *httpserver.Router) {
	r.HandleFunc("GET /test-fixture/hold", func(w http.ResponseWriter, req *http.Request) {
		d, _ := time.ParseDuration(req.URL.Query().Get("for"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("started\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(d):
		case <-req.Context().Done():
			return
		}
		_, _ = w.Write([]byte("finished " + strconv.Itoa(int(d.Milliseconds())) + "\n"))
	})
	r.HandleFunc("GET /test-fixture/stream", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "1000000000")
		for {
			if _, err := w.Write(make([]byte, 1024)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
}
