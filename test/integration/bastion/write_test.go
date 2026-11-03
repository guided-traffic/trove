//go:build integration

package bastion

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// relaxed raises the Bastion's request limits so that a loop of requests in a test is not the limit's subject.
const relaxed = "  limits:\n    authenticated:\n      requests_per_second: 100000\n      burst: 100000\n    anonymous:\n      requests_per_second: 100000\n      burst: 100000\n    misses:\n      authenticated_per_minute: 100000\n      anonymous_per_minute: 100000\n"

// TestR_BAS_014_WritesAreRefusedInTheFormatsShape: an `npm publish`-shaped PUT and a `nuget push`-shaped PUT are
// refused 403 bastion_read_only in the client's own error shape, naming the main installation; the Bastion reads
// none of the upload body; 1,000 PUTs to one path cause one probe; and the main installation records no
// package.published. A read route that takes a body (npm's bulk advisories) is served by the main installation.
func TestR_BAS_014_WritesAreRefusedInTheFormatsShape(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	for _, anon := range []bool{true, false} {
		name := "anonymous off"
		if anon {
			name = "anonymous on"
			w.anonymousPolicy()
		}
		t.Run(name, func(t *testing.T) {
			w.setAnonymous(anon)
			b := w.newBastion(t, bastionOptions{Settings: relaxed}).wait()
			r := b.reps[0]
			tok := w.publisher
			published := w.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('package.published', 'npm.publish')`)

			t.Run("npm publish", func(t *testing.T) {
				m := w.front.Mark()
				got := r.do(t, "PUT", "/api/npm/"+npmAll+"/"+escapeNPM("@acme/sdk"), callOpts{token: tok, addr: "198.51.100.91", body: npmPublishBody(t, "@acme/sdk", "9.0.0"), header: []string{"Content-Type", "application/json"}})
				if got.Status != http.StatusForbidden || got.Code() != "bastion_read_only" {
					t.Fatalf("npm publish: %d %s", got.Status, got.Body)
				}
				for _, k := range []string{`"error"`, `"message"`} {
					if !bytes.Contains(got.Body, []byte(k)) {
						t.Fatalf("not npm's error shape (%s missing): %s", k, got.Body)
					}
				}
				if !bytes.Contains(got.Body, []byte("http://"+mainHost)) {
					t.Fatalf("the refusal does not name the main installation: %s", got.Body)
				}
				probes := 0
				for _, x := range w.front.Since(m) {
					if x.Probe {
						probes++
						if x.Header.Get("Authorization") != "" {
							t.Fatal("the probe carries the caller's credential")
						}
					} else if x.Method == "PUT" {
						t.Fatal("the real upload reached the main installation")
					}
				}
				if probes != 1 {
					t.Fatalf("%d probes", probes)
				}
			})
			t.Run("nuget push", func(t *testing.T) {
				body, ct := nugetForm(t, nupkg(t, "Acme.Sdk", "9.0.0"))
				got := r.do(t, "PUT", "/api/nuget/"+ngAll+"/v3/package", callOpts{token: tok, addr: "198.51.100.92", body: body, header: []string{"Content-Type", ct}})
				code, msg, ok := strings.Cut(string(got.Body), ": ")
				if got.Status != http.StatusForbidden || !ok || code != "bastion_read_only" || strings.ContainsAny(string(got.Body), "\r\n") {
					t.Fatalf("nuget push: %d %q", got.Status, got.Body)
				}
				if !strings.Contains(msg, "http://"+mainHost) || !strings.HasPrefix(got.Header.Get("Content-Type"), "text/plain") {
					t.Fatalf("nuget push: %q, Content-Type %q", got.Body, got.Header.Get("Content-Type"))
				}
			})
			t.Run("less than 64 KiB of the body are read", func(t *testing.T) {
				// The client announces 4 MiB and sends 1 KiB. A Bastion that read the body would wait for the rest.
				c, err := net.Dial("tcp", strings.TrimPrefix(r.client, "http://"))
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				head := "PUT /api/npm/" + npmAll + "/x HTTP/1.1\r\nHost: " + baseHost + "\r\nAuthorization: Bearer " + tok + "\r\nX-Forwarded-For: 198.51.100.93\r\nContent-Type: application/json\r\nContent-Length: 4194304\r\n\r\n"
				if _, err := c.Write(append([]byte(head), bytes.Repeat([]byte("x"), 1024)...)); err != nil {
					t.Fatal(err)
				}
				res, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatalf("no answer before the body was sent: %v", err)
				}
				defer res.Body.Close()
				if res.StatusCode != http.StatusForbidden {
					t.Fatalf("status %d", res.StatusCode)
				}
			})
			t.Run("1000 PUTs, one probe", func(t *testing.T) {
				m := w.front.Mark()
				path := "/api/npm/" + npmAll + "/thousand"
				for i := 0; i < 1000; i++ {
					if got := r.do(t, "PUT", path, callOpts{token: tok, addr: "198.51.100.94", body: []byte(`{}`), header: []string{"Content-Type", "application/json"}}); got.Status != http.StatusForbidden {
						t.Fatalf("PUT %d: %d", i, got.Status)
					}
				}
				n := 0
				for _, x := range w.front.Since(m) {
					if x.Probe {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("1,000 PUTs caused %d probes, want 1", n)
				}
			})
			t.Run("the main installation recorded no publish", func(t *testing.T) {
				if got := w.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('package.published', 'npm.publish')`); got != published {
					t.Fatalf("publish events: %d before, %d after", published, got)
				}
			})
			t.Run("a read route with a body", func(t *testing.T) {
				path := "/api/npm/" + npmAll + "/-/npm/v1/security/advisories/bulk"
				body := []byte(`{"@acme/sdk":["1.0.0"],"plain-lib":["1.0.0"]}`)
				m := w.front.Mark()
				got := r.do(t, "POST", path, callOpts{token: w.full.token, addr: "198.51.100.95", body: body, header: []string{"Content-Type", "application/json"}})
				want := w.api("POST", path, w.full.token, body)
				if got.Status != want.Status || want.Status != http.StatusOK || !bytes.Equal(bytes.TrimSpace(got.Body), bytes.TrimSpace(want.Body)) {
					t.Fatalf("bulk advisories: Bastion %d %.200s, main installation %d %.200s", got.Status, got.Body, want.Status, want.Body)
				}
				var probe, real bool
				for _, x := range w.front.Since(m) {
					if x.Probe && x.Status == http.StatusNoContent {
						probe = true
					}
					if !x.Probe && x.Method == "POST" && x.Kind == "forward" {
						real = true
					}
				}
				if !probe || !real {
					t.Fatalf("a 204 probe (%v) and the real request (%v) were expected", probe, real)
				}
			})
		})
	}
}
