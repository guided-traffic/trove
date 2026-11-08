package deb

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/test/debstub/debgen"
)

func newHost(t *testing.T) (*formattest.Host, *twinStore) {
	t.Helper()
	tw := newTwin()
	h := formattest.NewHost(New(), formattest.HostOptions{Data: twinPort{twinStore: tw}, Resolver: noShadow{}})
	return h, tw
}

func put(t *testing.T, h *formattest.Host, file string, body []byte, q url.Values) *formattest.Response {
	t.Helper()
	r, err := h.Serve(context.Background(), http.MethodPut, "upload/"+file, bytes.NewReader(body), false, func(r *http.Request) { r.URL.RawQuery = q.Encode() })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hello(t *testing.T, version string, mod func(*debgen.Spec)) []byte {
	t.Helper()
	s := debgen.Hello("hello", version, "amd64")
	if mod != nil {
		mod(&s)
	}
	d, err := debgen.Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return d.Bytes
}

func problemCode(t *testing.T, r *formattest.Response) string {
	t.Helper()
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", r.Body.String(), err)
	}
	return p.Code
}

func suiteQuery(suite string, components ...string) url.Values {
	q := url.Values{"distribution": {suite}}
	for _, c := range components {
		q.Add("component", c)
	}
	return q
}

// R-DEB-006: a new file answers 201 with the stored path, its identity and the numbers the index waits on; the identical bytes uploaded
// again answer 200 and add the coordinates of the request.
func TestR_DEB_006_UploadAnswers201ThenAddsCoordinatesWith200(t *testing.T) {
	h, tw := newHost(t)
	body := hello(t, "2.10-3", nil)
	r := put(t, h, "hello_2.10-3_amd64.deb", body, suiteQuery("stable"))
	if r.Code != http.StatusCreated {
		t.Fatalf("first upload = %d %s", r.Code, r.Body.String())
	}
	var a Answer
	if err := json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Path != "pool/h/hello/hello_2.10-3_amd64.deb" || a.Package != "hello" || a.Version != "2.10-3" || a.Architecture != "amd64" {
		t.Errorf("answer = %+v", a)
	}
	if len(a.Coordinates) != 1 || a.Coordinates[0].Distribution != "stable" || a.Coordinates[0].Component != "main" {
		t.Errorf("coordinates = %+v", a.Coordinates)
	}
	if len(a.Index) != 1 || a.Index[0].Distribution != "stable" || a.Index[0].ChangeSeq != 1 {
		t.Errorf("index = %+v", a.Index)
	}

	r = put(t, h, "hello_2.10-3_amd64.deb", body, suiteQuery("testing", "contrib"))
	if r.Code != http.StatusOK {
		t.Fatalf("second upload = %d %s", r.Code, r.Body.String())
	}
	if err := json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Coordinates) != 2 || len(a.Index) != 2 {
		t.Errorf("after the second upload: %+v", a)
	}
	b, found, _ := tw.Binary(context.Background(), 1, "pool/h/hello/hello_2.10-3_amd64.deb")
	if !found || len(b.Coordinates) != 2 {
		t.Errorf("stored coordinates = %+v", b.Coordinates)
	}
}

// R-DEB-006: the refusals of an upload carry their codes.
func TestR_DEB_006_UploadRefusals(t *testing.T) {
	h, _ := newHost(t)
	good := hello(t, "2.10-3", nil)
	if r := put(t, h, "hello_2.10-3_amd64.deb", good, suiteQuery("stable")); r.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", r.Code, r.Body.String())
	}
	other := hello(t, "2.10-3", func(s *debgen.Spec) { s.Line = "different bytes" })
	for _, tc := range []struct {
		name   string
		file   string
		body   []byte
		q      url.Values
		status int
		code   string
	}{
		{"no distribution", "hello_2.10-3_amd64.deb", good, url.Values{}, 400, errs.CodeDebDistributionRequired},
		{"suite outside the grammar", "hello_2.10-3_amd64.deb", good, suiteQuery("Stable"), 400, errs.CodeDebInvalidCoordinate},
		{"file name without .deb", "hello_2.10-3_amd64.udeb", good, suiteQuery("stable"), 400, errs.CodeDebInvalidFileName},
		{"other bytes for a stored version", "hello_2.10-3_amd64.deb", other, suiteQuery("stable"), 409, errs.CodeDebVersionExists},
		{"not a package", "hello_1.0-1_amd64.deb", []byte("garbage"), suiteQuery("stable"), 422, errs.CodeDebInvalidArchive},
		{"33 coordinates", "hello_2.10-3_amd64.deb", good, manyCoordinates(), 422, errs.CodeValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := put(t, h, tc.file, tc.body, tc.q)
			if r.Code != tc.status || problemCode(t, r) != tc.code {
				t.Errorf("= %d %s, want %d %s", r.Code, r.Body.String(), tc.status, tc.code)
			}
		})
	}
}

func manyCoordinates() url.Values {
	q := url.Values{}
	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		q.Add("distribution", s)
	}
	for _, c := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		q.Add("component", c)
	}
	return q
}

// R-DEB-009: a version number that differs only by its epoch gives the same file name and is refused.
func TestR_DEB_006_TheSameFileNameOfAnotherEpochIsRefused(t *testing.T) {
	h, _ := newHost(t)
	if r := put(t, h, "hello_2.10-3_amd64.deb", hello(t, "2.10-3", nil), suiteQuery("stable")); r.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", r.Code, r.Body.String())
	}
	r := put(t, h, "hello_2.10-3_amd64.deb", hello(t, "1:2.10-3", nil), suiteQuery("stable"))
	if r.Code != http.StatusConflict || problemCode(t, r) != errs.CodeDebFilenameConflict {
		t.Errorf("= %d %s", r.Code, r.Body.String())
	}
}

// R-DEB-013: deleting a file through its route removes the stored file and writes the journaled event; the bytes are no longer served.
func TestR_DEB_013_DeleteRemovesTheFileAndJournalsIt(t *testing.T) {
	h, tw := newHost(t)
	if r := put(t, h, "hello_2.10-3_amd64.deb", hello(t, "2.10-3", nil), suiteQuery("stable")); r.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", r.Code, r.Body.String())
	}
	const path = "pool/h/hello/hello_2.10-3_amd64.deb"
	if r, err := h.Serve(context.Background(), http.MethodGet, path, nil, false); err != nil || r.Code != http.StatusOK {
		t.Fatalf("get = %v %v", r, err)
	}
	r, err := h.Serve(context.Background(), http.MethodDelete, path, nil, false)
	if err != nil || r.Code != http.StatusNoContent {
		t.Fatalf("delete = %v %v", r, err)
	}
	if _, found, _ := tw.Binary(context.Background(), 1, path); found {
		t.Error("the row of the deleted file is still there")
	}
	if len(tw.Events) != 1 || !strings.HasPrefix(tw.Events[0], "deb.package.deleted ") {
		t.Errorf("events = %v", tw.Events)
	}
	if r, err := h.Serve(context.Background(), http.MethodGet, path, nil, false); err != nil || r.Code != http.StatusNotFound {
		t.Errorf("get after delete = %v %v", r, err)
	}
}

// R-DEB-009: a list of coordinates is validated before anything is decided.
func TestR_DEB_009_CoordinateList(t *testing.T) {
	pair := func(s, c string) [2]string { return [2]string{s, c} }
	t.Run("empty", func(t *testing.T) {
		_, err := CoordinateList(nil)
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeValidation {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("duplicates are dropped", func(t *testing.T) {
		got, err := CoordinateList([][2]string{pair("stable", "main"), pair("stable", "main"), pair("stable", "contrib")})
		if err != nil || len(got) != 2 {
			t.Errorf("= %v %v", got, err)
		}
	})
	t.Run("outside the grammar", func(t *testing.T) {
		_, err := CoordinateList([][2]string{pair("stable", "Main")})
		if e, _ := errs.As(err); e == nil || e.Code != errs.CodeDebInvalidCoordinate {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("bounds", func(t *testing.T) {
		var many [][2]string
		for i := 0; i < 33; i++ {
			many = append(many, pair("s", "c"+string(rune('a'+i%26))+string(rune('a'+i/26))))
		}
		_, err := CoordinateList(many)
		e, _ := errs.As(err)
		if e == nil || len(e.Fields) != 1 || e.Fields[0].Code != "out_of_range" {
			t.Errorf("33 coordinates: %v", err)
		}
		many = many[:0]
		for i := 0; i < 65; i++ {
			many = append(many, pair("s"+string(rune('a'+i%26))+string(rune('a'+i/26)), "main"))
		}
		_, err = CoordinateList(many)
		if e, _ := errs.As(err); e == nil || len(e.Fields) != 1 {
			t.Errorf("65 coordinates: %v", err)
		}
	})
}

// R-DEB-007: the properties of a stored file list its identity and its coordinates, sorted.
func TestR_DEB_007_FilePropertiesListTheCoordinates(t *testing.T) {
	b := rows.Binary{Package: "hello", Version: "2.10-3", Architecture: "amd64", Source: "hello"}
	got := FileProperties(b, []rows.Coordinate{{Suite: "testing", Component: "main"}, {Suite: "stable", Component: "main"}})
	if got["deb.coordinate"] != "stable/main,testing/main" || got["deb.package"] != "hello" || got["deb.architecture"] != "amd64" {
		t.Errorf("properties = %v", got)
	}
	if _, ok := FileProperties(b, nil)["deb.coordinate"]; ok {
		t.Error("a file without coordinates has a coordinate property")
	}
}

// R-DEB-030: versions are ordered by the dpkg rules and a string that is no version sorts before every version.
func TestR_DEB_030_ViewCompareFollowsDpkg(t *testing.T) {
	a := viewAdapter{}
	if a.Compare("1.0~rc1", "1.0") >= 0 || a.Compare("1:0.1", "2.0") <= 0 || a.Compare("1.0-1", "0:1.0-1") != 0 {
		t.Error("dpkg order violated")
	}
	if a.Compare("not a version!", "0.1") >= 0 {
		t.Error("an invalid string sorts after a version")
	}
}
