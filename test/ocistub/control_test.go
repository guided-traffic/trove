// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

const controlToken = "control-secret"

func (f *fixture) control(method, path string, body any) reply {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.reg+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+controlToken)
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: out}
}

func TestControlNeedsItsToken(t *testing.T) {
	off := newFixture(t, Config{})
	if r := off.get(off.reg+"/-/stub/counts", map[string]string{"Authorization": "Bearer " + controlToken}); r.status != 404 {
		t.Errorf("control API without a configured token = %d, want 404", r.status)
	}
	f := newFixture(t, Config{ControlToken: controlToken})
	if r := f.get(f.reg+"/-/stub/counts", nil); r.status != 401 {
		t.Errorf("no token = %d", r.status)
	}
	if r := f.get(f.reg+"/-/stub/counts", map[string]string{"Authorization": "Bearer nope"}); r.status != 401 {
		t.Errorf("wrong token = %d", r.status)
	}
	if r := f.control("GET", "/-/stub/nothing", nil); r.status != 404 {
		t.Errorf("unknown control path = %d", r.status)
	}
	// Control requests are not counted.
	if n := f.stub.Counts().Total(); n != 0 {
		t.Errorf("control requests were counted: %d", n)
	}
}

func TestControlCountsAndReset(t *testing.T) {
	f := newFixture(t, Config{ControlToken: controlToken})
	f.get(f.reg+"/v2/", nil)
	var c Counts
	f.control("GET", "/-/stub/counts", nil).json(t, &c)
	if c.Total() != 1 || c.WithStatus(401) != 1 {
		t.Fatalf("counts over the API: %+v", c)
	}
	if r := f.control("POST", "/-/stub/reset", nil); r.status != 204 {
		t.Fatalf("reset = %d", r.status)
	}
	f.control("GET", "/-/stub/counts", nil).json(t, &c)
	if c.Total() != 0 {
		t.Errorf("counts after reset: %d", c.Total())
	}
	var inflight Inflight
	f.control("GET", "/-/stub/inflight", nil).json(t, &inflight)
	if inflight.Open != 0 {
		t.Errorf("inflight = %+v", inflight)
	}
}

func TestControlModeThrottleQuota(t *testing.T) {
	f := newFixture(t, Config{ControlToken: controlToken})
	r := f.control("POST", "/-/stub/mode", Mode{Auth: AuthNone, Catalog: true, PullQuota: &PullQuota{Limit: 2}})
	if r.status != 200 {
		t.Fatalf("set mode = %d %s", r.status, r.body)
	}
	var m Mode
	f.control("GET", "/-/stub/mode", nil).json(t, &m)
	if !m.Catalog || m.PullQuota == nil || m.PullQuota.Limit != 2 {
		t.Errorf("mode over the API = %+v", m)
	}
	if r := f.control("POST", "/-/stub/mode", Mode{Down: "boom"}); r.status != 400 {
		t.Errorf("invalid mode = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/mode", map[string]any{"nope": 1}); r.status != 400 {
		t.Errorf("unknown field = %d", r.status)
	}
	f.manifestOf("library/alpine", "3")
	var q QuotaState
	f.control("GET", "/-/stub/quota", nil).json(t, &q)
	if q.Used != 1 || q.Remaining != 1 {
		t.Errorf("quota = %+v", q)
	}

	if r := f.control("POST", "/-/stub/throttle", Throttle{Mode: ThrottleHub}); r.status != 200 {
		t.Fatalf("set throttle = %d %s", r.status, r.body)
	}
	if r := f.manifestOf("library/alpine", "3"); r.status != 429 {
		t.Errorf("throttled via the API = %d", r.status)
	}
	var st ThrottleState
	f.control("GET", "/-/stub/throttle", nil).json(t, &st)
	if st.Throttled != 1 || st.Throttle.Mode != ThrottleHub {
		t.Errorf("throttle state = %+v", st)
	}
	if r := f.control("POST", "/-/stub/throttle", Throttle{Mode: "x"}); r.status != 400 {
		t.Errorf("invalid throttle = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/reset?all=true", nil); r.status != 204 {
		t.Fatalf("reset all = %d", r.status)
	}
	var cleared Mode
	f.control("GET", "/-/stub/mode", nil).json(t, &cleared)
	if cleared.Catalog || cleared.PullQuota != nil || cleared.Auth != "" {
		t.Errorf("mode after reset all = %+v", cleared)
	}
}

func TestControlTagsAndImages(t *testing.T) {
	f := newFixture(t, Config{ControlToken: controlToken})
	var repos []string
	f.control("GET", "/-/stub/repositories", nil).json(t, &repos)
	if len(repos) != Default().Len() {
		t.Errorf("repositories = %v", repos)
	}
	var tags map[string]string
	f.control("GET", "/-/stub/tags?name=acme/a", nil).json(t, &tags)
	if len(tags) != 5 {
		t.Errorf("tags = %v", tags)
	}
	if r := f.control("GET", "/-/stub/tags?name=nope", nil); r.status != 404 {
		t.Errorf("tags of an unknown repository = %d", r.status)
	}
	var info ImageInfo
	f.control("GET", "/-/stub/image?name=library/alpine&reference=3", nil).json(t, &info)
	want, _ := f.stub.Digest("library/alpine", "3")
	if info.Digest != want || len(info.Manifests) != 4 || len(info.Blobs()) == 0 {
		t.Errorf("image = %+v", info)
	}
	if r := f.control("GET", "/-/stub/image?name=library/alpine&reference=nope", nil); r.status != 404 {
		t.Errorf("unknown image = %d", r.status)
	}

	var moved struct{ Digest string }
	r := f.control("POST", "/-/stub/move-tag", tagRequest{Name: "acme/a", Tag: "latest"})
	r.json(t, &moved)
	if now, _ := f.stub.Digest("acme/a", "latest"); r.status != 200 || moved.Digest != now {
		t.Errorf("move-tag = %d %s, serving %s", r.status, r.body, now)
	}
	d, _ := f.stub.Digest("acme/a", "1.0")
	if r := f.control("POST", "/-/stub/set-tag", tagRequest{Name: "acme/a", Tag: "stable", Digest: d}); r.status != 204 {
		t.Errorf("set-tag = %d %s", r.status, r.body)
	}
	if r := f.control("POST", "/-/stub/delete-tag", tagRequest{Name: "acme/a", Tag: "stable"}); r.status != 204 {
		t.Errorf("delete-tag = %d %s", r.status, r.body)
	}
	for name, req := range map[string]tagRequest{
		"unknown repo": {Name: "nope/x", Tag: "1"},
		"no tag":       {Name: "acme/a"},
	} {
		want := 404
		if name == "no tag" {
			want = 400
		}
		if r := f.control("POST", "/-/stub/move-tag", req); r.status != want {
			t.Errorf("move-tag %s = %d, want %d", name, r.status, want)
		}
	}
}

func TestControlRevokeTokens(t *testing.T) {
	f := newFixture(t, Config{ControlToken: controlToken})
	hdr := f.bearer("repository:acme/a:pull")
	if r := f.get(f.reg+"/v2/acme/a/tags/list", hdr); r.status != 200 {
		t.Fatalf("token = %d", r.status)
	}
	if r := f.control("POST", "/-/stub/revoke-tokens", nil); r.status != 204 {
		t.Fatalf("revoke = %d", r.status)
	}
	if r := f.get(f.reg+"/v2/acme/a/tags/list", hdr); r.status != 401 {
		t.Errorf("revoked token = %d", r.status)
	}
}
