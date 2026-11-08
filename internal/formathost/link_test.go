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
	var unusable map[qname.Repo]bool
	var begin error
	router := linkRouter{members: virt.Members(), admitted: true}
	var dec *linkDecider
	repos.by[qr("alien")] = repository.Repository{ID: "6", Name: qr("alien"), Class: repository.Local, Format: "other-format"}
	run := func(mod func(*linkItems, *fakeDecider, *formats.SourceRef), c formats.Commit) (*linkItems, *fakeDecider, formats.Committed, error) {
		li := &linkItems{live: map[string]items.Item{"it/other/" + path: src}}
		d := &fakeDecider{}
		d.decision = authz.Decision{Outcome: authz.Allow}
		f := from
		unusable, begin, router = nil, nil, linkRouter{members: virt.Members(), admitted: true}
		if mod != nil {
			mod(li, d, &f)
		}
		dec = &linkDecider{fakeDecider: d, unusable: unusable, begin: begin}
		h, err := New(Options{Runner: nopRunner{}, Decider: dec, Items: li, Packages: mustPackages(t), Repos: repos, Deps: Deps{
			Formats: func(string) (formats.Format, bool) { return classFormat{portFormat{id: "probe"}}, true }, Refusal: router,
		}})
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
	t.Run("a virtual source is linked from a member the caller may read, skipping one it may not", func(t *testing.T) {
		li, _, _, err := run(func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			l.live["it/other/"+path] = src
			l.live["it/second/"+path] = src
			unusable = map[qname.Repo]bool{qr("other"): true}
		}, commit)
		if err != nil || li.commits != 1 || len(dec.asked) != 2 {
			t.Fatalf("err %v commits %d asked %v", err, li.commits, dec.asked)
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
		"a member the caller may not read holds the blob (M1: the 201 would confirm it)": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			delete(l.live, "it/other/"+path)
			l.live["it/second/"+path] = src
			unusable = map[qname.Repo]bool{qr("second"): true}
		},
		"a member the routing dropped holds the blob": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			delete(l.live, "it/other/"+path)
			l.live["it/second/"+path] = src
			router.members = []repository.Repository{other}
		},
		"a virtual source whose own patterns do not admit the name": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			router.admitted = false
		},
		"a remote member that a global claim refuses": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("both")
			delete(l.live, "it/other/"+path)
			l.live["it/second/"+path] = src
			router.members = []repository.Repository{{ID: "4", Name: qr("second"), Class: repository.Remote, Format: "probe"}}
			router.refused = map[qname.Repo]bool{qr("second"): true}
		},
		"a replica that has not learned the source repository (stale)": func(*linkItems, *fakeDecider, *formats.SourceRef) {
			begin = errs.E(errs.Unavailable, errs.CodeAuthzStale, "stale")
		},
		"an index whose epoch is unknown": func(*linkItems, *fakeDecider, *formats.SourceRef) {
			begin = errs.E(errs.PreconditionFailed, errs.CodeAuthzEpochUnknown, "unknown")
		},
		"a source of another format than the target's": func(l *linkItems, _ *fakeDecider, f *formats.SourceRef) {
			f.Repo = qr("alien")
			l.live["it/alien/"+path] = src
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

// linkDecider adds the per-member read check of a virtual source to the fake decision point.
type linkDecider struct {
	*fakeDecider
	unusable map[qname.Repo]bool
	asked    []qname.Repo
	// begin, when set, is the error of the start of the source's decision.
	begin error
}

func (d *linkDecider) Begin(ctx context.Context, sub authz.Subject, names ...qname.Repo) (*authz.Index, error) {
	if d.begin != nil {
		return nil, d.begin
	}
	return d.fakeDecider.Begin(ctx, sub, names...)
}

func (d *linkDecider) MemberUsableOn(_ context.Context, _ *authz.Index, _ authz.Subject, m qname.Repo, _ authz.Coordinate) (bool, bool) {
	d.asked = append(d.asked, m)
	return !d.unusable[m], false
}

// linkRouter is the routing of the virtual source: it admits the members it is given, in order.
type linkRouter struct {
	members  []repository.Repository
	admitted bool
	refused  map[qname.Repo]bool
}

func (r linkRouter) Route(context.Context, repository.Repository, repository.RouteRequest) (repository.Route, error) {
	return repository.Route{Admitted: r.admitted, Members: r.members}, nil
}

func (r linkRouter) RemoteRefused(_ context.Context, m repository.Repository, _ string, _ *repository.Repository) (bool, error) {
	return r.refused[m.Name], nil
}

func mustPackages(t *testing.T) *packages.Store {
	t.Helper()
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	return pk
}
