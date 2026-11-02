package nuget

import (
	"context"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
)

// The registration documents (R-NUGET-009): the index of an ID, a page fetched on its own
// and a leaf, in the three hives. Which versions a hive lists is R-NUGET-004's; which an
// endpoint exposes is R-NUGET-034's, and paging runs over what is left of both.

// registrationKind is the document a registration route serves.
type registrationKind int

const (
	kindIndex registrationKind = iota
	kindPage
	kindLeaf
)

func (f *Format) handleRegistrationIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return f.serveRegistration(ctx, rq, w, kindIndex)
}

func (f *Format) handleRegistrationPage(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return f.serveRegistration(ctx, rq, w, kindPage)
}

func (f *Format) handleRegistrationLeaf(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return f.serveRegistration(ctx, rq, w, kindLeaf)
}

// serveRegistration renders one registration document of the ID of the request from the
// rows of one read (Store.Registration). An ID with no version visible in the hive has no
// document, whatever the kind; a leaf of a version the hive does not list is missing too.
func (f *Format) serveRegistration(ctx context.Context, rq *formats.Request, w http.ResponseWriter, kind registrationKind) error {
	if rq.Repo.Class == formats.Remote {
		return f.remoteRegistration(ctx, rq, w, kind)
	}
	hive, ok := registration.ParseHive(rq.Params.Get("hive"))
	if !ok {
		return errNoSuchResource()
	}
	lowerID := rq.Params.Get("id")
	paging := f.replicaOf(rq.Host).cfg.Registration
	pg := registration.Paging{PageSize: paging.PageSize, InlineBelow: paging.InlineBelow}
	links := rq.Host.Links(rq)
	doc := registration.Doc{
		Feed:    registration.Feed(links.Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/"),
		Hive:    hive,
		LowerID: lowerID,
		Paging:  pg,
	}
	repoID := rq.RepoRef().ID
	keep := exposes(viewOf(rq), lowerID)

	variant := string(hive)
	lower, upper, ver := rq.Params.Get("lower"), rq.Params.Get("upper"), rq.Params.Get("version")
	switch kind {
	case kindIndex:
		variant += "/index"
	case kindPage:
		variant += "/page/" + lower + "/" + upper
	case kindLeaf:
		variant += "/leaf/" + ver
	}
	return f.serveDocument(ctx, rq, w, lowerID, docSpec{
		variant: variant,
		gzip:    hive.Gzipped(),
		// The paging changes the bytes of the index and of the pages' bounds.
		settings: strconv.Itoa(pg.PageSize) + "/" + strconv.Itoa(pg.InlineBelow),
		render: func(ctx context.Context, st Store) ([]byte, int64, bool, error) {
			pkg, found, err := st.Registration(ctx, repoID, lowerID)
			if err != nil || !found {
				return nil, 0, false, err
			}
			vs := registration.Visible(pkg.Versions, hive, keep)
			if len(vs) == 0 {
				return nil, 0, false, nil
			}
			var body []byte
			switch kind {
			case kindIndex:
				body, err = doc.Index(vs)
				found = true
			case kindPage:
				body, found, err = doc.Page(vs, lower, upper)
			case kindLeaf:
				body, found, err = doc.Leaf(vs, ver)
			}
			if err != nil {
				return nil, 0, false, internalError(err)
			}
			return body, pkg.Rev, found, nil
		},
	})
}
