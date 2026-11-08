package formathost

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/packages/view"
)

type viewAdapterStub struct {
	want   formats.ViewFields
	got    []formats.ViewVersion
	find   formats.ViewFind
	limit  int
	stored []formats.StoredQuery
}

func (s *viewAdapterStub) Stored(_ context.Context, _ formats.DataPorts, q formats.StoredQuery) (formats.Stored, error) {
	s.stored = append(s.stored, q)
	st := formats.Stored{Versions: []string{"2.0.0", "1.0.0"}}
	if q.Tags {
		st.Tags = map[string]string{"latest": "2.0.0"}
	}
	return st, nil
}

func (viewAdapterStub) Compare(a, b string) int { return len(a) - len(b) }
func (s *viewAdapterStub) Describe(_ context.Context, _ formats.DataPorts, vs []formats.ViewVersion, want formats.ViewFields, visible func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	s.want, s.got = want, vs
	return map[formats.VersionID]formats.ViewData{vs[0].ID: {Description: "d", Keywords: []string{"k"}, HasReadme: visible("1")}}, nil
}

func (s *viewAdapterStub) Find(_ context.Context, _ formats.DataPorts, f formats.ViewFind, limit int) ([]formats.VersionID, error) {
	s.find, s.limit = f, limit
	return []formats.VersionID{1, 2}, nil
}

func (s *viewAdapterStub) Withheld(context.Context, formats.DataPorts, []string) ([]formats.VersionID, error) {
	return []formats.VersionID{3}, nil
}

type viewFormat struct {
	formats.Base
	id string
	a  formats.ViewAdapter
}

func (f viewFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{Facts: catalog.Facts{ID: formats.ID(f.id), Classes: []formats.Class{formats.Local}, Layout: "x", PatternSubject: formats.SubjectPath}}
}
func (viewFormat) Routes() []formats.Route { return nil }
func (viewFormat) Match(string, string) (*formats.Route, formats.Params, error) {
	return nil, nil, nil
}
func (viewFormat) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{} }
func (viewFormat) PackageDocumentPath(string) (formats.Path, bool) {
	return formats.Path{}, false
}
func (f viewFormat) View() formats.ViewAdapter { return f.a }

// Exposure is the format's one definition of a pre-release; a format with an id of "plain"
// has none.
func (f viewFormat) Exposure() formats.ExposureAdapter {
	if f.id == "plain" {
		return nil
	}
	return preExposure{}
}

type preExposure struct{}

func (preExposure) ValidateRange(string) error             { return nil }
func (preExposure) Prerelease(v string) (bool, bool)       { return v == "pre", v != "junk" }
func (preExposure) InRange(string, string) bool            { return false }
func (preExposure) Dimensions() formats.ExposureDimensions { return formats.ExposureDimensions{} }

type noPorts struct{}

func (noPorts) FormatData(formats.ID) any { return nil }

// TestR_EP_016_ViewAdaptersListStoredVersionsOfLocalAndRemoteRepositories: the bridge is the
// source of the exposure check (Q-EP-61, Q-ARCH-253): it asks the view adapter of the repository's
// format for a local or a remote repository, passes the tag switch on, and answers a virtual
// repository and a format without a view adapter as unlisted without asking anyone.
func TestR_EP_016_ViewAdaptersListStoredVersionsOfLocalAndRemoteRepositories(t *testing.T) {
	stub := &viewAdapterStub{}
	vs := NewViewAdapters([]formats.Format{viewFormat{id: "alpha", a: stub}, viewFormat{id: "plain"}}, noPorts{})
	ctx := context.Background()

	got, err := vs.Stored(ctx, authz.StoredRepo{ID: 7, Name: qr("a-local"), Format: "alpha", Class: "local"}, "p", 11, true)
	if err != nil || !got.Listed || !slices.Equal(got.Versions, []string{"2.0.0", "1.0.0"}) || got.Tags["latest"] != "2.0.0" {
		t.Fatalf("local: %+v %v", got, err)
	}
	if q := stub.stored[0]; q.Repo.ID != 7 || q.Repo.Name != qr("a-local") || q.Repo.Class != formats.Local || q.Package != "p" || q.Limit != 11 || !q.Tags {
		t.Fatalf("the adapter was asked %+v", q)
	}
	if got, err = vs.Stored(ctx, authz.StoredRepo{ID: 8, Name: qr("a-remote"), Format: "alpha", Class: "remote"}, "p", 11, false); err != nil || !got.Listed || len(got.Tags) != 0 {
		t.Fatalf("remote: %+v %v", got, err)
	}
	if stub.stored[1].Tags {
		t.Fatal("the tags were asked for a format without tags")
	}
	for name, repo := range map[string]authz.StoredRepo{
		"a virtual repository":        {ID: 9, Name: qr("a-virtual"), Format: "alpha", Class: "virtual"},
		"a format without an adapter": {ID: 10, Name: qr("p-local"), Format: "plain", Class: "local"},
		"a format that is unknown":    {ID: 11, Name: qr("x-local"), Format: "nope", Class: "local"},
	} {
		if got, err := vs.Stored(ctx, repo, "p", 11, true); err != nil || got.Listed || len(got.Versions) != 0 {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if len(stub.stored) != 2 {
		t.Fatalf("the adapter was asked %d times, want 2", len(stub.stored))
	}
}

func TestR_API_029_ViewAdaptersBridgeTheRegisteredFormats(t *testing.T) {
	stub := &viewAdapterStub{}
	vs := NewViewAdapters([]formats.Format{viewFormat{id: "zeta", a: stub}, viewFormat{id: "plain"}, viewFormat{id: "alpha", a: stub}}, noPorts{})
	if got := vs.Formats(); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Fatalf("formats %v: a format without an adapter is listed, or the list is not sorted", got)
	}
	if _, ok := vs.For("plain"); ok {
		t.Fatal("a format without an adapter has one")
	}
	a, ok := vs.For("alpha")
	if !ok || a.Compare("aa", "b") <= 0 || !a.Prerelease("pre") || a.Prerelease("1.0.0") || a.Prerelease("junk") {
		t.Fatal("Compare does not reach the format, or Prerelease is not the exposure adapter's answer (a text that is no version is none)")
	}
	got, err := a.Describe(context.Background(), []view.Version{{ID: 5, Format: "alpha", Repo: "it/rr", Package: "p", Version: "1"}},
		view.FieldSummary|view.FieldReadme, func(v string) bool { return v == "1" })
	if err != nil || got[5].Description != "d" || !got[5].HasReadme || !slices.Equal(got[5].Keywords, []string{"k"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if stub.want != formats.ViewSummary|formats.ViewReadme || stub.got[0].Repo.Name != qr("rr") || stub.got[0].Package != "p" {
		t.Fatalf("%+v", stub)
	}
	ids, err := a.Find(context.Background(), view.Find{Repos: []string{"it/rr"}, Text: "x"}, 9)
	if err != nil || !slices.Equal(ids, []int64{1, 2}) || stub.find.Text != "x" || stub.limit != 9 {
		t.Fatalf("%v %v %+v", ids, err, stub)
	}
	if w, _ := a.Withheld(context.Background(), nil); !slices.Equal(w, []int64{3}) {
		t.Fatalf("%v", w)
	}
}

// sizerFormat is a view format with the optional ViewSizer.
type sizerFormat struct {
	viewFormat
	asked *[]formats.ViewVersion
}

func (s sizerFormat) ViewSizes(_ context.Context, _ formats.DataPorts, vs []formats.ViewVersion) (map[formats.VersionID]int64, error) {
	*s.asked = append(*s.asked, vs...)
	return map[formats.VersionID]int64{vs[0].ID: 42}, nil
}

// TestR_API_029_OnlyAFormatWithAViewSizerIsAskedForSizes: the core finds the size of a version through the capability, so the adapter
// of a format without ViewSizer has no Sizes method and is never asked; the bridge of one that has it passes the versions and the
// answer through (ADR-177).
func TestR_API_029_OnlyAFormatWithAViewSizerIsAskedForSizes(t *testing.T) {
	var asked []formats.ViewVersion
	vs := NewViewAdapters([]formats.Format{
		viewFormat{id: "plain", a: &viewAdapterStub{}},
		sizerFormat{viewFormat: viewFormat{id: "alpha", a: &viewAdapterStub{}}, asked: &asked},
	}, noPorts{})
	plain, ok := vs.For("plain")
	if !ok {
		t.Fatal("no adapter for plain")
	}
	if _, isSizer := plain.(view.Sizer); isSizer {
		t.Fatal("the adapter of a format without ViewSizer can be asked for sizes")
	}
	alpha, _ := vs.For("alpha")
	z, isSizer := alpha.(view.Sizer)
	if !isSizer {
		t.Fatal("the adapter of a format with ViewSizer cannot be asked for sizes")
	}
	got, err := z.Sizes(context.Background(), []view.Version{{ID: 7, Format: "alpha", Repo: "p/r", Package: "app", Version: "1"}})
	if err != nil || len(got) != 1 || got[7] != 42 {
		t.Fatalf("sizes = %v, %v", got, err)
	}
	if len(asked) != 1 || asked[0].ID != 7 || asked[0].Package != "app" || asked[0].Version != "1" {
		t.Fatalf("the format was asked %+v", asked)
	}
}
