package formathost

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/packages/view"
)

type viewAdapterStub struct {
	want  formats.ViewFields
	got   []formats.ViewVersion
	find  formats.ViewFind
	limit int
}

func (viewAdapterStub) Compare(a, b string) int  { return len(a) - len(b) }
func (viewAdapterStub) Prerelease(v string) bool { return v == "pre" }
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

type noPorts struct{}

func (noPorts) FormatData(formats.ID) any { return nil }

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
	if !ok || a.Compare("aa", "b") <= 0 || !a.Prerelease("pre") {
		t.Fatal("Compare or Prerelease do not reach the format")
	}
	got, err := a.Describe(context.Background(), []view.Version{{ID: 5, Format: "alpha", Repo: "r", Package: "p", Version: "1"}},
		view.FieldSummary|view.FieldReadme, func(v string) bool { return v == "1" })
	if err != nil || got[5].Description != "d" || !got[5].HasReadme || !slices.Equal(got[5].Keywords, []string{"k"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if stub.want != formats.ViewSummary|formats.ViewReadme || stub.got[0].Repo.Key != "r" || stub.got[0].Package != "p" {
		t.Fatalf("%+v", stub)
	}
	ids, err := a.Find(context.Background(), view.Find{Repos: []string{"r"}, Text: "x"}, 9)
	if err != nil || !slices.Equal(ids, []int64{1, 2}) || stub.find.Text != "x" || stub.limit != 9 {
		t.Fatalf("%v %v %+v", ids, err, stub)
	}
	if w, _ := a.Withheld(context.Background(), nil); !slices.Equal(w, []int64{3}) {
		t.Fatalf("%v", w)
	}
}
