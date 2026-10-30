package formathost

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
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
	exp := items.Expected{SHA256: o.Expect.SHA256, SHA512: o.Expect.SHA512, SHA1: o.Expect.SHA1, MD5: o.Expect.MD5}
	sp, err := i.h.o.Items.Spool(ctx, body, items.IngestOptions{ContentLength: rq.ContentLength, MaxSize: limit, Expect: exp})
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

// Commit implements [formats.Ingestor]: the item store's one commit with the
// origin Upload. The allowed write decisions of the request are recorded in its
// transaction, after the item row and before the storage hook, where the generic
// layer records them.
func (i ingestor) Commit(ctx context.Context, rq *formats.Request, sp formats.Spooled, c formats.Commit) (formats.Committed, error) {
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
	res, err := i.h.o.Items.Commit(ctx, items.CommitSpec{
		Repo: *rq.Repo, Path: c.Path, Spooled: s.s, MediaType: c.MediaType, Declared: s.expect,
		Origin: items.OriginUpload, CreateOnly: c.CreateOnly, MayOverwrite: c.MayOverwrite,
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		Payload: &commitPayload{req: rq, inspection: c.Inspection, payload: c.Payload},
		Extra: func(ctx context.Context, tx meta.Tx, _ items.Ref) error {
			return st.record(ctx, tx, i.h.o.Decider, rq.Subject)
		},
		BeginCommit: func() { httpserver.BeginCommit(ctx) },
	})
	if errors.Is(err, items.ErrOverwriteDenied) {
		// The path appeared meanwhile: the format passed MayOverwrite=false because
		// it did not hold overwrite when it decided.
		return formats.Committed{}, errs.E(errs.Forbidden, errs.CodeForbidden,
			"The action overwrite is not permitted on "+rq.Repo.Key+"/"+c.Path.Path+".")
	}
	if err != nil {
		return formats.Committed{}, err
	}
	return formats.Committed{
		Item:    formats.ItemRef{ID: res.Item.ID, Repo: repoRef(*rq.Repo), Path: c.Path.Path},
		Created: res.Created, Overwritten: res.Overwritten, Identical: !res.Created,
	}, nil
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
	it, found, err := s.h.o.Items.Live(ctx, itemsRepo(repo), p.Path)
	if err != nil || !found {
		return formats.Item{}, false, err
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
	if err := items.ContentError(row); err != nil {
		return err
	}
	if f, ok := s.h.o.Formats(rq.Repo.Format); ok {
		if err := f.ServeCheck(ctx, s.h, it.Ref.Repo, it.Ref, mode); err != nil {
			return err
		}
	}
	edge.ServeItem(w, rq.Request, row,
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
	return nil
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
	row, found, err := s.h.o.Items.Live(ctx, itemsRepo(it.Ref.Repo), it.Ref.Path)
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
	row, found, err := p.s.Package(ctx, repo.ID, name)
	if err != nil || !found {
		return formats.PackageRow{}, false, err
	}
	return formats.PackageRow{Ref: packageRef(repo, row.Ref), DisplayName: row.DisplayName, Attributes: row.Attributes}, true, nil
}

func (p packageStore) Versions(ctx context.Context, repo formats.RepoRef, name string) ([]formats.VersionRow, formats.RevSeq, error) {
	vs, rev, err := p.s.Versions(ctx, repo.ID, name)
	return versionRows(vs), formats.RevSeq(rev), err
}

func (p packageStore) RevSeqs(ctx context.Context, repos []formats.RepoRef, name string) (map[int64]formats.RevSeq, error) {
	ids := make([]int64, len(repos))
	for i, r := range repos {
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
