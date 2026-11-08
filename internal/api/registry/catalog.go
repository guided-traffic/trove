package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The catalog (R-OCI-017): the image names the caller may read in the local repositories and in the caches of the remote
// repositories of the format, with their full names, in byte order, in pages.

const (
	// CatalogMaxPage is the largest page the catalog answers, whatever n a client asks (R-OCI-017).
	CatalogMaxPage = 1000
	// catalogFetch is the number of names read from the store per round trip; a name the caller may not read is dropped after
	// the read, so a page may need several.
	catalogFetch = 500
)

// CatalogStore is what the catalog reads of the format's data port: the image names of one repository that hold a manifest,
// in byte order after the cursor name, at most limit (R-OCI-017; the OCI data port's CatalogNames). The registry root reads
// names and decides each one itself; the port knows no caller.
type CatalogStore interface {
	CatalogNames(ctx context.Context, repoID int64, after string, limit int) ([]string, error)
}

// CatalogOptions configure [NewCatalog].
type CatalogOptions struct {
	// Repos lists the repositories; the catalog keeps those of Format that are served.
	Repos repository.Reader
	// Decider is the decision point: which repositories the caller may read, and which image names of them (a grant scoped to a
	// package pattern covers exactly the names it matches, R-OCI-006).
	Decider authz.Decider
	// Store reads the names of a repository. Nil answers 501: the data port of the format does not offer the page.
	Store CatalogStore
	// Format is the ID of the format whose repositories the registry serves ("oci").
	Format string
	// Bound is the dispatcher's reading of the host root's binding (dispatch.Dispatcher.BoundRepository): on a bound host the
	// catalog lists the bound repository only, without its prefix (R-OCI-017, R-OCI-029).
	Bound func(*http.Request) (qname.Repo, bool, error)
	// WriteError renders an error in the format's shape (R-OCI-003).
	WriteError func(http.ResponseWriter, *http.Request, error)
}

// Catalog is the handler of GET and HEAD of /v2/_catalog.
type Catalog struct{ o CatalogOptions }

// NewCatalog creates the catalog handler.
func NewCatalog(o CatalogOptions) (*Catalog, error) {
	if o.Repos == nil || o.Decider == nil || o.Format == "" || o.Bound == nil || o.WriteError == nil {
		return nil, errors.New("registry: the catalog needs the repository reader, the decision point, the format, the bound-host function and an error writer")
	}
	return &Catalog{o: o}, nil
}

// catalogRepo is a repository the catalog may list, with the prefix of its names.
type catalogRepo struct {
	repo   repository.Repository
	id     int64
	prefix string
}

// ServeHTTP answers a page of the catalog. A registry token needs registry:catalog:* (the enforcement point checked it); the
// names are the ones the caller's trove token may read, decided per name on one index of the decision point. A caller that
// may read nothing, the anonymous one without an anonymous grant among them, gets an empty list (R-OCI-017).
func (h *Catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(route.APIVersionHeader, route.APIVersion)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.o.WriteError(w, r, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The catalog answers GET and HEAD.",
			errs.WithAllow(http.MethodGet, http.MethodHead)))
		return
	}
	n, last, err := catalogPage(r)
	if err != nil {
		h.o.WriteError(w, r, err)
		return
	}
	if h.o.Store == nil {
		h.o.WriteError(w, r, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The data port of the registry does not list image names."))
		return
	}
	names, more, err := h.page(r, n, last)
	if err != nil {
		h.o.WriteError(w, r, err)
		return
	}
	body, err := json.Marshal(struct {
		Repositories []string `json:"repositories"`
	}{names})
	if err != nil {
		h.o.WriteError(w, r, errs.Wrap(err, "registry.catalog.body"))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("X-Content-Type-Options", "nosniff")
	if more {
		hd.Set("Link", "</v2/_catalog?last="+queryEscape(names[len(names)-1])+"&n="+strconv.Itoa(n)+`>; rel="next"`)
	}
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// queryEscape escapes a catalog name for the query of the Link header: the names hold letters, digits, '.', '_', '-' and '/'.
func queryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), "&", "%26")
}

// catalogPage reads n and last. n defaults to and is capped at CatalogMaxPage; n=0 is an empty page; a value that is no
// non-negative integer is 400 UNSUPPORTED (a parameter trove does not support, R-OCI-008).
func catalogPage(r *http.Request) (n int, last string, err error) {
	q := r.URL.Query()
	n = CatalogMaxPage
	if v, ok := q["n"]; ok {
		if len(v) != 1 {
			return 0, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter n is given once.")
		}
		parsed, perr := strconv.Atoi(v[0])
		if perr != nil || parsed < 0 {
			return 0, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter n is a non-negative integer.")
		}
		n = min(parsed, CatalogMaxPage)
	}
	if v := q["last"]; len(v) > 1 {
		return 0, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter last is given once.")
	} else if len(v) == 1 {
		last = v[0]
	}
	return n, last, nil
}

// page collects up to n names after last, and whether more follow. The repositories are walked in the order of their name
// prefix "<project>/<key>/": a prefix is never a prefix of another (a key holds no '/'), so the names of the repositories in
// that order are in byte order as a whole.
func (h *Catalog) page(r *http.Request, n int, last string) (names []string, more bool, err error) {
	ctx := r.Context()
	repos, err := h.candidates(r)
	if err != nil {
		return nil, false, err
	}
	names = []string{}
	if n == 0 || len(repos) == 0 {
		return names, false, nil
	}
	keys := make([]qname.Repo, len(repos))
	for i, c := range repos {
		keys[i] = c.repo.Name
	}
	acc, err := edge.Resolve(ctx, h.o.Decider, edge.SubjectFrom(r), keys...)
	if err != nil {
		return nil, false, err
	}
	readable, err := acc.Readable(ctx, keys)
	if err != nil {
		return nil, false, err
	}
	may := map[qname.Repo]bool{}
	for _, k := range readable {
		may[k] = true
	}
	for _, c := range repos {
		if !may[c.repo.Name] {
			continue
		}
		after, skip := resume(c.prefix, last)
		if skip {
			continue
		}
		for {
			batch, err := h.o.Store.CatalogNames(ctx, c.id, after, catalogFetch)
			if err != nil {
				return nil, false, errs.Wrap(err, "registry.catalog.names")
			}
			if len(batch) > 0 {
				after = batch[len(batch)-1]
				ok, err := acc.PathsAllowed(ctx, authz.Coordinate{Repository: c.repo.Name}, authz.Read, batch,
					func(name string) (string, string) { return name, "" })
				if err != nil {
					return nil, false, err
				}
				for i, name := range batch {
					if !ok[i] {
						continue
					}
					if len(names) == n {
						return names, true, nil
					}
					names = append(names, c.prefix+name)
				}
			}
			if len(batch) < catalogFetch {
				break
			}
		}
	}
	return names, false, nil
}

// resume says where in a repository's names the page after last starts: after "" for a repository whose names all sort after
// last, after the part of last below the prefix for the repository last belongs to, and skip for a repository whose names all
// sort before last.
func resume(prefix, last string) (after string, skip bool) {
	switch {
	case last == "" || last < prefix:
		return "", false
	case strings.HasPrefix(last, prefix):
		return strings.TrimPrefix(last, prefix), false
	}
	return "", true
}

// candidates lists the repositories the catalog may list, in the order of their name prefix: the local repositories and the
// remote repositories of the format that are served, or, on a bound host, the bound repository alone with an empty prefix. A
// virtual repository is not expanded: its members appear under their own names (R-OCI-017).
func (h *Catalog) candidates(r *http.Request) ([]catalogRepo, error) {
	bound, isBound, err := h.o.Bound(r)
	if err != nil {
		return nil, err
	}
	all, err := h.o.Repos.List(r.Context())
	if err != nil {
		return nil, err
	}
	var out []catalogRepo
	for _, repo := range all {
		if repo.Format != h.o.Format || repo.Disabled || repo.Lifecycle == repository.Deleting ||
			(repo.Class != repository.Local && repo.Class != repository.Remote) {
			continue
		}
		id, err := strconv.ParseInt(repo.ID, 10, 64)
		if err != nil {
			continue // a repository without a numeric row id has no rows in the format's tables
		}
		if isBound {
			if repo.Name == bound {
				out = append(out, catalogRepo{repo: repo, id: id})
			}
			continue
		}
		out = append(out, catalogRepo{repo: repo, id: id, prefix: repo.Name.Project + "/" + repo.Name.Key + "/"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].prefix < out[j].prefix })
	return out, nil
}
