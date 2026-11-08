package formathost

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
)

// linkItems is the item store of a link: it holds one live item and records the linked commit.
type linkItems struct {
	ItemOps
	live    map[string]items.Item
	spec    items.CommitSpec
	digests blob.Digests
	commits int
	err     error
}

func (l *linkItems) Live(_ context.Context, repo items.RepoRef, path string) (items.Item, bool, error) {
	it, ok := l.live[repo.Name.String()+"/"+path]
	return it, ok, nil
}

func (l *linkItems) CommitLinked(_ context.Context, spec items.CommitSpec, d blob.Digests) (items.CommitResult, error) {
	l.spec, l.digests = spec, d
	l.commits++
	return items.CommitResult{Created: true, Item: items.Item{ID: 99}}, l.err
}

// linkRepos is the repository reader of a link: it knows the repositories by name.
type linkRepos struct {
	repository.Reader
	by map[qname.Repo]repository.Repository
}

func (l linkRepos) ByName(_ context.Context, n qname.Repo) (repository.Repository, error) {
	if r, ok := l.by[n]; ok {
		return r, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested name.")
}

// The link of R-STOR-008 for a format's request (ADR-161): the host resolves the source repository by name, finds the item (through
// the members of a virtual repository), decides the read on the named repository and commits without a body. A failure of any check
// is the answer for an item that does not exist.
func TestR_STOR_008_LinkResolvesTheSourceByNameAndChecksIt(t *testing.T) {
	other := repository.Repository{ID: "3", Name: qr("other"), Class: repository.Local, Format: "probe"}
	second := repository.Repository{ID: "4", Name: qr("second"), Class: repository.Local, Format: "probe"}
	virt := repository.Assemble(repository.Repository{ID: "5", Name: qr("both"), Class: repository.Virtual, Format: "probe"}, []repository.Repository{other, second}, nil, 60)
	const path = "base/_blobs/sha256:aa"
	src := items.Item{ID: 5, Path: path, Kind: items.KindFile, Size: 42, ContentStatus: "ok", SHA1: make([]byte, 20), MD5: make([]byte, 16), SHA512: make([]byte, 64)}
	src.Digest[0] = 0xaa
	repos := linkRepos{by: map[qname.Repo]repository.Repository{qr("other"): other, qr("second"): second, qr("both"): virt}}
	from := formats.SourceRef{Repo: qr("other"), Path: path}
	run := func(mod func(*linkItems, *fakeDecider, *formats.SourceRef), c formats.Commit) (*linkItems, *fakeDecider, formats.Committed, error) {
		li := &linkItems{live: map[string]items.Item{"it/other/" + path: src}}
		d := &fakeDecider{}
		d.decision = authz.Decision{Outcome: authz.Allow}
		f := from
		if mod != nil {
			mod(li, d, &f)
		}
		h, err := New(Options{Runner: nopRunner{}, Decider: d, Items: li, Packages: mustPackages(t), Repos: repos, Deps: Deps{Formats: func(string) (formats.Format, bool) { return nil, false }}})
		if err != nil {
			t.Fatal(err)
		}
		rq := testRequest(formats.Upload)
		defer h.Begin(rq)()
		out, err := h.Ingest().Link(context.Background(), rq, f, c)
		return li, d, out, err
	}
	commit := formats.Commit{Path: formats.Path{Path: "app/_blobs/sha256:aa"}, MediaType: "application/octet-stream", CreateOnly: true}

	t.Run("a readable live source is linked with the digests and the size of its blob", func(t *testing.T) {
		li, d, out, err := run(nil, commit)
		if err != nil || li.commits != 1 || !out.Created || out.Item.ID != 99 {
			t.Fatalf("%+v %v", out, err)
		}
		if li.digests.SHA256[0] != 0xaa || li.digests.Size != 42 || len(li.spec.Path.Path) == 0 || !li.spec.CreateOnly || li.spec.Repo.Name != qr("key") {
			t.Errorf("spec %+v digests %+v", li.spec, li.digests)
		}
		if len(d.got.Actions) != 1 || d.got.Actions[0] != authz.Read || d.got.Repository != qr("other") || d.got.Path != path || d.got.Class != repository.Local {
			t.Errorf("the read was decided as %+v", d.got)
		}
	})
	t.Run("a virtual source is read through its members in order", func(t *testing.T) {
		li, d, _, err := run(func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			delete(l.live, "it/other/"+path)
			l.live["it/second/"+path] = src
		}, commit)
		if err != nil || li.commits != 1 {
			t.Fatalf("%v", err)
		}
		if d.got.Repository != qr("both") || d.got.Class != repository.Virtual {
			t.Errorf("the read was decided on %+v: the caller reads the virtual repository", d.got)
		}
	})
	gone := map[string]func(*linkItems, *fakeDecider, *formats.SourceRef){
		"no such item":       func(l *linkItems, _ *fakeDecider, _ *formats.SourceRef) { l.live = nil },
		"no such repository": func(_ *linkItems, _ *fakeDecider, f *formats.SourceRef) { f.Repo = qr("ghost") },
		"a member holds none": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			l.live = nil
		},
		"a folder": func(l *linkItems, _ *fakeDecider, _ *formats.SourceRef) {
			it := src
			it.Kind = items.KindFolder
			l.live["it/other/"+path] = it
		},
		"a blob that is damaged": func(l *linkItems, _ *fakeDecider, _ *formats.SourceRef) {
			it := src
			it.ContentStatus = "corrupt"
			l.live["it/other/"+path] = it
		},
		"a version a finding blocks": func(l *linkItems, _ *fakeDecider, _ *formats.SourceRef) {
			it := src
			it.Block = vuln.BlockRow{FindingID: "f1"}
			l.live["it/other/"+path] = it
		},
		"a source the caller may not read": func(_ *linkItems, d *fakeDecider, _ *formats.SourceRef) {
			d.decision = authz.Decision{Outcome: authz.DenyHidden}
		},
	}
	for name, mod := range gone {
		t.Run(name+" is not found and commits nothing", func(t *testing.T) {
			li, _, _, err := run(mod, commit)
			if errs.KindOf(err) != errs.NotFound || li.commits != 0 {
				t.Fatalf("err %v, commits %d", err, li.commits)
			}
		})
	}
	t.Run("companions are refused", func(t *testing.T) {
		c := commit
		c.Companions = []formats.Companion{{}}
		if li, _, _, err := run(nil, c); errs.KindOf(err) != errs.Internal || li.commits != 0 {
			t.Fatalf("err %v", err)
		}
	})
}

func mustPackages(t *testing.T) *packages.Store {
	t.Helper()
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	return pk
}
