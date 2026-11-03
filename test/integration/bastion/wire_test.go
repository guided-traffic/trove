//go:build integration

package bastion

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// asBastion sends a request to the main installation through the front the way the Bastion does: the caller's
// credential in Authorization, the Bastion token in Trove-Bastion-Authorization, the interface version and the
// forwarded headers of R-BAS-017.
func (w *world) asBastion(method, path, caller, bst string, hdr ...string) (int, http.Header, []byte) {
	w.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, w.front.URL()+path, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	if caller != "" {
		req.Header.Set("Authorization", "Bearer "+caller)
	}
	if bst != "" {
		req.Header.Set(iface.HeaderAuthorization, "Bearer "+bst)
	}
	req.Header.Set(iface.HeaderInterface, itoa(iface.InterfaceVersion))
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("X-Forwarded-Host", baseHost)
	req.Header.Set("X-Forwarded-Proto", "https")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: w.front.ca.Pool(), MinVersion: tls.VersionTLS12}}, Timeout: 30 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

type marks struct{ class, repo, read, subject string }

func markOf(h http.Header) marks {
	return marks{h.Get(iface.HeaderClass), h.Get(iface.HeaderRepository), h.Get(iface.HeaderRead), h.Get(iface.HeaderSubject)}
}

// TestR_BAS_013_TheMarkingContractAtTheWire: what the Bastion receives from the real main installation for each
// kind of request and caller: the class (artifact, shared, private, absent, pass), the shape and the subject;
// a refusal that hides an item is `pass` with the body of a missing one; nothing is marked for a request
// without a valid Bastion token.
func TestR_BAS_013_TheMarkingContractAtTheWire(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")
	tgz := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	cases := []struct {
		name   string
		path   string
		tok    string
		status int
		class  string
		read   string
	}{
		{"tarball $FULL", tgz, w.full.token, 200, "artifact", "full"},
		{"tarball $PART", tgz, w.part.token, 200, "artifact", "partial"},
		{"document $FULL", doc, w.full.token, 200, "shared", "full"},
		{"document $PART", doc, w.part.token, 200, "private", "partial"},
		{"search", "/api/nuget/" + ngAll + "/v3/search?q=acme", w.full.token, 200, "pass", "full"},
		{"missing name, local repository, $FULL", "/api/npm/" + npmLocal + "/no-such-package", w.full.token, 404, "absent", "full"},
		{"a name $PART may not read", "/api/npm/" + npmLocal + "/plain-lib", w.part.token, 404, "pass", "partial"},
		{"nuget document $FULL", "/api/nuget/" + ngAll + "/v3/flatcontainer/acme.sdk/index.json", w.full.token, 200, "shared", "full"},
		{"nupkg $FULL", nupkgPath(ngAll, "Acme.Sdk", "1.0.0"), w.full.token, 200, "artifact", "full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, h, _ := w.asBastion("GET", c.path, c.tok, w.bst)
			m := markOf(h)
			if status != c.status || m.class != c.class || m.read != c.read {
				t.Fatalf("status %d, class %q, read %q; want %d, %q, %q", status, m.class, m.read, c.status, c.class, c.read)
			}
			if m.repo == "" || h.Get(iface.HeaderInterface) != itoa(iface.InterfaceVersion) {
				t.Fatalf("repository %q, interface %q", m.repo, h.Get(iface.HeaderInterface))
			}
			if !strings.HasPrefix(m.subject, "token=") || !strings.Contains(m.subject, "principal=") {
				t.Fatalf("subject %q", m.subject)
			}
		})
	}

	t.Run("a hidden name answers as a missing one", func(t *testing.T) {
		_, _, hidden := w.asBastion("GET", "/api/npm/"+npmLocal+"/plain-lib", w.part.token, w.bst)
		_, _, missing := w.asBastion("GET", "/api/npm/"+npmLocal+"/no-such-package", w.part.token, w.bst)
		if !bytes.Equal(normalizeInstance(hidden), normalizeInstance(missing)) {
			t.Fatalf("a hidden package is told from a missing one:\n%s\n%s", hidden, missing)
		}
	})
	t.Run("an invalid credential is subject invalid", func(t *testing.T) {
		status, h, _ := w.asBastion("GET", tgz, "trv_pat_"+strings.Repeat("0", 40), w.bst)
		if status != http.StatusUnauthorized || h.Get(iface.HeaderSubject) != "invalid" {
			t.Fatalf("%d, subject %q", status, h.Get(iface.HeaderSubject))
		}
	})
	t.Run("without a valid Bastion token nothing is marked", func(t *testing.T) {
		// A valid token of another type in the Bastion's header: served as an ordinary request.
		status, h, _ := w.asBastion("GET", tgz, w.full.token, w.full.token)
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		for k := range h {
			if strings.HasPrefix(k, iface.HeaderPrefix) {
				t.Fatalf("an ordinary request got %s", k)
			}
		}
		// A revoked Bastion token: 401 bastion_token_invalid as a problem, on a package route too.
		second := w.issueBastionToken("partners")
		w.mustAPI("DELETE", "/api/v1/bastions/partners/tokens/"+second.id, 204, nil)
		status, h, body := w.asBastion("GET", tgz, w.full.token, second.token)
		if status != http.StatusUnauthorized || !bytes.Contains(body, []byte("bastion_token_invalid")) || !strings.HasPrefix(h.Get("Content-Type"), "application/problem+json") {
			t.Fatalf("revoked Bastion token: %d %s", status, body)
		}
		for k := range h {
			if strings.HasPrefix(k, iface.HeaderClass) || strings.HasPrefix(k, iface.HeaderRead) {
				t.Fatalf("a refused Bastion request got %s", k)
			}
		}
	})
}

// normalizeInstance removes the per-request members of a problem body.
func normalizeInstance(b []byte) []byte {
	s := string(b)
	for _, k := range []string{`"requestId":"`, `"instance":"`} {
		for {
			i := strings.Index(s, k)
			if i < 0 {
				break
			}
			j := strings.Index(s[i+len(k):], `"`)
			s = s[:i] + s[i+len(k)+j+1:]
		}
	}
	return []byte(s)
}

// TestR_BAS_017_TheClientAddressAndTheBastionsHost: a caller at 203.0.113.7 who sends a forged forwarded-for
// entry through the Bastion appears at the main installation (its audit) as 203.0.113.7; documents are rendered
// on the Bastion's host; no body the Bastion served names the main installation's host.
func TestR_BAS_017_TheClientAddressAndTheBastionsHost(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{Backend: "volume"}).wait()
	r := b.reps[0]
	const caller = "203.0.113.7"
	forged := callOpts{token: w.full.token, header: []string{"X-Forwarded-For", "192.0.2.1, " + caller, "Forwarded", "for=192.0.2.2"}}

	var bodies [][]byte
	s := npmRun(t, r, npmAll, forged, "@acme/sdk")
	g := nugetRun(t, r, ngAll, forged, "Acme.Sdk")
	if !s.ok() || !g.ok() {
		t.Fatalf("runs: %+v %+v", s, g)
	}
	for _, p := range []string{"/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk"), "/api/nuget/" + ngAll + "/v3/index.json", "/api/nuget/" + ngAll + "/v3/flatcontainer/acme.sdk/index.json"} {
		got := r.do(t, "GET", p, forged)
		bodies = append(bodies, got.Body)
		if got.Status != http.StatusOK {
			t.Fatalf("%s: %d", p, got.Status)
		}
		for k, vs := range got.Header {
			for _, v := range vs {
				if strings.Contains(v, mainHost) {
					t.Fatalf("%s: header %s = %q names the main installation", p, k, v)
				}
			}
		}
	}
	for i, body := range bodies {
		if bytes.Contains(body, []byte(mainHost)) {
			t.Fatalf("a document names the main installation's host: %.300s", body)
		}
		// The package document and the service index carry URLs; the flat container's version list has none.
		if i < 2 && !bytes.Contains(body, []byte(baseHost)) {
			t.Fatalf("a document is not rendered on the Bastion's host: %.300s", body)
		}
	}
	// The main installation's audit: the tarball and the package it served for the caller carry the caller's address.
	testWait(t, "the main installation's audit of the cold downloads", func() bool {
		return w.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND host(source_ip) = $1`, caller) >= 2
	})
	if n := w.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND host(source_ip) IN ('192.0.2.1', '192.0.2.2', '127.0.0.1')`); n != 0 {
		t.Fatalf("%d downloads were recorded for the forged address or the Bastion's own", n)
	}
	// A failed credential from the caller is the caller's failure at the main installation: its request log names the
	// caller's address (R-AUTH-038 counts that address).
	r.get(t, tarballPath(npmAll, "@acme/sdk", "1.0.0"), maintest.GenerateTokenWithPrefix(t, "trv_pat_"), "X-Forwarded-For", "192.0.2.1, "+caller)
	testWait(t, "the main installation's request log of the refused credential", func() bool {
		for _, line := range strings.Split(w.main.Logs.String(), "\n") {
			if strings.Contains(line, `"request completed"`) && strings.Contains(line, `"status":401`) && strings.Contains(line, `"remote_addr":"`+caller+`"`) {
				return true
			}
		}
		return false
	})
}

func testWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within 20s: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
