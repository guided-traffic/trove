package formathost

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// spooled adapts a spooled body to formats.Spooled and remembers the digests the
// protocol announced, which the commit stores as the item's declared digests.
type spooled struct {
	s      *items.Spooled
	expect items.Expected
}

var _ formats.Spooled = (*spooled)(nil)

func (p *spooled) Digests() formats.Digests {
	return formats.Digests{
		SHA256: append([]byte(nil), p.s.SHA256[:]...), SHA512: append([]byte(nil), p.s.SHA512[:]...),
		SHA1: append([]byte(nil), p.s.SHA1[:]...), MD5: append([]byte(nil), p.s.MD5[:]...),
	}
}

func (p *spooled) Size() int64 { return p.s.Size }

func (p *spooled) Open() (io.ReadCloser, error) { return io.NopCloser(p.s.Reader()), nil }

func (p *spooled) ReaderAt() (io.ReaderAt, error) {
	ra, ok := p.s.Section(0, p.s.Size).(io.ReaderAt)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The spooled body cannot be read at an offset.")
	}
	return ra, nil
}

// commitPayload is what travels from Ingest().Commit through the item store's
// CommitSpec.Payload to the hook: the request the commit belongs to, and the
// format's own inspection and payload.
type commitPayload struct {
	req        *formats.Request
	inspection *formats.Inspection
	payload    any
}

type ingestor struct{ h *Host }

// Spool implements [formats.Ingestor]: the repository's upload limit, the body
// length, the announced digests, one pass (R-STOR-004).
func (i ingestor) Spool(ctx context.Context, rq *formats.Request, body io.Reader, o formats.IngestOptions) (formats.Spooled, error) {
	limit, err := files.MaxUpload(*rq.Repo)
	if err != nil {
		return nil, err
	}
	if o.MaxSize > 0 && (limit == 0 || o.MaxSize < limit) {
		limit = o.MaxSize
	}
	declared := o.Length
	if declared <= 0 {
		declared = -1 // unknown: the spool reads to the end (Q-ARCH-264)
	}
	exp := items.Expected{SHA256: o.Expect.SHA256, SHA512: o.Expect.SHA512, SHA1: o.Expect.SHA1, MD5: o.Expect.MD5}
	sp, err := i.h.o.Items.Spool(ctx, body, items.IngestOptions{ContentLength: declared, MaxSize: limit, Expect: exp})
	if err != nil {
		return nil, err
	}
	s := &spooled{s: sp, expect: exp}
	if st := stateOf(rq); st != nil {
		st.track(s)
	} else {
		_ = sp.Close()
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The request was not prepared by the format dispatcher.")
	}
	return s, nil
}

// Link implements [formats.Ingestor]: the commit of an item with the content of the item named by from, with no body (R-STOR-008).
// The host resolves the source repository by its name, finds the item (through the members of a virtual repository, in order), and
// checks it: a live file whose blob the store serves, with no download block of the vulnerability scanning, and readable by the
// request's subject on a fresh index. Everything else is errs.NotFound, the answer for an item that does not exist, so a name or a
// path tells a caller nothing about a repository it may not read. The size and the digests are those of the blob row, read with the
// item. The commit runs as Commit does, on the same state of the request.
func (i ingestor) Link(ctx context.Context, rq *formats.Request, from formats.SourceRef, c formats.Commit) (formats.Committed, error) {
	st := stateOf(rq)
	if st == nil {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The request was not prepared by the format dispatcher.")
	}
	if len(c.Companions) > 0 {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "A linked commit takes no companions.")
	}
	src, err := i.h.linkSource(ctx, rq, from)
	if err != nil {
		return formats.Committed{}, err
	}
	if err := i.h.recheck(ctx, rq); err != nil {
		return formats.Committed{}, err
	}
	var d blob.Digests
	d.SHA256, d.Size = src.Digest, src.Size
	copy(d.SHA1[:], src.SHA1)
	copy(d.MD5[:], src.MD5)
	copy(d.SHA512[:], src.SHA512)
	res, err := i.h.o.Items.CommitLinked(ctx, items.CommitSpec{
		Repo: *rq.Repo, Path: c.Path, MediaType: c.MediaType,
		Origin: items.OriginUpload, CreateOnly: c.CreateOnly, MayOverwrite: c.MayOverwrite,
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		Payload: &commitPayload{req: rq, inspection: c.Inspection, payload: c.Payload},
		Extra: func(ctx context.Context, tx meta.Tx, _ items.Ref) error {
			return st.record(ctx, tx, i.h.o.Decider, rq.Subject)
		},
		BeginCommit: func() { httpserver.BeginCommit(ctx) },
	}, d)
	if errors.Is(err, items.ErrOverwriteDenied) {
		denied := c.Path.Path
		var od *items.OverwriteDeniedError
		if errors.As(err, &od) {
			denied = od.Path
		}
		return formats.Committed{}, errs.E(errs.Forbidden, errs.CodeForbidden,
			"The action overwrite is not permitted on "+rq.Repo.Name.String()+"/"+denied+".")
	}
	if err != nil {
		return formats.Committed{}, err
	}
	return formats.Committed{
		Item:    formats.ItemRef{ID: res.Item.ID, Repo: repoRef(*rq.Repo), Path: c.Path.Path},
		Created: res.Created, Overwritten: res.Overwritten, Identical: !res.Created,
	}, nil
}

// SourceRouter is the routing of a virtual repository (claims, pin rules, member patterns) as the link of a virtual source needs it;
// *repository.Routing implements it, and the composition root binds it as Deps.Refusal (a compile-time assertion in internal/app). Without it
// a virtual source is not linked. The derived request of the source carries the endpoint's name and no exposure view: the source is
// decided by the caller's index, and the exposure of the endpoint the mount arrived on is the target's.
type SourceRouter interface {
	Route(ctx context.Context, virtual repository.Repository, req repository.RouteRequest) (repository.Route, error)
}

// linkSource resolves the source of a link and checks what R-STOR-008 asks of it. The read of the source is decided as a read of
// that repository is: through [Host.Authorize] on a request derived for the source (the decision, the repository's include and
// exclude patterns and, for a remote repository, the refusal of a name a global claim covers), and for a virtual repository through
// the routing of the name and the per-member read check the virtual engine makes (R-AUTHZ-009 rules 2 and 3), so a link can never
// check less than a read. The source must be a repository of the format of the target. Everything else is errs.NotFound.
func (h *Host) linkSource(ctx context.Context, rq *formats.Request, from formats.SourceRef) (items.Item, error) {
	gone := errs.E(errs.NotFound, errs.CodeNotFound, "The content is not stored.")
	if h.o.Repos == nil {
		return items.Item{}, errs.E(errs.Internal, errs.CodeInternal, "This host cannot resolve a repository by name.")
	}
	repo, err := h.o.Repos.ByName(ctx, from.Repo)
	if err != nil {
		if errs.KindOf(err) == errs.NotFound {
			return items.Item{}, gone
		}
		return items.Item{}, err
	}
	f, known := h.o.Formats(repo.Format)
	if !known || repo.Format != rq.Repo.Format || repo.Lifecycle == repository.Deleting || repo.Disabled {
		return items.Item{}, gone
	}
	// The read decision is the caller's on the named repository, on an index that knows it, before the item is looked up.
	idx, err := h.o.Decider.Begin(ctx, rq.Subject, from.Repo)
	if err != nil {
		// A replica whose authorization state has not learned the repository, or cannot confirm itself, refuses as every other refusal does:
		// the fallback of a mount is always safe, and a 5xx here would tell a repository that was just created from one that does not exist.
		if e, ok := errs.As(err); ok && (e.Code == errs.CodeAuthzStale || e.Code == errs.CodeAuthzEpochUnknown) {
			return items.Item{}, gone
		}
		return items.Item{}, err
	}
	info := f.Classify(formats.Path{Path: from.Path})
	srq := &formats.Request{
		Request: rq.Request, Repo: &repo, Route: &formats.Route{Name: "link.source", Kind: formats.Download},
		Subject: rq.Subject, Index: idx, Endpoint: formats.EndpointFacts{Name: rq.Endpoint.Name}, Host: h, Log: rq.Log,
	}
	read := formats.Target{Package: info.Package, Path: from.Path, Actions: []formats.Action{formats.ActionRead}}
	if err := h.Authorize(ctx, srq, read); err != nil {
		if k := errs.KindOf(err); k == errs.NotFound || k == errs.Forbidden || k == errs.Unauthenticated {
			return items.Item{}, gone
		}
		return items.Item{}, err
	}
	holders := []repository.Repository{repo}
	if repo.Class == repository.Virtual {
		if holders, err = h.routedMembers(ctx, srq, idx, repo, info.Package, from.Path); err != nil {
			return items.Item{}, err
		}
	}
	for _, m := range holders {
		ref, err := h.liveRef(ctx, repoRef(m))
		if err != nil {
			return items.Item{}, err
		}
		it, found, err := h.o.Items.Live(ctx, ref, from.Path)
		if err != nil {
			return items.Item{}, err
		}
		if !found {
			continue
		}
		if it.Kind != items.KindFile || it.ContentStatus != "ok" || it.Block.FindingID != "" {
			return items.Item{}, gone
		}
		return it, nil
	}
	return items.Item{}, gone
}

// routedMembers are the members of a virtual source in the order a read consults them: those the routing admits for the name (claims,
// pin rules, the members' patterns) and the caller may use (MemberUsable, unaudited); a remote member also needs that a global claim
// does not refuse the name. A host without the routing fails closed.
func (h *Host) routedMembers(ctx context.Context, srq *formats.Request, idx *authz.Index, v repository.Repository, pkg, path string) ([]repository.Repository, error) {
	router, ok := h.o.Refusal.(SourceRouter)
	if !ok {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The content is not stored.")
	}
	route, err := router.Route(ctx, v, repository.RouteRequest{Name: pkg, Path: path})
	if err != nil {
		return nil, err
	}
	if !route.Admitted {
		return nil, nil
	}
	coord := coordinate(srq, formats.Target{Package: pkg, Path: path})
	var out []repository.Repository
	for _, m := range route.Members {
		usable, _ := h.o.Decider.MemberUsableOn(ctx, idx, srq.Subject, m.Name, coord)
		if !usable {
			continue
		}
		if m.Class == repository.Remote && h.o.Refusal != nil && pkg != "" {
			refused, err := h.o.Refusal.RemoteRefused(ctx, m, pkg, &v)
			if err != nil {
				return nil, err
			}
			if refused {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// Commit implements [formats.Ingestor]: the item store's one commit with the
// origin Upload. The allowed write decisions of the request are recorded in its
// transaction, after the item row and before the storage hook, where the generic
// layer records them.
func (i ingestor) Commit(ctx context.Context, rq *formats.Request, sp formats.Spooled, c formats.Commit) (formats.Committed, error) {
	return i.commit(ctx, rq, sp, c, nil)
}

// commit is Commit with a step of the caller in the commit transaction, after the recorded write decisions: the upload sessions end
// theirs there, so that the item and the end of the session are one commit (R-STOR-011 Finalize).
func (i ingestor) commit(ctx context.Context, rq *formats.Request, sp formats.Spooled, c formats.Commit, also func(ctx context.Context, tx meta.Tx, ref items.Ref) error) (formats.Committed, error) {
	s, ok := sp.(*spooled)
	if !ok {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The body was not spooled by this host.")
	}
	st := stateOf(rq)
	if st == nil {
		return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The request was not prepared by the format dispatcher.")
	}
	// The body has arrived: the write is decided again on the index of this moment
	// (R-AUTHZ-015, Q-ARCH-225).
	if err := i.h.recheck(ctx, rq); err != nil {
		return formats.Committed{}, err
	}
	companions := make([]items.Companion, len(c.Companions))
	for n, cc := range c.Companions {
		cs, ok := cc.Spooled.(*spooled)
		if !ok {
			return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The body was not spooled by this host.")
		}
		companions[n] = items.Companion{
			Path: cc.Path, Spooled: cs.s, MediaType: cc.MediaType, Declared: cs.expect, Generated: cc.Generated,
			Payload: &commitPayload{req: rq, payload: cc.Payload},
		}
	}
	res, err := i.h.o.Items.Commit(ctx, items.CommitSpec{
		Repo: *rq.Repo, Path: c.Path, Spooled: s.s, MediaType: c.MediaType, Declared: s.expect, Companions: companions,
		Origin: items.OriginUpload, CreateOnly: c.CreateOnly, MayOverwrite: c.MayOverwrite,
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		Payload: &commitPayload{req: rq, inspection: c.Inspection, payload: c.Payload},
		Extra: func(ctx context.Context, tx meta.Tx, ref items.Ref) error {
			if err := st.record(ctx, tx, i.h.o.Decider, rq.Subject); err != nil {
				return err
			}
			if also != nil {
				return also(ctx, tx, ref)
			}
			return nil
		},
		BeginCommit: func() { httpserver.BeginCommit(ctx) },
	})
	if errors.Is(err, items.ErrOverwriteDenied) {
		// The path appeared meanwhile: the format passed MayOverwrite=false because
		// it did not hold overwrite when it decided. The path is the one that holds the
		// other content, a companion's when the lead's is free.
		denied := c.Path.Path
		var od *items.OverwriteDeniedError
		if errors.As(err, &od) {
			denied = od.Path
		}
		return formats.Committed{}, errs.E(errs.Forbidden, errs.CodeForbidden,
			"The action overwrite is not permitted on "+rq.Repo.Name.String()+"/"+denied+".")
	}
	if err != nil {
		return formats.Committed{}, err
	}
	out := formats.Committed{
		Item:    formats.ItemRef{ID: res.Item.ID, Repo: repoRef(*rq.Repo), Path: c.Path.Path},
		Created: res.Created, Overwritten: res.Overwritten, Identical: !res.Created,
	}
	for n, cr := range res.Companions {
		out.Companions = append(out.Companions, formats.ItemRef{ID: cr.Item.ID, Repo: repoRef(*rq.Repo), Path: c.Companions[n].Path.Path})
	}
	return out, nil
}

// Close implements [formats.Ingestor].
func (ingestor) Close(sp formats.Spooled) {
	if s, ok := sp.(*spooled); ok {
		_ = s.s.Close()
	}
}

type itemStore struct{ h *Host }

// handle is what Stat read: the item row, kept in formats.Item.Handle.
type handle struct{ it items.Item }

// Stat implements [formats.ItemStore].
func (s itemStore) Stat(ctx context.Context, repo formats.RepoRef, p formats.Path) (formats.Item, bool, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return formats.Item{}, false, err
	}
	ref, err := s.h.liveRef(ctx, repo)
	if err != nil {
		return formats.Item{}, false, err
	}
	ref.Facts = serveKeyOf(repo, p)
	it, found, err := s.h.o.Items.Live(ctx, ref, p.Path)
	if err != nil {
		return formats.Item{}, false, err
	}
	if !found {
		// A local repository that holds no item at the path holds none for any subject (R-BAS-013).
		if repo.Class == formats.Local {
			bastionmain.NoteMissing(ctx)
		}
		return formats.Item{}, false, nil
	}
	return itemOf(repo, it), true, nil
}

// Serve implements [formats.ItemStore]: the format's serve-time check, then the
// conditional headers, Range, Repr-Digest and the streaming of the generic layer,
// and the download statistics.
func (s itemStore) Serve(ctx context.Context, rq *formats.Request, w http.ResponseWriter, it formats.Item, mode formats.ServeMode) error {
	row, err := s.row(ctx, it)
	if err != nil {
		return err
	}
	// A blob marked missing is looked for again once per interval, and served when its object is back (Q-STOR-153).
	if row, err = s.h.o.Items.Usable(ctx, row); err != nil {
		bastionmain.NoteUnknown(ctx) // a serve-time refusal is never a negative entry (R-BAS-013)
		return err
	}
	if f, ok := s.h.o.Formats(rq.Repo.Format); ok {
		if err := f.ServeCheck(ctx, s.h, it.Ref.Repo, it.Ref, mode); err != nil {
			bastionmain.NoteUnknown(ctx)
			return err
		}
	}
	// The block of the vulnerability scanning comes after the format's own check, so that a version the caller may not
	// read stays the format's 404 (R-VULN-015); every route that serves a stored item passes here: the format's
	// downloads, the remote hit and the cache view, and every tier of virtual resolution.
	if err := s.h.refuseBlocked(ctx, rq, it.Ref.Repo, row); err != nil {
		bastionmain.NoteUnknown(ctx) // a block is never a negative entry (R-BAS-013)
		return err
	}
	// An object that cannot be opened is returned unwritten: the format's dispatcher answers it, and the remote engine
	// first fills the item of a remote repository again when its object is missing (R-STOR-027).
	return edge.ServeItem(w, rq.Request, row,
		func(rng *items.Range) (io.ReadCloser, error) { return s.h.o.Items.Open(ctx, row, rng) },
		edge.ServeOptions{
			Buffer: s.h.o.DownloadBuffer,
			Served: func() {
				s.h.o.Items.Served(ctx, items.ServedEvent{
					Item: row, Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx),
					Endpoint: rq.Subject.Endpoint, CountStats: true,
				})
			},
		})
}

// Open implements [formats.ItemStore].
func (s itemStore) Open(ctx context.Context, it formats.Item) (io.ReadCloser, error) {
	row, err := s.row(ctx, it)
	if err != nil {
		return nil, err
	}
	return s.h.o.Items.Open(ctx, row, nil)
}

// row returns the item row of a formats.Item: the one Stat read, or a new read for
// an item that was built another way.
func (s itemStore) row(ctx context.Context, it formats.Item) (items.Item, error) {
	if h, ok := it.Handle.(handle); ok {
		return h.it, nil
	}
	if err := formats.RepositoryAllowed(ctx, it.Ref.Repo); err != nil {
		return items.Item{}, err
	}
	ref, err := s.h.liveRef(ctx, it.Ref.Repo)
	if err != nil {
		return items.Item{}, err
	}
	ref.Facts = serveKeyOf(it.Ref.Repo, formats.Path{Path: it.Ref.Path})
	row, found, err := s.h.o.Items.Live(ctx, ref, it.Ref.Path)
	if err != nil {
		return items.Item{}, err
	}
	if !found {
		return items.Item{}, errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	}
	return row, nil
}

type packageStore struct{ s *packages.Store }

func (p packageStore) Package(ctx context.Context, repo formats.RepoRef, name string) (formats.PackageRow, bool, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return formats.PackageRow{}, false, err
	}
	row, found, err := p.s.Package(ctx, repo.ID, name)
	if err != nil {
		return formats.PackageRow{}, false, err
	}
	if !found {
		// A local repository that holds no such package holds none for any subject (R-BAS-013).
		if repo.Class == formats.Local {
			bastionmain.NoteMissing(ctx)
		}
		return formats.PackageRow{}, false, nil
	}
	return formats.PackageRow{Ref: packageRef(repo, row.Ref), DisplayName: row.DisplayName, Attributes: row.Attributes}, true, nil
}

func (p packageStore) Versions(ctx context.Context, repo formats.RepoRef, name string) ([]formats.VersionRow, formats.RevSeq, error) {
	if err := formats.RepositoryAllowed(ctx, repo); err != nil {
		return nil, 0, err
	}
	vs, rev, err := p.s.Versions(ctx, repo.ID, name)
	return versionRows(vs), formats.RevSeq(rev), err
}

func (p packageStore) RevSeqs(ctx context.Context, repos []formats.RepoRef, name string) (map[int64]formats.RevSeq, error) {
	ids := make([]int64, len(repos))
	for i, r := range repos {
		if err := formats.RepositoryAllowed(ctx, r); err != nil {
			return nil, err
		}
		ids[i] = r.ID
	}
	revs, err := p.s.RevSeqs(ctx, ids, name)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]formats.RevSeq, len(revs))
	for id, r := range revs {
		out[id] = formats.RevSeq(r)
	}
	return out, nil
}
