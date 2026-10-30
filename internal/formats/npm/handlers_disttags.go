package npm

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// Dist-tags (R-NPM-016, flow F5). The read is a document of the package; the writes
// need deploy and the ownership of the name, run under the package row lock and
// raise packages.rev_seq, so a tag change is visible on every replica as soon as it
// has returned (R-NPM-010) and changes the validator of the package document.

// maxTagBody bounds the body of a tag write, a JSON string holding a version.
const maxTagBody = 1 << 10

// handleDistTags is GET -/package/{name}/dist-tags: 200 with the tag map.
func (f *Format) handleDistTags(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	st, _, err := f.authorizeRead(ctx, rq, n, "")
	if err != nil {
		return err
	}
	// >>> WP2.9 exposure
	// The filtered tags need the exposed versions, to know where latest moves
	// (R-NPM-048): they come from the rows of the whole document.
	if view := viewOf(rq); view != nil {
		p, found, err := st.LocalDocument(ctx, rq.RepoRef().ID, n.String())
		if err != nil {
			return err
		}
		if !found || !exposeRows(view, n.String(), &p) {
			return notFound()
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		return writeJSON(w, http.StatusOK, p.Tags)
	}
	// <<< WP2.9 exposure
	tags, found, err := st.DistTags(ctx, rq.RepoRef().ID, n.String())
	if err != nil {
		return err
	}
	if !found {
		return notFound()
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	return writeJSON(w, http.StatusOK, tags.Tags)
}

// handleDistTagSet is PUT -/package/{name}/dist-tags/{tag} with a JSON string: the tag
// points to that version afterwards. latest may be moved to any live version, older
// ones included (R-NPM-016); a version that is not live is refused.
func (f *Format) handleDistTagSet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	tag := rq.Params.Get("tag")
	if err := semver.ValidateTag(tag); err != nil {
		return err
	}
	version, err := readTagVersion(w, rq)
	if err != nil {
		return err
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	st, repo, err := f.writeTarget(ctx, rq, n)
	if err != nil {
		return err
	}
	audit := auditOf(rq)
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		pkg, _, err := pw.EnsureLocked(ctx, repo, n.String(), n.String())
		if err != nil {
			return err
		}
		if err := f.claim(ctx, pw, opDistTag, n.String(), version, false); err != nil {
			return err
		}
		live, err := st.LiveVersions(ctx, tx, int64(pkg.ID))
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(live, func(v rows.LiveVersion) bool { return v.Version == version }) {
			return errs.E(errs.Invalid, errs.CodeNPMInvalidTag, "The version "+version+" is not published in this package; a tag points to a live version.")
		}
		cur, err := st.Tags(ctx, tx, int64(pkg.ID))
		if err != nil {
			return err
		}
		if cur[tag] == version {
			return nil // the tag points there already: the request changes nothing
		}
		tags := maps.Clone(cur)
		tags[tag] = version
		_, at, err := pw.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		err = st.SetTag(ctx, tx, rows.SetTagInput{
			RepoID: repo.ID, PackageID: int64(pkg.ID), Name: n.String(), Tag: tag, Version: version, Previous: cur[tag],
			Tags: tags, At: at, Audit: audit,
		})
		if err != nil {
			return err
		}
		return pw.Notify(areaMetadataChanged, formats.PackageChanged(repo.ID, n.String()))
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDistTagDelete is DELETE -/package/{name}/dist-tags/{tag}: latest cannot be
// removed (400), an unknown tag is 404.
func (f *Format) handleDistTagDelete(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	tag := rq.Params.Get("tag")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	st, repo, err := f.writeTarget(ctx, rq, n)
	if err != nil {
		return err
	}
	audit := auditOf(rq)
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		pkg, _, err := pw.EnsureLocked(ctx, repo, n.String(), n.String())
		if err != nil {
			return err
		}
		if err := f.claim(ctx, pw, opDistTag, n.String(), "", false); err != nil {
			return err
		}
		cur, err := st.Tags(ctx, tx, int64(pkg.ID))
		if err != nil {
			return err
		}
		prev, ok := cur[tag]
		switch {
		case tag == "latest" && ok:
			return errs.E(errs.Invalid, errs.CodeNPMInvalidTag, "The tag latest cannot be removed; move it with npm dist-tag add.")
		case !ok:
			return notFound()
		}
		tags := maps.Clone(cur)
		delete(tags, tag)
		_, at, err := pw.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		err = st.DeleteTag(ctx, tx, rows.DeleteTagInput{
			RepoID: repo.ID, PackageID: int64(pkg.ID), Name: n.String(), Tag: tag, Previous: prev, Tags: tags, At: at, Audit: audit,
		})
		if err != nil {
			return err
		}
		return pw.Notify(areaMetadataChanged, formats.PackageChanged(repo.ID, n.String()))
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeTarget finds the port and the repository of a write on an existing package: a
// write never creates a package (R-NPM-016, R-NPM-017), so a name without a row is
// 404 before the transaction takes any lock.
func (f *Format) writeTarget(ctx context.Context, rq *formats.Request, n name.Name) (Store, formats.RepoRef, error) {
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, formats.RepoRef{}, err
	}
	repo := rq.RepoRef()
	_, found, err := rq.Host.Packages().Package(ctx, repo, n.String())
	if err != nil {
		return nil, formats.RepoRef{}, err
	}
	if !found {
		return nil, formats.RepoRef{}, notFound()
	}
	return st, repo, nil
}

// readTagVersion reads the body of a tag write, a JSON string, and checks that it is
// a canonical version.
func readTagVersion(w http.ResponseWriter, rq *formats.Request) (string, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, rq.Body, maxTagBody))
	if err != nil {
		return "", errs.E(errs.Invalid, semver.CodeInvalidVersion, "The body of a tag write is a JSON string holding a version.", errs.WithCause(err))
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil {
		return "", errs.E(errs.Invalid, semver.CodeInvalidVersion, "The body of a tag write is a JSON string holding a version.", errs.WithCause(err))
	}
	v, ok := canonicalVersion(version)
	if !ok {
		return "", errs.E(errs.Invalid, semver.CodeInvalidVersion, "The version of a tag is a canonical SemVer version without build metadata.")
	}
	return v, nil
}
