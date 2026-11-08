package debstub

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/openpgp"
)

// fixture is a running stub with a client that trusts its CA.
type fixture struct {
	t      *testing.T
	stub   *Server
	run    *Running
	client *http.Client
	base   string
}

const testToken = "control-token-for-tests"

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	if cfg.BulkParagraphs == 0 {
		cfg.BulkParagraphs = 500
	}
	stub, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := NewSelfSigned(LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	tr := &http.Transport{TLSClientConfig: ss.ClientConfig(), DisableCompression: true, DialContext: run.DialContext}
	t.Cleanup(tr.CloseIdleConnections)
	return &fixture{t: t, stub: stub, run: run, client: &http.Client{Transport: tr, Timeout: 30 * time.Second}, base: run.URL}
}

// resp is an answer with its body read.
type resp struct {
	status int
	header http.Header
	body   []byte
}

func (f *fixture) do(method, path string, hdr map[string]string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, f.base+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	// Go's client would clean a "/./" path; the request must carry it as written.
	if i := strings.Index(path, "/./"); i >= 0 {
		req.URL.Path, req.URL.RawPath = path, path
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp{status: res.StatusCode, header: res.Header, body: body}
}

func (f *fixture) get(path string) resp { f.t.Helper(); return f.do("GET", path, nil) }

func (f *fixture) must200(path string) []byte {
	f.t.Helper()
	r := f.get(path)
	if r.status != 200 {
		f.t.Fatalf("GET %s = %d %q", path, r.status, r.body)
	}
	return r.body
}

func (f *fixture) control(method, path, body string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, f.base+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

// certs are the trove-side certificates of the named stub keys, parsed by trove's own library.
func (f *fixture) certs(names ...string) []openpgp.Certificate {
	f.t.Helper()
	ring, err := f.stub.Keyring(names...)
	if err != nil {
		f.t.Fatal(err)
	}
	certs, err := openpgp.ParseCertificates(ring, time.Now(), openpgp.Limits{})
	if err != nil {
		f.t.Fatalf("trove does not accept the stub's keys: %v", err)
	}
	return certs
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func gunzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// releaseFile is one checksum line of a Release.
type releaseFile struct {
	hash string
	size int
	path string
}

// parseRelease reads the header fields and the checksum lists of a Release text.
func parseRelease(t *testing.T, text []byte) (fields map[string]string, sums map[string][]releaseFile) {
	t.Helper()
	fields, sums = map[string]string{}, map[string][]releaseFile{}
	cur := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(text), "\n"), "\n") {
		if strings.HasPrefix(line, " ") {
			parts := strings.Fields(line)
			if len(parts) != 3 {
				t.Fatalf("checksum line %q", line)
			}
			size, err := strconv.Atoi(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			sums[cur] = append(sums[cur], releaseFile{hash: parts[0], size: size, path: parts[2]})
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("Release line %q", line)
		}
		fields[name] = strings.TrimSpace(value)
		if value == "" {
			cur = name
		}
	}
	return fields, sums
}
