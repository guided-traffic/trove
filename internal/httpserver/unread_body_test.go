package httpserver

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// refuse answers 429 without reading the request body, as the rate limit does.
var refuse = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "slow down", http.StatusTooManyRequests)
})

func TestUnreadBody(t *testing.T) {
	t.Run("R-ARCH-005 an early answer to a request with a large unread body is not lost to a connection reset", func(t *testing.T) {
		for _, size := range []int{64 << 10, 256 << 10, 2 << 20} {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				_, addr := start(t, ServerConfig{Handler: refuse})
				c, err := net.Dial("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				if _, err := fmt.Fprintf(c, "PUT /x HTTP/1.1\r\nHost: t\r\nContent-Length: %d\r\n\r\n", size); err != nil {
					t.Fatal(err)
				}
				// A proxy writes the whole body before it reads the answer: the write
				// fails when the server resets the connection with the body unread.
				body := make([]byte, 32<<10)
				for n := 0; n < size; n += len(body) {
					if _, err := c.Write(body[:min(len(body), size-n)]); err != nil {
						t.Fatalf("writing the body failed after %d of %d bytes: %v", n, size, err)
					}
				}
				br := bufio.NewReader(c)
				resp, err := http.ReadResponse(br, nil)
				if err != nil {
					t.Fatalf("the answer was lost: %v", err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("status %d, want 429", resp.StatusCode)
				}
				// The connection ends with a FIN or stays open for the next request; a
				// reset is the failure.
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, err := br.ReadByte(); err != nil && !errors.Is(err, io.EOF) && !isTimeout(err) {
					t.Fatalf("the connection ended with %v, want a clean end", err)
				}
			})
		}
	})

	t.Run("R-ARCH-005 a request that waits for 100 Continue is answered without asking for its body", func(t *testing.T) {
		_, addr := start(t, ServerConfig{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		})})
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(c, "PUT /x HTTP/1.1\r\nHost: t\r\nContent-Length: 1048576\r\nExpect: 100-continue\r\n\r\n")
		got, _ := io.ReadAll(bufio.NewReader(c))
		if strings.Contains(string(got), "100 Continue") || !strings.Contains(string(got), "401") {
			t.Fatalf("answer %q: want the 401 and no 100 Continue", got)
		}
	})

	t.Run("R-ARCH-005 a body beyond the bound does not hold the connection past the bound", func(t *testing.T) {
		_, addr := start(t, ServerConfig{Handler: refuse})
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(30 * time.Second))
		// The headers announce more than the bound and the body never comes.
		_, _ = fmt.Fprintf(c, "PUT /x HTTP/1.1\r\nHost: t\r\nContent-Length: %d\r\n\r\n", 1<<30)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("no answer at once: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d, want the 429 at once", resp.StatusCode)
		}
	})
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
