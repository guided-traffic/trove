package files

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// packageSeam is the seam of a format with package names and a protocol of its
// own: a path "<name>/-/<name>-<version>.tgz" belongs to the package <name> and
// its version, a path below "<name>/" to the package, anything else to none.
type packageSeam struct {
	removals []Removal
	refuse   error
}

func (s *packageSeam) Classify(repo repository.Repository, p repository.Cleaned) PathClass {
	if repo.Format != "pkgfmt" {
		return PathClass{}
	}
	pc := PathClass{Kind: authz.KindArtifact, ByName: true}
	name, rest, _ := strings.Cut(p.Path, "/")
	if name == "" || name == "loose.txt" {
		return pc
	}
	pc.Package = name
	if file, ok := strings.CutPrefix(rest, "-/"+name+"-"); ok {
		pc.Version = strings.TrimSuffix(file, ".tgz")
	}
	return pc
}

func (s *packageSeam) ServeCheck(context.Context, repository.Repository, items.Item) error {
	return nil
}

func (s *packageSeam) RawDeploy(repo repository.Repository) error {
	if repo.Format == "pkgfmt" {
		return errs.E(errs.Conflict, errs.CodeConflict, "The repository takes content only through its own protocol.")
	}
	return nil
}

func (s *packageSeam) CheckRemoval(_ context.Context, _ meta.Tx, rm Removal) error {
	s.removals = append(s.removals, rm)
	return s.refuse
}

func packageService(t *testing.T, st *store, seam *packageSeam) *Service {
	t.Helper()
	svc, err := New(Options{Items: st, Formats: seam, Repos: repos{
		"generic-local": {ID: "5", Key: "generic-local", Class: repository.Local, Format: "generic"},
		"pkg-local":     {ID: "7", Key: "pkg-local", Class: repository.Local, Format: "pkgfmt"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// readCounter fails the test when the body is read.
type readCounter struct{ reads int }

func (r *readCounter) Read([]byte) (int, error) { r.reads++; return 0, errors.New("the body was read") }

func TestR_EP_014_FilesDecideOnTheFormatsCoordinate(t *testing.T) {
	ctx := context.Background()
	t.Run("R-EP-014 a download of a format's file is decided on the package and the version the format gives it", func(t *testing.T) {
		acc := &access{allow: true}
		st := &store{found: true, live: items.Item{Kind: items.KindFile, Path: "ui/-/ui-1.0.0.tgz"}}
		if _, err := packageService(t, st, &packageSeam{}).Stat(ctx, caller(acc), "pkg-local", repository.Cleaned{Path: "ui/-/ui-1.0.0.tgz"}); err != nil {
			t.Fatal(err)
		}
		if len(acc.decides) != 1 || acc.decides[0].Package != "ui" || acc.decides[0].Version != "1.0.0" || acc.decides[0].Path != "ui/-/ui-1.0.0.tgz" {
			t.Fatalf("decisions = %+v", acc.decides)
		}
	})
	t.Run("R-EP-014 a path of a generic repository is decided on the path alone", func(t *testing.T) {
		acc := &access{allow: true}
		st := &store{found: true, live: items.Item{Kind: items.KindFile, Path: "ui/-/ui-1.0.0.tgz"}}
		if _, err := packageService(t, st, &packageSeam{}).Stat(ctx, caller(acc), "generic-local", repository.Cleaned{Path: "ui/-/ui-1.0.0.tgz"}); err != nil {
			t.Fatal(err)
		}
		if acc.decides[0].Package != "" || acc.decides[0].Version != "" {
			t.Fatalf("decisions = %+v", acc.decides)
		}
	})
	t.Run("R-AUTHZ-016 a repository the snapshot does not hold is decided on the path and refused as a hidden one", func(t *testing.T) {
		acc, st := &access{allow: false}, &store{}
		_, err := packageService(t, st, &packageSeam{}).Stat(ctx, caller(acc), "ghost", repository.Cleaned{Path: "ui/-/ui-1.0.0.tgz"})
		if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound || e.Code != errs.CodeNotFound {
			t.Fatalf("err = %v", err)
		}
		if acc.decides[0].Package != "" {
			t.Fatalf("decisions = %+v", acc.decides)
		}
	})
	t.Run("R-EP-014 R-STOR-017 the entries of a listing are decided on the coordinate the format gives each of them", func(t *testing.T) {
		acc := &access{allow: true}
		st := &store{found: true, live: items.Item{Kind: items.KindFolder}}
		if _, err := packageService(t, st, &packageSeam{}).Children(ctx, caller(acc), "pkg-local", ChildrenQuery{Folders: true, Limit: 10, Strict: true}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"a.bin=a.bin@", "secret/b.bin=secret@"}; !slices.Equal(acc.classified, want) {
			t.Fatalf("classified %v, want %v", acc.classified, want)
		}
	})
	t.Run("R-API-025 the items operations' shared step is completed with the format's coordinate", func(t *testing.T) {
		svc := packageService(t, &store{}, &packageSeam{})
		got := svc.Classified(ctx, authz.Coordinate{Repository: "pkg-local", Path: "ui/-/ui-1.0.0.tgz", Actions: []authz.Action{authz.Read}})
		if got.Package != "ui" || got.Version != "1.0.0" {
			t.Fatalf("coordinate = %+v", got)
		}
		for _, c := range []authz.Coordinate{
			{Repository: "generic-local", Path: "ui/-/ui-1.0.0.tgz"},
			{Repository: "ghost", Path: "ui/x"},
			{Repository: "pkg-local"},
			{Repository: "pkg-local", Path: "ui/x", Package: "other"},
		} {
			if got := svc.Classified(ctx, c); got.Package != c.Package || got.Version != "" {
				t.Errorf("%+v became %+v", c, got)
			}
		}
	})
}

func TestR_AUTHZ_018_FilesDeleteAsksTheFormat(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-018 R-EP-014 the subtree is decided per item on its package", func(t *testing.T) {
		acc, seam := &access{allow: true}, &packageSeam{}
		st := &store{found: true, live: items.Item{Kind: items.KindFolder}}
		if _, err := packageService(t, st, seam).Delete(ctx, caller(acc), DeleteRequest{Repo: "pkg-local", Path: "ui", Strict: true}); err != nil {
			t.Fatal(err)
		}
		if acc.decides[0].Package != "ui" || len(acc.recorded) != 1 || acc.recorded[0].Package != "ui" {
			t.Fatalf("decisions %+v, recorded %+v", acc.decides, acc.recorded)
		}
		if want := []string{"ui/x=ui@", "ui/y=ui@"}; !slices.Equal(acc.classified, want) {
			t.Fatalf("classified %v, want %v", acc.classified, want)
		}
	})
	t.Run("R-AUTHZ-018 Q-AUTHZ-161 the format is asked about the removed files with the request's index and subject", func(t *testing.T) {
		idx := new(authz.Index)
		acc, seam := &access{allow: true, idx: idx}, &packageSeam{}
		st := &store{found: true, live: items.Item{Kind: items.KindFolder}}
		if _, err := packageService(t, st, seam).Delete(ctx, caller(acc), DeleteRequest{Repo: "pkg-local", Path: "ui", Strict: true}); err != nil {
			t.Fatal(err)
		}
		if len(seam.removals) != 1 {
			t.Fatalf("removals = %+v", seam.removals)
		}
		rm := seam.removals[0]
		if rm.Repo.Key != "pkg-local" || !slices.Equal(rm.Files, []string{"ui/x", "ui/y"}) || rm.Index != idx || rm.Subject.Endpoint != "customers" {
			t.Fatalf("removal = %+v", rm)
		}
	})
	t.Run("R-AUTHZ-018 a refusal of the format stops the delete", func(t *testing.T) {
		owned := errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned")
		acc, seam := &access{allow: true}, &packageSeam{refuse: owned}
		st := &store{found: true, live: items.Item{Kind: items.KindFile}}
		_, err := packageService(t, st, seam).Delete(ctx, caller(acc), DeleteRequest{Repo: "pkg-local", Path: "ui/-/ui-1.0.0.tgz", Strict: true})
		if !errors.Is(err, owned) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_STOR_007_FilesRefusesRawDeploysIntoFormats(t *testing.T) {
	ctx := context.Background()
	for _, folder := range []bool{false, true} {
		name := "a file"
		if folder {
			name = "a folder"
		}
		t.Run("R-STOR-007 R-API-025 Q-ARCH-223 "+name+" of a format with a protocol of its own is refused before the body is read", func(t *testing.T) {
			acc, st, body := &access{allow: true}, &store{}, &readCounter{}
			req := DeployRequest{Repo: "pkg-local", Path: repository.Cleaned{Path: "ui/-/ui-2.0.0.tgz", Folder: folder}, Body: body, ContentLength: 1}
			_, err := packageService(t, st, &packageSeam{}).Deploy(ctx, caller(acc), req)
			if e, ok := errs.As(err); !ok || e.Kind != errs.Conflict {
				t.Fatalf("err = %v", err)
			}
			if body.reads != 0 || st.commit != nil || st.folder != nil {
				t.Fatalf("reads %d, commit %v, folder %v", body.reads, st.commit, st.folder)
			}
			if len(acc.decides) != 1 || acc.decides[0].Package != "ui" {
				t.Fatalf("the deploy was not decided first, on the format's coordinate: %+v", acc.decides)
			}
		})
	}
	t.Run("R-GENERIC-004 a generic repository takes the deploy", func(t *testing.T) {
		acc, st := &access{allow: true}, &store{}
		if _, err := packageService(t, st, &packageSeam{}).Deploy(ctx, caller(acc), upload("ui/-/ui-2.0.0.tgz")); err != nil {
			t.Fatal(err)
		}
	})
}
