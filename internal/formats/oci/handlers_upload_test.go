package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/qname"
)

// memSessions is the upload-session port as the host implements it, to the extent a format can tell: a session belongs to the
// binding it was started with, an offset other than the received count is a 416 that reports the count, a digest that is not the
// computed one is DIGEST_INVALID and keeps the session open. It records the binding of every call.
type memSessions struct {
	h *formattest.Host

	mu       sync.Mutex
	sessions map[uuid.UUID]*memSession
	started  int
	bindings []string
	partSize int64
}

type memSession struct {
	binding string
	data    []byte
}

func (m *memSessions) find(ref formats.SessionRef) (*memSession, error) {
	m.bindings = append(m.bindings, ref.Binding)
	s, ok := m.sessions[ref.ID]
	if !ok || s.binding != ref.Binding {
		return nil, errs.E(errs.NotFound, errs.CodeOCIBlobUploadUnknown, "The upload does not exist.")
	}
	return s, nil
}

func (m *memSessions) Start(_ context.Context, rq *formats.Request, target formats.RepoRef, binding string) (formats.UploadSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if target.ID != rq.RepoRef().ID {
		return formats.UploadSession{}, errs.E(errs.Internal, errs.CodeInternal, "wrong target")
	}
	m.started++
	id := uuid.New()
	m.sessions[id] = &memSession{binding: binding}
	return formats.UploadSession{ID: id, Target: target, Binding: binding, PartSize: m.partSize}, nil
}

func (m *memSessions) Get(_ context.Context, _ *formats.Request, ref formats.SessionRef) (formats.UploadSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(ref)
	if err != nil {
		return formats.UploadSession{}, err
	}
	return formats.UploadSession{ID: ref.ID, Binding: s.binding, Received: int64(len(s.data)), PartSize: m.partSize}, nil
}

func (m *memSessions) Append(_ context.Context, _ *formats.Request, ref formats.SessionRef, offset int64, body io.Reader) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(ref)
	if err != nil {
		return 0, err
	}
	if offset >= 0 && offset != int64(len(s.data)) {
		return int64(len(s.data)), errs.E(errs.RangeNotSatisfiable, errs.CodeOCIBlobUploadInvalid, "The chunk does not start at the received count.")
	}
	if body != nil {
		b, _ := io.ReadAll(body)
		s.data = append(s.data, b...)
	}
	return int64(len(s.data)), nil
}

func (m *memSessions) Finalize(_ context.Context, _ *formats.Request, ref formats.SessionRef, offset int64, sum [32]byte, last io.Reader, c formats.Commit) (formats.Committed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(ref)
	if err != nil {
		return formats.Committed{}, err
	}
	if offset >= 0 && offset != int64(len(s.data)) {
		return formats.Committed{Received: int64(len(s.data))}, errs.E(errs.RangeNotSatisfiable, errs.CodeOCIBlobUploadInvalid, "The chunk does not start at the received count.")
	}
	var b []byte
	if last != nil { // a request of the test host with no body has none; a server's request always has one
		b, _ = io.ReadAll(last)
	}
	all := append(append([]byte(nil), s.data...), b...)
	if sha256.Sum256(all) != sum {
		return formats.Committed{}, errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest of the content differs from the digest the request names.")
	}
	m.h.PutItem(c.Path.Path, c.MediaType, all)
	delete(m.sessions, ref.ID)
	return formats.Committed{Created: true}, nil
}

func (m *memSessions) Abort(_ context.Context, _ *formats.Request, ref formats.SessionRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.find(ref); err != nil {
		return err
	}
	delete(m.sessions, ref.ID)
	return nil
}

type uploadFixture struct {
	*fixture
	up *memSessions
}

// withSessions builds the fixture with the session port bound to its own host.
func withSessions(t *testing.T, o formattest.HostOptions) *uploadFixture {
	t.Helper()
	tw := newTwin()
	o.Data = tw
	if o.Resolver == nil {
		o.Resolver = proceedResolver{}
	}
	up := &memSessions{sessions: map[uuid.UUID]*memSession{}, partSize: 5 << 20}
	o.Uploads = up
	f := New()
	h := formattest.NewHost(f, o)
	tw.host = h
	up.h = h
	return &uploadFixture{fixture: &fixture{t: t, f: f, h: h, twin: tw}, up: up}
}

func (fx *uploadFixture) post(path string, query string, body []byte) *formattest.Response {
	fx.t.Helper()
	return fx.request(http.MethodPost, path, body, "application/octet-stream", func(r *http.Request) { r.URL.RawQuery = query })
}

// start opens a session under the image name and returns its location path (relative to the repository) and id.
func (fx *uploadFixture) start(name string) (path string, id string) {
	fx.t.Helper()
	resp := fx.post(name+"/blobs/uploads/", "", nil)
	fx.wantStatus(resp, http.StatusAccepted, "")
	id = resp.Header().Get("Docker-Upload-UUID")
	return name + "/blobs/uploads/" + id, id
}

func (fx *uploadFixture) patch(path string, body []byte, rng string) *formattest.Response {
	fx.t.Helper()
	return fx.request(http.MethodPatch, path, body, "application/octet-stream", func(r *http.Request) {
		if rng != "" {
			r.Header.Set("Content-Range", rng)
		}
	})
}

func (fx *uploadFixture) finish(path, digest string, body []byte) *formattest.Response {
	fx.t.Helper()
	return fx.request(http.MethodPut, path, body, "application/octet-stream", func(r *http.Request) { r.URL.RawQuery = "digest=" + digest })
}

// TestAnUploadSessionIsStartedWithTheHeadersOfTheSpecification: 202 with the location of the session, Range 0-0, the minimum
// chunk length of the session's part size and the session id (R-OCI-011).
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestAnUploadSessionIsStartedWithTheHeadersOfTheSpecification(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	resp := fx.post("team/app/blobs/uploads/", "", nil)
	fx.wantStatus(resp, http.StatusAccepted, "")
	h := resp.Header()
	id := h.Get("Docker-Upload-UUID")
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("Docker-Upload-UUID = %q", id)
	}
	if got := h.Get("Location"); !strings.HasSuffix(got, "/v2/it/main/team/app/blobs/uploads/"+id) || !strings.HasPrefix(got, "https://") {
		t.Errorf("Location = %q", got)
	}
	if h.Get("Range") != "0-0" || h.Get("OCI-Chunk-Min-Length") != "5242880" || h.Get("Content-Length") != "0" {
		t.Errorf("headers = %v", h)
	}
	if fx.up.started != 1 || fx.up.bindings != nil {
		t.Errorf("started %d sessions", fx.up.started)
	}
}

// TestAChunkedUploadStoresTheBlobUnderTheName: chunks with and without Content-Range, the Range of every answer, the
// finalize with the last bytes, the blob pulled through the name afterwards.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestAChunkedUploadStoresTheBlobUnderTheName(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	data := []byte("0123456789abcdefghij0123456789")
	digest := sha(data)
	path, id := fx.start("team/app")

	r1 := fx.patch(path, data[:10], "0-9")
	fx.wantStatus(r1, http.StatusAccepted, "")
	if r1.Header().Get("Range") != "0-9" || r1.Header().Get("Docker-Upload-UUID") != id || !strings.HasSuffix(r1.Header().Get("Location"), path) {
		t.Errorf("first chunk: %v", r1.Header())
	}
	r2 := fx.patch(path, data[10:20], "")
	fx.wantStatus(r2, http.StatusAccepted, "")
	if r2.Header().Get("Range") != "0-19" {
		t.Errorf("second chunk: %v", r2.Header())
	}
	st := fx.get(path)
	if st.Code != http.StatusNoContent || st.Header().Get("Range") != "0-19" || st.Header().Get("Docker-Upload-UUID") != id || st.Body.Len() != 0 {
		t.Errorf("status: %d %v", st.Code, st.Header())
	}
	done := fx.finish(path, digest, data[20:])
	fx.wantStatus(done, http.StatusCreated, "")
	if done.Header().Get("Docker-Content-Digest") != digest || !strings.HasSuffix(done.Header().Get("Location"), "/v2/it/main/team/app/blobs/"+digest) {
		t.Errorf("finish: %v", done.Header())
	}
	got := fx.get("team/app/blobs/" + digest)
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("the blob: %d %q", got.Code, got.Body)
	}
	if other := fx.get("other/blobs/" + digest); other.Code != http.StatusNotFound {
		t.Errorf("the blob is readable through another name: %d", other.Code)
	}
	if again := fx.get(path); again.Code != http.StatusNotFound || errorCode(again) != "BLOB_UPLOAD_UNKNOWN" {
		t.Errorf("a finished session: %d %s", again.Code, again.Body)
	}
}

// TestR_OCI_011_AChunkThatDoesNotStartAtTheReceivedCountIs416WithTheRange: the form the specification uses for a session without
// bytes is Range 0-0; a session with bytes reports them.
func TestR_OCI_011_AChunkThatDoesNotStartAtTheReceivedCountIs416WithTheRange(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	path, id := fx.start("app")
	r := fx.patch(path, []byte("0123456789"), "10-19")
	fx.wantStatus(r, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	if r.Header().Get("Range") != "0-0" || r.Header().Get("Docker-Upload-UUID") != id || !strings.HasSuffix(r.Header().Get("Location"), path) {
		t.Errorf("headers = %v", r.Header())
	}
	fx.wantStatus(fx.patch(path, []byte("abcde"), "0-4"), http.StatusAccepted, "")
	r = fx.patch(path, []byte("abcde"), "0-4")
	fx.wantStatus(r, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	if r.Header().Get("Range") != "0-4" {
		t.Errorf("Range = %q", r.Header().Get("Range"))
	}
	// the last bytes of a finalize are held to the same rule
	d := sha([]byte("abcdefgh"))
	r = fx.request(http.MethodPut, path, []byte("fgh"), "application/octet-stream", func(r *http.Request) {
		r.URL.RawQuery = "digest=" + d
		r.Header.Set("Content-Range", "9-11")
	})
	fx.wantStatus(r, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	if r.Header().Get("Range") != "0-4" {
		t.Errorf("Range of a final chunk = %q", r.Header().Get("Range"))
	}
}

// TestContentRangeIsReadStrictly: a header that is not <start>-<end> in decimal, ends before it starts or does not cover
// the body is 400 BLOB_UPLOAD_INVALID and reaches no session.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestContentRangeIsReadStrictly(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	path, _ := fx.start("app")
	for _, rng := range []string{"bytes 0-4/*", "0-", "-4", "a-4", "0-b", "5-4", "0-9", "00-4", "0-04", "-1-3", "0 - 4", "0-4,5-9"} {
		resp := fx.patch(path, []byte("abcde"), rng)
		if resp.Code != http.StatusBadRequest || errorCode(resp) != "BLOB_UPLOAD_INVALID" {
			t.Errorf("Content-Range %q: %d %s", rng, resp.Code, resp.Body)
		}
	}
	if len(fx.up.bindings) != 0 {
		t.Errorf("a bad header reached the session: %v", fx.up.bindings)
	}
}

// TestAWrongDigestIsRefusedAndTheSessionStaysOpen: 400 DIGEST_INVALID, nothing stored, the session continues.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestAWrongDigestIsRefusedAndTheSessionStaysOpen(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	path, _ := fx.start("app")
	fx.wantStatus(fx.patch(path, []byte("abc"), ""), http.StatusAccepted, "")
	for _, d := range []string{digestZero, "", "sha256:xyz", "sha512:" + strings.Repeat("0", 128)} {
		fx.wantStatus(fx.finish(path, d, nil), http.StatusBadRequest, "DIGEST_INVALID")
	}
	if stored(fx.h, "app/_blobs/"+digestZero) {
		t.Fatal("a blob was stored under a digest it does not have")
	}
	fx.wantStatus(fx.get(path), http.StatusNoContent, "")
	fx.wantStatus(fx.finish(path, sha([]byte("abc")), nil), http.StatusCreated, "")
}

// TestASessionBelongsToTheImageNameOfItsPath: every call passes the image name of the path as the binding; a session of
// another name and an id that is no UUID are 404 BLOB_UPLOAD_UNKNOWN, the same answer on every method.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestASessionBelongsToTheImageNameOfItsPath(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	_, id := fx.start("team/app")
	for _, tc := range []struct{ method, name string }{
		{http.MethodGet, "team/other"},
		{http.MethodPatch, "team/other"},
		{http.MethodPut, "team/other"},
		{http.MethodDelete, "team/other"},
		{http.MethodGet, "team"},
		{http.MethodPatch, "other"},
	} {
		resp := fx.request(tc.method, tc.name+"/blobs/uploads/"+id, []byte("x"), "application/octet-stream", func(r *http.Request) { r.URL.RawQuery = "digest=" + digestZero })
		if resp.Code != http.StatusNotFound || errorCode(resp) != "BLOB_UPLOAD_UNKNOWN" {
			t.Errorf("%s through %s: %d %s", tc.method, tc.name, resp.Code, resp.Body)
		}
	}
	for _, bad := range []string{"not-a-uuid", "0e4b8d38", "../x"} {
		for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			resp, err := fx.h.Serve(context.Background(), method, "team/app/blobs/uploads/"+bad, nil, false, func(r *http.Request) { r.URL.RawQuery = "digest=" + digestZero })
			if err != nil {
				continue // a path the router does not match is the dispatcher's 404
			}
			if resp.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d %s", method, bad, resp.Code, resp.Body)
			}
		}
	}
	for _, b := range fx.up.bindings {
		if b != "team/other" && b != "team" && b != "other" {
			t.Errorf("a call carried the binding %q", b)
		}
	}
	// the session of the right name is intact
	fx.wantStatus(fx.get("team/app/blobs/uploads/"+id), http.StatusNoContent, "")
}

// TestACancelledSessionIsGone: DELETE answers 204 and the session no longer exists.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestACancelledSessionIsGone(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	path, _ := fx.start("app")
	del := fx.request(http.MethodDelete, path, nil, "")
	if del.Code != http.StatusNoContent || del.Body.Len() != 0 {
		t.Fatalf("cancel: %d %s", del.Code, del.Body)
	}
	fx.wantStatus(fx.get(path), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
	fx.wantStatus(fx.request(http.MethodDelete, path, nil, ""), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
}

// TestAMonolithicPostStoresTheBlobWithoutASession: 201 with the blob's location; a body that is not the digest's content is
// 400 DIGEST_INVALID and stores nothing; neither opens a session; the host decides deploy on the blob's path.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestAMonolithicPostStoresTheBlobWithoutASession(t *testing.T) {
	var deployed []string
	fx := withSessions(t, formattest.HostOptions{Allow: func(tg formats.Target) error {
		for _, a := range tg.Actions {
			if a == formats.ActionDeploy {
				if tg.Path == "" {
					return fmt.Errorf("a deploy decision without a path")
				}
				deployed = append(deployed, tg.Path)
			}
		}
		return nil
	}})
	data := []byte("a whole layer")
	d := sha(data)
	resp := fx.post("team/app/blobs/uploads/", "digest="+d, data)
	fx.wantStatus(resp, http.StatusCreated, "")
	if resp.Header().Get("Docker-Content-Digest") != d || !strings.HasSuffix(resp.Header().Get("Location"), "/team/app/blobs/"+d) {
		t.Errorf("headers = %v", resp.Header())
	}
	if got := fx.get("team/app/blobs/" + d); got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("pull: %d", got.Code)
	}
	bad := fx.post("app/blobs/uploads/", "digest="+digestZero, data)
	fx.wantStatus(bad, http.StatusBadRequest, "DIGEST_INVALID")
	if stored(fx.h, "app/_blobs/"+digestZero) {
		t.Error("a blob with the wrong digest was stored")
	}
	fx.wantStatus(fx.post("app/blobs/uploads/", "digest=nope", data), http.StatusBadRequest, "DIGEST_INVALID")
	if fx.up.started != 0 {
		t.Errorf("a monolithic upload opened %d sessions", fx.up.started)
	}
	if len(deployed) == 0 || deployed[0] != "team/app/_blobs/"+d {
		t.Errorf("deploy decided on %v", deployed)
	}
}

// TestAnUploadRefusedByTheDecisionPointStartsNothing: a 403 of Authorize reaches the client and no bytes are read.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestAnUploadRefusedByTheDecisionPointStartsNothing(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{Allow: func(formats.Target) error {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
	}})
	data := []byte("x")
	resp := fx.post("app/blobs/uploads/", "digest="+sha(data), data)
	fx.wantStatus(resp, http.StatusForbidden, "DENIED")
}

// TestTheLocationIsTheLinkBuildersAndNotTheRequests: the location of a session is built by the link builder.
//
// Mechanism of R-OCI-011, not a proof of its criteria: it checks one step of an upload; the criteria are the three-chunk upload
// over three replicas, the 416 form and the conformance push category.
func TestTheLocationIsTheLinkBuildersAndNotTheRequests(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{BaseURL: "https://registry.example.com"})
	resp := fx.post("app/blobs/uploads/", "", nil)
	if got := resp.Header().Get("Location"); !strings.HasPrefix(got, "https://registry.example.com/v2/it/main/app/blobs/uploads/") {
		t.Errorf("Location = %q", got)
	}
}

// --- R-OCI-012

// mountFixture holds a source image name with one blob and a target name, on one repository.
func mountHost(t *testing.T, allow func(formats.Target) error) (*uploadFixture, []byte, string) {
	t.Helper()
	fx := withSessions(t, formattest.HostOptions{Allow: allow})
	data := []byte("the base layer")
	d := fx.blob("base", data)
	return fx, data, d
}

const mountFrom = "from=it/main/base"

// TestR_OCI_012_AMountLinksTheBlobWithoutUploadingBytes: 201 with the blob's location; the item exists under the target name, no
// session was opened and no body was read; it is readable through the target name.
func TestR_OCI_012_AMountLinksTheBlobWithoutUploadingBytes(t *testing.T) {
	fx, data, d := mountHost(t, nil)
	resp := fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil)
	fx.wantStatus(resp, http.StatusCreated, "")
	if resp.Header().Get("Docker-Content-Digest") != d || !strings.HasSuffix(resp.Header().Get("Location"), "/team/app/blobs/"+d) {
		t.Errorf("headers = %v", resp.Header())
	}
	if got := fx.get("team/app/blobs/" + d); got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("the mounted blob: %d", got.Code)
	}
	if fx.up.started != 0 {
		t.Errorf("a mount opened %d sessions", fx.up.started)
	}
	// the same mount again is the same answer (the blob is there)
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusCreated, "")
}

// shape is what a client can see of an answer that opened a session: the status, the headers that do not carry the session, the body.
func shape(resp *formattest.Response) string {
	h := resp.Header().Clone()
	h.Del("Location")
	h.Del("Docker-Upload-UUID")
	return fmt.Sprintf("%d %v %s", resp.Code, h, resp.Body)
}

// TestR_OCI_012_AMountThatCannotBeServedOpensASessionAndSaysNothingMore: no readable source, no source repository, no such name, no such
// blob, a source that is no name, no from, a blocked blob: all are 202 with a session, and none differs from another in anything but the
// session id.
func TestR_OCI_012_AMountThatCannotBeServedOpensASessionAndSaysNothingMore(t *testing.T) {
	fx, _, d := mountHost(t, func(tg formats.Target) error {
		if strings.HasPrefix(tg.Path, "secret/") {
			for _, a := range tg.Actions {
				if a == formats.ActionRead {
					return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
				}
			}
		}
		return nil
	})
	fx.blob("secret", []byte("the secret layer"))
	secret := sha([]byte("the secret layer"))
	unreadable := fx.post("team/app/blobs/uploads/", "mount="+secret+"&from=it/main/secret", nil)
	fx.wantStatus(unreadable, http.StatusAccepted, "")
	want := shape(unreadable)
	for name, query := range map[string]string{
		"a name the repository holds nothing of": "mount=" + secret + "&from=it/main/nothing",
		"a repository that does not exist":       "mount=" + secret + "&from=it/nowhere/secret",
		"a project that does not exist":          "mount=" + secret + "&from=zz/main/secret",
		"a blob the source does not hold":        "mount=" + sha([]byte("no such")) + "&from=it/main/secret",
		"a blob of another name":                 "mount=" + d + "&from=it/main/secret",
		"no from":                                "mount=" + secret,
		"an empty from":                          "mount=" + secret + "&from=",
		"a from without a name":                  "mount=" + secret + "&from=it/main",
		"a from that is no name":                 "mount=" + secret + "&from=it/main/Upper",
		"a from with an encoded slash":           "mount=" + secret + "&from=it%2Fmain%2Fsecret",
	} {
		resp := fx.post("team/app/blobs/uploads/", query, nil)
		if got := shape(resp); got != want {
			t.Errorf("%s: %s\nwant %s", name, got, want)
		}
		if resp.Header().Get("Docker-Upload-UUID") == "" {
			t.Errorf("%s: no session was opened", name)
		}
	}
	if stored(fx.h, "team/app/_blobs/"+secret) || stored(fx.h, "team/app/_blobs/"+d) {
		t.Error("a refused mount stored a blob")
	}
	// the readable source still works beside them
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusCreated, "")
}

// TestAMountNeedsDeployOnTheTarget: a caller who may read the source but not deploy to the target name is refused (403), not
// answered with a session.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestAMountNeedsDeployOnTheTarget(t *testing.T) {
	fx, _, d := mountHost(t, func(tg formats.Target) error {
		for _, a := range tg.Actions {
			if a == formats.ActionDeploy && strings.HasPrefix(tg.Path, "team/") {
				return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
			}
		}
		return nil
	})
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusForbidden, "DENIED")
	if stored(fx.h, "team/app/_blobs/"+d) {
		t.Error("a refused mount stored a blob")
	}
}

// TestAMountIsRefusedWhenTheSourceIsNotTheBlobItsDigestNames: an item of the source path whose content is not the digest's
// (another format's file at the same path) is not linked: the commit hook refuses it.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestAMountIsRefusedWhenTheSourceIsNotTheBlobItsDigestNames(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	d := sha([]byte("what the digest names"))
	fx.h.PutItem(BlobPath("base", d), "application/octet-stream", []byte("something else"))
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusConflict, "DENIED")
	if stored(fx.h, "team/app/_blobs/"+d) {
		t.Error("a blob of the wrong content was linked")
	}
}

// TestADigestThatIsNoDigestIs400: the answer does not depend on the source.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestADigestThatIsNoDigestIs400(t *testing.T) {
	fx, _, _ := mountHost(t, nil)
	for _, d := range []string{"", "nope", "sha256:abc", "sha512:" + strings.Repeat("0", 128)} {
		fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusBadRequest, "DIGEST_INVALID")
	}
}

// TestAManifestDigestIsNoBlobSource: a mount reads `<name>/_blobs/<digest>`, and a manifest lives in `_manifests/`: the digest of a
// stored manifest is the same session as any unknown digest, and nothing is linked under the target.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestAManifestDigestIsNoBlobSource(t *testing.T) {
	fx, _, _ := mountHost(t, nil)
	m := []byte(`{"schemaVersion":2}`)
	d := sha(m)
	fx.h.PutItem(ManifestPath("base", d), manifestType, m)
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+d+"&"+mountFrom, nil), http.StatusAccepted, "")
	if stored(fx.h, "team/app/_blobs/"+d) || stored(fx.h, "team/app/_manifests/"+d) {
		t.Error("a manifest was linked")
	}
}

// TestABoundHostNamesTheSourceWithoutProjectAndKey: on a host bound to the repository, from is an image name of it.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestABoundHostNamesTheSourceWithoutProjectAndKey(t *testing.T) {
	fx, _, d := mountHost(t, nil)
	serve := func(query string, bound ...qname.Repo) *formattest.Response {
		rq, err := fx.h.NewRequest(context.Background(), http.MethodPost, "team/app/blobs/uploads/", nil)
		if err != nil {
			t.Fatal(err)
		}
		rq.URL.RawQuery = query
		rq.Endpoint.Bound, rq.Endpoint.BoundRepo = true, rq.Repo.Name
		if len(bound) > 0 {
			rq.Endpoint.BoundRepo = bound[0]
		}
		return fx.h.ServeRequest(rq, false)
	}
	fx.wantStatus(serve("mount="+d+"&from=base"), http.StatusCreated, "")
	// `from` is read against the repository the host is bound to (the virtual repository whose deployment target the request acts on is not the
	// request's repository): bound elsewhere, the same name is another repository's and the mount is the session
	fx.wantStatus(serve("mount="+d+"&from=base", qname.Repo{Project: "it", Key: "elsewhere"}), http.StatusAccepted, "")
	// the shared host's spelling is not a name on a bound host
	fx.wantStatus(serve("mount="+d+"&"+mountFrom), http.StatusAccepted, "")
}

// TestTheSourceOfAMountIsParsedOneWay: the table the registry root reads the same function by for the grant on the source.
//
// Mechanism of R-OCI-012, not a proof of its criteria: it checks one rule of the mount source or one refusal; the criteria are
// the grant-limited 201 and 202 outcomes, the mount without from and the absence of uploaded bytes.
func TestTheSourceOfAMountIsParsedOneWay(t *testing.T) {
	bound := qname.Repo{Project: "web", Key: "docker-local"}
	for _, tc := range []struct {
		from  string
		bound qname.Repo
		repo  qname.Repo
		name  string
		cache bool
		ok    bool
	}{
		{"web/docker-local/base", qname.Repo{}, bound, "base", false, true},
		{"web/docker-local/library/nginx", qname.Repo{}, bound, "library/nginx", false, true},
		{"web/docker-hub~cache/library/alpine", qname.Repo{}, qname.Repo{Project: "web", Key: "docker-hub"}, "library/alpine", true, true},
		{"base", bound, bound, "base", false, true},
		{"library/nginx", bound, bound, "library/nginx", false, true},
		{"web/docker-local/base", bound, bound, "web/docker-local/base", false, true},
		{"", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/docker-local", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/docker-local/", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/docker-local/Base", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/docker-local/a//b", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/docker-local/_blobs", qname.Repo{}, qname.Repo{}, "", false, false},
		{"Web/docker-local/base", qname.Repo{}, qname.Repo{}, "", false, false},
		{"web/~cache/base", qname.Repo{}, qname.Repo{}, "", false, false},
		{"Upper", bound, qname.Repo{}, "", false, false},
		{"", bound, qname.Repo{}, "", false, false},
	} {
		repo, name, cache, ok := MountSource(tc.from, tc.bound)
		if repo != tc.repo || name != tc.name || cache != tc.cache || ok != tc.ok {
			t.Errorf("MountSource(%q, %v) = %v %q %v %v, want %v %q %v %v", tc.from, tc.bound, repo, name, cache, ok, tc.repo, tc.name, tc.cache, tc.ok)
		}
	}
}
