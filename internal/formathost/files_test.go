package formathost

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// namedFormat is a format with package names and a protocol of its own: the first
// segment of a path names the package, "loose.txt" names none.
type namedFormat struct{ formats.Base }

func (namedFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "named", DisplayName: "Named", Classes: []formats.Class{formats.Local, formats.Remote}, Layout: "named",
			ProtocolBasePath: "/api/named/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
		},
		ContractVersion: formats.ContractVersion,
	}
}
func (namedFormat) Routes() []formats.Route { return nil }
func (namedFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (namedFormat) Classify(p formats.Path) formats.PathInfo {
	name, _, _ := strings.Cut(p.Path, "/")
	if name == "" || name == "loose.txt" {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	return formats.PathInfo{Kind: formats.Content, Package: name, Version: "1.0.0"}
}

func (namedFormat) PackageDocumentPath(n string) (formats.Path, bool) {
	return formats.Path{Path: n}, true
}

// rawFormat is a format whose protocol is /files/ itself, as the generic format's.
type rawFormat struct{ portFormat }

func (rawFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "raw", DisplayName: "Raw", Classes: []formats.Class{formats.Local}, Layout: "raw",
			ProtocolBasePath: "/files/{key}/", PatternSubject: formats.SubjectPath,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func seamOf(own Ownership) *FilesSeam {
	lookup := func(id string) (formats.Format, bool) {
		switch id {
		case "named":
			return namedFormat{}, true
		case "raw":
			return rawFormat{}, true
		}
		return nil, false
	}
	return NewFilesSeam(func() (formats.Host, error) { return nil, nil }, Deps{Formats: lookup, Ownership: own})
}

func TestR_EP_014_FilesSeamClassifiesForTheGenericLayer(t *testing.T) {
	s := seamOf(nil)
	got := s.Classify(repository.Repository{Format: "named"}, repository.Cleaned{Path: "left-pad/-/left-pad-1.0.0.tgz"})
	if got.Package != "left-pad" || got.Version != "1.0.0" || !got.ByName || got.Kind != authz.KindArtifact {
		t.Fatalf("class = %+v", got)
	}
	if got := s.Classify(repository.Repository{Format: "raw"}, repository.Cleaned{Path: "a/b"}); got.ByName || got.Package != "" {
		t.Fatalf("a path format: %+v", got)
	}
	if got := s.Classify(repository.Repository{Format: "ghost"}, repository.Cleaned{Path: "a/b"}); got != (files.PathClass{}) {
		t.Fatalf("an unknown format: %+v", got)
	}
}

func TestR_STOR_007_FilesSeamRefusesRawDeploysIntoAProtocolOfItsOwn(t *testing.T) {
	s := seamOf(nil)
	if err := s.RawDeploy(repository.Repository{Key: "raw-local", Format: "raw"}); err != nil {
		t.Fatalf("a format whose protocol is /files/: %v", err)
	}
	err := s.RawDeploy(repository.Repository{Key: "named-local", Format: "named"})
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Conflict || e.Code != errs.CodeConflict || !strings.Contains(e.Detail, "/api/named/named-local/") {
		t.Fatalf("err = %v", err)
	}
	if e, ok := errs.As(s.RawDeploy(repository.Repository{Key: "x", Format: "ghost"})); !ok || e.Kind != errs.Conflict {
		t.Fatalf("an unknown format takes nothing: %v", e)
	}
}

// TestR_AUTHZ_018_FilesSeamCheckRemovalFailsClosed: the ownership question of a raw
// removal is asked where it applies, and a removal that needs it and cannot have it
// asked is refused (Q-AUTHZ-161). The question itself, under the package row lock,
// runs against PostgreSQL in the npm integration tests.
func TestR_AUTHZ_018_FilesSeamCheckRemovalFailsClosed(t *testing.T) {
	ctx := context.Background()
	local := repository.Repository{ID: "3", Key: "named-local", Class: repository.Local, Format: "named"}
	remote := repository.Repository{ID: "4", Key: "named-remote", Class: repository.Remote, Format: "named"}
	idx := new(authz.Index)
	for _, tc := range []struct {
		name string
		own  Ownership
		rm   files.Removal
		// ok says the removal passes; want is the kind of its refusal otherwise.
		ok   bool
		want errs.Kind
	}{
		{"a remote repository's cache asks nothing", nil, files.Removal{Repo: remote, Files: []string{"left-pad/x"}, Index: idx}, true, 0},
		{"a format without package names asks nothing", nil, files.Removal{Repo: repository.Repository{Class: repository.Local, Format: "raw"}, Files: []string{"a"}, Index: idx}, true, 0},
		{"files the format places in no package ask nothing", nil, files.Removal{Repo: local, Files: []string{"loose.txt"}, Index: idx}, true, 0},
		{"no ownership rule bound refuses", nil, files.Removal{Repo: local, Files: []string{"left-pad/-/left-pad-1.0.0.tgz"}, Index: idx}, false, errs.Internal},
		{"no index of a request refuses", &fakeOwnership{}, files.Removal{Repo: local, Files: []string{"left-pad/-/left-pad-1.0.0.tgz"}}, false, errs.Internal},
	} {
		t.Run("R-AUTHZ-018 "+tc.name, func(t *testing.T) {
			err := seamOf(tc.own).CheckRemoval(ctx, nil, tc.rm)
			if tc.ok && err != nil || !tc.ok && errs.KindOf(err) != tc.want {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// recheckDecider hands out a new index at every Begin and, with revoke, refuses
// every write decided on an index other than the first, as a grant removed while a
// body was on the wire would.
type recheckDecider struct {
	fakeDecider
	first, fresh *authz.Index
	asked        []*authz.Index
	revoke       bool
}

func (d *recheckDecider) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	d.fresh = new(authz.Index)
	return d.fresh, nil
}

func (d *recheckDecider) DecideOn(_ context.Context, idx *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	d.got = c
	d.asked = append(d.asked, idx)
	if d.revoke && idx != d.first {
		return authz.Decision{Outcome: authz.DenyVisible, Reason: authz.ReasonNoGrant, Missing: authz.Deploy}
	}
	return authz.Decision{Outcome: authz.Allow}
}

// TestR_AUTHZ_015_AFormatsWriteIsDecidedAgainWhenItCommits: the write decisions a
// request was allowed are decided again on a new index before the transaction of the
// write opens, and the request decides on that index from then on (Q-ARCH-225).
func TestR_AUTHZ_015_AFormatsWriteIsDecidedAgainWhenItCommits(t *testing.T) {
	ctx := context.Background()
	start := func(revoke bool) (*Host, *recheckDecider, *formats.Request) {
		d := &recheckDecider{first: new(authz.Index), revoke: revoke}
		h := newTestHost(t, d)
		rq := testRequest(formats.Upload)
		rq.Index = d.first
		if err := h.Authorize(ctx, rq, formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
			t.Fatal(err)
		}
		return h, d, rq
	}
	t.Run("R-AUTHZ-015 a grant removed after the first decision stops the write before its transaction", func(t *testing.T) {
		h, d, rq := start(true)
		ran := false
		err := h.Write(ctx, rq, func(formats.Tx, formats.PackageWriter) error { ran = true; return nil })
		if errs.KindOf(err) != errs.Forbidden || ran {
			t.Fatalf("err = %v, the transaction ran: %v", err, ran)
		}
		if last := d.asked[len(d.asked)-1]; last != d.fresh {
			t.Fatal("the write was not decided on a new index")
		}
		commit := formats.Commit{Path: formats.Path{Path: "left-pad/-/left-pad-1.0.0.tgz"}}
		if _, err := h.Ingest().Commit(ctx, rq, &spooled{}, commit); errs.KindOf(err) != errs.Forbidden {
			t.Fatalf("the commit of an upload: %v", err)
		}
	})
	t.Run("R-AUTHZ-015 an allowed write goes on, on the new index, with the new decision recorded", func(t *testing.T) {
		h, d, rq := start(false)
		if err := h.recheck(ctx, rq); err != nil {
			t.Fatal(err)
		}
		if rq.Index != d.fresh || rq.Index == d.first {
			t.Fatal("the request still decides on the index it began with")
		}
		if got := stateOf(rq).allowed; len(got) != 1 || got[0].d.Outcome != authz.Allow || got[0].c.Package != "left-pad" {
			t.Fatalf("allowed = %+v", got)
		}
	})
	t.Run("R-AUTHZ-015 a request that was allowed no write takes no new index", func(t *testing.T) {
		d := &recheckDecider{first: new(authz.Index)}
		h := newTestHost(t, d)
		rq := testRequest(formats.Upload)
		rq.Index = d.first
		if err := h.recheck(ctx, rq); err != nil || rq.Index != d.first || d.fresh != nil {
			t.Fatalf("err %v, a new index was taken", err)
		}
	})
}
